// Package countmin implements a Count-min sketch with conservative update.
//
// A sketch estimates how many times each item was added. The estimate is
// never less than the actual count, and is higher by more than epsilon*N
// with probability at most delta, where epsilon and delta are the parameters
// passed to [New] and N is the sum of all counts added. Estimates are capped
// at [math.MaxUint32] (about 4.3 billion): if an item is added more times
// than that, it is reported as MaxUint32.
//
// The sketch uses d = ⌈ln(1/delta)⌉ rows, each with w = ⌈e/epsilon⌉ uint32
// counters. Each item maps to one counter in each row, and its estimate is
// the smallest value among them. Add and AddN use a conservative update:
// it only increases the item's counters that are below the new estimate.
// This keeps the same accuracy guarantee, but for skewed data, the actual
// error is much lower than with regular increments. The tradeoff is that
// counts can't be decreased.
//
// To choose epsilon, note that the error is relative to the total N, not to
// each item's count. With epsilon = 0.001 and N = 1 million, estimates may be
// off by up to 1000, so counts much smaller than epsilon*N are lost in noise.
// Pick epsilon below the smallest fraction of N you care about.
//
// Delta is the probability that a given estimate exceeds that bound;
// 0.01 is a common choice, and lowering it is cheap because d grows
// with ln(1/delta).
//
// The sketch takes about 4*d*w bytes: ~55 KB for (ε=0.001, δ=0.01),
// ~540 KB for (ε=0.0001, δ=0.01), and ~5.4 MB for (ε=0.00001, δ=0.01).
//
// Hashing is deterministic: wyhash final 4.3 with seed 0, so the same items
// always produce the same sketch. The sketch is not designed for adversarially
// chosen items: anyone who knows the seed can create items that artificially
// increase other items' estimates.
package countmin

import (
	"math"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// maxDepth is the max number of rows.
const maxDepth = 16

// salts are random odd constants. Row i takes the top 32 bits of the item's
// hash times salts[i], so each row gets its own position from one hash.
var salts = [maxDepth]uint64{
	0x6ca123e582c49bc7, 0xd9f3b4a63fd8553b, 0xc9528447c21b47c3, 0x8dbf3116c2d5335b,
	0xdb34adea38a5d947, 0x69deb2bc639f0119, 0xc002a6893ce6d15d, 0xe383cf925fa01f87,
	0xca0c180d02251aed, 0x6c3b736d3fb488a7, 0xa2b2173e8bea10f3, 0x412dfdc8054605b3,
	0x6ce759a982ac9dbb, 0x5fbc8e3af21e5baf, 0x19ed312bbbe1edef, 0xfc8cab916c79f08d,
}

// Sketch is a count-min sketch: Count may overestimate (by more than
// epsilon*Total with probability at most delta), but never underestimates.
//
// Sketch is not safe for concurrent use if any goroutine calls Add or AddN.
type Sketch struct {
	counters []uint32 // d rows of width counters, row i at [i*width, (i+1)*width)
	width    uint64
	salts    []uint64 // salts[:d]
	total    uint64
}

// New creates a sketch whose estimates exceed the true count
// by more than epsilon*N with probability at most delta.
// Panics if epsilon or delta is not in (0, 1).
func New(epsilon, delta float64) *Sketch {
	if !(epsilon > 0 && epsilon < 1) {
		panic("countmin: epsilon must be in (0, 1)")
	}
	if !(delta > 0 && delta < 1) {
		panic("countmin: delta must be in (0, 1)")
	}
	d, w := params(epsilon, delta)
	if d > maxDepth {
		panic("countmin: delta is too small")
	}
	// The counters' size in bytes must fit in an int (matters on 32-bit).
	if w > math.MaxUint32 || uint64(d)*w > math.MaxInt/4 {
		panic("countmin: sketch is too large")
	}
	// The allocator rounds the allocation up to its size class (or whole
	// pages for large sizes). Appending to a nil slice reports the rounded
	// capacity, so widen the rows to use it all: lower error for free.
	counters := append([]uint32(nil), make([]uint32, uint64(d)*w)...)
	// Index math needs width <= 2^32 (see index), so cap it.
	w = min(uint64(cap(counters)/d), math.MaxUint32)
	return &Sketch{
		counters: counters[:uint64(d)*w],
		width:    w,
		salts:    salts[:d],
	}
}

// Add adds 1 to the item's count.
func (s *Sketch) Add(item []byte) {
	s.AddN(item, 1)
}

// AddN adds n to the item's count.
func (s *Sketch) AddN(item []byte, n uint32) {
	h := wyhash.Hash(item, wyhash.Seed0)
	var idx [maxDepth]uint64
	est := uint32(math.MaxUint32)
	for i := range s.salts {
		j := s.index(h, i)
		idx[i&(maxDepth-1)] = j
		est = min(est, s.counters[j])
	}
	v := est + n
	if v < est {
		v = math.MaxUint32
	}
	for i := range s.salts {
		j := idx[i&(maxDepth-1)]
		s.counters[j] = max(s.counters[j], v)
	}
	s.total += uint64(n)
}

// Count returns the estimated count of the item.
// It's never below the true count, except that counts above
// [math.MaxUint32] are reported as MaxUint32.
func (s *Sketch) Count(item []byte) uint32 {
	h := wyhash.Hash(item, wyhash.Seed0)
	est := uint32(math.MaxUint32)
	for i := range s.salts {
		est = min(est, s.counters[s.index(h, i)])
	}
	return est
}

// Total returns the sum of all counts added (N in the error bound).
func (s *Sketch) Total() uint64 {
	return s.total
}

// Size returns the memory used by the sketch's counters, in bytes.
func (s *Sketch) Size() int {
	return len(s.counters) * 4
}

// index returns the position of the item with hash h in row i.
func (s *Sketch) index(h uint64, i int) uint64 {
	return uint64(i)*s.width + (h*s.salts[i]>>32)*s.width>>32
}

// params returns the number of rows d and counters per row w that give
// error <= epsilon*N with probability >= 1-delta.
//
// Per row, the expected excess of an item's counter is at most N/w <= epsilon*N/e,
// so by Markov's inequality it exceeds epsilon*N with probability at most 1/e.
// The estimate exceeds it only if all d rows do: at most e^-d <= delta.
func params(epsilon, delta float64) (d int, w uint64) {
	d = max(int(math.Ceil(math.Log(1/delta))), 1)
	wf := math.Ceil(math.E / epsilon)
	if wf > math.MaxUint32 {
		return d, math.MaxUint32 + 1
	}
	return d, uint64(wf)
}
