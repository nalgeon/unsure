// Package countmin implements a count-min sketch with conservative update.
//
// A sketch estimates how many times each item was added. The estimate is
// never below the true count, and is above it by more than epsilon*N
// with probability at most delta, where N is the total of all counts added.
// Estimates are capped at [math.MaxUint32] (about 4.3 billion): an item
// added more times than that is reported as MaxUint32.
//
// The sketch is d = ⌈ln(1/delta)⌉ rows of w = ⌈e/epsilon⌉ uint32 counters.
// Each item maps to one counter per row, and its estimate is the smallest
// of them. Add uses conservative update: it raises only the item's counters
// that would otherwise be below the new estimate. This keeps the same
// guarantee, but on skewed data the actual error is much lower than with
// plain increments. The tradeoff is that counts can't be decremented.
//
// Hashing is deterministic: wyhash final 4.3 with a fixed seed
// (0 unless set in [Options]), so the same items always produce the same
// sketch. The sketch is not designed for adversarially chosen items:
// anyone who knows the seed can craft items that inflate others' estimates.
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
// Sketch is not safe for concurrent use if any goroutine calls Add.
type Sketch struct {
	counters []uint32 // d rows of width counters, row i at [i*width, (i+1)*width)
	width    uint64
	salts    []uint64 // salts[:d]
	seed     uint64   // premixed with wyhash.Seed
	total    uint64
}

// Options configures a Sketch. The zero value gives the defaults.
type Options struct {
	// Seed for the hash function. Default 0.
	// Sketches must have the same seed to be merged or compared.
	Seed uint64
}

// New creates a sketch whose estimates exceed the true count
// by more than epsilon*N with probability at most delta,
// using the default options.
// Panics if epsilon or delta is not in (0, 1).
func New(epsilon, delta float64) *Sketch {
	return NewWith(epsilon, delta, Options{})
}

// NewWith is like [New], but with the given options.
func NewWith(epsilon, delta float64, opts Options) *Sketch {
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
		seed:     wyhash.Seed(opts.Seed),
	}
}

// Add adds n to the item's count.
func (s *Sketch) Add(item []byte, n uint32) {
	h := wyhash.Hash(item, s.seed)
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
	h := wyhash.Hash(item, s.seed)
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
