package tdigest

import (
	"fmt"
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
		case "sorted", "reversed":
			xs[i] = rng.Float64()
		case "discrete":
			// Few distinct values, many duplicates.
			xs[i] = float64(rng.IntN(10))
		default:
			panic(dist)
		}
	}
	switch dist {
	case "sorted":
		slices.Sort(xs)
	case "reversed":
		slices.Sort(xs)
		slices.Reverse(xs)
	}
	return xs
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

// rankRange returns the fractions of sorted values less than x
// and less than or equal to x.
func rankRange(sorted []float64, x float64) (float64, float64) {
	lo, _ := slices.BinarySearchFunc(sorted, x, func(a, b float64) int {
		if a < b {
			return -1
		}
		return 1
	})
	hi, _ := slices.BinarySearchFunc(sorted, x, func(a, b float64) int {
		if a <= b {
			return -1
		}
		return 1
	})
	n := float64(len(sorted))
	return float64(lo) / n, float64(hi) / n
}

// rankError returns how far q is from the range of ranks of x in sorted.
func rankError(sorted []float64, x, q float64) float64 {
	lo, hi := rankRange(sorted, x)
	switch {
	case q < lo:
		return lo - q
	case q > hi:
		return q - hi
	}
	return 0
}

func fill(d *Digest, xs []float64) {
	for _, x := range xs {
		d.Add(x)
	}
}

// check verifies the digest's invariants.
func check(t *testing.T, d *Digest, delta int) {
	t.Helper()
	d.flush()
	cs := d.centroids
	if len(cs) > delta+2 {
		t.Errorf("%d centroids, more than δ+2 = %d", len(cs), delta+2)
	}
	if len(d.cum) != len(cs)+1 {
		t.Fatalf("len(cum) = %d, want %d", len(d.cum), len(cs)+1)
	}
	w := 0.0
	for i, c := range cs {
		if c.weight < 1 || c.weight != math.Trunc(c.weight) {
			t.Fatalf("centroid %d: weight %v", i, c.weight)
		}
		if i > 0 && c.mean < cs[i-1].mean {
			t.Fatalf("centroid %d: mean %v < previous %v", i, c.mean, cs[i-1].mean)
		}
		if c.mean < d.min || c.mean > d.max {
			t.Fatalf("centroid %d: mean %v outside [%v, %v]", i, c.mean, d.min, d.max)
		}
		if d.cum[i] != w {
			t.Fatalf("cum[%d] = %v, want %v", i, d.cum[i], w)
		}
		w += c.weight
	}
	if w != d.total || d.cum[len(cs)] != w {
		t.Fatalf("total weight %v, total %v", w, d.total)
	}
}

func TestNew(t *testing.T) {
	d := New()
	if cap(d.centroids) != defaultDelta+2 || cap(d.buf) != bufFactor*defaultDelta {
		t.Errorf("New: %d centroids, %d buffer", cap(d.centroids), cap(d.buf))
	}
	for _, delta := range []int{minDelta, 100, 1000, maxDelta} {
		d := NewCompression(delta)
		want := 16*2*(delta+2) + 8*(delta+3) + 8*2*bufFactor*delta
		if d.Size() != want {
			t.Errorf("NewCompression(%d): size %d, want %d", delta, d.Size(), want)
		}
	}
}

func TestNewCompressionPanics(t *testing.T) {
	for _, delta := range []int{-1, 0, minDelta - 1, maxDelta + 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewCompression(%d) didn't panic", delta)
				}
			}()
			NewCompression(delta)
		})
	}
}

func TestEmpty(t *testing.T) {
	d := New()
	if c := d.Count(); c != 0 {
		t.Errorf("Count = %d", c)
	}
	for _, q := range []float64{0, 0.5, 1} {
		if v := d.Quantile(q); !math.IsNaN(v) {
			t.Errorf("Quantile(%v) = %v, want NaN", q, v)
		}
	}
	if v := d.CDF(0); !math.IsNaN(v) {
		t.Errorf("CDF(0) = %v, want NaN", v)
	}
	// Merging empty digests keeps them empty.
	d.Merge(New())
	New().Merge(d)
	if d.Count() != 0 || !math.IsNaN(d.Quantile(0.5)) {
		t.Errorf("after merging empty: Count = %d, Quantile = %v", d.Count(), d.Quantile(0.5))
	}
}

func TestSingle(t *testing.T) {
	d := New()
	d.Add(42)
	if c := d.Count(); c != 1 {
		t.Errorf("Count = %d", c)
	}
	for _, q := range []float64{0, 0.01, 0.5, 0.99, 1} {
		if v := d.Quantile(q); v != 42 {
			t.Errorf("Quantile(%v) = %v", q, v)
		}
	}
	for x, want := range map[float64]float64{41: 0, 42: 0.5, 43: 1} {
		if v := d.CDF(x); v != want {
			t.Errorf("CDF(%v) = %v, want %v", x, v, want)
		}
	}
}

func TestInvalid(t *testing.T) {
	d := New()
	for _, x := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		d.Add(x)
	}
	if c := d.Count(); c != 0 {
		t.Errorf("Count = %d after adding NaN and Inf", c)
	}
	d.Add(1)
	d.Add(2)
	for _, q := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		if v := d.Quantile(q); !math.IsNaN(v) {
			t.Errorf("Quantile(%v) = %v, want NaN", q, v)
		}
	}
	if v := d.CDF(math.NaN()); !math.IsNaN(v) {
		t.Errorf("CDF(NaN) = %v, want NaN", v)
	}
	if v := d.CDF(math.Inf(-1)); v != 0 {
		t.Errorf("CDF(-Inf) = %v", v)
	}
	if v := d.CDF(math.Inf(1)); v != 1 {
		t.Errorf("CDF(+Inf) = %v", v)
	}
}

// TestExact checks that the estimates are exact while all centroids have
// weight 1: Quantile(q) is the value at rank ⌊q·n⌋ (0-based, clamped),
// and CDF counts the values below x plus half of those equal to x.
func TestExact(t *testing.T) {
	const n = 50
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = float64(i)
	}
	sorted := slices.Clone(xs)
	rand.New(rand.NewPCG(1, 0)).Shuffle(n, func(i, j int) { xs[i], xs[j] = xs[j], xs[i] })
	d := New()
	fill(d, xs)
	check(t, d, defaultDelta)
	for _, c := range d.centroids {
		if c.weight != 1 {
			t.Fatalf("n=%d: centroid with weight %v", n, c.weight)
		}
	}
	for i := 0; i <= 1000; i++ {
		q := float64(i) / 1000
		want := sorted[min(int(q*n), n-1)]
		if v := d.Quantile(q); v != want {
			t.Errorf("Quantile(%v) = %v, want %v", q, v, want)
		}
	}
	for _, x := range sorted {
		for _, dx := range []float64{-0.5, 0, 0.5} {
			want := rank(sorted, x+dx)
			if v := d.CDF(x + dx); math.Abs(v-want) > 1e-12 {
				t.Errorf("CDF(%v) = %v, want %v", x+dx, v, want)
			}
		}
	}
}

func TestMinMax(t *testing.T) {
	for _, dist := range []string{"uniform", "normal", "lognormal", "sorted", "reversed"} {
		xs := makeValues(dist, 1, 100_000)
		d := New()
		fill(d, xs)
		lo, hi := slices.Min(xs), slices.Max(xs)
		if v := d.Quantile(0); v != lo {
			t.Errorf("%s: Quantile(0) = %v, want %v", dist, v, lo)
		}
		if v := d.Quantile(1); v != hi {
			t.Errorf("%s: Quantile(1) = %v, want %v", dist, v, hi)
		}
		if v := d.CDF(lo - 1); v != 0 {
			t.Errorf("%s: CDF below min = %v", dist, v)
		}
		if v := d.CDF(hi + 1); v != 1 {
			t.Errorf("%s: CDF above max = %v", dist, v)
		}
	}
}

// TestRankNextToMinMax checks that ranks right next to the
// min and max (q = 1/n and 1-1/n) are not NaN.
// TestRadixSort checks radixSort against slices.Sort, including
// both flip modes, ±0, duplicates and all exponents.
func TestRadixSort(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 1))
	for it := range 1000 {
		n := 1 + r.IntN(3000)
		xs := make([]float64, n)
		for i := range xs {
			switch it % 4 {
			case 0:
				xs[i] = r.NormFloat64()
			case 1: // random bits with any finite exponent
				xs[i] = math.Float64frombits(r.Uint64()&^(0x7ff<<52) | uint64(r.IntN(0x7ff))<<52)
			case 2: // few distinct values, ±0
				xs[i] = float64(r.IntN(5) - 2)
				if r.IntN(3) == 0 {
					xs[i] = math.Copysign(0, -1)
				}
			case 3: // large offset, same high bytes
				xs[i] = 1e12 + r.Float64()
			}
		}
		flip := uint64(0)
		if it%2 == 1 {
			flip = ^uint64(0)
			for i := range xs {
				xs[i] = -xs[i]
			}
		}
		keys := make([]uint64, n)
		for i, x := range xs {
			keys[i] = key(x)
		}
		if flip != 0 {
			for i := range xs {
				xs[i] = -xs[i]
			}
		}
		slices.Sort(xs)
		got := radixSort(keys, make([]uint64, n), flip)
		for i, k := range got {
			if v := value(k); v != xs[i] {
				t.Fatalf("it=%d n=%d: [%d] = %v, want %v", it, n, i, v, xs[i])
			}
		}
	}
}

func TestRankNextToMinMax(t *testing.T) {
	for _, delta := range []int{10, 20, 50, 100, 200, 500} {
		for n := 2; n <= 20_000; n = n*11/10 + 1 {
			for seed := range uint64(5) {
				r := rand.New(rand.NewPCG(seed, uint64(n)))
				d := NewCompression(delta)
				for range n {
					d.Add(r.NormFloat64())
				}
				for _, q := range []float64{1 / float64(n), 1 - 1/float64(n)} {
					if v := d.Quantile(q); math.IsNaN(v) {
						t.Fatalf("δ=%d n=%d seed=%d: Quantile(%v) = NaN", delta, n, seed, q)
					}
				}
			}
		}
	}
}

func TestDuplicates(t *testing.T) {
	d := New()
	for range 10_000 {
		d.Add(7)
	}
	check(t, d, defaultDelta)
	for _, q := range []float64{0, 0.001, 0.5, 0.999, 1} {
		if v := d.Quantile(q); v != 7 {
			t.Errorf("Quantile(%v) = %v", q, v)
		}
	}
	if v := d.CDF(7); v != 0.5 {
		t.Errorf("CDF(7) = %v", v)
	}
}

// TestMonotonic checks that Quantile and CDF never decrease.
func TestMonotonic(t *testing.T) {
	for _, dist := range []string{"uniform", "lognormal", "discrete"} {
		xs := makeValues(dist, 1, 100_000)
		d := New()
		fill(d, xs)
		prev := math.Inf(-1)
		for i := 0; i <= 10_000; i++ {
			v := d.Quantile(float64(i) / 10_000)
			if v < prev {
				t.Fatalf("%s: Quantile(%v) = %v < %v", dist, float64(i)/10_000, v, prev)
			}
			prev = v
		}
		lo, hi := d.Quantile(0), d.Quantile(1)
		prev = 0
		for i := 0; i <= 10_000; i++ {
			x := lo + (hi-lo)*float64(i)/10_000
			v := d.CDF(x)
			if v < prev || v > 1 {
				t.Fatalf("%s: CDF(%v) = %v after %v", dist, x, v, prev)
			}
			prev = v
		}
	}
}

// accuracyQ are the quantiles checked for accuracy, with the max rank
// error allowed for a single digest at δ=100 and n=1M: about 3× the largest
// error over 10 digests of uniform, normal, and lognormal values.
var accuracyQ = []struct{ q, maxErr float64 }{
	{0.0001, 0.0004},
	{0.001, 0.0008},
	{0.01, 0.0012},
	{0.1, 0.0015},
	{0.5, 0.0015},
	{0.9, 0.0015},
	{0.99, 0.0012},
	{0.999, 0.0008},
	{0.9999, 0.0004},
}

// TestAccuracy checks the rank error of Quantile and the error of CDF
// for different distributions and input orders.
func TestAccuracy(t *testing.T) {
	const n = 1_000_000
	for _, dist := range []string{"uniform", "normal", "lognormal", "sorted", "reversed", "discrete"} {
		xs := makeValues(dist, 1, n)
		d := New()
		fill(d, xs)
		check(t, d, defaultDelta)
		sorted := slices.Clone(xs)
		slices.Sort(sorted)
		for _, a := range accuracyQ {
			v := d.Quantile(a.q)
			if e := rankError(sorted, v, a.q); e > a.maxErr {
				t.Errorf("%s: Quantile(%v) = %v, rank error %.6f > %v", dist, a.q, v, e, a.maxErr)
			}
			if dist == "discrete" {
				continue
			}
			x := sorted[int(a.q*n)]
			if e := math.Abs(d.CDF(x) - rank(sorted, x)); e > a.maxErr {
				t.Errorf("%s: CDF(%v) error %.6f > %v", dist, x, e, a.maxErr)
			}
		}
	}
}

// TestCompression checks that the error shrinks as δ grows: at least as
// 1/δ above the default, and no faster than 1/δ^1.5 below it.
func TestCompression(t *testing.T) {
	const n = 1_000_000
	xs := makeValues("normal", 1, n)
	sorted := slices.Clone(xs)
	slices.Sort(sorted)
	for _, delta := range []int{minDelta, 50, 100, 500, 1000} {
		d := NewCompression(delta)
		fill(d, xs)
		check(t, d, delta)
		scale := float64(defaultDelta) / float64(delta)
		if scale > 1 {
			scale = math.Pow(scale, 1.5)
		}
		for _, a := range accuracyQ {
			v := d.Quantile(a.q)
			if e := rankError(sorted, v, a.q); e > a.maxErr*scale {
				t.Errorf("δ=%d: Quantile(%v) rank error %.6f > %v", delta, a.q, e, a.maxErr*scale)
			}
		}
	}
}

func TestMerge(t *testing.T) {
	const n = 1_000_000
	const parts = 10
	for _, dist := range []string{"uniform", "lognormal", "sorted"} {
		xs := makeValues(dist, 1, n)
		d := New()
		for p := range parts {
			part := New()
			fill(part, xs[p*n/parts:(p+1)*n/parts])
			d.Merge(part)
			check(t, d, defaultDelta)
		}
		if d.Count() != n {
			t.Errorf("%s: Count = %d", dist, d.Count())
		}
		sorted := slices.Clone(xs)
		slices.Sort(sorted)
		if d.Quantile(0) != sorted[0] || d.Quantile(1) != sorted[n-1] {
			t.Errorf("%s: min/max %v/%v, want %v/%v", dist, d.Quantile(0), d.Quantile(1), sorted[0], sorted[n-1])
		}
		// Merging loses some accuracy: each merge pass moves
		// values between centroids.
		for _, a := range accuracyQ {
			v := d.Quantile(a.q)
			if e := rankError(sorted, v, a.q); e > 2*a.maxErr {
				t.Errorf("%s: Quantile(%v) rank error %.6f > %v", dist, a.q, e, 2*a.maxErr)
			}
		}
	}
}

func TestMergeBuffered(t *testing.T) {
	// Both digests have unflushed values: all of them are merged,
	// and other's estimates don't change.
	a, b := New(), New()
	a.Add(1)
	a.Add(4)
	b.Add(2)
	b.Add(3)
	a.Merge(b)
	if a.Count() != 4 || b.Count() != 2 {
		t.Errorf("Count = %d, %d", a.Count(), b.Count())
	}
	for q, want := range map[float64]float64{0: 1, 0.25: 2, 0.5: 3, 0.75: 4, 1: 4} {
		if v := a.Quantile(q); v != want {
			t.Errorf("Quantile(%v) = %v, want %v", q, v, want)
		}
	}
	if v := b.Quantile(0.5); v != 3 {
		t.Errorf("other's Quantile(0.5) = %v", v)
	}
}

func TestMergeSelf(t *testing.T) {
	d := New()
	fill(d, makeValues("uniform", 1, 10_000))
	q := d.Quantile(0.5)
	d.Merge(d)
	check(t, d, defaultDelta)
	if d.Count() != 20_000 {
		t.Errorf("Count = %d", d.Count())
	}
	if v := d.Quantile(0.5); math.Abs(v-q) > 0.01 {
		t.Errorf("Quantile(0.5) = %v, was %v", v, q)
	}
}

func TestMergeCompression(t *testing.T) {
	// Merging a digest with a different compression keeps the receiver's.
	for _, deltas := range [][2]int{{100, 1000}, {1000, 100}, {10, 10000}} {
		a, b := NewCompression(deltas[0]), NewCompression(deltas[1])
		fill(a, makeValues("normal", 1, 100_000))
		fill(b, makeValues("normal", 2, 100_000))
		a.Merge(b)
		check(t, a, deltas[0])
		if a.Count() != 200_000 {
			t.Errorf("δ=%v: Count = %d", deltas, a.Count())
		}
		if v := a.Quantile(0.5); math.Abs(v) > 0.05 {
			t.Errorf("δ=%v: median = %v", deltas, v)
		}
	}
}

func TestLargeValues(t *testing.T) {
	// Values with a large offset and a small spread.
	const n = 100_000
	xs := makeValues("uniform", 1, n)
	for i := range xs {
		xs[i] = 1e12 + xs[i]
	}
	d := New()
	fill(d, xs)
	check(t, d, defaultDelta)
	if v := d.Quantile(0.5); math.Abs(v-(1e12+0.5)) > 0.01 {
		t.Errorf("Quantile(0.5) = %v", v)
	}
}

func TestNoAllocs(t *testing.T) {
	xs := makeValues("uniform", 1, 100_000)
	d, other := New(), New()
	fill(other, xs)
	i := 0
	if n := testing.AllocsPerRun(100_000, func() {
		d.Add(xs[i%len(xs)])
		i++
	}); n != 0 {
		t.Errorf("Add: %v allocs", n)
	}
	if n := testing.AllocsPerRun(1000, func() {
		d.Add(xs[i%len(xs)])
		i++
		d.Quantile(0.99)
		d.CDF(0.5)
	}); n != 0 {
		t.Errorf("Quantile/CDF: %v allocs", n)
	}
	if n := testing.AllocsPerRun(1000, func() { d.Merge(other) }); n != 0 {
		t.Errorf("Merge: %v allocs", n)
	}
}
