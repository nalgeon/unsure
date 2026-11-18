package bench

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/axiomhq/hyperloglog"
	"github.com/nalgeon/unsure/hll"
	"github.com/nalgeon/unsure/ull"
)

// UltraLogLog is compared against HyperLogLog implementations, our hll and
// axiomhq/hyperloglog, at the same number of one-byte registers, and in
// TestULLAccuracy also at ours with half the registers (the default p=13).
// The adapters implement hllSketch from hll_test.go.

// unsureULL adapts our UltraLogLog Sketch to the hllSketch interface.
type unsureULL struct{ s *ull.Sketch }

func (s unsureULL) Add(item []byte)             { s.s.Add(item) }
func (s unsureULL) Count() uint64               { return uint64(s.s.Count()) }
func (s unsureULL) Merge(other hllSketch) error { return s.s.Merge(other.(unsureULL).s) }

var ullImpls = []hllImpl{
	{"unsure", func(p int) hllSketch { return unsureULL{ull.NewPrecision(p)} }},
	{"hll", func(p int) hllSketch { return unsureHLL{hll.NewPrecision(p)} }},
	{"axiom", func(p int) hllSketch {
		s, err := hyperloglog.NewSketch(uint8(p), true)
		if err != nil {
			panic(err)
		}
		return axiomHLL{s}
	}},
}

// ullStdErr is UltraLogLog's theoretical relative standard error.
func ullStdErr(m float64) float64 { return math.Sqrt(0.6118931496978437 / m) }

// TestULLAccuracy reports the bias and relative standard error of all
// implementations at the same number of registers (p=14, 16 KB), and of ours
// at its default p=13 (8 KB), over many independent sketches and
// cardinalities from tiny to far beyond the number of registers. Checks that
// ours matches its theoretical error, beats HyperLogLog's at the same memory,
// and nearly matches it with half the memory.
func TestULLAccuracy(t *testing.T) {
	const p = 14
	const trials = 1000
	points := []int{100, 1000, 10_000, 20_000, 40_000, 80_000, 160_000, 1_000_000}
	type config struct {
		name string
		p    int
		im   hllImpl
	}
	configs := []config{{"unsure/p=13", p - 1, ullImpls[0]}}
	for _, im := range ullImpls {
		configs = append(configs, config{im.name, p, im})
	}
	res := make(map[string][]hllResult)
	for _, c := range configs {
		res[c.name] = hllAccuracy(func() hllSketch { return c.im.new(c.p) }, points, trials)
	}
	notBetter, worse := 0, 0
	for j, n := range points {
		line := fmt.Sprintf("p=%d n=%d (%.2gm):", p, n, float64(n)/(1<<p))
		for _, c := range configs {
			r := res[c.name][j]
			line += fmt.Sprintf(" impl=%s bias=%+.2f%% rmse=%.2f%%", c.name, r.bias*100, r.rmse*100)
		}
		t.Log(line)
		hl := res["hll"][j]
		for _, c := range configs {
			if c.im.name != "unsure" {
				continue
			}
			r := res[c.name][j]
			stdErr := ullStdErr(float64(int(1) << c.p))
			if math.Abs(r.bias) > stdErr/2 {
				t.Errorf("%s n=%d: bias %+.4f, more than stdErr/2 (%.4f)", c.name, n, r.bias, stdErr/2)
			}
			if r.rmse > stdErr*1.2 {
				t.Errorf("%s n=%d: rmse %.4f, more than 1.2 × stdErr (%.4f)", c.name, n, r.rmse, stdErr)
			}
		}
		// Asymptotically 0.782 vs 1.04 × 1/√m at the same m, and
		// 1.106 vs 1.04 with half the registers. With 1000 trials,
		// an RMSE is within ±5% about 95% of the time.
		if n >= 1<<p && res["unsure"][j].rmse > hl.rmse*0.85 {
			notBetter++
		}
		if n >= 1<<p && res["unsure/p=13"][j].rmse > hl.rmse*1.15 {
			worse++
		}
	}
	if notBetter > 1 {
		t.Errorf("p=%d: rmse is over 0.85× our HyperLogLog's at %d cardinalities", p, notBetter)
	}
	if worse > 1 {
		t.Errorf("p=%d: rmse is over 1.15× our HyperLogLog's at p=%d at %d cardinalities", p-1, p, worse)
	}
}

// TestULLSize reports the heap memory each implementation retains
// after adding n distinct items.
func TestULLSize(t *testing.T) {
	for _, p := range []int{10, 13, 14, 18} {
		for _, n := range []int{100, 1_000, 10_000, 100_000, 1_000_000} {
			line := fmt.Sprintf("p=%d n=%d:", p, n)
			sizes := make(map[string]int)
			for _, im := range ullImpls {
				// A single measurement may catch unrelated heap changes,
				// so take the median of several.
				var runs []int
				for range 5 {
					_, bytes := retained(func() hllSketch {
						s := im.new(p)
						var key [16]byte
						for i := range n {
							binary.LittleEndian.PutUint64(key[8:], uint64(i))
							s.Add(key[:])
						}
						return s
					})
					runs = append(runs, bytes)
				}
				slices.Sort(runs)
				bytes := runs[len(runs)/2]
				sizes[im.name] = bytes
				line += fmt.Sprintf(" impl=%s %.1f KB", im.name, float64(bytes)/1024)
			}
			t.Log(line)
			// Our histogram takes 1 KB, 768 bytes more than hll's.
			if sizes["unsure"] > sizes["hll"]+1024 {
				t.Errorf("p=%d n=%d: %d bytes, more than hll %d + 1 KB", p, n, sizes["unsure"], sizes["hll"])
			}
		}
	}
}

// Benchmarks: the same setup as the hll benchmarks (see hll_test.go).

func BenchmarkULLAdd(b *testing.B) {
	keys := makeKeys(1, hllBenchKeys)
	for _, p := range hllBenchP {
		for _, im := range ullImpls {
			b.Run(fmt.Sprintf("p=%d/impl=%s", p, im.name), func(b *testing.B) {
				s := im.new(p)
				// Fill the sketch, so the timed loop measures the steady state.
				for _, k := range makeKeys(3, hllBenchKeys) {
					s.Add(k)
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

func BenchmarkULLCount(b *testing.B) {
	keys := makeKeys(1, hllBenchKeys)
	for _, p := range hllBenchP {
		for _, im := range ullImpls {
			b.Run(fmt.Sprintf("p=%d/impl=%s", p, im.name), func(b *testing.B) {
				s := im.new(p)
				for _, k := range keys {
					s.Add(k)
				}
				for b.Loop() {
					sinkCount64 = s.Count()
				}
			})
		}
	}
}

// BenchmarkULLMerge is BenchmarkHLLMerge for ullImpls.
func BenchmarkULLMerge(b *testing.B) {
	for _, p := range hllBenchP {
		for _, cs := range []string{"same", "disjoint"} {
			for _, im := range ullImpls {
				b.Run(fmt.Sprintf("p=%d/case=%s/impl=%s", p, cs, im.name), func(b *testing.B) {
					benchHLLMerge(b, im, p, cs)
				})
			}
		}
	}
}
