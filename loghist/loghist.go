// Package loghist implements a log-linear histogram.
//
// A histogram estimates quantiles (like the median or 99th percentile)
// and the CDF for a stream of numbers. It counts values in buckets,
// and the width of each bucket increases as the numbers get larger.
// This way, every quantile estimate is within a fixed relative error
// of the true value, from the median up to the maximum.
//
// The error is measured in values: with the default 0.39%, if the true
// 99.9th percentile is 200 ms, Quantile(0.999) returns a value between
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
// Memory depends on the range of the values, not on their number: 8·2^m bytes
// per octave between the smallest and the largest absolute value, separately
// for positive and negative values. The buckets are allocated as the range
// grows, and never shrink. A single extreme value, such as 1e-300 among values
// from 1 to 1e6, adds about a thousand octaves (1 MB with m=7).
//
// The minimum and maximum are tracked exactly, so Quantile(0) and Quantile(1)
// return them. Zero, negative zero and subnormal values (below 2.2e-308
// in absolute value) count as zero. NaN and infinite values are ignored.
//
// Histograms with the same relative error can be merged. Merging is exact:
// the result is the same as adding all values to one histogram. The order
// of the values doesn't matter either.
package loghist

import "math"

// Mantissa bits per bucket: default and range.
const (
	defaultBits = 7
	minBits     = 3
	maxBits     = 13
)

const (
	signBit = 1 << 63
	// Values with smaller bits (zero and subnormals) count as zero.
	minNormal = 1 << 52
)

// Histogram estimates quantiles of a stream of numbers.
//
// Allocates memory as the range of the values grows.
// Not safe for concurrent use.
type Histogram struct {
	// Buckets for positive values and for the absolute values of negative ones.
	pos, neg store
	// Number of zero and subnormal values.
	zero uint64
	// Number of values added.
	count uint64
	// Exact minimum and maximum of all values, ±Inf if empty.
	min, max float64
	// The bucket index of a value is its bits >> shift, shift = 52 - m.
	shift uint
}

// store counts values in a dense range of buckets.
type store struct {
	// counts[j] is the number of values in bucket offset+j.
	// Covers whole octaves: offset and len are multiples of 2^m.
	counts []uint64
	offset int
}

// New creates a histogram with the default relative error of 0.39% (1/257).
func New() *Histogram {
	return newBits(defaultBits)
}

// NewError creates a histogram with a relative error of at most relErr.
// The actual error bound is 1/(2^(m+1)+1) for the smallest m that
// satisfies relErr.
//
// Panics if relErr is not in [0.0001, 0.1].
func NewError(relErr float64) *Histogram {
	if !(relErr >= 0.0001 && relErr <= 0.1) {
		panic("loghist: relErr must be in [0.0001, 0.1]")
	}
	m := minBits
	for maxError(m) > relErr {
		m++
	}
	return newBits(m)
}

func newBits(m int) *Histogram {
	return &Histogram{
		min:   math.Inf(1),
		max:   math.Inf(-1),
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
	bits := math.Float64bits(x)
	abs := bits &^ signBit
	if abs < minNormal {
		h.zero++
		return
	}
	s := &h.pos
	if bits != abs {
		s = &h.neg
	}
	i := int(abs >> h.shift)
	j := i - s.offset
	if uint(j) >= uint(len(s.counts)) {
		h.cover(s, i, i)
		j = i - s.offset
	}
	s.counts[j]++
}

// Count returns the number of values added to the histogram.
func (h *Histogram) Count() int {
	return int(h.count)
}

// Quantile returns the estimated value X such that q proportion of
// the data values are less than or equal. to X The estimate is within
// the relative error of the true value. Quantile(0) and Quantile(1) are
// the exact minimum and maximum.
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
	// Find the bucket holding the value of rank k: negative values
	// from the largest absolute value down, then zeros, then positive ones.
	var cum uint64
	neg := h.neg.counts
	for j := len(neg) - 1; j >= 0; j-- {
		cum += neg[j]
		if cum > k {
			return h.clamp(-h.value(h.neg.offset + j))
		}
	}
	cum += h.zero
	if cum > k {
		return h.clamp(0)
	}
	for j, c := range h.pos.counts {
		cum += c
		if cum > k {
			return h.clamp(h.value(h.pos.offset + j))
		}
	}
	return h.max
}

// CDF returns the estimated fraction of values less than x, where values
// equal to x count as half (mid-rank convention). Values are assumed to be
// spread evenly within each bucket. CDF is 0 below the minimum and 1 above
// the maximum.
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
	negTotal := h.neg.sum(len(h.neg.counts))
	bits := math.Float64bits(x)
	abs := bits &^ signBit
	var below float64
	switch {
	case abs < minNormal:
		below = float64(negTotal) + float64(h.zero)/2
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
// Panics if the histograms have different relative errors.
func (h *Histogram) Merge(other *Histogram) {
	if h.shift != other.shift {
		panic("loghist: merging histograms with different relative errors")
	}
	h.merge(&h.pos, &other.pos)
	h.merge(&h.neg, &other.neg)
	h.zero += other.zero
	h.count += other.count
	h.min = min(h.min, other.min)
	h.max = max(h.max, other.max)
}

// merge adds the counts of o to s.
func (h *Histogram) merge(s, o *store) {
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
// Written as a·2/(1+a/b) so it doesn't overflow.
func (h *Histogram) value(i int) float64 {
	a, b := h.bounds(i)
	return a * 2 / (1 + a/b)
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
