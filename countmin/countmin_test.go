package countmin

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
	counts []uint32
}

// makeStream draws n events from u items: uniformly if s == 0,
// otherwise from a Zipf distribution with exponent s (> 1).
func makeStream(s float64, u, n int) *stream {
	r := rand.New(rand.NewPCG(1, uint64(s*1000)))
	st := &stream{
		keys:   makeKeys(1, u),
		events: make([]int32, n),
		counts: make([]uint32, u),
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

func (st *stream) fill(s *Sketch) {
	for _, id := range st.events {
		s.Add(st.keys[id], 1)
	}
}

func TestParams(t *testing.T) {
	tests := []struct {
		eps, delta float64
		d, w       int
	}{
		{0.5, 0.5, 1, 6},
		{0.01, 0.01, 5, 272},
		{0.001, 0.01, 5, 2719},
		{0.001, 0.001, 7, 2719},
		{1e-6, 1e-5, 12, 2718282},
	}
	for _, tt := range tests {
		d, w := params(tt.eps, tt.delta)
		if d != tt.d || w != tt.w {
			t.Errorf("params(%g, %g) = (%d, %d), want (%d, %d)", tt.eps, tt.delta, d, w, tt.d, tt.w)
		}
		s := New(tt.eps, tt.delta)
		if len(s.salts) != tt.d {
			t.Errorf("New(%g, %g): d = %d, want %d", tt.eps, tt.delta, len(s.salts), tt.d)
		}
		// Allocator slack may only widen the rows.
		if s.width < uint64(tt.w) || len(s.counters) != tt.d*int(s.width) {
			t.Errorf("New(%g, %g): width = %d, %d counters, want width >= %d",
				tt.eps, tt.delta, s.width, len(s.counters), tt.w)
		}
	}
}

func TestNewPanics(t *testing.T) {
	tests := []struct {
		name       string
		eps, delta float64
	}{
		{"eps=0", 0, 0.01},
		{"eps=1", 1, 0.01},
		{"eps<0", -0.1, 0.01},
		{"eps=NaN", math.NaN(), 0.01},
		{"delta=0", 0.01, 0},
		{"delta=1", 0.01, 1},
		{"delta=NaN", 0.01, math.NaN()},
		{"delta too small", 0.01, 1e-8},
		{"too large", 1e-10, 0.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%g, %g) didn't panic", tt.eps, tt.delta)
				}
			}()
			New(tt.eps, tt.delta)
		})
	}
}

func TestEmpty(t *testing.T) {
	s := New(0.01, 0.01)
	for _, k := range makeKeys(1, 1000) {
		if c := s.Count(k); c != 0 {
			t.Fatalf("empty sketch: Count(%x) = %d", k, c)
		}
	}
	if s.Total() != 0 {
		t.Fatalf("empty sketch: Total() = %d", s.Total())
	}
}

func TestSingleItem(t *testing.T) {
	s := New(0.01, 0.01)
	k := []byte("alice")
	for i := range uint32(10) {
		s.Add(k, i)
	}
	if c := s.Count(k); c != 45 {
		t.Fatalf("Count = %d, want 45", c)
	}
	if s.Total() != 45 {
		t.Fatalf("Total = %d, want 45", s.Total())
	}
}

func TestEdgeKeys(t *testing.T) {
	keys := [][]byte{
		nil,
		{0},
		{0, 0},
		[]byte("a"),
		[]byte("hello, world"),
		make([]byte, 1<<16),
	}
	s := New(0.001, 0.01)
	for i, k := range keys {
		s.Add(k, uint32(i+1))
	}
	for i, k := range keys {
		if c := s.Count(k); c < uint32(i+1) {
			t.Fatalf("key (len=%d): Count = %d, want >= %d", len(k), c, i+1)
		}
	}
	// nil and empty are the same item.
	if s.Count(nil) != s.Count([]byte{}) {
		t.Fatalf("Count(nil) = %d, Count([]byte{}) = %d", s.Count(nil), s.Count([]byte{}))
	}
}

func TestSaturation(t *testing.T) {
	s := New(0.01, 0.01)
	k := []byte("alice")
	s.Add(k, math.MaxUint32-1)
	s.Add(k, 5)
	if c := s.Count(k); c != math.MaxUint32 {
		t.Fatalf("Count = %d, want %d", c, uint32(math.MaxUint32))
	}
	s.Add(k, math.MaxUint32)
	if c := s.Count(k); c != math.MaxUint32 {
		t.Fatalf("Count = %d, want %d", c, uint32(math.MaxUint32))
	}
	// Total doesn't saturate at uint32.
	if want := uint64(2*math.MaxUint32 + 4); s.Total() != want {
		t.Fatalf("Total = %d, want %d", s.Total(), want)
	}
}

// TestConservativeUpdate checks that estimates are never below the true count
// and never above what plain increments of the same counters would give.
func TestConservativeUpdate(t *testing.T) {
	for _, zipf := range []float64{0, 1.1} {
		t.Run(fmt.Sprintf("zipf=%g", zipf), func(t *testing.T) {
			st := makeStream(zipf, 10_000, 200_000)
			s := New(0.01, 0.01) // small: plenty of collisions
			st.fill(s)
			// Plain count-min with the same layout.
			plain := make([]uint32, len(s.counters))
			for _, id := range st.events {
				h := wyhash.Hash(st.keys[id], s.seed)
				for i := range s.salts {
					plain[s.index(h, i)]++
				}
			}
			lower := 0
			for id, k := range st.keys {
				h := wyhash.Hash(k, s.seed)
				p := uint32(math.MaxUint32)
				for i := range s.salts {
					p = min(p, plain[s.index(h, i)])
				}
				c := s.Count(k)
				if c < st.counts[id] {
					t.Fatalf("item %d: Count = %d, below true count %d", id, c, st.counts[id])
				}
				if c > p {
					t.Fatalf("item %d: Count = %d, above plain count-min %d", id, c, p)
				}
				if c < p {
					lower++
				}
			}
			t.Logf("conservative update lower than plain for %d of %d items", lower, len(st.keys))
			if lower == 0 {
				t.Errorf("conservative update never helped")
			}
		})
	}
}

// TestErrorBound checks that at most about delta of the items
// have estimates above their true count by more than epsilon*N.
func TestErrorBound(t *testing.T) {
	const u = 100_000
	const n = 2_000_000
	absent := makeKeys(2, 100_000)
	for _, zipf := range []float64{0, 1.1, 1.5} {
		st := makeStream(zipf, u, n)
		for _, tg := range []struct{ eps, delta float64 }{{1e-3, 0.01}, {1e-4, 0.01}, {1e-4, 0.001}} {
			t.Run(fmt.Sprintf("zipf=%g/eps=%g/delta=%g", zipf, tg.eps, tg.delta), func(t *testing.T) {
				s := New(tg.eps, tg.delta)
				st.fill(s)
				if s.Total() != n {
					t.Fatalf("Total = %d, want %d", s.Total(), n)
				}
				thr := uint32(tg.eps * n)
				fail, present := 0, 0
				var errs []uint32
				for id, c := range st.counts {
					if c == 0 {
						continue
					}
					present++
					e := s.Count(st.keys[id]) - c
					errs = append(errs, e)
					if e > thr {
						fail++
					}
				}
				failAbsent := 0
				for _, k := range absent {
					if s.Count(k) > thr {
						failAbsent++
					}
				}
				slices.Sort(errs)
				rate := float64(fail) / float64(present)
				rateAbsent := float64(failAbsent) / float64(len(absent))
				t.Logf("εN=%d: p50=%d p99=%d max=%d; over εN: present %.5f, absent %.5f",
					thr, errs[len(errs)/2], errs[len(errs)*99/100], errs[len(errs)-1], rate, rateAbsent)
				if rate > tg.delta || rateAbsent > tg.delta {
					t.Errorf("over εN: present %.5f, absent %.5f, want <= %g", rate, rateAbsent, tg.delta)
				}
			})
		}
	}
}

func TestSeed(t *testing.T) {
	keys := makeKeys(1, 1000)
	a := NewWith(0.01, 0.01, Options{Seed: 1})
	b := NewWith(0.01, 0.01, Options{Seed: 1})
	c := NewWith(0.01, 0.01, Options{Seed: 2})
	for _, k := range keys {
		a.Add(k, 1)
		b.Add(k, 1)
		c.Add(k, 1)
	}
	if !slices.Equal(a.counters, b.counters) {
		t.Errorf("same seed, different counters")
	}
	if slices.Equal(a.counters, c.counters) {
		t.Errorf("different seeds, same counters")
	}
	if d := New(0.01, 0.01); d.seed != wyhash.Seed(0) {
		t.Errorf("default seed = %#x, want wyhash.Seed(0)", d.seed)
	}
}

// TestRowsIndependent checks that, for every pair of rows, the number of item
// pairs that share a counter in both is close to the 1/width² expected
// if rows were independent. Correlated rows would make the min of d counters
// no better than a few, and break the error bound.
func TestRowsIndependent(t *testing.T) {
	const n = 20_000
	s := New(0.01, 2e-7) // all maxDepth rows
	if len(s.salts) != maxDepth {
		t.Fatalf("d = %d, want %d", len(s.salts), maxDepth)
	}
	hashes := make([]uint64, n)
	for i, k := range makeKeys(1, n) {
		hashes[i] = wyhash.Hash(k, s.seed)
	}
	w := float64(s.width)
	want := float64(n) * (n - 1) / 2 / (w * w)
	for a := range maxDepth {
		for b := a + 1; b < maxDepth; b++ {
			cells := make(map[[2]uint64]int)
			for _, h := range hashes {
				cells[[2]uint64{s.index(h, a), s.index(h, b)}]++
			}
			pairs := 0
			for _, c := range cells {
				pairs += c * (c - 1) / 2
			}
			// Poisson-ish with mean ~1350: ±25% is over 9σ.
			if r := float64(pairs) / want; r < 0.75 || r > 1.25 {
				t.Errorf("rows %d, %d: %d pairs collide in both, want ~%.0f", a, b, pairs, want)
			}
		}
	}
}
