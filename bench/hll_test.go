package bench

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/axiomhq/hyperloglog"
	"github.com/nalgeon/unsure/hll"
	boom "github.com/tylertreat/BoomFilters"
)

// hllSketch is the common interface for our Sketch and the other implementations,
// so the same tests and benchmarks run against all of them.
// Merge is only called with a sketch of the same implementation.
type hllSketch interface {
	Add(item []byte)
	Count() uint64
	Merge(other hllSketch) error
}

// unsureHLL adapts our Sketch to the hllSketch interface.
type unsureHLL struct{ s *hll.Sketch }

func (s unsureHLL) Add(item []byte)             { s.s.Add(item) }
func (s unsureHLL) Count() uint64               { return uint64(s.s.Count()) }
func (s unsureHLL) Merge(other hllSketch) error { return s.s.Merge(other.(unsureHLL).s) }

// axiomHLL adapts github.com/axiomhq/hyperloglog to the hllSketch interface.
type axiomHLL struct{ s *hyperloglog.Sketch }

func (s axiomHLL) Add(item []byte)             { s.s.Insert(item) }
func (s axiomHLL) Count() uint64               { return s.s.Estimate() }
func (s axiomHLL) Merge(other hllSketch) error { return s.s.Merge(other.(axiomHLL).s) }

// boomHLL adapts github.com/tylertreat/BoomFilters to the hllSketch interface.
type boomHLL struct{ s *boom.HyperLogLog }

func (s boomHLL) Add(item []byte)             { s.s.Add(item) }
func (s boomHLL) Count() uint64               { return s.s.Count() }
func (s boomHLL) Merge(other hllSketch) error { return s.s.Merge(other.(boomHLL).s) }

type hllImpl struct {
	name string
	new  func(p int) hllSketch // 2^p registers
}

var hllImpls = []hllImpl{
	{"unsure", func(p int) hllSketch { return unsureHLL{hll.NewPrecision(p)} }},
	{"axiom", func(p int) hllSketch {
		s, err := hyperloglog.NewSketch(uint8(p), true)
		if err != nil {
			panic(err)
		}
		return axiomHLL{s}
	}},
	{"boom", func(p int) hllSketch {
		s, err := boom.NewHyperLogLog(1 << p)
		if err != nil {
			panic(err)
		}
		return boomHLL{s}
	}},
}

// TestHLLAccuracy reports the bias and relative standard error of all
// implementations at the same number of registers, over many independent
// sketches and cardinalities from tiny to far beyond the number of registers.
// Checks that ours is no less accurate than the reference.
func TestHLLAccuracy(t *testing.T) {
	const p = 14
	const m = 1 << p
	const trials = 100
	// From tiny, through the 2.5m–5m range where the original HyperLogLog
	// switches estimators, to far beyond m.
	points := []int{100, 1000, 10_000, 20_000, 40_000, 80_000, 160_000, 1_000_000}
	type result struct{ bias, rmse float64 }
	res := make(map[string][]result)
	for _, im := range hllImpls {
		sum := make([]float64, len(points))
		sum2 := make([]float64, len(points))
		var key [16]byte
		for trial := range trials {
			binary.LittleEndian.PutUint64(key[:8], uint64(trial))
			s := im.new(p)
			n := 0
			for j, pt := range points {
				for ; n < pt; n++ {
					binary.LittleEndian.PutUint64(key[8:], uint64(n))
					s.Add(key[:])
				}
				e := float64(s.Count())/float64(n) - 1
				sum[j] += e
				sum2[j] += e * e
			}
		}
		for j := range points {
			res[im.name] = append(res[im.name], result{
				sum[j] / float64(trials),
				math.Sqrt(sum2[j] / float64(trials)),
			})
		}
	}
	stdErr := 1.04 / math.Sqrt(m)
	worse := 0
	for j, n := range points {
		line := fmt.Sprintf("p=%d n=%d (%.2gm):", p, n, float64(n)/m)
		for _, im := range hllImpls {
			r := res[im.name][j]
			line += fmt.Sprintf(" impl=%s bias=%+.2f%% rmse=%.2f%%", im.name, r.bias*100, r.rmse*100)
		}
		t.Log(line)
		ours, ref := res["unsure"][j], res["axiom"][j]
		if math.Abs(ours.bias) > stdErr/2 {
			t.Errorf("n=%d: bias %+.4f, more than stdErr/2 (%.4f)", n, ours.bias, stdErr/2)
		}
		if ours.rmse > stdErr*1.2 {
			t.Errorf("n=%d: rmse %.4f, more than stdErr (%.4f)", n, ours.rmse, stdErr)
		}
		// Below m, the reference is in sparse mode and nearly exact.
		if n >= m && ours.rmse > ref.rmse*1.2 {
			worse++
		}
	}
	// With 100 trials, an RMSE is within ±20% about 95% of the time.
	if worse > 1 {
		t.Errorf("rmse is over 1.2× the reference at %d cardinalities", worse)
	}
}

// TestHLLSize reports the heap memory each implementation retains
// after adding n distinct items.
func TestHLLSize(t *testing.T) {
	for _, p := range []int{10, 14, 18} {
		for _, n := range []int{100, 1_000, 10_000, 100_000, 1_000_000} {
			line := fmt.Sprintf("p=%d n=%d:", p, n)
			sizes := make(map[string]int)
			for _, im := range hllImpls {
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
			// Ours has no sparse mode, so it's only checked once dense.
			// The register histogram adds a fixed 256 bytes.
			if n >= 1<<p && sizes["unsure"] > sizes["axiom"]+512 {
				t.Errorf("p=%d n=%d: %d bytes, more than reference %d", p, n, sizes["unsure"], sizes["axiom"])
			}
		}
	}
}

// Benchmarks

// hllBenchP are chosen so the registers (2^p bytes for ours) take
// 1 KB, 16 KB (fits in L1), and 256 KB (fits in L2).
var hllBenchP = []int{10, 14, 18}

// hllBenchKeys is the number of distinct keys a benchmark loop cycles through.
const hllBenchKeys = 1_000_000

func BenchmarkHLLAdd(b *testing.B) {
	keys := makeKeys(1, hllBenchKeys)
	for _, p := range hllBenchP {
		for _, im := range hllImpls {
			b.Run(fmt.Sprintf("p=%d/impl=%s", p, im.name), func(b *testing.B) {
				s := im.new(p)
				// Fill the sketch, so the timed loop measures the steady state
				// (dense registers, most adds don't change them).
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

var sinkCount64 uint64

func BenchmarkHLLCount(b *testing.B) {
	keys := makeKeys(1, hllBenchKeys)
	for _, p := range hllBenchP {
		for _, im := range hllImpls {
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

// hllMergeBatch is the total register size of the sketch copies that
// BenchmarkHLLMerge prepares at once for case=disjoint. Small enough to stay
// in L2, large enough to amortize StopTimer (which stops the world).
const hllMergeBatch = 1 << 20

// hllMergeOthers is the number of different sketches that
// BenchmarkHLLMerge merges in turn for case=disjoint.
const hllMergeOthers = 8

// BenchmarkHLLMerge merges two sketches filled with different keys.
// case=same merges into a sketch that already contains other, so no
// register grows. case=disjoint merges into fresh copies of the original
// sketch (prepared untimed, in batches), so about half of the registers
// grow, as in a real merge of unrelated sketches.
func BenchmarkHLLMerge(b *testing.B) {
	for _, p := range hllBenchP {
		for _, cs := range []string{"same", "disjoint"} {
			for _, im := range hllImpls {
				b.Run(fmt.Sprintf("p=%d/case=%s/impl=%s", p, cs, im.name), func(b *testing.B) {
					fill := func(seed uint64) hllSketch {
						s := im.new(p)
						for _, k := range makeKeys(seed, hllBenchKeys) {
							s.Add(k)
						}
						return s
					}
					base, other := fill(1), fill(2)
					// Not every implementation can clone,
					// so copy by merging into an empty sketch.
					copyBase := func() hllSketch {
						s := im.new(p)
						if err := s.Merge(base); err != nil {
							b.Fatal(err)
						}
						return s
					}
					if cs == "same" {
						s := copyBase()
						if err := s.Merge(other); err != nil {
							b.Fatal(err)
						}
						for b.Loop() {
							if err := s.Merge(other); err != nil {
								b.Fatal(err)
							}
						}
						return
					}
					// Merging the same other every time lets the branch
					// predictor learn the pattern of growing registers
					// (it does at p=10), so rotate between several.
					others := []hllSketch{other}
					for seed := range uint64(hllMergeOthers - 1) {
						others = append(others, fill(3+seed))
					}
					batch := make([]hllSketch, max(1, hllMergeBatch>>p))
					i := len(batch)
					for b.Loop() {
						if i == len(batch) {
							b.StopTimer()
							for j := range batch {
								batch[j] = copyBase()
							}
							i = 0
							b.StartTimer()
						}
						if err := batch[i].Merge(others[i%len(others)]); err != nil {
							b.Fatal(err)
						}
						i++
					}
				})
			}
		}
	}
}

// TestHLLVsMap compares the memory used by a Sketch and a set (map) of
// 1M distinct 8-byte strings (the example from the hll README).
func TestHLLVsMap(t *testing.T) {
	const n = 1_000_000
	_, mapBytes := retained(func() map[string]struct{} {
		set := make(map[string]struct{})
		var buf [8]byte
		for i := range n {
			binary.LittleEndian.PutUint64(buf[:], uint64(i))
			set[string(buf[:])] = struct{}{}
		}
		return set
	})
	_, sketchBytes := retained(func() *hll.Sketch {
		sketch := hll.New()
		var buf [8]byte
		for i := range n {
			binary.LittleEndian.PutUint64(buf[:], uint64(i))
			sketch.Add(buf[:])
		}
		return sketch
	})
	mb := func(bytes int) float64 { return float64(bytes) / 1e6 }
	t.Logf("n=%d strings of 8 bytes: map=%.2f MB, sketch: %.3f MB, %.0fx smaller",
		n, mb(mapBytes), mb(sketchBytes), float64(mapBytes)/float64(sketchBytes))
	if sketchBytes*1000 > mapBytes {
		t.Errorf("sketch is only %.1fx smaller than map, want at least 1000x", float64(mapBytes)/float64(sketchBytes))
	}
}
