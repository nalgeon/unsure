package bench

import (
	"encoding/binary"
	"fmt"
	"testing"

	ref "github.com/bits-and-blooms/bloom/v3"
	"github.com/jcalabro/gloom"
	"github.com/nalgeon/unsure/bloom"
)

// bloomFilter is the common interface for our Filter and the other implementations,
// so the same tests and benchmarks run against all of them.
type bloomFilter interface {
	Add(item []byte)
	Contains(item []byte) bool
}

// bbBloom adapts github.com/bits-and-blooms/bloom to the bloomFilter interface.
type bbBloom struct{ f *ref.BloomFilter }

func (b bbBloom) Add(item []byte)           { b.f.Add(item) }
func (b bbBloom) Contains(item []byte) bool { return b.f.Test(item) }

// gloomBloom adapts github.com/jcalabro/gloom to the bloomFilter interface.
type gloomBloom struct{ f *gloom.Filter }

func (g gloomBloom) Add(item []byte)           { g.f.Add(item) }
func (g gloomBloom) Contains(item []byte) bool { return g.f.Test(item) }

type bloomImpl struct {
	name string
	new  func(n int, fpProb float64) bloomFilter
}

var bloomImpls = []bloomImpl{
	{"unsure", func(n int, p float64) bloomFilter { return bloom.New(n, p) }},
	{"bitsblooms", func(n int, p float64) bloomFilter { return bbBloom{ref.NewWithEstimates(uint(n), p)} }},
	{"gloom", func(n int, p float64) bloomFilter { return gloomBloom{gloom.New(uint64(n), p)} }},
}

// maxBloomSizeRatio is how much memory Filter may use relative to the reference
// implementation for the same n and fpProb.
const maxBloomSizeRatio = 1.15

// TestBloomSize checks that New allocates no more than maxBloomSizeRatio
// times the reference implementation, and reports bits/item for all implementations.
func TestBloomSize(t *testing.T) {
	for _, n := range []int{1000, 100_000, 10_000_000} {
		for _, p := range []float64{0.1, 0.01, 0.001} {
			t.Run(fmt.Sprintf("n=%d/p=%g", n, p), func(t *testing.T) {
				_, got := allocated(func() *bloom.Filter { return bloom.New(n, p) })
				_, want := allocated(func() *ref.BloomFilter { return ref.NewWithEstimates(uint(n), p) })
				_, gl := allocated(func() *gloom.Filter { return gloom.New(uint64(n), p) })
				bpi := func(bytes int) float64 { return float64(bytes*8) / float64(n) }
				t.Logf("n=%d p=%g: unsure=%d bytes (%.2f bits/item), bitsblooms=%d bytes (%.2f bits/item), gloom=%d bytes (%.2f bits/item)",
					n, p, got, bpi(got), want, bpi(want), gl, bpi(gl))
				if float64(got) > float64(want)*maxBloomSizeRatio {
					t.Errorf("allocated %d bytes, exceeds %.2fx reference %d", got, maxBloomSizeRatio, want)
				}
			})
		}
	}
}

// TestBloomVsMap compares the memory used by a Filter and a map-based set
// holding 100K 8-byte strings (the example from the bloom README).
func TestBloomVsMap(t *testing.T) {
	const n = 100_000
	const p = 0.01
	_, setBytes := retained(func() map[string]struct{} {
		set := make(map[string]struct{})
		var buf [8]byte
		for i := range n {
			binary.LittleEndian.PutUint64(buf[:], uint64(i))
			set[string(buf[:])] = struct{}{}
		}
		return set
	})
	_, filterBytes := retained(func() *bloom.Filter {
		filter := bloom.New(n, p)
		var buf [8]byte
		for i := range n {
			binary.LittleEndian.PutUint64(buf[:], uint64(i))
			filter.Add(buf[:])
		}
		return filter
	})
	mb := func(bytes int) float64 { return float64(bytes) / 1e6 }
	bpi := func(bytes int) float64 { return float64(bytes*8) / n }
	t.Logf("n=%d strings of 8 bytes: map=%.2f MB (%.0f bits/item), filter p=%g: %.2f MB (%.1f bits/item), %.0fx smaller",
		n, mb(setBytes), bpi(setBytes), p, mb(filterBytes), bpi(filterBytes), float64(setBytes)/float64(filterBytes))
	if filterBytes*20 > setBytes {
		t.Errorf("filter is only %.1fx smaller than map, want at least 20x", float64(setBytes)/float64(filterBytes))
	}
}

// Benchmarks

// bloomBenchSizes are chosen so the filter (about 1.2 bytes per item at 1%)
// fits in L1, fits in L2, and is far larger than any cache.
var bloomBenchSizes = []int{10_000, 1_000_000, 50_000_000}

const bloomBenchP = 0.01

// bloomBenchKeys is the max number of distinct keys a benchmark loop cycles through.
// Keys hash to random positions, so even this many probes touch the whole filter.
const bloomBenchKeys = 1_000_000

// fillBloom adds n keys (the same ones makeKeys(seed, n) would return) to f,
// without materializing them all in memory.
func fillBloom(f bloomFilter, seed uint64, n int) {
	k := make([]byte, 16)
	binary.LittleEndian.PutUint64(k[:8], seed)
	for i := range n {
		binary.LittleEndian.PutUint64(k[8:], uint64(i))
		f.Add(k)
	}
}

// bloomNewSizes cover a tiny filter (where sizing dominates), and two where
// allocating and zeroing the memory takes a growing share.
var bloomNewSizes = []int{10, 10_000, 1_000_000}

var sinkFilter bloomFilter

func BenchmarkBloomNew(b *testing.B) {
	for _, n := range bloomNewSizes {
		for _, im := range bloomImpls {
			b.Run(fmt.Sprintf("n=%d/impl=%s", n, im.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					sinkFilter = im.new(n, bloomBenchP)
				}
			})
		}
	}
}

func BenchmarkBloomAdd(b *testing.B) {
	for _, n := range bloomBenchSizes {
		keys := makeKeys(1, min(n, bloomBenchKeys))
		for _, im := range bloomImpls {
			b.Run(fmt.Sprintf("n=%d/impl=%s", n, im.name), func(b *testing.B) {
				f := im.new(n, bloomBenchP)
				// Touch every page of the fresh filter, so the timed loop
				// doesn't pay for page faults on first write. Add's cost
				// doesn't depend on how full the filter is.
				fillBloom(f, 3, n/16)
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					f.Add(keys[i])
					i++
					if i == len(keys) {
						i = 0
					}
				}
			})
		}
	}
}

func BenchmarkBloomContainsHit(b *testing.B) {
	for _, n := range bloomBenchSizes {
		// The first keys of seed 1 are among the n added by fillBloom.
		keys := makeKeys(1, min(n, bloomBenchKeys))
		for _, im := range bloomImpls {
			b.Run(fmt.Sprintf("n=%d/impl=%s", n, im.name), func(b *testing.B) {
				f := im.new(n, bloomBenchP)
				fillBloom(f, 1, n)
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					sink = f.Contains(keys[i])
					i++
					if i == len(keys) {
						i = 0
					}
				}
			})
		}
	}
}

func BenchmarkBloomContainsMiss(b *testing.B) {
	for _, n := range bloomBenchSizes {
		others := makeKeys(2, min(n, bloomBenchKeys))
		for _, im := range bloomImpls {
			b.Run(fmt.Sprintf("n=%d/impl=%s", n, im.name), func(b *testing.B) {
				f := im.new(n, bloomBenchP)
				fillBloom(f, 1, n)
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					sink = f.Contains(others[i])
					i++
					if i == len(others) {
						i = 0
					}
				}
			})
		}
	}
}
