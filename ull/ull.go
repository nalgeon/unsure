// Package ull implements UltraLogLog, a more compact alternative to HyperLogLog.
//
// A sketch estimates the number of distinct items added to it (the
// cardinality), using a fixed amount of memory that depends only on the
// accuracy, not on the number of items.
//
// The sketch has m = 2^p one-byte registers, where p is the precision:
// 13 with [New], or between 8 and 18 with [NewPrecision]. The relative
// standard error of the estimate is stdErr = 0.782/√m: about 68% of estimates
// are within ±stdErr of the true count, 95% within ±2×stdErr, and 99.7%
// within ±3×stdErr. This holds for any cardinality, from zero up to billions.
//
// HyperLogLog with the same one-byte registers has stdErr = 1.04/√m,
// so UltraLogLog needs about 43% less memory for the same error.
//
// Each item's hash picks a register and a rank k (the number of
// leading zeros of the rest of the hash, plus one), as in HyperLogLog.
// Instead of keeping only the largest rank u it has seen, a register keeps
// u in its upper 6 bits, and whether u-1 and u-2 were also seen in the
// lower 2 bits. Adding an item twice has no effect.
//
// The estimate comes from the register values, using the FGRA estimator
// from the UltraLogLog paper (Otmar Ertl, 2024): a weighted sum of
// the registers, with special handling for nearly empty and nearly full
// sketches. It doesn't need bias tables, and its bias is negligible over
// the whole range. The sketch keeps a histogram of register values updated
// as the registers change, so [Sketch.Count] doesn't scan the registers.
//
// Sketches with the same precision can be merged: the result is the same
// as a single sketch that saw the items of both.
//
// The registers take 2^p bytes, and the histogram adds a fixed 1 KB.
// Each +2 to p halves the error and quadruples the memory:
//
//	p     stdErr   registers
//	8     4.89%    256 B
//	10    2.44%    1 KB
//	12    1.22%    4 KB
//	13    0.86%    8 KB (default)
//	14    0.61%    16 KB
//	16    0.31%    64 KB
//	18    0.15%    256 KB
//
// Hashing is deterministic, so the same items always produce the same sketch.
// The sketch is not designed for adversarially chosen items: anyone who knows
// the seed can create items that artificially increase the estimate.
package ull

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// Precision (log2 of the number of registers): default and range.
const (
	defaultP = 13
	minP     = 8
	maxP     = 18
)

// ErrMismatch is returned by [Sketch.Merge] when
// the sketches have different precisions.
var ErrMismatch = errors.New("ull: sketches have different precisions")

// Sketch estimates the number of distinct items added to it.
//
// Allocates memory at creation and never grows or shrinks.
// Not safe for concurrent use.
type Sketch struct {
	// A register is 0 if empty, or 4u + 2b₁ + b₀, where u is the largest
	// rank seen, b₁ is 1 if u-1 was seen, and b₀ if u-2 was seen.
	// Ranks are in [1, 65-p], so registers are at most 263-4p.
	regs []uint8
	// hist[r] is the number of registers with value r.
	hist [256]uint32
	p    uint8
}

// New creates a sketch with the default precision p=13:
// 2^13 registers (8 KB), relative standard error about 0.86%.
// That's close to HyperLogLog's common default (p=14, 0.81%)
// in half the memory.
func New() *Sketch {
	return NewPrecision(defaultP)
}

// NewPrecision creates a sketch with 2^p registers and relative standard
// error about 0.782/√(2^p). See the package docs for common values.
// Panics if p is not in [8, 18].
func NewPrecision(p int) *Sketch {
	if p < minP || p > maxP {
		panic("ull: p must be in [8, 18]")
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
	// The top p bits pick the register. The rank is the number of
	// leading zeros in the remaining 64-p bits, plus one. The sentinel bit
	// caps it at 65-p when all of them are zero.
	i := h >> (64 - s.p)
	k := bits.LeadingZeros64(h<<s.p|1<<(s.p-1)) + 1
	old := s.regs[i]
	r := pack(unpack(old) | 1<<k)
	// Whether the register changes depends on how full the sketch is,
	// and branching on it is slower than always writing. If it doesn't
	// change, the histogram updates cancel out.
	s.regs[i] = r
	s.hist[old]--
	s.hist[r]++
}

// unpack returns the set of ranks a register has seen,
// as a bitmask with bit k set for each rank k. Bit 0 (not a valid rank)
// is always set, so the empty register unpacks to 1.
func unpack(r uint8) uint64 {
	// Bits 2, 1, 0 of (4 | b₁b₀) are u, u-1, u-2. Shifting by u-2 would be
	// negative for u=1, so shift left by u, then right by 2. For the empty
	// register, it's bit 0.
	return uint64(4|r&3)<<(r>>2)>>2 | 1
}

// pack returns the register for a set of ranks from unpack:
// the largest rank u and whether u-1 and u-2 are in the set.
func pack(x uint64) uint8 {
	u := bits.Len64(x) - 1
	// For u ≤ 2, bit 0 lands in the history bits: clear it with 4>>u.
	// The empty register (u=0) packs to 0.
	return uint8(u<<2) | uint8(x<<2>>u)&3&^uint8(4>>u)
}

// FGRA estimator constants from the UltraLogLog paper, as computed in the
// reference implementation (hash4j). Register 4u+j contributes
// eta[j]·2^(-tau·u) to the sum, with tau chosen to minimize the error.
const (
	eta0 = 4.663135422063788
	eta1 = 2.1378502137958524
	eta2 = 2.781144650979996
	eta3 = 0.9824082545153715
	tau  = 0.8194911375910897
	// v is the asymptotic variance factor: stdErr = √(v/m).
	v = 0.6118931496978437

	etaX      = eta0 - eta1 - eta2 + eta3
	eta23X    = (eta2 - eta3) / etaX
	eta13X    = (eta1 - eta3) / etaX
	eta3012XX = (eta3*eta0 - eta1*eta2) / (etaX * etaX)
)

var (
	pow2Tau      = math.Pow(2, tau)
	pow2MinusTau = math.Pow(2, -tau)
	pow4MinusTau = math.Pow(4, -tau)
	phi1         = eta0 / (pow2Tau * (2*pow2Tau - 1))
	phiInitial   = etaX * pow4MinusTau / (2 - pow2MinusTau)
)

// contrib[r] is the contribution of a register with value r = 4u+j
// to the estimator sum. Count only uses it for 3 ≤ u < 65-p: smaller
// and saturated registers need the special terms.
var contrib = func() [256]float64 {
	var c [256]float64
	eta := [4]float64{eta0, eta1, eta2, eta3}
	for r := range c {
		c[r] = eta[r&3] * math.Pow(2, -tau*float64(r>>2))
	}
	return c
}()

// factors[p] scales the estimator sum to the estimate: m^(1+1/tau),
// times 1/(1 + v(1+tau)/(2m)) to remove the bias for finite m.
var factors = func() [maxP + 1]float64 {
	var f [maxP + 1]float64
	for p := minP; p <= maxP; p++ {
		m := float64(int(1) << p)
		f[p] = m * math.Pow(m, 1/tau) / (1 + v*(1+tau)/(2*m))
	}
	return f
}()

// Count returns the estimated number of distinct items added to the sketch.
func (s *Sketch) Count() int {
	est := s.estimate()
	if est >= math.MaxInt {
		// Only possible after ~2^63 items (2^31 on 32-bit platforms).
		return math.MaxInt
	}
	return int(math.Round(est))
}

// estimate returns the unrounded estimate of the number of distinct items.
func (s *Sketch) estimate() float64 {
	m := len(s.regs)
	w := 65 - int(s.p) // the largest rank
	h := &s.hist

	// Registers with ranks in [3, w-1] contribute by the table.
	// Four sums break the dependency chain of the additions.
	var sum0, sum1, sum2, sum3 float64
	for r := 12; r < 4*w; r += 4 {
		sum0 += float64(h[r]) * contrib[r]
		sum1 += float64(h[r+1]) * contrib[r+1]
		sum2 += float64(h[r+2]) * contrib[r+2]
		sum3 += float64(h[r+3]) * contrib[r+3]
	}
	sum := (sum0 + sum1) + (sum2 + sum3)

	// Registers that are empty or have u ≤ 2. Ranks 1 and 2 can
	// only leave registers 4 (u=1), 8 (u=2) and 10 (u=2, 1 seen).
	c0, c4, c8, c10 := h[0], h[4], h[8], h[10]
	if c0|c4|c8|c10 != 0 {
		z := smallRangeEstimate(c0, c4, c8, c10, m)
		sum += float64(c0)*sigma(z) +
			float64(c4)*pow2MinusTau*etaX*psiPrime(z, z*z) +
			float64(c8)*pow4MinusTau*(z*(eta0-eta1)+eta1) +
			float64(c10)*pow4MinusTau*(z*(eta2-eta3)+eta3)
	}

	// Saturated registers (u = w).
	c := h[4*w : 4*w+4]
	if c[0]|c[1]|c[2]|c[3] != 0 {
		sum += largeRangeContribution(c[0], c[1], c[2], c[3], m, w)
	}

	return factors[s.p] * math.Pow(sum, -1/tau)
}

// smallRangeEstimate estimates z = e^(-n/m) from the number of registers
// that are empty (c0) or only saw ranks 1 and 2 (c4, c8, c10).
func smallRangeEstimate(c0, c4, c8, c10 uint32, m int) float64 {
	alpha := float64(m) + 3*float64(c0+c4+c8+c10)
	beta := float64(m) - float64(c0) - float64(c4)
	gamma := 4*float64(c0) + 2*float64(c4) + 3*float64(c8) + float64(c10)
	z := (math.Sqrt(beta*beta+4*alpha*gamma) - beta) / (2 * alpha)
	z *= z
	return z * z
}

// largeRangeEstimate estimates z from the number of saturated registers
// with each combination of the two history bits.
func largeRangeEstimate(c0, c1, c2, c3 uint32, m int) float64 {
	alpha := float64(m) + 3*float64(c0+c1+c2+c3)
	beta := float64(c0+c1) + 2*float64(c2+c3)
	gamma := float64(m) + 2*float64(c0) + float64(c2) - float64(c3)
	return math.Sqrt((math.Sqrt(beta*beta+4*alpha*gamma) - beta) / (2 * alpha))
}

// largeRangeContribution is the contribution of the saturated registers
// to the estimator sum.
func largeRangeContribution(c0, c1, c2, c3 uint32, m, w int) float64 {
	z := largeRangeEstimate(c0, c1, c2, c3, m)
	rootZ := math.Sqrt(z)
	s := phi(rootZ, z) * float64(c0+c1+c2+c3)
	s += z * (1 + rootZ) * (float64(c0)*eta0 + float64(c1)*eta1 + float64(c2)*eta2 + float64(c3)*eta3)
	s += rootZ * pow2MinusTau * (float64(c0+c1)*(z*(eta0-eta2)+eta2) +
		float64(c2+c3)*(z*(eta1-eta3)+eta3))
	return s * math.Pow(pow2MinusTau, float64(w)) / ((1 + rootZ) * (1 + z))
}

// psiPrime is ψ(z)/etaX from the paper, with zz = z².
func psiPrime(z, zz float64) float64 {
	return (z+eta23X)*(zz+eta13X) + eta3012XX
}

// sigma is the contribution of an empty register, for z in [0, 1].
// It's +Inf at z = 1 (empty sketch), which makes the estimate 0.
func sigma(z float64) float64 {
	if z <= 0 {
		return eta3
	}
	if z >= 1 {
		return math.Inf(1)
	}
	powZ := z
	nextPowZ := z * z
	s := 0.0
	powTau := etaX
	for {
		prev := s
		nextNextPowZ := nextPowZ * nextPowZ
		s += powTau * (powZ - nextPowZ) * psiPrime(nextPowZ, nextNextPowZ)
		if !(s > prev) {
			return s / z
		}
		powZ = nextPowZ
		nextPowZ = nextNextPowZ
		powTau *= pow2Tau
	}
}

// phi is part of the contribution of saturated registers,
// for z in [0, 1] with zz = z².
func phi(z, zz float64) float64 {
	if z <= 0 {
		return 0
	}
	if z >= 1 {
		return phi1
	}
	prevPowZ := zz
	powZ := z
	nextPowZ := math.Sqrt(powZ)
	p := phiInitial / (1 + nextPowZ)
	ps := psiPrime(powZ, prevPowZ)
	s := nextPowZ * (ps + ps) * p
	for {
		prevPowZ = powZ
		powZ = nextPowZ
		prev := s
		nextPowZ = math.Sqrt(powZ)
		nextPs := psiPrime(powZ, prevPowZ)
		p *= pow2MinusTau / (1 + nextPowZ)
		s += nextPowZ * ((nextPs + nextPs) - (powZ+nextPowZ)*ps) * p
		if !(s > prev) {
			return s
		}
		ps = nextPs
	}
}

// Merge adds the items of other to s, so that s estimates the number of
// distinct items added to either. Other is unchanged.
// Returns [ErrMismatch] if the sketches have different precisions.
func (s *Sketch) Merge(other *Sketch) error {
	if s.p != other.p {
		return ErrMismatch
	}
	// Check 8 registers at a time (x from s, y from other) for the ones
	// y changes, so the slow path runs only for those. The number of
	// registers is a power of two ≥ 256, so there is no tail.
	const (
		lo  = 0x0101010101010101
		hi  = 0x8080808080808080
		low = 0x7f7f7f7f7f7f7f7f
	)
	// nonzero and zero set the high bit of each nonzero or zero byte of v.
	nonzero := func(v uint64) uint64 { return (v&low + low | v) & hi }
	zero := func(v uint64) uint64 { return ^(v&low + low | v) & hi }
	regs := s.regs[:len(other.regs)]
	for i := 0; i+8 <= len(regs); i += 8 {
		a, b := regs[i:i+8:i+8], other.regs[i:i+8:i+8]
		x := binary.LittleEndian.Uint64(a)
		y := binary.LittleEndian.Uint64(b)
		if x == y {
			continue
		}
		// The u of each byte is below 64, so each byte of t is
		// 128 + ux - uy without borrowing from the next one.
		t := (x>>2&(0x3f*lo) | hi) - y>>2&(0x3f*lo)
		// uy > ux: always a change.
		g := ^t & hi
		// Otherwise d = ux - uy (the low bits of t), and y changes x only
		// if y's ranks uy, uy-1, uy-2 include one x hasn't seen and that
		// is not below ux-2. As 3-bit windows (bit 2 is u, bits 1 and 0
		// the history), that's (wy >> d) &^ wx != 0 for d ≤ 2. Only the
		// low 3 bits of wx matter. An empty y has an empty window, and an
		// empty x with a non-empty y is covered by uy > ux.
		wx := x | 4*lo
		wy := y&(3*lo) | nonzero(y)>>5
		g |= nonzero(wy&^wx) & zero(t^hi)
		g |= nonzero(wy>>1&(3*lo)&^wx) & zero(t^(hi|lo))
		g |= nonzero(wy>>2&lo&^wx) & zero(t^(hi|2*lo))
		for ; g != 0; g &= g - 1 {
			j := bits.TrailingZeros64(g) >> 3
			old := a[j]
			r := pack(unpack(old) | unpack(b[j]))
			s.hist[old]--
			s.hist[r]++
			a[j] = r
		}
	}
	return nil
}

// Size returns the memory used by the sketch's data, in bytes.
func (s *Sketch) Size() int {
	return len(s.regs) + 4*len(s.hist)
}
