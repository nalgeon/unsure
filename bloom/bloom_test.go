package bloom

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
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
	s := NewSet(1000, 0.01)
	for _, k := range makeKeys(1, 1000) {
		if s.Contains(k) {
			t.Fatalf("empty set reports %x as present", k)
		}
	}
}

func TestNoFalseNegatives(t *testing.T) {
	for _, n := range []int{1, 10, 1000, 100_000} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			s := NewSet(n, 0.01)
			keys := makeKeys(1, n)
			for _, k := range keys {
				s.Add(k)
			}
			for _, k := range keys {
				if !s.Contains(k) {
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
	s := NewSet(len(keys), 0.01)
	for _, k := range keys {
		s.Add(k)
	}
	for _, k := range keys {
		if !s.Contains(k) {
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
			s := NewSet(n, p)
			for _, k := range members {
				s.Add(k)
			}
			fp := 0
			for _, k := range others {
				if s.Contains(k) {
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
	s := NewSet(n, p)
	fill(s, 1, n)
	fp := 0
	for _, k := range makeKeys(2, probes) {
		if s.Contains(k) {
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
				for f := range filters {
					s := NewSet(n, p)
					fill(s, uint64(100+f), n)
					for _, k := range others[:probes/filters] {
						if s.Contains(k) {
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

// TestSize checks the memory actually allocated by NewSet: no less than the
// information-theoretic minimum of n*log2(1/p) bits (which no approximate
// membership structure can beat). It also checks that Set.Size reports
// the allocated memory accurately.
func TestSize(t *testing.T) {
	for _, n := range []int{1000, 100_000, 10_000_000} {
		for _, p := range []float64{0.1, 0.01, 0.001} {
			t.Run(fmt.Sprintf("n=%d/p=%g", n, p), func(t *testing.T) {
				s, got := allocated(func() *Set { return NewSet(n, p) })
				minBytes := int(float64(n) * math.Log2(1/p) / 8)
				if got < minBytes {
					t.Errorf("allocated %d bytes, below the theoretical minimum %d; measurement is likely wrong", got, minBytes)
				}
				// Size leaves out the struct and allocator rounding:
				// up to 1/8 for small objects (size classes),
				// up to one 8KB page for large ones (>32KB).
				size := s.Size()
				slack := 256 + size/8
				if size > 32<<10 {
					slack = 256 + 8<<10
				}
				if size > got || got > size+slack {
					t.Errorf("Size() = %d, but NewSet allocated %d bytes", size, got)
				}
			})
		}
	}
}

// fill adds n keys (the same ones makeKeys(seed, n) would return) to s,
// without materializing them all in memory.
func fill(s *Set, seed uint64, n int) {
	k := make([]byte, 16)
	binary.LittleEndian.PutUint64(k[:8], seed)
	for i := range n {
		binary.LittleEndian.PutUint64(k[8:], uint64(i))
		s.Add(k)
	}
}
