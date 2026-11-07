// Package hll implements HyperLogLog with Ertl's improved estimator.
//
// A sketch estimates the number of distinct items added to it (the
// cardinality), using a fixed amount of memory that depends only on the
// accuracy, not on the number of items.
//
// The sketch has m = 2^p one-byte registers, where p is the precision:
// 14 with [New], or between 8 and 18 with [NewPrecision]. The relative
// standard error of the estimate is stdErr = 1.04/√m: about 68% of estimates
// are within ±stdErr of the true count, 95% within ±2×stdErr, and 99.7%
// within ±3×stdErr. This holds for any cardinality, from zero up to billions.
//
// Each item's hash picks a register and a rank (the number of leading zeros of
// the rest of the hash, plus one), and the register keeps the largest rank
// it has seen. Adding an item twice has no effect.
//
// The estimate comes from the histogram of register values, using the
// improved estimator by Otmar Ertl ("New cardinality estimation algorithms
// for HyperLogLog sketches", 2017). Unlike the original HyperLogLog, it doesn't
// need to switch to linear counting for small cardinalities or use bias tables,
// and its bias is negligible over the whole range. The sketch keeps the
// histogram up to date as registers change, so [Sketch.Count] doesn't scan
// the registers.
//
// Sketches with the same precision can be merged: the result is the same
// as a single sketch that saw the items of both.
//
// The registers take 2^p bytes, and the histogram adds a fixed 256 bytes.
// Each +2 to p halves the error and quadruples the memory:
//
//	p     stdErr   registers
//	8     6.50%    256 B
//	10    3.25%    1 KB
//	12    1.63%    4 KB
//	14    0.81%    16 KB (default)
//	16    0.41%    64 KB
//	18    0.20%    256 KB
//
// Hashing is deterministic: wyhash final 4.3 with seed 0, so the same items
// always produce the same sketch. The sketch is not designed for
// adversarially chosen items: anyone who knows the seed can craft items
// that inflate the estimate.
package hll

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// Precision (log2 of the number of registers): default and range.
const (
	defaultP = 14
	// Below p=8 the error exceeds 1.04/√m and the bias is a few percent,
	// while the registers would save at most 240 bytes next to the
	// 256-byte histogram.
	minP = 8
	maxP = 18
)

// alphaInf is the asymptotic bias correction constant 1/(2 ln 2).
const alphaInf = 0.7213475204444817

// ErrPrecisionMismatch is returned by [Sketch.Merge] when
// the sketches have different precisions.
var ErrPrecisionMismatch = errors.New("hll: sketches have different precisions")

// Sketch estimates the number of distinct items added to it,
// with relative standard error about 1.04/√(2^p).
//
// Sketch is not safe for concurrent use if any goroutine calls Add or Merge.
type Sketch struct {
	regs []uint8
	// hist[k] is the number of registers with value k.
	// Values are at most 65-p, so 64 entries are enough.
	hist [64]uint32
	p    uint8
}

// New creates a sketch with the default precision p=14:
// 2^14 registers (16 KB), relative standard error about 0.81%.
func New() *Sketch {
	return NewPrecision(defaultP)
}

// NewPrecision creates a sketch with 2^p registers and relative standard
// error about 1.04/√(2^p). See the package docs for common values.
// Panics if p is not in [8, 18].
func NewPrecision(p int) *Sketch {
	if p < minP || p > maxP {
		panic("hll: p must be in [8, 18]")
	}
	m := 1 << p
	s := &Sketch{
		regs: make([]uint8, m),
		p:    uint8(p),
	}
	s.hist[0] = uint32(m)
	return s
}

// Add adds an item to the sketch.
func (s *Sketch) Add(item []byte) {
	s.addHash(wyhash.Hash(item, wyhash.Seed0))
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
	// alphaInf is exact only for large m. The usual finite-m correction
	// moves the bias at p=8 from +0.5% to ±0.1% at large n, and from +0.2%
	// to -0.2% at small n. At p ≥ 10 both are within ±0.1%.
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
// Returns [ErrPrecisionMismatch] if the sketches have different precisions.
func (s *Sketch) Merge(other *Sketch) error {
	if s.p != other.p {
		return ErrPrecisionMismatch
	}
	// Compare 8 registers at a time. Registers are below 128, so in
	// (x | 0x80…) - y each byte is x+128-y without borrowing from the next,
	// and its high bit is clear if and only if y > x. The number of registers
	// is a power of two ≥ 16, so there is no tail.
	const hi = 0x8080808080808080
	regs := s.regs[:len(other.regs)]
	for i := 0; i+8 <= len(regs); i += 8 {
		a, b := regs[i:i+8:i+8], other.regs[i:i+8:i+8]
		x := binary.LittleEndian.Uint64(a)
		y := binary.LittleEndian.Uint64(b)
		// gt has the high bit set in each byte where b > a.
		gt := ^((x | hi) - y) & hi
		if gt == 0 {
			continue
		}
		// For each register j that grew (one set bit per byte in gt),
		// move its histogram count from the old value to the new one.
		for g := gt; g != 0; g &= g - 1 {
			j := bits.TrailingZeros64(g) >> 3
			s.hist[a[j]&63]--
			s.hist[b[j]&63]++
		}
		// Take b's byte where it's larger: spread each flag to 0xff.
		mask := (gt >> 7) * 0xff
		binary.LittleEndian.PutUint64(a, x&^mask|y&mask)
	}
	return nil
}

// Size returns the memory used by the sketch's registers and histogram,
// in bytes.
func (s *Sketch) Size() int {
	return len(s.regs) + 4*len(s.hist)
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
