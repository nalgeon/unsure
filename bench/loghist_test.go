package bench

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/DataDog/sketches-go/ddsketch"
	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/nalgeon/unsure/loghist"
	"github.com/nalgeon/unsure/tdigest"
)

// lhHist is the common interface for our Histogram and the other implementations,
// so the same tests and benchmarks run against all of them.
// Merge is only called with a histogram of the same implementation.
type lhHist interface {
	Add(x float64)
	Quantile(q float64) float64
	Merge(other lhHist)
}

// unsureLH adapts our Histogram to the lhHist interface.
type unsureLH struct{ h *loghist.Histogram }

func (h unsureLH) Add(x float64)              { h.h.Add(x) }
func (h unsureLH) Quantile(q float64) float64 { return h.h.Quantile(q) }
func (h unsureLH) Merge(other lhHist) {
	if err := h.h.Merge(other.(unsureLH).h); err != nil {
		panic(err)
	}
}

// hdrLH adapts github.com/HdrHistogram/hdrhistogram-go to the lhHist interface.
// It records integers, so the values are integer nanoseconds.
type hdrLH struct{ h *hdr.Histogram }

func (h hdrLH) Add(x float64) {
	if err := h.h.RecordValue(int64(x)); err != nil {
		panic(err)
	}
}
func (h hdrLH) Quantile(q float64) float64 { return float64(h.h.ValueAtQuantile(q * 100)) }
func (h hdrLH) Merge(other lhHist) {
	if dropped := h.h.Merge(other.(hdrLH).h); dropped != 0 {
		panic("hdr: dropped values")
	}
}

// ddLH adapts github.com/DataDog/sketches-go to the lhHist interface.
type ddLH struct{ s *ddsketch.DDSketch }

func (h ddLH) Add(x float64) {
	if err := h.s.Add(x); err != nil {
		panic(err)
	}
}
func (h ddLH) Quantile(q float64) float64 {
	x, err := h.s.GetValueAtQuantile(q)
	if err != nil {
		panic(err)
	}
	return x
}
func (h ddLH) Merge(other lhHist) {
	if err := h.s.MergeWith(other.(ddLH).s); err != nil {
		panic(err)
	}
}

// tdigestLH adapts our t-digest to the lhHist interface, for the accuracy
// comparison only: its rank error doesn't depend on the relative error.
type tdigestLH struct{ d *tdigest.Digest }

func (h tdigestLH) Add(x float64)              { h.d.Add(x) }
func (h tdigestLH) Quantile(q float64) float64 { return h.d.Quantile(q) }
func (h tdigestLH) Merge(other lhHist)         { h.d.Merge(other.(tdigestLH).d) }

type lhImpl struct {
	name string
	new  func(relErr float64) lhHist
}

// hdrMax is the highest value HdrHistogram tracks: 1 hour in nanoseconds.
const hdrMax = 3_600_000_000_000

var lhImpls = []lhImpl{
	{"unsure", func(relErr float64) lhHist { return unsureLH{loghist.NewError(relErr)} }},
	// HdrHistogram's precision is in significant decimal digits.
	// With d digits, the max error is about 10^-d, so d = ⌈-log10(relErr)⌉.
	// It reports the highest value in a bucket, so its error reaches
	// a full bucket width (about 0.8% for d=2, 0.1% for d=3).
	{"hdr", func(relErr float64) lhHist {
		digits := int(math.Ceil(-math.Log10(relErr) - 1e-9))
		return hdrLH{hdr.New(1, hdrMax, digits)}
	}},
	// DDSketch with its default logarithmic mapping and buffered paginated store.
	{"dd", func(relErr float64) lhHist {
		s, err := ddsketch.NewDefaultDDSketch(relErr)
		if err != nil {
			panic(err)
		}
		return ddLH{s}
	}},
}

// lhTDigest is our t-digest with the default δ=100, for the accuracy comparison.
var lhTDigest = lhImpl{"tdigest", func(float64) lhHist { return tdigestLH{tdigest.New()} }}

// lhValues returns n latencies in integer nanoseconds from the named distribution:
// lognormal (median 1 ms), pareto (heavy tail, α=1.5, from 100 µs),
// or uniform (1 to 2 ms).
func lhValues(dist string, seed uint64, n int) []float64 {
	rng := rand.New(rand.NewPCG(seed, 0))
	xs := make([]float64, n)
	for i := range xs {
		var x float64
		switch dist {
		case "lognormal":
			x = 1e6 * math.Exp(rng.NormFloat64())
		case "pareto":
			x = 1e5 * math.Pow(1-rng.Float64(), -1/1.5)
		case "uniform":
			x = 1e6 * (1 + rng.Float64())
		default:
			panic(dist)
		}
		xs[i] = max(1, math.Round(x))
	}
	return xs
}

// lhRelError returns the relative distance from x to the values of sorted
// at quantile q, which are between ranks ⌊q·(n-1)⌋ (as in ours and DDSketch)
// and ⌈q·n⌉ (as in HdrHistogram), counting from 0.
func lhRelError(sorted []float64, x, q float64) float64 {
	n := len(sorted)
	lo := sorted[int(q*float64(n-1))]
	hi := sorted[min(n-1, int(math.Ceil(q*float64(n))))]
	switch {
	case x < lo:
		return (lo - x) / lo
	case x > hi:
		return (x - hi) / hi
	}
	return 0
}

var lhAccuracyQ = []float64{0.5, 0.9, 0.99, 0.999, 0.9999}

// lhBand returns lhBandSize quantiles spread evenly around q, within 10%
// of its distance to the closer end (0.9989 to 0.9991 for q=0.999).
// A single quantile always lands at about the same spot in its bucket,
// so its error depends on luck; a band of quantiles averages over the
// whole bucket.
func lhBand(q float64) []float64 {
	w := 0.1 * min(q, 1-q)
	qs := make([]float64, lhBandSize)
	for i := range qs {
		qs[i] = q - w + 2*w*float64(i)/float64(lhBandSize-1)
	}
	return qs
}

const lhBandSize = 101

var lhRelErrs = []float64{0.01, 0.004, 0.001}

// TestLoghistAccuracy reports the mean relative value error of Quantile for
// all implementations, over a band of quantiles around each target (see lhBand)
// in several histograms of 1M values. Our t-digest
// (δ=100) is shown for comparison. Checks that ours stays within its bound.
func TestLoghistAccuracy(t *testing.T) {
	const n = 1_000_000
	const trials = 5
	for _, dist := range []string{"lognormal", "pareto", "uniform"} {
		for _, relErr := range lhRelErrs {
			impls := lhImpls
			if relErr == lhRelErrs[0] {
				impls = append(slices.Clone(lhImpls), lhTDigest)
			}
			for _, im := range impls {
				errs := make([]float64, len(lhAccuracyQ))
				worst := 0.0
				for trial := range uint64(trials) {
					xs := lhValues(dist, trial+1, n)
					h := im.new(relErr)
					for _, x := range xs {
						h.Add(x)
					}
					slices.Sort(xs)
					for i, q := range lhAccuracyQ {
						for _, bq := range lhBand(q) {
							err := lhRelError(xs, h.Quantile(bq), bq)
							errs[i] += err / trials / lhBandSize
							worst = max(worst, err)
						}
					}
				}
				line := fmt.Sprintf("err=%v %s impl=%s rel err:", relErr, dist, im.name)
				if im.name == "tdigest" {
					line = fmt.Sprintf("δ=100 %s impl=%s rel err:", dist, im.name)
				}
				for i, q := range lhAccuracyQ {
					line += fmt.Sprintf(" q=%v %.3f%%", q, errs[i]*100)
				}
				line += fmt.Sprintf(" max %.3f%%", worst*100)
				t.Log(line)
				if im.name == "unsure" && worst > relErr {
					t.Errorf("err=%v %s: max error %.4f%% over the bound", relErr, dist, worst*100)
				}
			}
		}
	}
}

// TestLoghistSize reports the heap memory each implementation retains
// after adding 1M values.
func TestLoghistSize(t *testing.T) {
	const n = 1_000_000
	for _, dist := range []string{"lognormal", "pareto"} {
		xs := lhValues(dist, 1, n)
		for _, relErr := range lhRelErrs {
			impls := lhImpls
			if relErr == lhRelErrs[0] {
				impls = append(slices.Clone(lhImpls), lhTDigest)
			}
			line := fmt.Sprintf("err=%v %s:", relErr, dist)
			for _, im := range impls {
				var runs []int
				for range 5 {
					_, bytes := retained(func() lhHist {
						h := im.new(relErr)
						for _, x := range xs {
							h.Add(x)
						}
						return h
					})
					runs = append(runs, bytes)
				}
				slices.Sort(runs)
				line += fmt.Sprintf(" impl=%s %.1f KB", im.name, float64(runs[len(runs)/2])/1024)
			}
			t.Log(line)
		}
	}
}

// Benchmarks

// lhBenchValues is the number of values a benchmark loop cycles through.
const lhBenchValues = 1_000_000

func BenchmarkLoghistAdd(b *testing.B) {
	xs := lhValues("lognormal", 1, lhBenchValues)
	for _, relErr := range lhRelErrs {
		for _, im := range lhImpls {
			b.Run(fmt.Sprintf("err=%v/impl=%s", relErr, im.name), func(b *testing.B) {
				h := im.new(relErr)
				// Fill the histogram, so the timed loop measures the steady state.
				for _, x := range lhValues("lognormal", 2, lhBenchValues) {
					h.Add(x)
				}
				i := 0
				for b.Loop() {
					h.Add(xs[i])
					i++
					if i == len(xs) {
						i = 0
					}
				}
			})
		}
	}
}

// lhBenchQ are the quantiles a Quantile benchmark loop cycles through.
var lhBenchQ = []float64{0.5, 0.9, 0.99, 0.999, 0.1, 0.01}

func BenchmarkLoghistQuantile(b *testing.B) {
	xs := lhValues("lognormal", 1, lhBenchValues)
	for _, relErr := range lhRelErrs {
		for _, im := range lhImpls {
			b.Run(fmt.Sprintf("err=%v/impl=%s", relErr, im.name), func(b *testing.B) {
				h := im.new(relErr)
				for _, x := range xs {
					h.Add(x)
				}
				i := 0
				for b.Loop() {
					sinkFloat = h.Quantile(lhBenchQ[i])
					i++
					if i == len(lhBenchQ) {
						i = 0
					}
				}
			})
		}
	}
}

// lhMergeOthers is the number of different histograms that
// BenchmarkLoghistMerge merges in turn.
const lhMergeOthers = 8

// BenchmarkLoghistMerge merges histograms of 100K values each into one,
// as when gathering histograms from many sources.
func BenchmarkLoghistMerge(b *testing.B) {
	for _, relErr := range lhRelErrs {
		for _, im := range lhImpls {
			b.Run(fmt.Sprintf("err=%v/impl=%s", relErr, im.name), func(b *testing.B) {
				others := make([]lhHist, lhMergeOthers)
				for i := range others {
					others[i] = im.new(relErr)
					for _, x := range lhValues("lognormal", uint64(i+2), lhBenchValues/10) {
						others[i].Add(x)
					}
				}
				h := im.new(relErr)
				for _, x := range lhValues("lognormal", 1, lhBenchValues) {
					h.Add(x)
				}
				i := 0
				for b.Loop() {
					h.Merge(others[i])
					i++
					if i == len(others) {
						i = 0
					}
				}
			})
		}
	}
}
