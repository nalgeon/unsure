// Package loghist implements a log-linear histogram.
//
// A histogram estimates quantiles (like the median or 99th percentile)
// and the CDF for a stream of numbers. It counts values in buckets,
// and the width of each bucket increases as the numbers get larger.
// This way, every quantile estimate is within a fixed relative error
// of the true value, from the median up to the maximum.
//
// The error is measured in values: with the default 0.39%, if the true
// 99th percentile is 200 ms, Quantile(0.99) returns a value between
// 199.2 and 200.8 ms. The relative error is set with [NewError]:
//
//	requested   max error   memory (values from 1 to 1e6)
//	0.1         5.9%        1.3 KB
//	0.01        0.78%       10 KB
//	0.005       0.39%       20 KB    (default)
//	0.001       0.098%      80 KB
//	0.0001      0.0061%     1.3 MB
//
// The histogram divides the range of values into buckets and counts
// how many values fall into each one. Each power of two (an octave, such as
// 4 to 8) is split into 2^m buckets of equal width. The default is m=7,
// 128 buckets per octave.
//
// The error guarantee applies to absolute values in the range [lo, hi), which
// is 1e-9 to 1e18 by default (set with [NewRange]). Values smaller than lo are
// treated as zero. Values at or above ±hi are treated as overflow: quantiles
// that land on these return the exact minimum or maximum. NaN and infinite
// values are ignored.
//
// Memory depends on the range of the values, not on their number: 8·2^m bytes
// per octave between the smallest and the largest absolute value in [lo, hi),
// separately for positive and negative values. The buckets are allocated
// as the range grows, and never shrink. The range limits the memory: with
// the default range and error, at most 90 octaves or 90 KB per sign.
//
// Quantile(0) and Quantile(1) return the exact minimum and maximum.
//
// Histograms with the same relative error and range can be merged. Merging
// is exact: the result is the same as adding all values to one histogram.
package loghist

import (
	"errors"
	"math"
)

// Mantissa bits per bucket: default and minimum. The relErr limit
// of 0.0001 gives at most 13 bits (64 KB per octave and sign).
const (
	defaultBits = 7
	minBits     = 3
)

// Default range of absolute values with the relative error bound.
const (
	defaultLo = 1e-9
	defaultHi = 1e18
)

const (
	signBit = 1 << 63
	// Bits of the smallest normal float64. Subnormals always count as zero.
	minNormal = 1 << 52
)

// ErrMismatch is returned by [Histogram.Merge] when the histograms
// have different relative errors or ranges.
var ErrMismatch = errors.New("loghist: histograms have different parameters")

// Histogram estimates quantiles of a stream of numbers.
//
// Allocates memory as the range of the values grows.
// Not safe for concurrent use.
type Histogram struct {
	// Buckets for positive values and for the absolute values of negative ones.
	pos, neg store
	// Number of values closer to zero than lo.
	zero uint64
	// Number of values added.
	count uint64
	// Exact minimum and maximum of all values, ±Inf if empty.
	min, max float64
	// Bits of the absolute values lo and hi: values in [lo, hi) go to the buckets.
	lo, hi uint64
	// The bucket index of a value is its bits >> shift, shift = 52 - m.
	shift uint
}

// store counts values in a dense range of buckets.
type store struct {
	// counts[j] is the number of values in bucket offset+j.
	// Covers whole octaves: offset and len are multiples of 2^m.
	counts []uint64
	offset int
	// Number of values with absolute values hi or more.
	over uint64
}

// New creates a histogram with the default relative error of 0.39% (1/257)
// for absolute values from 1e-9 to 1e18.
func New() *Histogram {
	return newHist(defaultBits, defaultLo, defaultHi)
}

// NewError creates a histogram with a relative error of at most relErr
// for absolute values from 1e-9 to 1e18. The actual error bound is
// 1/(2^(m+1)+1) for the smallest m that satisfies relErr.
//
// Panics if relErr is not in [0.0001, 0.1].
func NewError(relErr float64) *Histogram {
	return NewRange(relErr, defaultLo, defaultHi)
}

// NewRange creates a histogram with a relative error of at most relErr
// for absolute values in [lo, hi). Values closer to zero than lo count
// as zero, and values at or beyond ±hi count as overflow. Use lo = 0
// and hi = +Inf to cover all float64 values (subnormals still count as zero),
// at the cost of unbounded memory.
//
// Panics if relErr is not in [0.0001, 0.1], or unless 0 <= lo < hi.
func NewRange(relErr, lo, hi float64) *Histogram {
	if !(relErr >= 0.0001 && relErr <= 0.1) {
		panic("loghist: relErr must be in [0.0001, 0.1]")
	}
	if !(lo >= 0 && lo < hi) {
		panic("loghist: range must satisfy 0 <= lo < hi")
	}
	m := minBits
	for maxError(m) > relErr {
		m++
	}
	return newHist(m, lo, hi)
}

func newHist(m int, lo, hi float64) *Histogram {
	// Subnormals count as zero, so lo is at least minNormal. A range
	// of subnormals becomes empty: hi = lo sends every value to zero
	// or overflow (and keeps hi-lo in Add from wrapping around).
	loBits := max(math.Float64bits(lo), minNormal)
	return &Histogram{
		min:   math.Inf(1),
		max:   math.Inf(-1),
		lo:    loBits,
		hi:    max(math.Float64bits(hi), loBits),
		shift: uint(52 - m),
	}
}

// Add adds a value to the histogram. NaN and infinite values are ignored.
func (h *Histogram) Add(x float64) {
	// x-x is NaN for NaN and ±Inf, 0 otherwise.
	if x-x != 0 {
		return
	}
	h.count++
	h.min = min(h.min, x)
	h.max = max(h.max, x)
	// Negative values go to their own store, by absolute value. The bits
	// of a non-negative float64 sort like the value, so the rest of Add
	// works on the bits of the absolute value.
	bits := math.Float64bits(x)
	abs := bits &^ signBit
	s := &h.pos
	if bits != abs {
		s = &h.neg
	}
	if abs < h.lo {
		h.zero++
		return
	}
	if abs >= h.hi {
		s.over++
		return
	}
	// If the bucket isn't allocated yet, grow the store to its octave.
	i := int(abs >> h.shift)
	j := i - s.offset
	if uint(j) >= uint(len(s.counts)) {
		h.cover(s, i, i)
		j = i - s.offset
	}
	s.counts[j]++
}

// Count returns the number of values added to the histogram,
// capped at [math.MaxInt].
func (h *Histogram) Count() int {
	return int(min(h.count, math.MaxInt))
}

// Quantile returns the estimated value of rank ⌊q·(n−1)⌋ (counting from 0)
// among the n values added. The estimate is within the relative error
// of the true value. Quantile(0) and Quantile(1) are the exact minimum
// and maximum.
//
// Returns NaN if the histogram is empty or q is not in [0, 1].
func (h *Histogram) Quantile(q float64) float64 {
	if !(q >= 0 && q <= 1) || h.count == 0 {
		return math.NaN()
	}
	// The conversion rounds the product, so the compiler doesn't fuse it
	// into the conversion to an integer (on arm64).
	k := uint64(float64(q * float64(h.count-1)))
	if k == 0 {
		return h.min
	}
	if k >= h.count-1 {
		return h.max
	}
	// Find the bucket holding the value of rank k, scanning from the
	// closer end: high quantiles, the most common ones, are near the top.
	if k > (h.count-1)/2 {
		return h.clamp(h.fromTop(h.count - 1 - k))
	}
	return h.clamp(h.fromBottom(k))
}

// CDF returns the estimated fraction of values less than x, where values
// equal to x count as half (mid-rank convention). Values are assumed to be
// spread evenly within each bucket. Values closer to zero than lo, or at or
// beyond ±hi, count as half if x is among them. CDF is 0 below the minimum
// and 1 above the maximum.
//
// Returns NaN if the histogram is empty or x is NaN.
func (h *Histogram) CDF(x float64) float64 {
	if x != x || h.count == 0 {
		return math.NaN()
	}
	if x < h.min {
		return 0
	}
	if x > h.max {
		return 1
	}
	negTotal := h.neg.over + h.neg.sum(len(h.neg.counts))
	bits := math.Float64bits(x)
	abs := bits &^ signBit
	var below float64
	switch {
	case abs < h.lo:
		below = float64(negTotal) + float64(h.zero)/2
	case abs >= h.hi && bits == abs:
		below = float64(negTotal+h.zero+h.pos.sum(len(h.pos.counts))) + float64(h.pos.over)/2
	case abs >= h.hi:
		below = float64(h.neg.over) / 2
	case bits == abs:
		// Positive values within [min, max] are spread over the bucket.
		below = float64(negTotal+h.zero) + h.below(&h.pos, x, h.min, h.max)
	default:
		// Negative values below x are those with absolute values above -x.
		// Their absolute values are within [-max, -min].
		below = float64(negTotal) - h.below(&h.neg, -x, -h.max, -h.min)
	}
	return below / float64(h.count)
}

// Merge adds the values of other to h, so that h estimates the quantiles
// of the values added to either. Other doesn't change.
//
// Returns [ErrMismatch] if the histograms have different relative errors
// or ranges, and doesn't change h.
func (h *Histogram) Merge(other *Histogram) error {
	if h.shift != other.shift || h.lo != other.lo || h.hi != other.hi {
		return ErrMismatch
	}
	h.merge(&h.pos, &other.pos)
	h.merge(&h.neg, &other.neg)
	h.zero += other.zero
	h.count += other.count
	h.min = min(h.min, other.min)
	h.max = max(h.max, other.max)
	return nil
}

// merge adds the counts of o to s.
func (h *Histogram) merge(s, o *store) {
	s.over += o.over
	if len(o.counts) == 0 {
		return
	}
	h.cover(s, o.offset, o.offset+len(o.counts)-1)
	dst := s.counts[o.offset-s.offset:]
	for j, c := range o.counts {
		dst[j] += c
	}
}

// Size returns the memory used by the histogram's buckets, in bytes.
func (h *Histogram) Size() int {
	return 8 * (cap(h.pos.counts) + cap(h.neg.counts))
}

// fromBottom returns the estimate for the value of rank k, counting from 0
// in ascending order: negative overflow, negative values from the largest
// absolute value down, zeros, positive values, positive overflow.
// Overflow returns ±Inf, which Quantile clamps to the minimum or maximum.
func (h *Histogram) fromBottom(k uint64) float64 {
	return h.scan(&h.neg, &h.pos, k)
}

// fromTop is like fromBottom in descending order.
func (h *Histogram) fromTop(k uint64) float64 {
	// 0 - x rather than -x, so that zero stays +0.
	return 0 - h.scan(&h.pos, &h.neg, k)
}

// scan returns the estimate for the value of rank k, counting from 0
// through the overflow of a, the buckets of a from the largest down,
// zeros, the buckets of b from the smallest up, and the overflow of b.
// Values in a are negated.
func (h *Histogram) scan(a, b *store, k uint64) float64 {
	if k < a.over {
		return math.Inf(-1)
	}
	j, k := scanDown(a.counts, k-a.over)
	if j >= 0 {
		return -h.value(a.offset + j)
	}
	if k < h.zero {
		return 0
	}
	j, _ = scanUp(b.counts, k-h.zero)
	if j < len(b.counts) {
		return h.value(b.offset + j)
	}
	return math.Inf(1)
}

// scanUp returns the index of the bucket holding the value of rank k,
// counting from 0 from the first bucket. If counts hold k values or fewer,
// returns len(counts) and k minus their total.
func scanUp(counts []uint64, k uint64) (int, uint64) {
	j := 0
	for ; j < len(counts); j += 8 {
		b := (*[8]uint64)(counts[j:])
		s := (b[0] + b[1]) + (b[2] + b[3]) + (b[4] + b[5]) + (b[6] + b[7])
		if s > k {
			break
		}
		k -= s
	}
	for ; j < len(counts); j++ {
		if counts[j] > k {
			return j, 0
		}
		k -= counts[j]
	}
	return j, k
}

// scanDown is like scanUp, counting from the last bucket.
// Returns -1 and k minus the total if counts hold k values or fewer.
func scanDown(counts []uint64, k uint64) (int, uint64) {
	j := len(counts)
	for ; j > 0; j -= 8 {
		b := (*[8]uint64)(counts[j-8:])
		s := (b[0] + b[1]) + (b[2] + b[3]) + (b[4] + b[5]) + (b[6] + b[7])
		if s > k {
			break
		}
		k -= s
	}
	for j--; j >= 0; j-- {
		if counts[j] > k {
			return j, 0
		}
		k -= counts[j]
	}
	return j, k
}

// below returns the estimated number of values in s less than x > 0,
// where all values are within [lo, hi].
func (h *Histogram) below(s *store, x, lo, hi float64) float64 {
	i := int(math.Float64bits(x) >> h.shift)
	j := i - s.offset
	if j < 0 {
		return 0
	}
	if j >= len(s.counts) {
		return float64(s.sum(len(s.counts)))
	}
	// The fraction of the bucket below x, within [lo, hi].
	a, b := h.bounds(i)
	lo, hi = max(a, lo), min(b, hi)
	frac := 0.5
	if hi > lo {
		frac = (x - lo) / (hi - lo)
	}
	return float64(s.sum(j)) + float64(s.counts[j])*frac
}

// cover grows s to include the buckets lo to hi, extended to whole octaves.
func (h *Histogram) cover(s *store, lo, hi int) {
	if len(s.counts) > 0 {
		lo = min(lo, s.offset)
		hi = max(hi, s.offset+len(s.counts)-1)
	}
	mask := 1<<(52-h.shift) - 1
	lo &^= mask
	hi |= mask
	if len(s.counts) > 0 && lo == s.offset && hi == s.offset+len(s.counts)-1 {
		return
	}
	counts := make([]uint64, hi-lo+1)
	if len(s.counts) > 0 {
		copy(counts[s.offset-lo:], s.counts)
	}
	s.counts = counts
	s.offset = lo
}

// bounds returns the range of values [a, b) in bucket i.
func (h *Histogram) bounds(i int) (a, b float64) {
	a = math.Float64frombits(uint64(i) << h.shift)
	b = math.Float64frombits(uint64(i+1) << h.shift)
	return a, b
}

// value returns the estimate for values in bucket i: 2ab/(a+b),
// which has the same relative error at both ends of [a, b).
func (h *Histogram) value(i int) float64 {
	a, b := h.bounds(i)
	// The last bucket ends at +Inf, but its values don't exceed MaxFloat64.
	b = min(b, math.MaxFloat64)
	// Written as a·(2/(1+a/b)), so it doesn't overflow.
	return a * (2 / (1 + a/b))
}

// clamp limits an estimate to the exact range of the values.
func (h *Histogram) clamp(x float64) float64 {
	return min(max(x, h.min), h.max)
}

// sum returns the number of values in the first j buckets of s.
func (s *store) sum(j int) uint64 {
	var n uint64
	for _, c := range s.counts[:j] {
		n += c
	}
	return n
}

// maxError returns the max relative error with m mantissa bits.
// The widest buckets are at the start of an octave, [a, a(1+2^-m)),
// and the estimate 2ab/(a+b) is within (b-a)/(b+a) of any value in [a, b).
func maxError(m int) float64 {
	return 1 / float64(int(1)<<(m+1)+1)
}
