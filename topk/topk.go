// Package topk implements HeavyKeeper for finding the most frequent items
// in a stream.
//
// A sketch has two parts: a table of buckets that estimates item counts,
// and a min-heap with the k items that have the largest estimates so far.
// The table has two rows, and each item maps to one bucket in each row.
// A bucket holds a fingerprint of the item and a count:
//
//   - If the bucket is empty or owned by the item, its count increases.
//   - If it's owned by another item, its count decays (decreases by 1) with
//     probability 0.8^count. When the count reaches zero, the new item takes
//     over the bucket.
//
// The item's estimated count is the largest count among its buckets.
// Frequent items quickly reach counts that make decay very unlikely,
// so they keep their buckets. Rare items keep replacing each other without
// affecting the frequent ones. An item that becomes frequent later in the
// stream can still outnumber a less frequent owner and take over its bucket.
// Unlike Count-min or Space-Saving methods for Top-K, rare items don't
// artificially increase the counts of frequent items.
//
// Counts are estimates: they're usually exact for the most frequent items,
// but may be slightly lower than the actual count for less common items
// (an item can lose some events while competing for a bucket). Counts are
// capped at [math.MaxUint32] (about 4.3 billion).
//
// Accuracy depends on how skewed the stream is. For a Zipf distribution
// over 1M items, the sketch finds the actual top k with ≥99% precision
// for Zipf exponent s ≥ 0.8 and k in 10..1000, and 80–97% for s = 0.6.
// For flatter streams (where all items are about equally frequent),
// there is no clear top k, and the results become less accurate.
//
// Each row has max(16k, 256) buckets of 8 bytes. With the heap, the sketch
// takes about 350 bytes per k plus the items: ~5 KB for k=10 and ~360 KB
// for k=1000 with 16-byte items.
//
// Buckets store a 32-bit fingerprint (half of the item's hash). If two items map
// to the same bucket and have the same fingerprint, they share the same count,
// so the count can be higher than the actual count. This almost never happens
// on normal, non-adversarial inputs.
//
// The sketch is deterministic: the same items added in the same order
// always produce the same sketch.
package topk

import (
	"cmp"
	"math"
	"math/bits"
	"slices"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// Table shape: two rows of max(widthFactor*k, minWidth) buckets.
const (
	widthFactor = 16
	minWidth    = 256
)

// decayBase is the base of the decay probability: an attempt decays
// a bucket with count c with probability decayBase^c.
const decayBase = 0.8

// decayThr[c] is decayBase^c scaled to 2^64: an attempt decays a bucket with
// count c if a random uint64 is below it. It's 0 from about c=422, so the
// table stops there and larger counts never decay. decayLog[c] is
// ln(1 - decayBase^c), for drawing the number of attempts until a decay.
var decayThr, decayLog = func() ([]uint64, []float64) {
	thr := []uint64{math.MaxUint64} // count 0 is never decayed
	logs := []float64{math.Inf(-1)}
	for c := 1; ; c++ {
		p := math.Pow(decayBase, float64(c))
		t := uint64(math.Ldexp(p, 64))
		if t == 0 {
			return thr, logs
		}
		thr = append(thr, t)
		logs = append(logs, math.Log1p(-p))
	}
}()

// salts are random odd constants. Row i takes the top bits of the item's
// hash times salts[i], so each row gets its own position from one hash.
var salts = [2]uint64{0x6ca123e582c49bc7, 0xd9f3b4a63fd8553b}

// Entry is a frequent item with its estimated count.
type Entry struct {
	Item  string
	Count int
}

// Sketch finds the most frequent items in a stream.
//
// The table and heap are allocated at creation. Add and AddN only allocate
// to copy an item when it enters the top k.
// Not safe for concurrent use.
type Sketch struct {
	k       int
	buckets []bucket          // two rows of width buckets
	width   uint64            // number of buckets per row
	heap    []node            // min-heap by count, at most k nodes
	slots   []slot            // heap items, never move
	index   map[uint64]uint32 // item hash -> slot
	total   uint64
	rng     uint64
}

// bucket is a table cell, owned by the item with this fingerprint
// (the top 32 bits of its hash). Empty if count is 0.
type bucket struct {
	fp    uint32
	count uint32
}

// node is a heap element. The count is stored inline, so heap operations
// don't touch the slots except to update their positions.
type node struct {
	count uint64
	slot  uint32
}

// slot is an item in the heap. Its buffer is reused when the item is replaced.
type slot struct {
	hash uint64
	pos  uint32 // index in the heap
	item []byte
}

// New creates a sketch that finds the k most frequent items.
// Panics if k < 1 or k is too large.
func New(k int) *Sketch {
	if k < 1 {
		panic("topk: k must be at least 1")
	}
	if k > math.MaxInt32/widthFactor {
		panic("topk: k is too large")
	}
	w := max(widthFactor*k, minWidth)
	// The allocator rounds the allocation up to its size class (or whole
	// pages for large sizes). Appending to a nil slice reports the rounded
	// capacity, so widen the rows to use it all: higher accuracy for free.
	buckets := append([]bucket(nil), make([]bucket, 2*w)...)
	w = cap(buckets) / 2
	return &Sketch{
		k:       k,
		buckets: buckets[:2*w],
		width:   uint64(w),
		heap:    make([]node, 0, k),
		slots:   make([]slot, 0, k),
		index:   make(map[uint64]uint32, k),
	}
}

// Add adds 1 to the item's count.
func (s *Sketch) Add(item []byte) {
	s.add(item, 1)
}

// AddN adds n to the item's count.
// Panics if n is negative.
func (s *Sketch) AddN(item []byte, n int) {
	if n <= 0 {
		if n < 0 {
			panic("topk: n must not be negative")
		}
		return
	}
	s.add(item, uint64(n))
}

// add adds n > 0 to the item's count.
func (s *Sketch) add(item []byte, n uint64) {
	s.total += n
	h := wyhash.Hash(item, wyhash.Seed0)
	fp := uint32(h >> 32)

	// Update the item's buckets and get its estimate.
	var est uint32
	for i, salt := range salts {
		j, _ := bits.Mul64(h*salt, s.width)
		b := &s.buckets[uint64(i)*s.width+j]
		if b.count > 0 && b.fp != fp {
			// The bucket is owned by another item.
			c, rest := s.decay(b.count, n)
			b.count = c
			if c > 0 {
				continue
			}
			// The item took over the bucket in one of the n attempts;
			// rest is the remaining count.
			b.fp, b.count = fp, uint32(min(rest, math.MaxUint32))
		} else {
			// The bucket is empty or owned by the item.
			b.fp = fp
			b.count = uint32(min(uint64(b.count)+n, math.MaxUint32))
		}
		est = max(est, b.count)
	}

	// Update the heap. An estimate below the heap's minimum can't change it:
	// the item is either not in the top k, or already has a larger count.
	// An estimate of 0 means the item does not own any bucket: it's not counted yet.
	count := uint64(est)
	full := len(s.heap) == s.k
	if count == 0 || full && count <= s.heap[0].count {
		return
	}
	if i, ok := s.index[h]; ok {
		// The item is already in the heap; keep the largest estimate.
		// The estimated count in the buckets may decrease because of the decay,
		// but the true count never decreases.
		p := s.slots[i].pos
		if count > s.heap[p].count {
			s.heap[p].count = count
			s.down(p)
		}
		return
	}
	if !full {
		// Add a new item to the heap.
		i := uint32(len(s.slots))
		p := uint32(len(s.heap))
		s.slots = append(s.slots, slot{hash: h, pos: p, item: append([]byte(nil), item...)})
		s.heap = append(s.heap, node{count: count, slot: i})
		s.index[h] = i
		s.up(p)
		return
	}
	// Replace the item with the smallest count.
	root := &s.heap[0]
	sl := &s.slots[root.slot]
	delete(s.index, sl.hash)
	s.index[h] = root.slot
	sl.hash = h
	sl.item = append(sl.item[:0], item...)
	root.count = count
	s.down(0)
}

// Top returns up to k most frequent items, by Count descending
// (ties by Item ascending).
func (s *Sketch) Top() []Entry {
	entries := make([]Entry, len(s.heap))
	for i, nd := range s.heap {
		entries[i] = Entry{
			Item:  string(s.slots[nd.slot].item),
			Count: toInt(nd.count),
		}
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.Item, b.Item)
	})
	return entries
}

// Count returns the estimated count of the item if it's among the top k,
// or 0 otherwise. Counts of the top k items are always at least 1.
func (s *Sketch) Count(item []byte) int {
	i, ok := s.index[wyhash.Hash(item, wyhash.Seed0)]
	if !ok {
		return 0
	}
	return toInt(s.heap[s.slots[i].pos].count)
}

// Total returns the sum of all counts added.
func (s *Sketch) Total() int {
	return toInt(s.total)
}

// Size returns the approximate memory used by the sketch's data, in bytes.
func (s *Sketch) Size() int {
	const bucketSize = 8
	const nodeSize = 16         // count + slot, padded
	const slotSize = 8 + 8 + 24 // hash, pos + padding, slice header
	const indexSize = 20        // 12-byte entry padded to 16, control byte, ~7/8 load
	size := len(s.buckets)*bucketSize + s.k*(nodeSize+slotSize+indexSize)
	for i := range s.slots {
		size += cap(s.slots[i].item)
	}
	return size
}

// decay makes n attempts to decay a bucket with a given count owned by another
// item. Each attempt decrements the count with probability decayBase^count
// (counts of len(decayThr) and above never decay). Returns the new count,
// and if it reached zero, the number of attempts left including the one
// that zeroed it.
func (s *Sketch) decay(count uint32, n uint64) (uint32, uint64) {
	for n > 0 && count < uint32(len(decayThr)) {
		// Number of attempts until the next decrement.
		var t uint64
		if n == 1 {
			// Common case (Add): one attempt that succeeds
			// if a random number is below the threshold.
			if s.rand() >= decayThr[count] {
				return count, 0
			}
			t = 1
		} else {
			// Geometric distribution with p = decayBase^count.
			u := float64(s.rand()>>11+1) / (1 << 53) // (0, 1]
			tf := math.Floor(math.Log(u)/decayLog[count]) + 1
			if tf > float64(n) {
				return count, 0
			}
			t = uint64(tf)
		}
		count--
		if count == 0 {
			return 0, n - t + 1
		}
		n -= t
	}
	return count, 0
}

// rand returns a pseudo-random number (wyrand).
func (s *Sketch) rand() uint64 {
	s.rng += 0xa0761d6478bd642f
	hi, lo := bits.Mul64(s.rng, s.rng^0xe7037ed1a0b428db)
	return hi ^ lo
}

// up moves the node at pos up the heap until its parent's count is not larger.
func (s *Sketch) up(pos uint32) {
	h := s.heap
	nd := h[pos]
	for pos > 0 {
		parent := (pos - 1) / 2
		if h[parent].count <= nd.count {
			break
		}
		h[pos] = h[parent]
		s.slots[h[pos].slot].pos = pos
		pos = parent
	}
	h[pos] = nd
	s.slots[nd.slot].pos = pos
}

// down moves the node at pos down the heap until its children's counts
// are not smaller.
func (s *Sketch) down(pos uint32) {
	h := s.heap
	n := uint32(len(h))
	nd := h[pos]
	for {
		c := 2*pos + 1
		if c >= n {
			break
		}
		if r := c + 1; r < n && h[r].count < h[c].count {
			c = r
		}
		if h[c].count >= nd.count {
			break
		}
		h[pos] = h[c]
		s.slots[h[pos].slot].pos = pos
		pos = c
	}
	h[pos] = nd
	s.slots[nd.slot].pos = pos
}

// toInt converts a count to int, capped at [math.MaxInt]
// (matters only on 32-bit platforms).
func toInt(n uint64) int {
	return int(min(n, math.MaxInt))
}
