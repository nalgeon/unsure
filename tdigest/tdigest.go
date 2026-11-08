// Package tdigest implements the merging t-digest with the arcsine scale function.
//
// A digest estimates quantiles (such as the median or the 99th percentile)
// and the CDF of a stream of numbers, using a fixed amount of memory that
// depends only on the accuracy, not on the number of values.
//
// The digest summarizes the values as a sorted list of centroids, each with
// a mean and a weight (the number of values it represents). Centroids near
// the median are large, and centroids near the tails are small, so the tails
// are summarized in more detail.
//
// The accuracy is set by the compression δ: 100 with [New], or between 10 and
// 10000 with [NewCompression]. The error is measured in ranks: if Quantile(0.99)
// has rank error 0.0003, the true rank of the returned value is between 0.9897
// and 0.9903. Mean rank error for 1M random values (uniform, normal, lognormal),
// with the typical number of centroids and the memory used:
//
//	δ      centroids  memory   q=0.5    q=0.99   q=0.999
//	50     31         4 KB     0.06%    0.08%    0.04%
//	100    61         8 KB     0.02%    0.03%    0.02%   (default)
//	200    118        16 KB    0.012%   0.011%   0.007%
//	500    292        40 KB    0.005%   0.002%   0.001%
//	1000   587        80 KB    0.003%   0.001%   0.0005%
//
// The worst of 10 digests is about 2× the mean. Values added in sorted order
// give about 10× smaller errors, and merged digests about 2× larger.
// At extreme quantiles with default δ (q=0.0001 or 0.9999 at δ=100), the
// outermost centroid holds more values than the quantile's rank, so the
// rank error is about as large as q (or 1-q) itself.
//
// Added values go to a buffer. When it's full, the digest sorts it and merges
// it with the centroids in one pass (Dunning, 2019). Queries flush the buffer
// first, so they change the digest's internal state.
//
// The minimum and maximum are tracked exactly, so Quantile(0) and Quantile(1)
// return them. Centroids with weight 1 are treated as exact values, so with
// few values (up to about δ/2 at the median), the estimates are exact.
//
// Digests can be merged, even with different compressions:
// the result keeps the receiver's compression.
//
// The digest is deterministic: the same values added in the same order
// always produce the same digest.
package tdigest

import (
	"math"
	"slices"
)

// Compression: default and range.
const (
	defaultDelta = 100
	minDelta     = 10
	maxDelta     = 10000
)

// bufFactor is the buffer size relative to the compression.
// Larger buffers make flushes less frequent, which amortizes the merge pass
// (about 0.6·δ output centroids) over more values.
const bufFactor = 5

// centroid represents weight values with the given mean.
// Weights are integers, exact as float64 up to 2^53.
type centroid struct {
	mean   float64
	weight float64
}

// Digest estimates quantiles of a stream of numbers.
//
// Digest is not safe for concurrent use, even if all goroutines only call
// Quantile or CDF: queries flush the buffer and change the digest.
type Digest struct {
	// Sorted by mean, at most δ+2 entries.
	centroids []centroid
	// Scratch space for the merge pass, swapped with centroids after merge.
	tmp []centroid
	// cum[i] is the total weight of centroids[:i], len(centroids)+1 entries.
	cum []float64
	// Values added since the last flush.
	buf []float64
	// Total weight of the centroids.
	total float64
	// Exact minimum and maximum of all values, ±Inf if empty.
	min, max float64
	// Cosine and sine of 2π/δ, the increment in asin(2q-1) per unit of k.
	cos, sin float64
	// Whether the next flush merges in descending order.
	desc bool
}

// New creates a digest with the default compression δ=100:
// 8 KB, mean rank error about 0.02% at the median and the 99th percentile.
func New() *Digest {
	return NewCompression(defaultDelta)
}

// NewCompression creates a digest with compression δ: at most δ+2 centroids,
// about 80·δ bytes. Larger δ means larger size but smaller errors, see the package docs.
// Panics if δ is not in [10, 10000].
func NewCompression(delta int) *Digest {
	if delta < minDelta || delta > maxDelta {
		panic("tdigest: delta must be in [10, 10000]")
	}
	maxCentroids := delta + 2
	return &Digest{
		centroids: make([]centroid, 0, maxCentroids),
		tmp:       make([]centroid, 0, maxCentroids),
		cum:       make([]float64, 1, maxCentroids+1),
		buf:       make([]float64, 0, bufFactor*delta),
		min:       math.Inf(1),
		max:       math.Inf(-1),
		cos:       math.Cos(2 * math.Pi / float64(delta)),
		sin:       math.Sin(2 * math.Pi / float64(delta)),
	}
}

// Add adds a value to the digest. NaN and infinite values are ignored.
func (d *Digest) Add(x float64) {
	// x-x is NaN for NaN and ±Inf, 0 otherwise.
	if x-x != 0 {
		return
	}
	d.buf = append(d.buf, x)
	if len(d.buf) == cap(d.buf) {
		d.flush()
	}
}

// Count returns the number of values added to the digest.
func (d *Digest) Count() uint64 {
	return uint64(d.total) + uint64(len(d.buf))
}

// Quantile returns the estimated value below which a fraction q of the
// values fall. Quantile(0) and Quantile(1) are the exact minimum and maximum.
// Returns NaN if the digest is empty or q is not in [0, 1].
func (d *Digest) Quantile(q float64) float64 {
	if !(q >= 0 && q <= 1) {
		return math.NaN()
	}
	d.flush()
	cs := d.centroids
	n := len(cs)
	if n == 0 {
		return math.NaN()
	}
	total := d.total
	// The rank of the value to find. The first and last values
	// are the exact minimum and maximum. The conversion rounds the
	// product, so the compiler doesn't fuse it into later subtractions
	// (on arm64), which would make exact ranks like 0.82·50 = 41 inexact.
	idx := float64(q * total)
	if idx < 1 {
		return d.min
	}
	// The rank total-1 maps to the max, as the tail interpolation below
	// does for a last centroid of weight 1 or > 2. With weight 2 it would
	// divide 0 by 0.
	if idx >= total-1 {
		return d.max
	}
	// A centroid's values are spread around its mean, so half of its
	// weight is on each side. In the tails, interpolate between the
	// min/max (the first/last value) and the first/last centroid.
	first, last := cs[0], cs[n-1]
	if first.weight > 1 && idx < first.weight/2 {
		return d.min + (idx-1)/(first.weight/2-1)*(first.mean-d.min)
	}
	if last.weight > 1 && total-idx <= last.weight/2 {
		return d.max - (total-idx-1)/(last.weight/2-1)*(d.max-last.mean)
	}
	// Find the pair of centroids whose centers (cum + weight/2) surround idx:
	// center(i) ≤ idx < center(i+1). The tails above ensure that
	// center(0) ≤ idx < center(n-1).
	lo, hi := 0, n-1
	for hi-lo > 1 {
		mid := int(uint(lo+hi) >> 1)
		if d.cum[mid]+cs[mid].weight/2 <= idx {
			lo = mid
		} else {
			hi = mid
		}
	}
	left, right := cs[lo], cs[hi]
	z1 := idx - (d.cum[lo] + left.weight/2)
	z2 := d.cum[hi] + right.weight/2 - idx
	// A centroid of weight 1 is a single value at its mean,
	// not spread around it.
	if left.weight == 1 {
		if z1 < 0.5 {
			return left.mean
		}
		z1 -= 0.5
	}
	if right.weight == 1 {
		if z2 <= 0.5 {
			return right.mean
		}
		z2 -= 0.5
	}
	return left.mean + (right.mean-left.mean)*z1/(z1+z2)
}

// CDF returns the estimated fraction of values less than x, where values
// equal to x count as half (mid-rank convention). CDF is 0 below the minimum
// and 1 above the maximum.
// Returns NaN if the digest is empty or x is NaN.
func (d *Digest) CDF(x float64) float64 {
	if x != x {
		return math.NaN()
	}
	d.flush()
	cs := d.centroids
	n := len(cs)
	if n == 0 {
		return math.NaN()
	}
	if x < d.min {
		return 0
	}
	if x > d.max {
		return 1
	}
	total := d.total
	if n == 1 {
		if d.max == d.min {
			return 0.5
		}
		return (x - d.min) / (d.max - d.min)
	}
	// Tails: interpolate between the min/max (a single value) and the
	// first/last centroid's center. A centroid of weight 1 is at the
	// min/max itself, so x is not in the tail.
	first, last := cs[0], cs[n-1]
	if x < first.mean {
		if x == d.min {
			return 0.5 / total
		}
		return (1 + (x-d.min)/(first.mean-d.min)*(first.weight/2-1)) / total
	}
	if x > last.mean {
		if x == d.max {
			return 1 - 0.5/total
		}
		return 1 - (1+(d.max-x)/(d.max-last.mean)*(last.weight/2-1))/total
	}
	// lo is the first centroid with mean ≥ x, hi the first with mean > x.
	lo := searchMean(cs, x)
	hi := lo
	for hi < n && cs[hi].mean == x {
		hi++
	}
	if hi > lo {
		// Centroids at exactly x: half of their weight is below x.
		return (d.cum[lo] + (d.cum[hi]-d.cum[lo])/2) / total
	}
	// Interpolate between the centers of the surrounding centroids,
	// excluding the half value of centroids with weight 1.
	i := lo - 1
	left, right := cs[i], cs[i+1]
	if left.weight == 1 && right.weight == 1 {
		return (d.cum[i] + 1) / total
	}
	var leftEx, rightEx float64
	if left.weight == 1 {
		leftEx = 0.5
	} else if right.weight == 1 {
		rightEx = 0.5
	}
	dw := (left.weight+right.weight)/2 - leftEx - rightEx
	base := d.cum[i] + left.weight/2 + leftEx
	return (base + dw*(x-left.mean)/(right.mean-left.mean)) / total
}

// Merge adds the values of other to d, so that d estimates the quantiles
// of the values added to either. Other's estimates don't change, but it
// flushes its buffer. The compressions may differ: d keeps its own.
func (d *Digest) Merge(other *Digest) {
	other.flush()
	if len(other.centroids) == 0 {
		return
	}
	d.flush()
	a, b := d.centroids, other.centroids
	c := d.compressor(d.total + other.total)
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].mean <= b[j].mean {
			c.add(a[i].mean, a[i].weight)
			i++
		} else {
			c.add(b[j].mean, b[j].weight)
			j++
		}
	}
	for ; i < len(a); i++ {
		c.add(a[i].mean, a[i].weight)
	}
	for ; j < len(b); j++ {
		c.add(b[j].mean, b[j].weight)
	}
	d.min = min(d.min, other.min)
	d.max = max(d.max, other.max)
	d.finish(&c)
}

// Size returns the memory used by the digest's centroids
// and buffer, in bytes.
func (d *Digest) Size() int {
	return 16*(cap(d.centroids)+cap(d.tmp)) + 8*(cap(d.cum)+cap(d.buf))
}

// flush merges the buffered values into the centroids.
func (d *Digest) flush() {
	buf := d.buf
	if len(buf) == 0 {
		return
	}
	// Merging always goes left to right, so the leftmost centroids
	// of each pass tend to be fuller than the rightmost. Every other
	// flush works on negated values, so it merges right to left instead.
	desc := d.desc
	d.desc = !desc
	cs := d.centroids
	if desc {
		negate(buf)
		slices.Reverse(cs)
		negateMeans(cs)
	}
	slices.Sort(buf)
	lo, hi := buf[0], buf[len(buf)-1]
	if desc {
		lo, hi = -hi, -lo
	}
	d.min = min(d.min, lo)
	d.max = max(d.max, hi)

	c := d.compressor(d.total + float64(len(buf)))
	i, j := 0, 0
	for i < len(buf) && j < len(cs) {
		if buf[i] <= cs[j].mean {
			c.add(buf[i], 1)
			i++
		} else {
			c.add(cs[j].mean, cs[j].weight)
			j++
		}
	}
	for ; i < len(buf); i++ {
		c.add(buf[i], 1)
	}
	for ; j < len(cs); j++ {
		c.add(cs[j].mean, cs[j].weight)
	}
	c.flush()
	if desc {
		slices.Reverse(c.out)
		negateMeans(c.out)
	}
	d.buf = buf[:0]
	d.finish(&c)
}

// compressor returns a compressor for the merge pass
// that writes to d's scratch space.
func (d *Digest) compressor(total float64) compressor {
	return compressor{out: d.tmp[:0], total: total, cos: d.cos, sin: d.sin}
}

// finish replaces the centroids with the compressor's output
// and recomputes the cumulative weights.
func (d *Digest) finish(c *compressor) {
	c.flush()
	d.tmp = d.centroids[:0]
	d.centroids = c.out
	d.total = c.total
	cum := d.cum[:1]
	w := 0.0
	for _, c := range d.centroids {
		w += c.weight
		cum = append(cum, w)
	}
	d.cum = cum
}

// compressor merges (mean, weight) items, sorted by mean, into centroids.
// A centroid grows while its span in k(q) = δ/(2π)·asin(2q-1) is at most 1,
// where q is the fraction of the total weight before (or within) it.
type compressor struct {
	out   []centroid
	total float64 // total weight of all items
	// Cosine and sine of 2π/δ.
	cos, sin float64
	// The current centroid: weight w, and the sum of (mean-base) × weight
	// over its items, where base is the mean of its first item. Summing
	// offsets from base keeps the sum precise when the values are large
	// but close to each other (such as 1e12 + small noise).
	base, sum, w float64
	// Total weight of the centroids in out.
	before float64
	// Max weight of the current centroid.
	cap float64
}

// add adds an item to the current centroid, or starts a new one
// if it doesn't fit.
func (c *compressor) add(mean, weight float64) {
	if c.w+weight <= c.cap {
		c.sum += (mean - c.base) * weight
		c.w += weight
		return
	}
	c.next(mean, weight)
}

// next appends the current centroid (if any) to out and starts a new one.
func (c *compressor) next(mean, weight float64) {
	c.flush()
	c.base, c.sum, c.w = mean, 0, weight
	// The centroid starts at q = before/total and ends at q(k(q)+1).
	// With k scaled to a = asin(2q-1), q(a) = (sin(a)+1)/2, the end is
	// (sin(a+s)+1)/2 for s = 2π/δ, or 1 once a+s reaches π/2. Expanding
	// sin(a+s) = sin(a)·cos(s) + cos(a)·sin(s), where sin(a) = 2q-1 and
	// cos(a) = 2·√(q(1-q)), avoids asin and sin. Multiplied by total:
	b, t := c.before, c.total
	limit := t
	if 2*b-t < c.cos*t {
		limit = (t+(2*b-t)*c.cos)/2 + math.Sqrt(b*(t-b))*c.sin
	}
	c.cap = limit - b
}

// flush appends the current centroid (if any) to out.
func (c *compressor) flush() {
	if c.w == 0 {
		return
	}
	c.out = append(c.out, centroid{c.base + c.sum/c.w, c.w})
	c.before += c.w
	c.sum, c.w = 0, 0
}

// searchMean returns the index of the first centroid with mean ≥ x.
func searchMean(cs []centroid, x float64) int {
	lo, hi := 0, len(cs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if cs[mid].mean < x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func negate(xs []float64) {
	for i := range xs {
		xs[i] = -xs[i]
	}
}

func negateMeans(cs []centroid) {
	for i := range cs {
		cs[i].mean = -cs[i].mean
	}
}
