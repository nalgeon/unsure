package bench

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
	"testing"

	minwise "github.com/dgryski/go-minhash"
	"github.com/nalgeon/unsure/internal/wyhash"
	"github.com/nalgeon/unsure/minhash"
)

// MinHash is compared against dgryski/go-minhash MinWise, classic MinHash
// with k values per item (computed as h1 + i·h2), at the same k. It takes
// pluggable hash functions, so it gets wyhash like ours, with two seeds.

type mhSketch interface {
	Add(item []byte)
	Merge(other mhSketch) error
	Signature() mhSignature
}

type mhSignature interface {
	Similarity(other mhSignature) float64
}

// unsureMH adapts our Sketch to mhSketch.
type unsureMH struct{ s *minhash.Sketch }

func (s unsureMH) Add(item []byte)            { s.s.Add(item) }
func (s unsureMH) Merge(other mhSketch) error { return s.s.Merge(other.(unsureMH).s) }
func (s unsureMH) Signature() mhSignature     { return unsureSig{s.s.Signature()} }

type unsureSig struct{ sig minhash.Signature }

func (s unsureSig) Similarity(other mhSignature) float64 {
	return s.sig.Similarity(other.(unsureSig).sig)
}

// dgryskiMH adapts MinWise to mhSketch. Its signature is the sketch itself:
// the k minimums, compared with MinWise.Similarity.
type dgryskiMH struct{ m *minwise.MinWise }

func (s dgryskiMH) Add(item []byte) { s.m.Push(item) }
func (s dgryskiMH) Merge(other mhSketch) error {
	s.m.Merge(other.(dgryskiMH).m)
	return nil
}
func (s dgryskiMH) Signature() mhSignature { return s }
func (s dgryskiMH) Similarity(other mhSignature) float64 {
	return s.m.Similarity(other.(dgryskiMH).m)
}

var wyhashSeed1 = wyhash.Seed(1)

type mhImpl struct {
	name string
	new  func(k int) mhSketch
}

var mhImpls = []mhImpl{
	{"unsure", func(k int) mhSketch { return unsureMH{minhash.New(k)} }},
	{"dgryski", func(k int) mhSketch {
		h1 := func(b []byte) uint64 { return wyhash.Hash(b, wyhash.Seed0) }
		h2 := func(b []byte) uint64 { return wyhash.Hash(b, wyhashSeed1) }
		return dgryskiMH{minwise.NewMinWise(h1, h2, k)}
	}},
}

type mhResult struct{ bias, rmse float64 }

// mhAccuracy estimates the similarity of pairs of sets A = S ∪ X and
// B = S ∪ Y with |A ∪ B| = u for each u in points (ascending), and
// |S| / u ≈ j, |X| = |Y|. Returns the bias and RMSE of the estimate at
// each point over independent trials, run in parallel.
func mhAccuracy(newSketch func() mhSketch, j float64, points []int, trials int) []mhResult {
	errs := make([][]float64, trials) // errs[trial][point]
	next := make(chan int)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			// Keys are (trial·3 + part, index), where part is
			// 0 for S, 1 for X and 2 for Y.
			var key [16]byte
			add := func(s mhSketch, trial, part, from, to int) {
				binary.LittleEndian.PutUint64(key[:8], uint64(trial*3+part))
				for i := from; i < to; i++ {
					binary.LittleEndian.PutUint64(key[8:], uint64(i))
					s.Add(key[:])
				}
			}
			for trial := range next {
				a, b := newSketch(), newSketch()
				shared, diff := 0, 0
				for _, u := range points {
					s := int(math.Round(j * float64(u)))
					d := (u - s) / 2
					add(a, trial, 0, shared, s)
					add(b, trial, 0, shared, s)
					add(a, trial, 1, diff, d)
					add(b, trial, 2, diff, d)
					shared, diff = s, d
					want := float64(s) / float64(s+2*d)
					got := a.Signature().Similarity(b.Signature())
					errs[trial] = append(errs[trial], got-want)
				}
			}
		})
	}
	for trial := range trials {
		next <- trial
	}
	close(next)
	wg.Wait()
	res := make([]mhResult, len(points))
	for p := range points {
		var sum, sum2 float64
		for _, e := range errs {
			sum += e[p]
			sum2 += e[p] * e[p]
		}
		res[p] = mhResult{sum / float64(trials), math.Sqrt(sum2 / float64(trials))}
	}
	return res
}

// TestMinHashAccuracy reports the bias and RMSE of the similarity estimate
// for pairs of sets with similarity J = 0.1 / 0.5 / 0.9 and union sizes u
// from k/16 to 64k, for all implementations at k=256, next to classic
// MinHash's standard error √(J(1−J)/k) (stdErr).
//
// When all buckets are filled, they split the union into k disjoint parts,
// so the k samples are distinct items (sampling without replacement), and
// the error is stdErr·√((u−k)/(u−1)) (oph), below stdErr for u of a few k.
// When some buckets are empty, their copies of other buckets add error.
// Checks that ours has no bias, matches oph for u ≥ 4k, where almost all
// buckets are filled, and stays within 1.5× stdErr for smaller sets.
func TestMinHashAccuracy(t *testing.T) {
	const k = 256
	const trials = 5000
	points := []int{16, 64, 256, 1024, 4096, 16384}
	for _, j := range []float64{0.1, 0.5, 0.9} {
		res := make(map[string][]mhResult)
		for _, im := range mhImpls {
			res[im.name] = mhAccuracy(func() mhSketch { return im.new(k) }, j, points, trials)
		}
		for p, u := range points {
			// Rounding |S| moves J at small u, so use the actual J.
			s := math.Round(j * float64(u))
			d := float64((u - int(s)) / 2)
			jj := s / (s + 2*d)
			stdErr := math.Sqrt(jj * (1 - jj) / k)
			oph := stdErr * math.Sqrt(max(0, float64(u-k))/float64(u-1))
			line := fmt.Sprintf("k=%d J=%.1f u=%d (%.3gk) stdErr=%.2f%%", k, j, u, float64(u)/k, stdErr*100)
			if u >= 4*k {
				line += fmt.Sprintf(" oph=%.2f%%", oph*100)
			}
			line += ":"
			for _, im := range mhImpls {
				r := res[im.name][p]
				line += fmt.Sprintf(" impl=%s bias=%+.2f%% rmse=%.2f%%", im.name, r.bias*100, r.rmse*100)
			}
			t.Log(line)
			// With 5000 trials, the mean error is within ±stdErr/20
			// (3.5 standard deviations), and the RMSE within ±3% (3σ).
			r := res["unsure"][p]
			if math.Abs(r.bias) > stdErr/20 {
				t.Errorf("J=%.1f u=%d: bias %+.4f, more than stdErr/20 (%.4f)", j, u, r.bias, stdErr/20)
			}
			if u < 4*k {
				if r.rmse > stdErr*1.5 {
					t.Errorf("J=%.1f u=%d: rmse %.4f, more than 1.5 × stdErr (%.4f)", j, u, r.rmse, stdErr)
				}
				continue
			}
			// At u=4k, about k/e⁴ ≈ 2% of buckets are still empty.
			if r.rmse > oph*1.07 {
				t.Errorf("J=%.1f u=%d: rmse %.4f, more than 1.07 × oph (%.4f)", j, u, r.rmse, oph)
			}
		}
	}
}

// TestMinHashSize reports the heap memory each implementation retains
// for a sketch with k buckets after adding 10,000 distinct items.
func TestMinHashSize(t *testing.T) {
	keys := makeKeys(1, 10_000)
	for _, k := range mhBenchK {
		line := fmt.Sprintf("k=%d:", k)
		for _, im := range mhImpls {
			// A single measurement may catch unrelated heap changes,
			// so take the median of several.
			var runs []int
			for range 5 {
				_, bytes := retained(func() mhSketch {
					s := im.new(k)
					for _, key := range keys {
						s.Add(key)
					}
					return s
				})
				runs = append(runs, bytes)
			}
			slices.Sort(runs)
			line += fmt.Sprintf(" impl=%s %.1f KB", im.name, float64(runs[len(runs)/2])/1024)
		}
		t.Log(line)
	}
}

// Benchmarks

// mhBenchK are typical signature sizes.
var mhBenchK = []int{64, 256, 1024}

// mhBenchKeys is the number of distinct keys a benchmark loop cycles through.
const mhBenchKeys = 1_000_000

func BenchmarkMinHashAdd(b *testing.B) {
	keys := makeKeys(1, mhBenchKeys)
	for _, k := range mhBenchK {
		for _, im := range mhImpls {
			b.Run(fmt.Sprintf("k=%d/impl=%s", k, im.name), func(b *testing.B) {
				s := im.new(k)
				// Fill the sketch, so the timed loop measures the steady state
				// (most adds don't change it).
				for _, key := range makeKeys(3, 100*k) {
					s.Add(key)
				}
				i := 0
				for b.Loop() {
					s.Add(keys[i])
					i++
					if i == len(keys) {
						i = 0
					}
				}
			})
		}
	}
}

var sinkSig minhash.Signature

// BenchmarkMinHashSignature measures our Signature for a set of n items.
// For n ≫ k it only copies the buckets, for n < k it also fills the empty
// ones. The reference's signature is its internal slice, so it's not
// benchmarked.
func BenchmarkMinHashSignature(b *testing.B) {
	for _, k := range mhBenchK {
		for _, n := range []int{k / 16, k / 4, 100 * k} {
			b.Run(fmt.Sprintf("k=%d/n=%d/impl=unsure", k, n), func(b *testing.B) {
				s := minhash.New(k)
				for _, key := range makeKeys(1, n) {
					s.Add(key)
				}
				for b.Loop() {
					sinkSig = s.Signature()
				}
			})
		}
	}
}

// BenchmarkMinHashSimilarity compares the signatures of two sets of 10k
// items with similarity 0.5.
func BenchmarkMinHashSimilarity(b *testing.B) {
	shared, xs, ys := makeKeys(1, 5000), makeKeys(2, 2500), makeKeys(3, 2500)
	for _, k := range mhBenchK {
		for _, im := range mhImpls {
			b.Run(fmt.Sprintf("k=%d/impl=%s", k, im.name), func(b *testing.B) {
				x, y := im.new(k), im.new(k)
				for _, key := range shared {
					x.Add(key)
					y.Add(key)
				}
				for _, key := range xs {
					x.Add(key)
				}
				for _, key := range ys {
					y.Add(key)
				}
				sx, sy := x.Signature(), y.Signature()
				for b.Loop() {
					sinkFloat = sx.Similarity(sy)
				}
			})
		}
	}
}

// BenchmarkMinHashMerge merges a sketch of 10k items into another one
// with different items. After the first loop, nothing changes.
func BenchmarkMinHashMerge(b *testing.B) {
	xs, ys := makeKeys(1, 10_000), makeKeys(2, 10_000)
	for _, k := range mhBenchK {
		for _, im := range mhImpls {
			b.Run(fmt.Sprintf("k=%d/impl=%s", k, im.name), func(b *testing.B) {
				x, y := im.new(k), im.new(k)
				for _, key := range xs {
					x.Add(key)
				}
				for _, key := range ys {
					y.Add(key)
				}
				for b.Loop() {
					if err := x.Merge(y); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
