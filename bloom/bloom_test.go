package bloom

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"
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

func TestEmpty(t *testing.T) {
	f := New(1000, 0.01)
	for _, k := range makeKeys(1, 1000) {
		if f.Contains(k) {
			t.Fatalf("empty filter reports %x as present", k)
		}
	}
}

func TestNoFalseNegatives(t *testing.T) {
	for _, n := range []int{1, 10, 1000, 100_000} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			f := New(n, 0.01)
			keys := makeKeys(1, n)
			for _, k := range keys {
				f.Add(k)
			}
			for _, k := range keys {
				if !f.Contains(k) {
					t.Fatalf("added key %x not found", k)
				}
			}
		})
	}
}

func TestEdgeKeys(t *testing.T) {
	keys := [][]byte{
		nil,
		{},
		{0},
		{0, 0},
		[]byte("a"),
		[]byte("hello, world"),
		make([]byte, 1<<16),
	}
	f := New(len(keys), 0.01)
	for _, k := range keys {
		f.Add(k)
	}
	for _, k := range keys {
		if !f.Contains(k) {
			t.Fatalf("added key (len=%d) not found", len(k))
		}
	}
}

// TestFalsePositiveRate checks that a filter filled to its declared capacity
// keeps the false positive rate close to the requested one.
func TestFalsePositiveRate(t *testing.T) {
	const n = 100_000
	const probes = 1_000_000
	members := makeKeys(1, n)
	others := makeKeys(2, probes)

	for _, p := range []float64{0.1, 0.01, 0.001} {
		t.Run(fmt.Sprintf("p=%g", p), func(t *testing.T) {
			f := New(n, p)
			for _, k := range members {
				f.Add(k)
			}
			fp := 0
			for _, k := range others {
				if f.Contains(k) {
					fp++
				}
			}
			rate := float64(fp) / probes
			t.Logf("requested=%g actual=%.5f", p, rate)
			// Allow some slack over the requested rate for statistical noise
			// and approximations in sizing.
			if rate > p*1.25 {
				t.Errorf("false positive rate %.5f exceeds requested %g", rate, p)
			}
		})
	}
}

// TestFalsePositiveRateLarge checks the false positive rate for a filter
// much larger than the CPU cache, where uneven block loads matter most.
func TestFalsePositiveRateLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	const n = 50_000_000
	const probes = 1_000_000
	const p = 0.01
	f := New(n, p)
	fill(f, 1, n)
	fp := 0
	for _, k := range makeKeys(2, probes) {
		if f.Contains(k) {
			fp++
		}
	}
	rate := float64(fp) / probes
	t.Logf("requested=%g actual=%.5f", p, rate)
	if rate > p*1.25 {
		t.Errorf("false positive rate %.5f exceeds requested %g", rate, p)
	}
}

// TestFalsePositiveRateSmall checks the false positive rate for filters
// with only a handful of blocks, where sizing approximations are least accurate.
func TestFalsePositiveRateSmall(t *testing.T) {
	const probes = 1_000_000
	others := makeKeys(2, probes)
	for _, n := range []int{1, 2, 5, 10, 50, 100, 500} {
		for _, p := range []float64{0.1, 0.01} {
			t.Run(fmt.Sprintf("n=%d/p=%g", n, p), func(t *testing.T) {
				// Average over many independent filters, since a single small
				// filter's rate depends heavily on where its few items landed.
				const filters = 100
				fp := 0
				for i := range filters {
					f := New(n, p)
					fill(f, uint64(100+i), n)
					for _, k := range others[:probes/filters] {
						if f.Contains(k) {
							fp++
						}
					}
				}
				rate := float64(fp) / probes
				t.Logf("requested=%g actual=%.5f", p, rate)
				if rate > p*1.25 {
					t.Errorf("false positive rate %.5f exceeds requested %g", rate, p)
				}
			})
		}
	}
}

// TestFalsePositiveRateLow checks a low false positive rate, where k is high
// enough to use salts the other tests don't. The filter is small so probes
// stay in cache.
func TestFalsePositiveRateLow(t *testing.T) {
	const n = 10_000
	const p = 1e-5
	const probes = 10_000_000 // ~100 expected false positives
	f := New(n, p)
	if k := len(f.salts); k != 14 {
		t.Fatalf("k = %d, want 14", k)
	}
	// Drop the allocator's extra blocks, which would lower the rate
	// well below the target at this small size and hide problems.
	_, load := params(p)
	f.blocks = f.blocks[:int(math.Ceil(n/load))]
	fill(f, 1, n)
	key := make([]byte, 16)
	binary.LittleEndian.PutUint64(key[:8], 2)
	fp := 0
	for i := range probes {
		binary.LittleEndian.PutUint64(key[8:], uint64(i))
		if f.Contains(key) {
			fp++
		}
	}
	rate := float64(fp) / probes
	t.Logf("requested=%g actual=%.3g (fp=%d)", p, rate, fp)
	if rate > p*1.25 {
		t.Errorf("false positive rate %.3g exceeds requested %g", rate, p)
	}
}

// allocated returns the number of heap bytes allocated by fn.
// Takes the min of several runs, since unrelated runtime allocations
// may occasionally land within a single measurement.
func allocated[T any](fn func() T) (T, int) {
	var v T
	bytes := math.MaxInt
	for range 3 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		v = fn()
		runtime.ReadMemStats(&after)
		bytes = min(bytes, int(after.TotalAlloc-before.TotalAlloc))
	}
	return v, bytes
}

// TestSize checks the memory actually allocated by New: no less than the
// information-theoretic minimum of n*log2(1/p) bits (which no approximate
// membership structure can beat). It also checks that Filter.Size reports
// the allocated memory accurately.
func TestSize(t *testing.T) {
	for _, n := range []int{1000, 100_000, 10_000_000} {
		for _, p := range []float64{0.1, 0.01, 0.001} {
			t.Run(fmt.Sprintf("n=%d/p=%g", n, p), func(t *testing.T) {
				f, got := allocated(func() *Filter { return New(n, p) })
				minBytes := int(float64(n) * math.Log2(1/p) / 8)
				if got < minBytes {
					t.Errorf("allocated %d bytes, below the theoretical minimum %d; measurement is likely wrong", got, minBytes)
				}
				// Size leaves out the struct and allocator rounding:
				// up to 1/8 for small objects (size classes),
				// up to one 8KB page for large ones (>32KB).
				size := f.Size()
				slack := 256 + size/8
				if size > 32<<10 {
					slack = 256 + 8<<10
				}
				if size > got || got > size+slack {
					t.Errorf("Size() = %d, but New allocated %d bytes", size, got)
				}
			})
		}
	}
}

// fill adds n keys (the same ones makeKeys(seed, n) would return) to f,
// without materializing them all in memory.
func fill(f *Filter, seed uint64, n int) {
	k := make([]byte, 16)
	binary.LittleEndian.PutUint64(k[:8], seed)
	for i := range n {
		binary.LittleEndian.PutUint64(k[8:], uint64(i))
		f.Add(k)
	}
}

func TestSeed(t *testing.T) {
	const n = 1000
	build := func(f *Filter) *Filter {
		fill(f, 1, n)
		return f
	}
	a := build(New(n, 0.01))
	b := build(New(n, 0.01))
	zero := build(NewWith(n, 0.01, Options{Seed: 0}))
	other := build(NewWith(n, 0.01, Options{Seed: 1}))

	if !slices.Equal(a.blocks, b.blocks) {
		t.Error("two filters with the same items and options differ")
	}
	if !slices.Equal(a.blocks, zero.blocks) {
		t.Error("New differs from NewWith with Seed: 0")
	}
	if slices.Equal(a.blocks, other.blocks) {
		t.Error("Seed: 1 gives the same filter as the default seed")
	}
	for _, k := range makeKeys(1, n) {
		if !other.Contains(k) {
			t.Fatalf("added key %x not found with Seed: 1", k)
		}
	}
}

// TestParams checks the sizing search against a slow, exhaustive one:
// every k from 1 to maxK, each load bisected to full precision.
func TestParams(t *testing.T) {
	refLoad := func(p float64, k int) float64 {
		lo, hi := 0.0, 1024.0
		for range 60 {
			mid := (lo + hi) / 2
			if fpRate(mid, k) <= p {
				lo = mid
			} else {
				hi = mid
			}
		}
		return lo
	}
	refParams := func(p float64) (k int, load float64) {
		for kk := 1; kk <= maxK; kk++ {
			if l := refLoad(p, kk); l > load {
				k, load = kk, l
			}
		}
		return k, load
	}

	ps := []float64{0.999, 0.9, 0.5}
	for p := 0.3; p > 1e-9; p /= 1.7 {
		ps = append(ps, p)
	}
	for _, p := range ps {
		k, load := params(p)
		wantK, wantLoad := refParams(p)
		// Near p=1, several k reach the load cap in fpRate and tie.
		// Any of them is fine.
		if k != wantK && refLoad(p, k) < wantLoad*(1-2e-6) {
			t.Errorf("p=%g: k=%d, want %d", p, k, wantK)
			continue
		}
		if load > wantLoad || load < wantLoad*(1-2e-6) {
			t.Errorf("p=%g: load=%.9f, want %.9f (within -2e-6)", p, load, wantLoad)
		}
		if fpRate(load, k) > p {
			t.Errorf("p=%g: fpRate(%g, %d) = %g exceeds p", p, load, k, fpRate(load, k))
		}
	}

	// Pin the common cases.
	for _, tt := range []struct {
		p float64
		k int
	}{{0.1, 3}, {0.01, 6}, {0.001, 9}} {
		if k, _ := params(tt.p); k != tt.k {
			t.Errorf("p=%g: k=%d, want %d", tt.p, k, tt.k)
		}
	}
}

func TestNewPanics(t *testing.T) {
	tests := []struct {
		name   string
		n      int
		fpProb float64
		want   string
	}{
		{"negative n", -1, 0.01, "n must not be negative"},
		{"p=0", 1000, 0, "fpProb must be in (0, 1)"},
		{"p=1", 1000, 1, "fpProb must be in (0, 1)"},
		{"p<0", 1000, -0.1, "fpProb must be in (0, 1)"},
		{"p>1", 1000, 1.5, "fpProb must be in (0, 1)"},
		{"p=NaN", 1000, math.NaN(), "fpProb must be in (0, 1)"},
		{"tiny p", 1000, 1e-300, "fpProb is too small"},
		{"huge n", math.MaxInt, 0.01, "filter is too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				got := recover()
				if got == nil {
					t.Fatalf("New(%d, %g) did not panic", tt.n, tt.fpProb)
				}
				if msg, _ := got.(string); !strings.Contains(msg, tt.want) {
					t.Fatalf("New(%d, %g) panicked with %v, want %q", tt.n, tt.fpProb, got, tt.want)
				}
			}()
			New(tt.n, tt.fpProb)
		})
	}

	// n=0 is valid: the smallest possible filter.
	if f := New(0, 0.01); f.Size() == 0 {
		t.Error("New(0, 0.01) has no blocks")
	}
}

func TestNoAllocs(t *testing.T) {
	f := New(1000, 0.01)
	key := []byte("alice")
	if n := testing.AllocsPerRun(100, func() { f.Add(key) }); n != 0 {
		t.Errorf("Add allocates %v times, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() { f.Contains(key) }); n != 0 {
		t.Errorf("Contains allocates %v times, want 0", n)
	}
}
