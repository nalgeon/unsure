package bench

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	caio "github.com/caio/go-tdigest/v4"
	influx "github.com/influxdata/tdigest"
	"github.com/nalgeon/unsure/tdigest"
)

// tdDigest is the common interface for our Digest and the other implementations,
// so the same tests and benchmarks run against all of them.
// Merge is only called with a digest of the same implementation.
type tdDigest interface {
	Add(x float64)
	Quantile(q float64) float64
	CDF(x float64) float64
	Merge(other tdDigest)
}

// unsureTD adapts our Digest to the tdDigest interface.
type unsureTD struct{ d *tdigest.Digest }

func (d unsureTD) Add(x float64)              { d.d.Add(x) }
func (d unsureTD) Quantile(q float64) float64 { return d.d.Quantile(q) }
func (d unsureTD) CDF(x float64) float64      { return d.d.CDF(x) }
func (d unsureTD) Merge(other tdDigest)       { d.d.Merge(other.(unsureTD).d) }

// influxTD adapts github.com/influxdata/tdigest to the tdDigest interface.
type influxTD struct{ d *influx.TDigest }

func (d influxTD) Add(x float64)              { d.d.Add(x, 1) }
func (d influxTD) Quantile(q float64) float64 { return d.d.Quantile(q) }
func (d influxTD) CDF(x float64) float64      { return d.d.CDF(x) }

// Merge adds the other digest's centroids one by one. AddCentroidList is
// broken in v0.0.1: once its buffer is exactly full, it silently drops all
// further centroids until something calls process (a query or a single Add).
func (d influxTD) Merge(other tdDigest) {
	for _, c := range other.(influxTD).d.Centroids() {
		d.d.AddCentroid(c)
	}
}

// caioTD adapts github.com/caio/go-tdigest to the tdDigest interface.
type caioTD struct{ d *caio.TDigest }

func (d caioTD) Add(x float64) {
	if err := d.d.Add(x); err != nil {
		panic(err)
	}
}
func (d caioTD) Quantile(q float64) float64 { return d.d.Quantile(q) }
func (d caioTD) CDF(x float64) float64      { return d.d.CDF(x) }
func (d caioTD) Merge(other tdDigest) {
	if err := d.d.Merge(other.(caioTD).d); err != nil {
		panic(err)
	}
}

type tdImpl struct {
	name string
	new  func(delta int) tdDigest
}

var tdImpls = []tdImpl{
	{"unsure", func(delta int) tdDigest { return unsureTD{tdigest.NewCompression(delta)} }},
	// influxdata scales k1 to [0, c] instead of [-δ/4, δ/4], so compression
	// c keeps as many centroids as our δ = 2c (the same scale function).
	{"influx", func(delta int) tdDigest { return influxTD{influx.NewWithCompression(float64(delta) / 2)} }},
	// caio is the older tree-based t-digest. At the same compression,
	// it keeps about 13× more centroids than ours.
	{"caio", func(delta int) tdDigest {
		d, err := caio.New(caio.Compression(float64(delta)))
		if err != nil {
			panic(err)
		}
		return caioTD{d}
	}},
}

// tdValues returns n values from the named distribution:
// uniform, normal, lognormal (skewed), or sorted (uniform, in ascending order).
func tdValues(dist string, seed uint64, n int) []float64 {
	rng := rand.New(rand.NewPCG(seed, 0))
	xs := make([]float64, n)
	for i := range xs {
		switch dist {
		case "uniform", "sorted":
			xs[i] = rng.Float64()
		case "normal":
			xs[i] = rng.NormFloat64()
		case "lognormal":
			xs[i] = math.Exp(rng.NormFloat64())
		default:
			panic(dist)
		}
	}
	if dist == "sorted" {
		slices.Sort(xs)
	}
	return xs
}

// tdRankError returns how far q is from the range of ranks
// (fractions of values less than, and less than or equal to) of x in sorted.
func tdRankError(sorted []float64, x, q float64) float64 {
	lo, _ := slices.BinarySearch(sorted, x)
	hi := lo
	for hi < len(sorted) && sorted[hi] == x {
		hi++
	}
	n := float64(len(sorted))
	switch {
	case q < float64(lo)/n:
		return float64(lo)/n - q
	case q > float64(hi)/n:
		return q - float64(hi)/n
	}
	return 0
}

var tdAccuracyQ = []float64{0.0001, 0.001, 0.01, 0.1, 0.5, 0.9, 0.99, 0.999, 0.9999}

// TestTDigestAccuracy reports the mean rank error of Quantile for all
// implementations at δ=100, over several digests of 1M values, either
// filled directly or merged from 10 digests of 100K values each.
// Checks that ours is no less accurate than the reference.
func TestTDigestAccuracy(t *testing.T) {
	const delta = 100
	const n = 1_000_000
	const parts = 10
	const trials = 5
	for _, mode := range []string{"single", "merged"} {
		for _, dist := range []string{"uniform", "normal", "lognormal", "sorted"} {
			errs := make(map[string][]float64)
			for _, im := range tdImpls {
				errs[im.name] = make([]float64, len(tdAccuracyQ))
			}
			for trial := range uint64(trials) {
				xs := tdValues(dist, trial+1, n)
				sorted := slices.Clone(xs)
				slices.Sort(sorted)
				for _, im := range tdImpls {
					d := im.new(delta)
					if mode == "single" {
						for _, x := range xs {
							d.Add(x)
						}
					} else {
						for p := range parts {
							part := im.new(delta)
							for _, x := range xs[p*n/parts : (p+1)*n/parts] {
								part.Add(x)
							}
							d.Merge(part)
						}
					}
					for i, q := range tdAccuracyQ {
						errs[im.name][i] += tdRankError(sorted, d.Quantile(q), q) / trials
					}
				}
			}
			worse := 0
			for _, im := range tdImpls {
				line := fmt.Sprintf("δ=%d %s %s impl=%s rank err:", delta, mode, dist, im.name)
				for i, q := range tdAccuracyQ {
					line += fmt.Sprintf(" q=%v %.1e", q, errs[im.name][i])
				}
				t.Log(line)
			}
			for i := range tdAccuracyQ {
				// Rank errors of a few values or less are noise.
				if errs["unsure"][i] > errs["influx"][i]*1.5 && errs["unsure"][i] > 1e-5 {
					worse++
				}
			}
			if worse > 1 {
				t.Errorf("%s %s: rank error over 1.5× the reference at %d quantiles", mode, dist, worse)
			}
		}
	}
}

// TestTDigestSize reports the heap memory each implementation retains
// after adding n values.
func TestTDigestSize(t *testing.T) {
	for _, delta := range []int{50, 100, 500} {
		for _, n := range []int{1_000, 1_000_000} {
			xs := tdValues("normal", 1, n)
			line := fmt.Sprintf("δ=%d n=%d:", delta, n)
			for _, im := range tdImpls {
				var runs []int
				for range 5 {
					_, bytes := retained(func() tdDigest {
						d := im.new(delta)
						for _, x := range xs {
							d.Add(x)
						}
						return d
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

var tdBenchDelta = []int{50, 100, 500}

// tdBenchValues is the number of values a benchmark loop cycles through.
const tdBenchValues = 1_000_000

func BenchmarkTDigestAdd(b *testing.B) {
	xs := tdValues("normal", 1, tdBenchValues)
	for _, delta := range tdBenchDelta {
		for _, im := range tdImpls {
			b.Run(fmt.Sprintf("delta=%d/impl=%s", delta, im.name), func(b *testing.B) {
				d := im.new(delta)
				// Fill the digest, so the timed loop measures the steady state.
				for _, x := range tdValues("normal", 2, tdBenchValues) {
					d.Add(x)
				}
				i := 0
				for b.Loop() {
					d.Add(xs[i])
					i++
					if i == len(xs) {
						i = 0
					}
				}
			})
		}
	}
}

var sinkFloat float64

// tdBenchQ are the quantiles a Quantile benchmark loop cycles through.
var tdBenchQ = []float64{0.5, 0.9, 0.99, 0.999, 0.1, 0.01}

func BenchmarkTDigestQuantile(b *testing.B) {
	xs := tdValues("normal", 1, tdBenchValues)
	for _, delta := range tdBenchDelta {
		for _, im := range tdImpls {
			b.Run(fmt.Sprintf("delta=%d/impl=%s", delta, im.name), func(b *testing.B) {
				d := im.new(delta)
				for _, x := range xs {
					d.Add(x)
				}
				d.Quantile(0.5) // flush the buffer
				i := 0
				for b.Loop() {
					sinkFloat = d.Quantile(tdBenchQ[i])
					i++
					if i == len(tdBenchQ) {
						i = 0
					}
				}
			})
		}
	}
}

func BenchmarkTDigestCDF(b *testing.B) {
	xs := tdValues("normal", 1, tdBenchValues)
	for _, delta := range tdBenchDelta {
		for _, im := range tdImpls {
			b.Run(fmt.Sprintf("delta=%d/impl=%s", delta, im.name), func(b *testing.B) {
				d := im.new(delta)
				for _, x := range xs {
					d.Add(x)
				}
				d.Quantile(0.5) // flush the buffer
				i := 0
				for b.Loop() {
					sinkFloat = d.CDF(xs[i])
					i++
					if i == len(xs) {
						i = 0
					}
				}
			})
		}
	}
}

// tdMergeOthers is the number of different digests that
// BenchmarkTDigestMerge merges in turn.
const tdMergeOthers = 8

// BenchmarkTDigestMerge merges digests of 100K values each into one digest,
// as when gathering digests from many sources. Each merge adds more values
// to the receiver, so its centroids keep changing. influxdata merges into
// a buffer and compresses (sorting everything) once it's full.
func BenchmarkTDigestMerge(b *testing.B) {
	for _, delta := range tdBenchDelta {
		for _, im := range tdImpls {
			b.Run(fmt.Sprintf("delta=%d/impl=%s", delta, im.name), func(b *testing.B) {
				others := make([]tdDigest, tdMergeOthers)
				for i := range others {
					others[i] = im.new(delta)
					for _, x := range tdValues("normal", uint64(i+2), tdBenchValues/10) {
						others[i].Add(x)
					}
					others[i].Quantile(0.5) // flush the buffer
				}
				d := im.new(delta)
				for _, x := range tdValues("normal", 1, tdBenchValues) {
					d.Add(x)
				}
				i := 0
				for b.Loop() {
					d.Merge(others[i])
					i++
					if i == len(others) {
						i = 0
					}
				}
			})
		}
	}
}
