package bench

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nalgeon/unsure/countmin"
	boom "github.com/tylertreat/BoomFilters"
)

// cmSketch is the common interface for our Sketch and the other implementations,
// so the same tests and benchmarks run against all of them.
type cmSketch interface {
	Add(item []byte)
	Count(item []byte) int
}

// boomCM adapts github.com/tylertreat/BoomFilters to the cmSketch interface.
type boomCM struct{ s *boom.CountMinSketch }

func (s boomCM) Add(item []byte)       { s.s.Add(item) }
func (s boomCM) Count(item []byte) int { return int(s.s.Count(item)) }

type cmImpl struct {
	name string
	new  func(eps, delta float64) cmSketch
}

var cmImpls = []cmImpl{
	{"unsure", func(eps, delta float64) cmSketch { return countmin.New(eps, delta) }},
	{"boom", func(eps, delta float64) cmSketch { return boomCM{boom.NewCountMinSketch(eps, delta)} }},
}

// cmStream is a sequence of events (item ids) over a set of keys, with true counts.
type cmStream struct {
	name   string
	keys   [][]byte
	events []int32
	counts []int
}

// makeCMStream draws n events from u items: uniformly if s == 0,
// otherwise from a Zipf distribution with exponent s (> 1).
func makeCMStream(name string, s float64, u, n int) *cmStream {
	r := rand.New(rand.NewPCG(1, uint64(s*1000)))
	st := &cmStream{
		name:   name,
		keys:   makeKeys(1, u),
		events: make([]int32, n),
		counts: make([]int, u),
	}
	var z *rand.Zipf
	if s > 0 {
		z = rand.NewZipf(r, s, 1, uint64(u-1))
	}
	for i := range st.events {
		var id int
		if z != nil {
			id = int(z.Uint64())
		} else {
			id = r.IntN(u)
		}
		st.events[i] = int32(id)
		st.counts[id]++
	}
	return st
}

// TestCountMinAccuracy reports memory and the error distribution of all
// implementations at the same (epsilon, delta), and checks that ours is
// no larger and no less accurate than the reference.
func TestCountMinAccuracy(t *testing.T) {
	const u = 1_000_000
	const n = 10_000_000
	const delta = 0.01
	streams := []*cmStream{
		makeCMStream("uniform", 0, u, n),
		makeCMStream("zipf1.1", 1.1, u, n),
		makeCMStream("zipf1.5", 1.5, u, n),
	}
	for _, st := range streams {
		for _, eps := range []float64{1e-3, 1e-4} {
			t.Run(fmt.Sprintf("%s/eps=%g", st.name, eps), func(t *testing.T) {
				type result struct {
					bytes          int
					mean           float64
					p50, p99, pmax int
				}
				var res []result
				for _, im := range cmImpls {
					s, bytes := allocated(func() cmSketch { return im.new(eps, delta) })
					for _, id := range st.events {
						s.Add(st.keys[id])
					}
					var errs []int
					sum := 0.0
					for id, c := range st.counts {
						if c == 0 {
							continue
						}
						e := s.Count(st.keys[id]) - c
						errs = append(errs, e)
						sum += float64(e)
					}
					slices.Sort(errs)
					r := result{bytes, sum / float64(len(errs)),
						errs[len(errs)/2], errs[len(errs)*99/100], errs[len(errs)-1]}
					res = append(res, r)
					t.Logf("%s eps=%g: impl=%s %d KB, err mean=%.1f p50=%d p99=%d max=%d (εN=%d)",
						st.name, eps, im.name, bytes/1024, r.mean, r.p50, r.p99, r.pmax, int(eps*n))
				}
				ours, ref := res[0], res[1]
				if ours.bytes > ref.bytes {
					t.Errorf("allocated %d bytes, more than reference %d", ours.bytes, ref.bytes)
				}
				if ours.mean > ref.mean*1.05 {
					t.Errorf("mean error %.2f, more than reference %.2f", ours.mean, ref.mean)
				}
			})
		}
	}
}

// TestCountMinVsMap compares the memory used by a Sketch and a map of counts
// over 1M distinct 8-byte strings (the example from the countmin README).
func TestCountMinVsMap(t *testing.T) {
	const n = 1_000_000
	const eps, delta = 1e-4, 0.01
	_, mapBytes := retained(func() map[string]uint32 {
		counts := make(map[string]uint32)
		var buf [8]byte
		for i := range n {
			binary.LittleEndian.PutUint64(buf[:], uint64(i))
			counts[string(buf[:])]++
		}
		return counts
	})
	_, sketchBytes := retained(func() *countmin.Sketch {
		sketch := countmin.New(eps, delta)
		var buf [8]byte
		for i := range n {
			binary.LittleEndian.PutUint64(buf[:], uint64(i))
			sketch.Add(buf[:])
		}
		return sketch
	})
	mb := func(bytes int) float64 { return float64(bytes) / 1e6 }
	t.Logf("n=%d strings of 8 bytes: map=%.2f MB, sketch eps=%g delta=%g: %.2f MB, %.0fx smaller",
		n, mb(mapBytes), eps, delta, mb(sketchBytes), float64(mapBytes)/float64(sketchBytes))
	if sketchBytes*20 > mapBytes {
		t.Errorf("sketch is only %.1fx smaller than map, want at least 20x", float64(mapBytes)/float64(sketchBytes))
	}
}

// Benchmarks

// cmBenchEps are chosen so the sketch (5 rows of ⌈e/eps⌉ counters, 4 bytes
// each for ours) fits in L1/L2 (54 KB), is around L2 (5.4 MB),
// and is far larger than any cache (54 MB).
var cmBenchEps = []float64{1e-3, 1e-5, 1e-6}

const cmBenchDelta = 0.01

// cmBenchKeys is the number of distinct keys a benchmark loop cycles through.
const cmBenchKeys = 1_000_000

func BenchmarkCountMinAdd(b *testing.B) {
	keys := makeKeys(1, cmBenchKeys)
	for _, eps := range cmBenchEps {
		for _, im := range cmImpls {
			b.Run(fmt.Sprintf("eps=%g/impl=%s", eps, im.name), func(b *testing.B) {
				s := im.new(eps, cmBenchDelta)
				// Touch the counters, so the timed loop doesn't pay for page
				// faults on first write.
				for _, k := range makeKeys(3, cmBenchKeys) {
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

var sinkCount int

func BenchmarkCountMinCount(b *testing.B) {
	keys := makeKeys(1, cmBenchKeys)
	for _, eps := range cmBenchEps {
		for _, im := range cmImpls {
			b.Run(fmt.Sprintf("eps=%g/impl=%s", eps, im.name), func(b *testing.B) {
				s := im.new(eps, cmBenchDelta)
				for _, k := range keys {
					s.Add(k)
				}
				i := 0
				for b.Loop() {
					sinkCount = s.Count(keys[i])
					i++
					if i == len(keys) {
						i = 0
					}
				}
			})
		}
	}
}
