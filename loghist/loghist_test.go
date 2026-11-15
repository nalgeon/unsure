package loghist

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// makeValues returns n values from the named distribution.
func makeValues(dist string, seed uint64, n int) []float64 {
	rng := rand.New(rand.NewPCG(seed, 0))
	xs := make([]float64, n)
	for i := range xs {
		switch dist {
		case "uniform":
			xs[i] = rng.Float64()
		case "normal":
			xs[i] = rng.NormFloat64()
		case "lognormal":
			xs[i] = math.Exp(rng.NormFloat64())
		case "pareto":
			// Heavy tail, α=1.5.
			xs[i] = math.Pow(1-rng.Float64(), -1/1.5)
		case "wide":
			// Both signs, magnitudes from 1e-100 to 1e100.
			xs[i] = math.Pow(10, rng.Float64()*200-100)
			if rng.IntN(2) == 0 {
				xs[i] = -xs[i]
			}
		case "discrete":
			// Few distinct values, many duplicates, including zeros.
			xs[i] = float64(rng.IntN(10) - 3)
		default:
			panic(dist)
		}
	}
	return xs
}

var dists = []string{"uniform", "normal", "lognormal", "pareto", "wide", "discrete"}

// newFull creates a histogram over the whole float64 range, for test data
// beyond the default range of 1e-9 to 1e18.
func newFull(relErr float64) *Histogram {
	return NewRange(relErr, 0, math.Inf(1))
}

func fill(h *Histogram, xs []float64) {
	for _, x := range xs {
		h.Add(x)
	}
}

// rank returns the fraction of sorted values less than x,
// with values equal to x counted as half.
func rank(sorted []float64, x float64) float64 {
	lo, _ := slices.BinarySearch(sorted, x)
	hi := lo
	for hi < len(sorted) && sorted[hi] == x {
		hi++
	}
	return (float64(lo) + float64(hi-lo)/2) / float64(len(sorted))
}

// relError returns the relative error of got vs want.
func relError(got, want float64) float64 {
	if got == want {
		return 0
	}
	return math.Abs(got-want) / math.Abs(want)
}

var quantiles = []float64{0, 0.0001, 0.001, 0.01, 0.1, 0.25, 0.5, 0.75, 0.9, 0.99, 0.999, 0.9999, 1}

func TestNew(t *testing.T) {
	h := New()
	if h.Count() != 0 || h.Size() != 0 {
		t.Errorf("Count, Size = %d, %d, want 0, 0", h.Count(), h.Size())
	}
	if h.shift != 52-defaultBits {
		t.Errorf("shift = %d, want %d", h.shift, 52-defaultBits)
	}
}

func TestNewError(t *testing.T) {
	tests := []struct {
		relErr float64
		m      int
	}{
		{0.1, 3}, {0.06, 3}, {0.05, 4}, {0.01, 6}, {0.005, 7}, {0.0039, 7},
		{0.001, 9}, {0.0001, 13},
	}
	for _, tt := range tests {
		h := NewError(tt.relErr)
		if m := 52 - int(h.shift); m != tt.m {
			t.Errorf("NewError(%v): m = %d, want %d", tt.relErr, m, tt.m)
		}
		if maxError(tt.m) > tt.relErr {
			t.Errorf("maxError(%d) = %v > %v", tt.m, maxError(tt.m), tt.relErr)
		}
	}
}

func TestNewErrorPanics(t *testing.T) {
	for _, relErr := range []float64{0, 0.00009, 0.11, 1, -0.01, math.NaN()} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewError(%v) did not panic", relErr)
				}
			}()
			NewError(relErr)
		}()
	}
}

func TestEmpty(t *testing.T) {
	h := New()
	for _, q := range []float64{0, 0.5, 1} {
		if got := h.Quantile(q); !math.IsNaN(got) {
			t.Errorf("Quantile(%v) = %v, want NaN", q, got)
		}
	}
	if got := h.CDF(0); !math.IsNaN(got) {
		t.Errorf("CDF(0) = %v, want NaN", got)
	}
}

func TestSingle(t *testing.T) {
	for _, x := range []float64{1.2345, -1.2345, 0, 1e300, -1e-300, math.MaxFloat64} {
		h := New()
		h.Add(x)
		for _, q := range []float64{0, 0.3, 0.5, 1} {
			if got := h.Quantile(q); got != x {
				t.Errorf("x=%v: Quantile(%v) = %v", x, q, got)
			}
		}
		if got := h.CDF(x); got != 0.5 {
			t.Errorf("x=%v: CDF(x) = %v, want 0.5", x, got)
		}
	}
}

func TestInvalid(t *testing.T) {
	h := New()
	h.Add(math.NaN())
	h.Add(math.Inf(1))
	h.Add(math.Inf(-1))
	if h.Count() != 0 {
		t.Errorf("Count = %d, want 0", h.Count())
	}
	h.Add(1)
	for _, q := range []float64{-0.1, 1.1, math.NaN()} {
		if got := h.Quantile(q); !math.IsNaN(got) {
			t.Errorf("Quantile(%v) = %v, want NaN", q, got)
		}
	}
	if got := h.CDF(math.NaN()); !math.IsNaN(got) {
		t.Errorf("CDF(NaN) = %v, want NaN", got)
	}
	if got := h.CDF(math.Inf(-1)); got != 0 {
		t.Errorf("CDF(-Inf) = %v, want 0", got)
	}
	if got := h.CDF(math.Inf(1)); got != 1 {
		t.Errorf("CDF(+Inf) = %v, want 1", got)
	}
}

func TestZero(t *testing.T) {
	h := New()
	for _, x := range []float64{0, math.Copysign(0, -1), 5e-324, -1e-310} {
		h.Add(x)
	}
	if h.zero != 4 || h.Size() != 0 {
		t.Errorf("zero = %d, Size = %d, want 4, 0", h.zero, h.Size())
	}
	h.Add(-1)
	h.Add(1)
	// Rank 2 is found from the bottom, rank 3 from the top.
	for _, q := range []float64{0.5, 0.6} {
		if got := h.Quantile(q); got != 0 || math.Signbit(got) {
			t.Errorf("Quantile(%v) = %v, want +0", q, got)
		}
	}
	if got := h.CDF(0); got != 0.5 {
		t.Errorf("CDF(0) = %v, want 0.5", got)
	}
}

func TestNewRangePanics(t *testing.T) {
	tests := [][3]float64{
		{0, 1, 2}, {0.01, -1, 2}, {0.01, 2, 2}, {0.01, 3, 2},
		{0.01, math.NaN(), 2}, {0.01, 1, math.NaN()}, {0.01, math.Inf(1), math.Inf(1)},
	}
	for _, tt := range tests {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewRange(%v, %v, %v) did not panic", tt[0], tt[1], tt[2])
				}
			}()
			NewRange(tt[0], tt[1], tt[2])
		}()
	}
}

// TestRange checks that values outside [lo, hi) count as zero or overflow,
// don't use memory, and that quantiles on them return 0, min or max.
func TestRange(t *testing.T) {
	h := NewRange(0.01, 1, 100)
	// Zero: below lo, including subnormals.
	for _, x := range []float64{0, 0.5, -0.999, 5e-324} {
		h.Add(x)
	}
	// Overflow: hi and beyond, on both sides.
	for _, x := range []float64{100, 1e300, math.MaxFloat64} {
		h.Add(x)
	}
	for _, x := range []float64{-100, -1e200} {
		h.Add(x)
	}
	if h.zero != 4 || h.pos.over != 3 || h.neg.over != 2 || h.Size() != 0 {
		t.Fatalf("zero, pos.over, neg.over, Size = %d, %d, %d, %d, want 4, 3, 2, 0",
			h.zero, h.pos.over, h.neg.over, h.Size())
	}
	// In range: 1 (lo itself) and 50.
	h.Add(1)
	h.Add(50)
	// Ascending: -1e200 -100 | 0 0.5 -0.999 5e-324 | 1 50 | 100 1e300 MaxFloat64.
	tests := []struct {
		k    int
		want float64
	}{
		{1, -1e200}, // negative overflow: the minimum
		{2, 0}, {5, 0},
		{6, 1},
		{8, math.MaxFloat64}, {9, math.MaxFloat64}, // positive overflow: the maximum
	}
	n := h.Count()
	for _, tt := range tests {
		q := float64(tt.k) / float64(n-1)
		got := h.Quantile(q)
		if tt.want == 0 && (got != 0 || math.Signbit(got)) {
			t.Errorf("Quantile(rank %d) = %v, want +0", tt.k, got)
		} else if tt.want != 0 && relError(got, tt.want) > maxError(6)*(1+1e-12) {
			t.Errorf("Quantile(rank %d) = %v, want %v", tt.k, got, tt.want)
		}
	}
	// CDF: values in zero and overflow count as half if x is among them.
	cdf := []struct{ x, want float64 }{
		{-1e300, 0},
		{-500, 1.0 / 11},
		{0.1, (2 + 2.0) / 11},
		{1000, (2 + 4 + 2 + 1.5) / 11},
	}
	for _, tt := range cdf {
		if got := h.CDF(tt.x); math.Abs(got-tt.want) > 1e-12 {
			t.Errorf("CDF(%v) = %v, want %v", tt.x, got, tt.want)
		}
	}
}

// TestDefaultRange checks that extreme values don't grow the default histogram.
func TestDefaultRange(t *testing.T) {
	h := New()
	fill(h, makeValues("lognormal", 1, 10_000))
	size := h.Size()
	for _, x := range []float64{1e-300, -1e-300, 1e-10, 1e300, -1e300, 1e18} {
		h.Add(x)
	}
	if h.Size() != size {
		t.Errorf("Size = %d, want %d", h.Size(), size)
	}
	// The whole range [1e-9, 1e18) fits in 90 octaves per sign.
	h.Add(1e-9)
	h.Add(9.9e17)
	h.Add(-1e-9)
	h.Add(-9.9e17)
	if want := 2 * 90 * 8 << defaultBits; h.Size() != want {
		t.Errorf("full range: Size = %d, want %d", h.Size(), want)
	}
}

// TestAccuracy checks that every quantile is within the relative error
// of the true value at its rank.
func TestAccuracy(t *testing.T) {
	const n = 100_000
	for _, relErr := range []float64{0.1, 0.01, 0.001, 0.0001} {
		for _, dist := range dists {
			xs := makeValues(dist, 1, n)
			h := newFull(relErr)
			fill(h, xs)
			slices.Sort(xs)
			bound := maxError(52-int(h.shift)) * (1 + 1e-12)
			for _, q := range quantiles {
				want := xs[int(q*float64(n-1))]
				if err := relError(h.Quantile(q), want); err > bound {
					t.Errorf("relErr=%v %s: Quantile(%v) = %v, want %v (error %.2e > %.2e)",
						relErr, dist, q, h.Quantile(q), want, err, bound)
				}
			}
		}
	}
}

// TestAllRanks checks the error bound at every rank of a small histogram.
func TestAllRanks(t *testing.T) {
	const n = 1000
	for _, dist := range dists {
		xs := makeValues(dist, 2, n)
		h := newFull(0.01)
		fill(h, xs)
		slices.Sort(xs)
		bound := maxError(6) * (1 + 1e-12)
		for k := range n {
			q := float64(k) / float64(n-1)
			// q·(n-1) may round down to k-1.
			k0 := int(q * float64(n-1))
			if err := relError(h.Quantile(q), xs[k0]); err > bound {
				t.Errorf("%s: rank %d: got %v, want %v", dist, k0, h.Quantile(q), xs[k0])
			}
		}
	}
}

// naiveQuantile finds the bucket of rank k with a plain linear scan
// in ascending order, as a reference for Quantile's two-ended block scan.
func naiveQuantile(h *Histogram, q float64) float64 {
	k := uint64(float64(q * float64(h.count-1)))
	if k == 0 {
		return h.min
	}
	if k >= h.count-1 {
		return h.max
	}
	cum := h.neg.over
	if cum > k {
		return h.min
	}
	for j := len(h.neg.counts) - 1; j >= 0; j-- {
		cum += h.neg.counts[j]
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

// TestQuantileScan checks that Quantile finds the same bucket as a linear
// scan at every rank, including one-sided data where the scan from either
// end crosses empty stores and zeros.
func TestQuantileScan(t *testing.T) {
	const n = 1000
	neg := func(xs []float64) []float64 {
		for i := range xs {
			xs[i] = -xs[i]
		}
		return xs
	}
	withZeros := func(xs []float64) []float64 {
		for i := range xs {
			if i%3 == 0 {
				xs[i] = 0
			}
		}
		return xs
	}
	sets := map[string][]float64{
		"positive":       makeValues("lognormal", 3, n),
		"negative":       neg(makeValues("lognormal", 3, n)),
		"positive+zeros": withZeros(makeValues("lognormal", 3, n)),
		"negative+zeros": withZeros(neg(makeValues("lognormal", 3, n))),
		"mixed+zeros":    withZeros(makeValues("normal", 3, n)),
		"zeros":          make([]float64, n),
		"wide":           makeValues("wide", 3, n),
	}
	// Ranges: everything in buckets, and many values in zero and overflow.
	ranges := [][2]float64{{0, math.Inf(1)}, {0.5, 2}}
	for name, xs := range sets {
		for _, r := range ranges {
			for _, relErr := range []float64{0.1, 0.01, 0.001} {
				h := NewRange(relErr, r[0], r[1])
				fill(h, xs)
				for k := range n {
					q := float64(k) / float64(n-1)
					if got, want := h.Quantile(q), naiveQuantile(h, q); got != want {
						t.Errorf("%s range=%v relErr=%v: Quantile(%v) = %v, want %v",
							name, r, relErr, q, got, want)
					}
				}
			}
		}
	}
}

// TestCDF checks that CDF(x) is off by at most the values
// within the relative error of x.
func TestCDF(t *testing.T) {
	const n = 100_000
	for _, dist := range dists {
		xs := makeValues(dist, 1, n)
		h := newFull(0.01)
		fill(h, xs)
		sorted := slices.Clone(xs)
		slices.Sort(sorted)
		// A bucket spans at most a factor of 1+2^-6.
		w := 1.0 / 64
		for _, x := range xs[:1000] {
			got := h.CDF(x)
			lo, hi := x/(1+w), x*(1+w)
			if x < 0 {
				lo, hi = hi, lo
			}
			tol := rank(sorted, hi) - rank(sorted, lo) + 1.0/n
			if math.Abs(got-rank(sorted, x)) > tol {
				t.Errorf("%s: CDF(%v) = %v, want %v ± %v", dist, x, got, rank(sorted, x), tol)
			}
		}
	}
}

func TestMonotonic(t *testing.T) {
	for _, dist := range dists {
		xs := makeValues(dist, 1, 10_000)
		h := newFull(0.005)
		fill(h, xs)
		prev := math.Inf(-1)
		for i := range 1001 {
			x := h.Quantile(float64(i) / 1000)
			if x < prev {
				t.Fatalf("%s: Quantile(%v) = %v < %v", dist, float64(i)/1000, x, prev)
			}
			prev = x
		}
		slices.Sort(xs)
		prev = 0
		for _, x := range xs {
			c := h.CDF(x)
			if c < prev {
				t.Fatalf("%s: CDF(%v) = %v < %v", dist, x, c, prev)
			}
			prev = c
		}
	}
}

func TestMinMax(t *testing.T) {
	for _, dist := range dists {
		xs := makeValues(dist, 1, 10_000)
		h := newFull(0.005)
		fill(h, xs)
		lo, hi := slices.Min(xs), slices.Max(xs)
		if h.Quantile(0) != lo || h.Quantile(1) != hi {
			t.Errorf("%s: Quantile(0), Quantile(1) = %v, %v, want %v, %v",
				dist, h.Quantile(0), h.Quantile(1), lo, hi)
		}
		// All estimates are within [min, max].
		for _, q := range quantiles {
			if x := h.Quantile(q); x < lo || x > hi {
				t.Errorf("%s: Quantile(%v) = %v outside [%v, %v]", dist, q, x, lo, hi)
			}
		}
	}
}

func TestLargeValues(t *testing.T) {
	h := New()
	for _, x := range []float64{math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64 / 3, 1} {
		h.Add(x)
	}
	for _, q := range quantiles {
		if x := h.Quantile(q); math.IsInf(x, 0) || math.IsNaN(x) {
			t.Errorf("Quantile(%v) = %v", q, x)
		}
	}
	if got := h.Quantile(0.5); relError(got, 1) > maxError(defaultBits) {
		t.Errorf("Quantile(0.5) = %v, want 1", got)
	}
}

// TestOrder checks that the order of values doesn't matter.
func TestOrder(t *testing.T) {
	xs := makeValues("wide", 1, 10_000)
	a, b := newFull(0.005), newFull(0.005)
	fill(a, xs)
	slices.Sort(xs)
	fill(b, xs)
	same(t, a, b)
}

// same checks that a and b give the same estimates.
func same(t *testing.T, a, b *Histogram) {
	t.Helper()
	if a.Count() != b.Count() {
		t.Fatalf("Count = %d, %d", a.Count(), b.Count())
	}
	for i := range 1001 {
		q := float64(i) / 1000
		if a.Quantile(q) != b.Quantile(q) {
			t.Fatalf("Quantile(%v) = %v, %v", q, a.Quantile(q), b.Quantile(q))
		}
		x := a.Quantile(q)
		if a.CDF(x) != b.CDF(x) {
			t.Fatalf("CDF(%v) = %v, %v", x, a.CDF(x), b.CDF(x))
		}
	}
}

// TestMerge checks that merging gives the same result as adding all values
// to one histogram, including parts with disjoint ranges.
func TestMerge(t *testing.T) {
	for _, dist := range dists {
		xs := makeValues(dist, 1, 100_000)
		slices.Sort(xs) // parts with different ranges
		all := newFull(0.005)
		fill(all, xs)
		merged := newFull(0.005)
		for p := range 10 {
			part := newFull(0.005)
			fill(part, xs[p*10_000:(p+1)*10_000])
			if err := merged.Merge(part); err != nil {
				t.Fatal(err)
			}
		}
		same(t, all, merged)
	}
}

func TestMergeEmpty(t *testing.T) {
	h := New()
	if err := h.Merge(New()); err != nil {
		t.Fatal(err)
	}
	if h.Count() != 0 || !math.IsNaN(h.Quantile(0.5)) {
		t.Errorf("Count, Quantile(0.5) = %d, %v, want 0, NaN", h.Count(), h.Quantile(0.5))
	}
	other := New()
	other.Add(-5)
	other.Add(7)
	if err := h.Merge(other); err != nil {
		t.Fatal(err)
	}
	if h.Quantile(0) != -5 || h.Quantile(1) != 7 {
		t.Errorf("Quantile(0), Quantile(1) = %v, %v, want -5, 7", h.Quantile(0), h.Quantile(1))
	}
}

func TestMergeSelf(t *testing.T) {
	xs := makeValues("wide", 1, 10_000)
	h, double := newFull(0.005), newFull(0.005)
	fill(h, xs)
	fill(double, xs)
	fill(double, xs)
	if err := h.Merge(h); err != nil {
		t.Fatal(err)
	}
	same(t, h, double)
}

// TestMergeMismatch checks that merging histograms with different relative
// errors or ranges returns ErrMismatch and leaves h unchanged.
func TestMergeMismatch(t *testing.T) {
	tests := map[string][2]*Histogram{
		"different relErr": {NewError(0.01), NewError(0.001)},
		"different lo":     {NewRange(0.01, 1, 100), NewRange(0.01, 2, 100)},
		"different hi":     {NewRange(0.01, 1, 10), NewRange(0.01, 1, 100)},
	}
	for name, tt := range tests {
		h, other := tt[0], tt[1]
		h.Add(5)
		other.Add(7)
		other.Add(8)
		if err := h.Merge(other); err != ErrMismatch {
			t.Errorf("%s: Merge = %v, want ErrMismatch", name, err)
		}
		if h.Count() != 1 || h.Quantile(1) != 5 {
			t.Errorf("%s: Count, max = %d, %v, want 1, 5", name, h.Count(), h.Quantile(1))
		}
	}
}

func TestSize(t *testing.T) {
	h := New()
	h.Add(1.5) // one octave [1, 2)
	if h.Size() != 8<<defaultBits {
		t.Errorf("Size = %d, want %d", h.Size(), 8<<defaultBits)
	}
	h.Add(1000) // octaves [1, 1024)
	h.Add(-1)
	if want := 8 << defaultBits * 11; h.Size() != want {
		t.Errorf("Size = %d, want %d", h.Size(), want)
	}
}

func TestNoAllocs(t *testing.T) {
	xs := makeValues("lognormal", 1, 10_000)
	h := New()
	fill(h, xs)
	allocs := testing.AllocsPerRun(10, func() {
		fill(h, xs)
		h.Quantile(0.99)
		h.CDF(1)
	})
	if allocs != 0 {
		t.Errorf("allocs = %v, want 0", allocs)
	}
}

// TestSizeRange checks the memory documented for values from 1 to 1,000,000
// (20 octaves, from 2^0 to 2^20).
func TestSizeRange(t *testing.T) {
	tests := []struct {
		relErr float64
		size   int
	}{
		{0.1, 1280}, {0.01, 10240}, {0.005, 20480}, {0.001, 81920}, {0.0001, 1310720},
	}
	for _, tt := range tests {
		h := NewError(tt.relErr)
		h.Add(1)
		h.Add(1_000_000)
		if h.Size() != tt.size {
			t.Errorf("NewError(%v): Size = %d, want %d", tt.relErr, h.Size(), tt.size)
		}
	}
}
