package bench

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"testing"
	"unsafe"

	dgryski "github.com/dgryski/go-topk"
	keiler "github.com/keilerkonzept/topk"
	"github.com/nalgeon/unsure/topk"
)

// tkSketch is the common interface for our Sketch and the other implementations,
// so the same tests and benchmarks run against all of them.
// Items are strings, since both reference libraries take strings.
type tkSketch interface {
	Add(item string)
	Top() []tkEntry
}

type tkEntry struct {
	item  string
	count uint64
}

// unsureTK adapts our Sketch. The item conversion doesn't copy.
type unsureTK struct{ s *topk.Sketch }

func (s unsureTK) Add(item string) { s.s.Add(unsafe.Slice(unsafe.StringData(item), len(item))) }
func (s unsureTK) Top() []tkEntry {
	top := s.s.Top()
	res := make([]tkEntry, len(top))
	for i, e := range top {
		res[i] = tkEntry{e.Item, e.Count}
	}
	return res
}

// dgryskiTK adapts github.com/dgryski/go-topk (Filtered Space-Saving).
// Space-Saving's error is up to N/m for m monitored items, so with m = k
// the bottom of the top k is filled with tail items with inflated counts.
// The usual practice is to monitor m > k items and report the top k of them.
// We use m = 3k, which takes about as much memory as our Sketch.
type dgryskiTK struct {
	s *dgryski.Stream
	k int
}

func newDgryskiTK(k int) dgryskiTK { return dgryskiTK{dgryski.New(3 * k), k} }

func (s dgryskiTK) Add(item string) { s.s.Insert(item, 1) }
func (s dgryskiTK) Top() []tkEntry {
	top := s.s.Keys()
	top = top[:min(s.k, len(top))]
	res := make([]tkEntry, len(top))
	for i, e := range top {
		res[i] = tkEntry{e.Key, uint64(e.Count)}
	}
	return res
}

// keilerTK adapts github.com/keilerkonzept/topk (HeavyKeeper).
type keilerTK struct{ s *keiler.Sketch }

func (s keilerTK) Add(item string) { s.s.Incr(item) }
func (s keilerTK) Top() []tkEntry {
	top := s.s.SortedSlice()
	res := make([]tkEntry, len(top))
	for i, e := range top {
		res[i] = tkEntry{e.Item, uint64(e.Count)}
	}
	return res
}

type tkImpl struct {
	name string
	new  func(k int) tkSketch
}

var tkImpls = []tkImpl{
	{"unsure", func(k int) tkSketch { return unsureTK{topk.New(k)} }},
	{"keiler", func(k int) tkSketch { return keilerTK{keiler.New(k)} }},
	{"dgryski", func(k int) tkSketch { return newDgryskiTK(k) }},
}

// tkStream is a sequence of events (item ids) over a set of keys, with true counts.
type tkStream struct {
	s      float64
	keys   []string
	events []int32
	counts []uint64
	sorted []uint64 // counts in descending order
}

// makeTKStream draws n events from u items with a Zipf distribution:
// the item of rank i has weight 1/i^s. Unlike [rand.Zipf], allows s <= 1.
// Ranks are shuffled, so item ids don't follow the frequencies.
func makeTKStream(s float64, u, n int) *tkStream {
	r := rand.New(rand.NewPCG(2, uint64(s*1000)))
	cdf := make([]float64, u)
	sum := 0.0
	for i := range cdf {
		sum += math.Pow(float64(i+1), -s)
		cdf[i] = sum
	}
	ids := r.Perm(u)
	st := &tkStream{
		s:      s,
		keys:   make([]string, u),
		events: make([]int32, n),
		counts: make([]uint64, u),
	}
	for i, k := range makeKeys(1, u) {
		st.keys[i] = string(k)
	}
	for i := range st.events {
		id := ids[sort.SearchFloat64s(cdf, r.Float64()*sum)]
		st.events[i] = int32(id)
		st.counts[id]++
	}
	st.sorted = slices.Clone(st.counts)
	slices.Sort(st.sorted)
	slices.Reverse(st.sorted)
	return st
}

// score reports how well top matches the true top k of the stream:
// precision is the fraction of k reported items whose true count is at least
// the true k-th count (so ties at the boundary don't matter), and countErr
// is the mean relative error |estimate-true|/true over the reported items.
func (st *tkStream) score(top []tkEntry, k int) (precision, countErr float64) {
	kth := st.sorted[k-1]
	top = top[:min(k, len(top))]
	hits := 0
	for _, e := range top {
		c := st.counts[binary.LittleEndian.Uint64([]byte(e.item[8:]))]
		if c >= kth {
			hits++
		}
		countErr += math.Abs(float64(e.count)-float64(c)) / float64(max(c, 1))
	}
	return float64(hits) / float64(k), countErr / float64(max(len(top), 1))
}

// tkStreams are 10M events over 1M items, from flat (s=0.6) to skewed (s=1.0).
var tkStreams = sync.OnceValue(func() []*tkStream {
	return []*tkStream{
		makeTKStream(0.6, tkItems, tkEvents),
		tkBenchStream(),
		makeTKStream(1.0, tkItems, tkEvents),
	}
})

const (
	tkItems  = 1_000_000
	tkEvents = 10_000_000
)

var tkKs = []int{10, 100, 1000}

// tkMemory returns the heap bytes retained by a sketch with a full top k.
// Builds many sketches at once and averages, since a single small
// sketch is within the noise of the runtime's own allocations.
func tkMemory(im tkImpl, st *tkStream, k int) int {
	copies := max(8, 2560/k)
	const events = 50_000
	_, bytes := retained(func() []tkSketch {
		sketches := make([]tkSketch, copies)
		for i := range sketches {
			sketches[i] = im.new(k)
			for _, id := range st.events[:events] {
				sketches[i].Add(st.keys[id])
			}
		}
		return sketches
	})
	return bytes / copies
}

// TestTopKAccuracy reports memory and accuracy (precision and count error
// of the top k) of all implementations, and checks that ours finds at least
// 97% of the true top k, and is about as precise as the reference (keiler)
// or better. Memory isn't checked: ours is smaller than the reference
// at k=10 and k=1000, but larger at k=100 (and much more precise).
func TestTopKAccuracy(t *testing.T) {
	for _, st := range tkStreams() {
		for _, k := range tkKs {
			t.Run(fmt.Sprintf("s=%g/k=%d", st.s, k), func(t *testing.T) {
				type result struct {
					bytes               int
					precision, countErr float64
				}
				res := map[string]result{}
				for _, im := range tkImpls {
					s := im.new(k)
					for _, id := range st.events {
						s.Add(st.keys[id])
					}
					p, e := st.score(s.Top(), k)
					bytes := tkMemory(im, st, k)
					res[im.name] = result{bytes, p, e}
					t.Logf("s=%g k=%d: impl=%-7s %6.1f KB, precision=%.3f count err=%.4f",
						st.s, k, im.name, float64(bytes)/1024, p, e)
				}
				ours, ref := res["unsure"], res["keiler"]
				// One item of 100 at the top-k boundary is noise.
				if ours.precision < 0.97 || ours.precision < ref.precision-0.01 {
					t.Errorf("precision %.3f, want >= 0.97 and >= reference %.3f - 0.01",
						ours.precision, ref.precision)
				}
			})
		}
	}
}

// Benchmarks

// tkBenchStream is the stream for the benchmarks: moderately skewed,
// so Add sees both the top items and the long tail.
var tkBenchStream = sync.OnceValue(func() *tkStream {
	return makeTKStream(0.8, tkItems, tkEvents)
})

// newTKWarm returns a sketch that has seen the first 1M events of st,
// so benchmarks run on a full sketch.
func newTKWarm(im tkImpl, st *tkStream, k int) tkSketch {
	s := im.new(k)
	for _, id := range st.events[:1_000_000] {
		s.Add(st.keys[id])
	}
	return s
}

func BenchmarkTopKAdd(b *testing.B) {
	st := tkBenchStream()
	for _, k := range tkKs {
		for _, im := range tkImpls {
			b.Run(fmt.Sprintf("k=%d/impl=%s", k, im.name), func(b *testing.B) {
				s := newTKWarm(im, st, k)
				i := 1_000_000
				for b.Loop() {
					s.Add(st.keys[st.events[i]])
					i++
					if i == len(st.events) {
						i = 0
					}
				}
			})
		}
	}
}

var sinkTop []tkEntry

func BenchmarkTopKTop(b *testing.B) {
	st := tkBenchStream()
	for _, k := range tkKs {
		for _, im := range tkImpls {
			b.Run(fmt.Sprintf("k=%d/impl=%s", k, im.name), func(b *testing.B) {
				s := newTKWarm(im, st, k)
				for b.Loop() {
					sinkTop = s.Top()
				}
			})
		}
	}
}
