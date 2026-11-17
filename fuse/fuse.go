// Package fuse implements a binary fuse filter: a static, compact probabilistic set.
//
// Like a Bloom filter, the filter answers "is this item in the set?". A "no" is
// always correct. A "yes" is wrong with probability 2^-b, where b is the number
// of bits in the fingerprint type T: about 0.39% for uint8, 0.0015% for uint16,
// and 2.3e-10 for uint32.
//
// Unlike a Bloom filter, the filter is static: it's built once from all
// the items, and you can't add more later. In exchange, it uses less memory:
// about 1.125·b bits per item for large sets (9 bits for uint8). A Bloom
// filter needs about 1.44·log2(1/p) bits per item in theory, and unsure's
// blocked Bloom filter takes about 12 bits at p = 0.39%.
//
// Each item maps to 3 cells in an array of b-bit fingerprints, located in 3
// consecutive segments of the array. The cells are filled so that their XOR equals
// the item's fingerprint, and Contains just checks this condition.
//
// The filter is deterministic, so the same items always produce the same filter.
// The filter is not designed for adversarially chosen items: anyone who knows the
// hash function can artificially increase false positives.
package fuse

import (
	"iter"
	"math"
	"math/bits"
	"slices"
	"unsafe"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// maxAttempts is how many times New tries to build the filter before giving up.
// With unique hashes, an attempt fails with probability well below 1/2
// (usually far below), so reaching it means a bug.
const maxAttempts = 100

// maxSegLen caps the segment length (from the reference implementation).
const maxSegLen = 1 << 18

// Filter is a static probabilistic set: Contains may report false positives
// but never false negatives. T sets the fingerprint size, and with it
// the false positive rate (2^-bits) and memory use (about 1.125·bits per item).
//
// Immutable after creation, so safe for concurrent use.
type Filter[T uint8 | uint16 | uint32] struct {
	layout
	fps []T // (segCount+2)·segLen fingerprints
}

// layout maps a hash to the 3 cells of an item.
type layout struct {
	seed        uint64 // added to the item hash before mixing; differs per build attempt
	segLen      uint32 // segment length, a power of 2
	segMask     uint32 // segLen - 1
	segCountLen uint32 // number of segments where the first cell can be, times segLen
}

// New builds a filter from the items. Duplicates are allowed.
//
// Items are hashed as they're read and not retained, so the iterator may
// reuse the same buffer for every item.
//
// Panics if there are too many items (more than about 3.8 billion).
func New[T uint8 | uint16 | uint32](items iter.Seq[[]byte]) *Filter[T] {
	var hashes []uint64
	for item := range items {
		hashes = append(hashes, wyhash.Hash(item, wyhash.Seed0))
	}
	f, _ := build[T](hashes)
	return f
}

// Contains estimates whether the item is in the filter.
// False means the item is definitely not in the filter, true means
// it's probably in the filter, with probability of a false positive 2^-bits
// for a T with that many bits.
func (f *Filter[T]) Contains(item []byte) bool {
	h := mix(wyhash.Hash(item, wyhash.Seed0) + f.seed)
	h0, h1, h2 := f.positions(h)
	return T(fingerprint(h)) == f.fps[h0]^f.fps[h1]^f.fps[h2]
}

// Size returns the memory used by the filter's data, in bytes.
func (f *Filter[T]) Size() int {
	return len(f.fps) * int(unsafe.Sizeof(T(0)))
}

// build builds a filter from item hashes and returns it along with
// the number of attempts it took. May reorder and shrink hashes.
func build[T uint8 | uint16 | uint32](hashes []uint64) (*Filter[T], int) {
	var b builder
	deduped := false
	for attempt := 1; ; attempt++ {
		if attempt > maxAttempts {
			panic("fuse: failed to build the filter")
		}
		l := newLayout(len(hashes))
		l.seed = uint64(attempt-1) * 0x9e3779b97f4a7c15
		if b.peel(&l, hashes) {
			return &Filter[T]{layout: l, fps: assign[T](&l, b.stack, b.found)}, attempt
		}
		// Duplicate hashes can never be peeled. Remove them once,
		// then keep retrying with new seeds.
		if !deduped {
			slices.Sort(hashes)
			hashes = slices.Compact(hashes)
			deduped = true
		}
	}
}

// newLayout sizes the filter for n hashes. The constants are from
// the reference implementation.
func newLayout(n int) layout {
	segLen := uint32(4)
	if n > 0 {
		segLen = 1 << int(math.Floor(math.Log(float64(n))/math.Log(3.33)+2.25))
		segLen = min(segLen, maxSegLen)
	}
	capacity := 0.0
	if n > 1 {
		sizeFactor := max(1.125, 0.875+0.25*math.Log(1e6)/math.Log(float64(n)))
		capacity = math.Round(float64(n) * sizeFactor)
	}
	segCount := max(math.Ceil(capacity/float64(segLen)), 3) - 2
	if (segCount+2)*float64(segLen) > math.MaxUint32 {
		panic("fuse: too many items")
	}
	return layout{
		segLen:      segLen,
		segMask:     segLen - 1,
		segCountLen: uint32(segCount) * segLen,
	}
}

// size returns the number of cells.
func (l *layout) size() int {
	return int(l.segCountLen) + 2*int(l.segLen)
}

// positions returns the 3 cells for a mixed hash: one in each of
// 3 consecutive segments, the first one chosen by the high bits of h.
func (l *layout) positions(h uint64) (h0, h1, h2 uint32) {
	hi, _ := bits.Mul64(h, uint64(l.segCountLen))
	h0 = uint32(hi)
	h1 = (h0 + l.segLen) ^ (uint32(h>>18) & l.segMask)
	h2 = (h0 + 2*l.segLen) ^ (uint32(h) & l.segMask)
	return h0, h1, h2
}

// builder holds the temporary memory for building a filter,
// reused across attempts.
type builder struct {
	starts []uint32 // counting sort: start of each bucket
	stack  []uint64 // hashes sorted by bucket, then the peeling order
	found  []uint8  // which of its 3 cells each stacked hash was peeled from
	count  []uint8  // number of hashes in each cell
	xor    []uint64 // XOR of the hashes in each cell
	queue  []uint32 // cells with a single hash
}

// peel finds an order to assign the hashes in: each hash in b.stack has a cell
// (b.found) that none of the hashes before it use. Returns false if there is
// no such order (the attempt failed).
func (b *builder) peel(l *layout, hashes []uint64) bool {
	n := len(hashes)
	size := l.size()

	// Sort the mixed hashes by their first cell, roughly, so that
	// the counting loop below walks the cell array in order.
	bucketBits := bits.Len32(l.segCountLen / l.segLen) // 2^bucketBits >= segCount
	shift := 64 - bucketBits
	b.starts = resize(b.starts, 1<<bucketBits+1)
	for _, h := range hashes {
		b.starts[mix(h+l.seed)>>shift+1]++
	}
	for i := 1; i < len(b.starts); i++ {
		b.starts[i] += b.starts[i-1]
	}
	b.stack = resize(b.stack, n)
	for _, h := range hashes {
		m := mix(h + l.seed)
		i := m >> shift
		b.stack[b.starts[i]] = m
		b.starts[i]++
	}

	// Count the hashes in each cell and XOR them together.
	b.count = resize(b.count, size)
	b.xor = resize(b.xor, size)
	count, xor := b.count, b.xor
	for _, h := range b.stack {
		h0, h1, h2 := l.positions(h)
		count[h0]++
		count[h1]++
		count[h2]++
		xor[h0] ^= h
		xor[h1] ^= h
		xor[h2] ^= h
		if count[h0] == 0 || count[h1] == 0 || count[h2] == 0 {
			return false // 256 hashes in a cell: many duplicates
		}
	}

	// Peel: take a cell with a single hash (now in xor), push the hash
	// to the stack, and remove it from its other 2 cells. Repeat until no
	// such cell is left.
	//
	// A cell enters the queue when its count is 1: at the start, or when
	// it drops from 2. So at most size cells enter it in total. Pushes are
	// branchless: always write, advance only if the count is 1.
	b.queue = resize(b.queue, size+1)
	queue := b.queue
	q := 0
	for i, c := range count {
		queue[q] = uint32(i)
		if c == 1 {
			q++
		}
	}
	stack := b.stack // the sorted hashes are no longer needed
	b.found = resize(b.found, n)
	found := b.found
	s := 0
	for q > 0 {
		q--
		i := queue[q]
		if count[i] != 1 {
			continue // emptied since it was queued
		}
		count[i] = 0
		h := xor[i]
		h0, h1, h2 := l.positions(h)
		// The other 2 cells, and which of the 3 cells i is.
		a, c, f := h1, h2, uint8(0)
		if i == h1 {
			a, f = h0, 1
		} else if i == h2 {
			c, f = h0, 2
		}
		stack[s] = h
		found[s] = f
		s++

		count[a]--
		xor[a] ^= h
		queue[q] = a
		if count[a] == 1 {
			q++
		}
		count[c]--
		xor[c] ^= h
		queue[q] = c
		if count[c] == 1 {
			q++
		}
	}
	b.stack = stack[:s]
	b.found = found[:s]
	return s == n
}

// assign fills the fingerprints in reverse peeling order: each hash sets its
// own cell so that its 3 cells XOR to its fingerprint. Its own cell is still
// zero, and no hash assigned later uses it.
func assign[T uint8 | uint16 | uint32](l *layout, stack []uint64, found []uint8) []T {
	fps := make([]T, l.size())
	for i := len(stack) - 1; i >= 0; i-- {
		h := stack[i]
		h0, h1, h2 := l.positions(h)
		p := [3]uint32{h0, h1, h2}[found[i]]
		fps[p] = T(fingerprint(h)) ^ fps[h0] ^ fps[h1] ^ fps[h2]
	}
	return fps
}

// mix is the murmur3 64-bit finalizer. It turns the item hash plus a seed
// into a new hash, so a failed build can retry without rehashing the items.
func mix(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// fingerprint returns the bits of h that the filter stores (after truncating to T).
func fingerprint(h uint64) uint64 {
	return h ^ h>>32
}

// resize returns a zeroed slice of length n, reusing buf if it's large enough.
func resize[E any](buf []E, n int) []E {
	if cap(buf) < n {
		return make([]E, n)
	}
	buf = buf[:n]
	clear(buf)
	return buf
}
