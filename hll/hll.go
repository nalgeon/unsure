// Package hll implements HyperLogLog with Ertl's improved estimator.
//
// A sketch estimates the number of distinct items added to it (the
// cardinality), using a fixed amount of memory that depends only on the
// accuracy, not on the number of items. The relative standard error of the
// estimate is at most stdErr, the parameter passed to [New]: about 68% of
// estimates are within ±stdErr of the true count, 95% within ±2×stdErr, and
// 99.7% within ±3×stdErr. This holds for any cardinality, from zero up to
// billions.
//
// The sketch has m = 2^p one-byte registers, with p chosen so that the
// standard error 1.04/√m is at most stdErr (p is between 4 and 18). Each
// item's hash picks a register and a rank (the number of leading zeros of
// the rest of the hash, plus one), and the register keeps the largest rank
// it has seen. Adding an item twice has no effect.
//
// The estimate comes from the histogram of register values, using the
// improved estimator by Otmar Ertl ("New cardinality estimation algorithms
// for HyperLogLog sketches", 2017). Unlike the original HyperLogLog, it doesn't
// need to switch to linear counting for small cardinalities or use bias tables,
// and it's unbiased over the whole range. The sketch keeps the histogram up
// to date as registers change, so [Sketch.Count] doesn't scan the registers.
//
// Sketches with the same precision and seed can be merged: the result is
// the same as a single sketch that saw the items of both.
//
// The sketch takes 2^p bytes: 1 KB at stdErr = 0.0325 (p=10), 16 KB at
// 0.0082 (p=14), and 256 KB at 0.0021 (p=18).
//
// Hashing is deterministic: wyhash final 4.3 with a fixed seed (0 unless set
// in [Options]), so the same items always produce the same sketch. The sketch
// is not designed for adversarially chosen items: anyone who knows the seed
// can craft items that inflate the estimate.
package hll

import (
	"errors"
	"math"
	"math/bits"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// Range of precisions (log2 of the number of registers).
const (
	minP = 4
	maxP = 18
)

// alphaInf is the asymptotic bias correction constant 1/(2 ln 2).
const alphaInf = 0.7213475204444817

// Errors returned by Sketch methods.
var (
	ErrPrecisionMismatch = errors.New("hll: sketches have different precisions")
	ErrSeedMismatch      = errors.New("hll: sketches have different seeds")
)

// Sketch estimates the number of distinct items added to it,
// with relative standard error at most stdErr.
//
// Sketch is not safe for concurrent use if any goroutine calls Add or Merge.
type Sketch struct {
	regs []uint8
	// hist[k] is the number of registers with value k.
	// Values are at most 65-p, so 64 entries are enough.
	hist [64]uint32
	p    uint8
	seed uint64 // premixed with wyhash.Seed
}

// Options configures a Sketch. The zero value gives the defaults.
type Options struct {
	// Seed for the hash function. Default 0.
	// Sketches must have the same seed to be merged.
	Seed uint64
}

// New creates a sketch with relative standard error at most stdErr,
// using the default options.
// Panics if stdErr is not in (0, 1) or is below 1.04/512 ≈ 0.00203
// (2^18 registers).
func New(stdErr float64) *Sketch {
	return NewWith(stdErr, Options{})
}

// NewWith is like [New], but with the given options.
func NewWith(stdErr float64, opts Options) *Sketch {
	if !(stdErr > 0 && stdErr < 1) {
		panic("hll: stdErr must be in (0, 1)")
	}
	p := precision(stdErr)
	if p > maxP {
		panic("hll: stdErr is too small")
	}
	m := 1 << p
	s := &Sketch{
		regs: make([]uint8, m),
		p:    uint8(p),
		seed: wyhash.Seed(opts.Seed),
	}
	s.hist[0] = uint32(m)
	return s
}

// Add adds an item to the sketch.
func (s *Sketch) Add(item []byte) {
	s.addHash(wyhash.Hash(item, s.seed))
}

// addHash adds an item with hash h.
func (s *Sketch) addHash(h uint64) {
	// The top p bits pick the register. The rank is the number of leading
	// zeros in the remaining 64-p bits, plus one. The sentinel bit caps it
	// at 65-p when all of them are zero.
	i := h >> (64 - s.p)
	r := uint8(bits.LeadingZeros64(h<<s.p|1<<(s.p-1))) + 1
	old := s.regs[i]
	if r > old {
		s.regs[i] = r
		s.hist[old&63]--
		s.hist[r&63]++
	}
}

// Count returns the estimated number of distinct items added to the sketch.
func (s *Sketch) Count() uint64 {
	m := float64(len(s.regs))
	q := 64 - int(s.p) // registers are in [0, q+1]
	// Ertl's improved estimator (Algorithm 6 in the paper).
	z := m * tau(1-float64(s.hist[q+1])/m)
	for k := q; k >= 1; k-- {
		z = 0.5 * (z + float64(s.hist[k]))
	}
	z += m * sigma(float64(s.hist[0])/m)
	// alphaInf is exact only for large m. With the usual finite-m correction,
	// the bias is +7% → 0.3% at p=4 and +1.7% → 0.1% at p=6.
	alpha := alphaInf / (1 + 1.079/m)
	est := alpha * m * m / z
	if est >= math.MaxUint64 {
		// All registers saturated: only possible after ~2^64 items.
		return math.MaxUint64
	}
	return uint64(math.Round(est))
}

// Merge adds the items of other to s, so that s estimates the number of
// distinct items added to either. Other is unchanged.
// Returns an error if the sketches have different precisions or seeds.
func (s *Sketch) Merge(other *Sketch) error {
	if s.p != other.p {
		return ErrPrecisionMismatch
	}
	if s.seed != other.seed {
		return ErrSeedMismatch
	}
	regs := s.regs[:len(other.regs)]
	for i, r := range other.regs {
		old := regs[i]
		if r > old {
			regs[i] = r
			s.hist[old&63]--
			s.hist[r&63]++
		}
	}
	return nil
}

// Size returns the memory used by the sketch's registers, in bytes.
func (s *Sketch) Size() int {
	return len(s.regs)
}

// precision returns the smallest p >= minP with 1.04/√(2^p) <= stdErr.
// It may be above maxP.
func precision(stdErr float64) int {
	x := 1.04 / stdErr
	// Tolerance so that stdErr = 1.04/2^(p/2) gives exactly p.
	p := int(math.Ceil(math.Log2(x*x) - 1e-9))
	return max(p, minP)
}

// sigma(x) = x + Σ_{k≥1} x^(2^k) 2^(k-1), for x in [0, 1].
// It's +Inf at x = 1 (empty sketch), which makes the estimate 0.
func sigma(x float64) float64 {
	if x == 1 {
		return math.Inf(1)
	}
	y := 1.0
	z := x
	for {
		x *= x
		prev := z
		z += x * y
		y += y
		if z == prev {
			return z
		}
	}
}

// tau(x) = (1 - x - Σ_{k≥1} (1 - x^(2^-k))² 2^-k) / 3, for x in [0, 1].
func tau(x float64) float64 {
	if x == 0 || x == 1 {
		return 0
	}
	y := 1.0
	z := 1 - x
	for {
		x = math.Sqrt(x)
		prev := z
		y *= 0.5
		z -= (1 - x) * (1 - x) * y
		if z == prev {
			return z / 3
		}
	}
}
