package topk

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// makeKeys returns n distinct 16-byte keys. Keys from different seeds don't overlap.
func makeKeys(seed uint64, n int) [][]byte {
	keys := make([][]byte, n)
	buf := make([]byte, 16*n)
	for i := range keys {
		k := buf[i*16 : (i+1)*16 : (i+1)*16]
		binary.LittleEndian.PutUint64(k[:8], seed)
		binary.LittleEndian.PutUint64(k[8:], uint64(i))
		keys[i] = k
	}
	return keys
}

// stream is a sequence of events (item ids) over a set of keys, with true counts.
type stream struct {
	keys   [][]byte
	events []int32
	counts []uint64
}

// makeStream draws n events from u items with a Zipf distribution
// with exponent s (> 1).
func makeStream(s float64, u, n int) *stream {
	r := rand.New(rand.NewPCG(1, uint64(s*1000)))
	z := rand.NewZipf(r, s, 1, uint64(u-1))
	st := &stream{
		keys:   makeKeys(1, u),
		events: make([]int32, n),
		counts: make([]uint64, u),
	}
	for i := range st.events {
		id := int(z.Uint64())
		st.events[i] = int32(id)
		st.counts[id]++
	}
	return st
}

func (st *stream) id(item string) int {
	return int(binary.LittleEndian.Uint64([]byte(item[8:])))
}

// narrow makes the sketch's table one bucket per row,
// so that all items compete for the same two buckets.
func narrow(s *Sketch) {
	s.buckets = s.buckets[:2]
	s.width = 1
}

// checkInvariants verifies the heap order and the index.
func checkInvariants(t *testing.T, s *Sketch) {
	t.Helper()
	if len(s.heap) > s.k || len(s.slots) != len(s.heap) || len(s.index) != len(s.heap) {
		t.Fatalf("k=%d, heap=%d, slots=%d, index=%d", s.k, len(s.heap), len(s.slots), len(s.index))
	}
	for p, nd := range s.heap {
		if p > 0 && s.heap[(p-1)/2].count > nd.count {
			t.Fatalf("heap order violated at %d", p)
		}
		sl := s.slots[nd.slot]
		if int(sl.pos) != p {
			t.Fatalf("slot %d: pos=%d, want %d", nd.slot, sl.pos, p)
		}
		if i, ok := s.index[sl.hash]; !ok || i != nd.slot {
			t.Fatalf("index[%x] = %d, %v, want %d", sl.hash, i, ok, nd.slot)
		}
	}
}

func TestNew(t *testing.T) {
	for _, k := range []int{1, 10, 16, 100, 1000, 12345} {
		s := New(k)
		want := max(widthFactor*k, minWidth)
		// Allocator slack may only widen the rows.
		if s.width < uint64(want) || len(s.buckets) != 2*int(s.width) {
			t.Errorf("New(%d): width=%d, %d buckets, want width >= %d", k, s.width, len(s.buckets), want)
		}
		if len(s.Top()) != 0 || s.Total() != 0 {
			t.Errorf("New(%d) isn't empty", k)
		}
	}
}

func TestNewPanics(t *testing.T) {
	for _, k := range []int{-1, 0, math.MaxInt32} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%d) didn't panic", k)
				}
			}()
			New(k)
		})
	}
}

func TestExact(t *testing.T) {
	// Fewer items than k and a wide table: counts are exact.
	s := New(100)
	keys := makeKeys(1, 50)
	for i, k := range keys {
		s.AddN(k, uint64(i+1))
		s.Add(k)
	}
	checkInvariants(t, s)
	top := s.Top()
	if len(top) != len(keys) {
		t.Fatalf("len(Top) = %d, want %d", len(top), len(keys))
	}
	for i, e := range top {
		id := len(keys) - 1 - i
		if e.Item != string(keys[id]) || e.Count != uint64(id+2) {
			t.Errorf("Top[%d] = (%x, %d), want (%x, %d)", i, e.Item, e.Count, keys[id], id+2)
		}
	}
	for i, k := range keys {
		if c, ok := s.Count(k); !ok || c != uint64(i+2) {
			t.Errorf("Count(%d) = %d, %v, want %d, true", i, c, ok, i+2)
		}
	}
	if c, ok := s.Count([]byte("missing")); ok || c != 0 {
		t.Errorf("Count(missing) = %d, %v, want 0, false", c, ok)
	}
	if want := uint64(50 * 53 / 2); s.Total() != want {
		t.Errorf("Total = %d, want %d", s.Total(), want)
	}
}

func TestAddZero(t *testing.T) {
	s := New(10)
	s.AddN([]byte("a"), 0)
	if len(s.Top()) != 0 || s.Total() != 0 {
		t.Errorf("AddN(0) changed the sketch")
	}
}

func TestTopTies(t *testing.T) {
	s := New(10)
	for _, item := range []string{"c", "a", "b", "d"} {
		s.AddN([]byte(item), 5)
	}
	s.AddN([]byte("d"), 1)
	var got []string
	for _, e := range s.Top() {
		got = append(got, e.Item)
	}
	if want := []string{"d", "a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("Top = %v, want %v", got, want)
	}
}

func TestZipf(t *testing.T) {
	const u, n = 100_000, 2_000_000
	for _, zs := range []float64{1.1, 1.5} {
		st := makeStream(zs, u, n)
		sorted := slices.Clone(st.counts)
		slices.Sort(sorted)
		for _, k := range []int{1, 10, 100, 1000} {
			t.Run(fmt.Sprintf("s=%g/k=%d", zs, k), func(t *testing.T) {
				s := New(k)
				for _, id := range st.events {
					s.Add(st.keys[id])
				}
				checkInvariants(t, s)
				if s.Total() != n {
					t.Errorf("Total = %d, want %d", s.Total(), n)
				}
				top := s.Top()
				if len(top) != k {
					t.Fatalf("len(Top) = %d, want %d", len(top), k)
				}
				kth := sorted[len(sorted)-k]
				hits := 0
				for i, e := range top {
					true := st.counts[st.id(e.Item)]
					// An item's buckets only count its own events.
					if e.Count > true {
						t.Errorf("Top[%d]: count %d > true count %d", i, e.Count, true)
					}
					if c, ok := s.Count([]byte(e.Item)); !ok || c != e.Count {
						t.Errorf("Count(Top[%d]) = %d, %v, want %d, true", i, c, ok, e.Count)
					}
					if true >= kth {
						hits++
					}
				}
				if p := float64(hits) / float64(k); p < 0.99 {
					t.Errorf("precision = %.3f, want >= 0.99", p)
				}
			})
		}
	}
}

func TestDeterministic(t *testing.T) {
	st := makeStream(1.1, 10_000, 100_000)
	var tops [2][]Entry
	for i := range tops {
		s := New(10)
		narrow(s) // lots of decay
		for _, id := range st.events {
			s.Add(st.keys[id])
		}
		tops[i] = s.Top()
	}
	if !slices.Equal(tops[0], tops[1]) {
		t.Errorf("same input, different results:\n%v\n%v", tops[0], tops[1])
	}
}

// TestDecay checks that taking over a bucket with count c takes
// 2^1 + ... + 2^c attempts on average, both with Add and AddN.
func TestDecay(t *testing.T) {
	const c = 6
	const want = 1<<(c+1) - 2
	const trials = 4000
	a, b := []byte("a"), []byte("b")
	var addSum, addNSum float64
	for range trials {
		s := New(1)
		narrow(s)
		s.rng = rand.Uint64()
		s.AddN(a, c)
		// Add until b takes over both buckets, counting attempts per bucket.
		fpb := uint32(wyhash.Hash(b, wyhash.Seed0) >> 32)
		for attempts := 1; s.buckets[0].fp != fpb || s.buckets[1].fp != fpb; attempts++ {
			s.Add(b)
			for j := range 2 {
				if s.buckets[j].fp == fpb && s.buckets[j].count == 1 {
					addSum += float64(attempts)
				}
			}
		}

		s = New(1)
		narrow(s)
		s.rng = rand.Uint64()
		s.AddN(a, c)
		const n = 1 << 20
		s.AddN(b, n)
		// b gets the attempts left after the one that zeroed the bucket.
		addNSum += float64(n - s.buckets[0].count)
	}
	// Each average has stddev ~ sqrt(sum 4^j)/sqrt(trials) ~ 1.2 (1% of want).
	for _, got := range []float64{addSum / (2 * trials), addNSum / trials} {
		if math.Abs(got-want) > 0.05*want {
			t.Errorf("mean attempts = %.1f, want %d", got, want)
		}
	}
}

func TestAddNLarge(t *testing.T) {
	s := New(1)
	narrow(s)
	a, b := []byte("a"), []byte("b")
	s.AddN(a, 10)
	// Takes over both buckets, count is capped.
	s.AddN(b, 1<<40)
	if top := s.Top(); len(top) != 1 || top[0].Item != "b" || top[0].Count != math.MaxUint32 {
		t.Errorf("Top = %v, want [b %d]", top, uint32(math.MaxUint32))
	}
	// Counts of 64 and more never decay, even with huge n.
	s = New(1)
	narrow(s)
	s.AddN(a, 64)
	s.AddN(b, math.MaxUint64-64)
	if top := s.Top(); len(top) != 1 || top[0].Item != "a" || top[0].Count != 64 {
		t.Errorf("Top = %v, want [a 64]", top)
	}
}

func TestZeroEstimate(t *testing.T) {
	// b's buckets are owned by a with counts of 64, which never decay,
	// so b's estimate is 0 and it doesn't get into the heap.
	s := New(10)
	narrow(s)
	s.AddN([]byte("a"), 64)
	s.AddN([]byte("b"), 1000)
	checkInvariants(t, s)
	if top := s.Top(); len(top) != 1 || top[0].Item != "a" {
		t.Errorf("Top = %v, want [a 64]", top)
	}
}

func TestReplace(t *testing.T) {
	// Items longer than the evicted one get a new buffer,
	// shorter ones reuse it.
	s := New(1)
	for i, item := range []string{"aa", "bbbb", "c"} {
		s.AddN([]byte(item), uint64(i+1)*10)
		checkInvariants(t, s)
		if top := s.Top(); len(top) != 1 || top[0].Item != item {
			t.Fatalf("Top = %v, want [%s]", top, item)
		}
	}
	if c, ok := s.Count([]byte("bbbb")); ok {
		t.Errorf("Count(evicted) = %d, true", c)
	}
}

func TestAddAllocs(t *testing.T) {
	st := makeStream(1.1, 10_000, 100_000)
	s := New(100)
	for _, id := range st.events {
		s.Add(st.keys[id])
	}
	i := 0
	allocs := testing.AllocsPerRun(10_000, func() {
		s.Add(st.keys[st.events[i]])
		i++
	})
	if allocs > 0 {
		t.Errorf("Add allocates %.2f times per call", allocs)
	}
}

func TestSize(t *testing.T) {
	s := New(1000)
	for _, k := range makeKeys(1, 1000) {
		s.Add(k)
	}
	// 2 rows of 8-byte buckets, 76 bytes of heap and index per item,
	// and the 16-byte items.
	if got, want := s.Size(), 2*int(s.width)*8+1000*(76+16); got != want {
		t.Errorf("Size = %d, want ~%d", got, want)
	}
}
