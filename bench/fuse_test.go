package bench

import (
	"encoding/binary"
	"fmt"
	"iter"
	"math"
	"testing"
	"unsafe"

	"github.com/FastFilter/xorfilter"
	"github.com/nalgeon/unsure/bloom"
	"github.com/nalgeon/unsure/fuse"
	"github.com/nalgeon/unsure/internal/wyhash"
)

// fuseFilter is the common interface for our Filter and the other implementations,
// so the same tests and benchmarks run against all of them.
type fuseFilter interface {
	Contains(item []byte) bool
	Size() int // bytes of filter data
}

// xorFuse adapts xorfilter.BinaryFuse, which takes uint64 keys:
// items are hashed with wyhash like ours, both when building and in Contains.
type xorFuse[T xorfilter.Unsigned] struct{ f *xorfilter.BinaryFuse[T] }

func (x xorFuse[T]) Contains(item []byte) bool { return x.f.Contains(wyhash.Hash(item, wyhash.Seed0)) }
func (x xorFuse[T]) Size() int                 { return len(x.f.Fingerprints) * int(unsafe.Sizeof(T(0))) }

type fuseImpl struct {
	name string
	bits int // fingerprint bits; the false positive rate is 2^-bits
	new  func(items iter.Seq[[]byte], n int) fuseFilter
}

// wyhashKeys hashes the items into the uint64 keys xorfilter takes.
func wyhashKeys(items iter.Seq[[]byte], n int) []uint64 {
	keys := make([]uint64, 0, n)
	for item := range items {
		keys = append(keys, wyhash.Hash(item, wyhash.Seed0))
	}
	return keys
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// newFuseBloom builds our Bloom filter at the same false positive rate
// as a fuse filter with the given fingerprint bits.
func newFuseBloom(items iter.Seq[[]byte], n, bits int) fuseFilter {
	f := bloom.New(n, math.Ldexp(1, -bits))
	for item := range items {
		f.Add(item)
	}
	return f
}

var fuseImpls = []fuseImpl{
	{"unsure", 8, func(items iter.Seq[[]byte], n int) fuseFilter { return fuse.New[uint8](items, n) }},
	{"unsure", 16, func(items iter.Seq[[]byte], n int) fuseFilter { return fuse.New[uint16](items, n) }},
	{"xorfilter", 8, func(items iter.Seq[[]byte], n int) fuseFilter {
		return xorFuse[uint8]{must(xorfilter.NewBinaryFuse[uint8](wyhashKeys(items, n)))}
	}},
	{"xorfilter", 16, func(items iter.Seq[[]byte], n int) fuseFilter {
		return xorFuse[uint16]{must(xorfilter.NewBinaryFuse[uint16](wyhashKeys(items, n)))}
	}},
	{"bloom", 8, func(items iter.Seq[[]byte], n int) fuseFilter { return newFuseBloom(items, n, 8) }},
	{"bloom", 16, func(items iter.Seq[[]byte], n int) fuseFilter { return newFuseBloom(items, n, 16) }},
}

// seqKeys yields the same keys as makeKeys(seed, n), reusing one buffer,
// so large filters can be built without materializing the keys.
func seqKeys(seed uint64, n int) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		var k [16]byte
		binary.LittleEndian.PutUint64(k[:8], seed)
		for i := range n {
			binary.LittleEndian.PutUint64(k[8:], uint64(i))
			if !yield(k[:]) {
				return
			}
		}
	}
}

// maxFuseSizeRatio is how much memory Filter may use relative to
// the reference implementation for the same n and fingerprint bits.
const maxFuseSizeRatio = 1.01

// TestFuseSize reports bits/item and the measured false positive rate
// for all implementations, and checks that our Filter is no larger
// than maxFuseSizeRatio times the reference.
func TestFuseSize(t *testing.T) {
	const probes = 2_000_000
	others := makeKeys(2, probes)
	for _, n := range []int{1000, 100_000, 10_000_000} {
		for _, bits := range []int{8, 16} {
			t.Run(fmt.Sprintf("n=%d/bits=%d", n, bits), func(t *testing.T) {
				sizes := map[string]int{}
				line := fmt.Sprintf("n=%d bits=%d:", n, bits)
				for _, im := range fuseImpls {
					if im.bits != bits {
						continue
					}
					f := im.new(seqKeys(1, n), n)
					bytes := f.Size()
					fp := 0
					for _, k := range others {
						if f.Contains(k) {
							fp++
						}
					}
					sizes[im.name] = bytes
					line += fmt.Sprintf(" %s=%.2f bits/item (fp %.2g)", im.name, float64(bytes*8)/float64(n), float64(fp)/probes)
				}
				t.Log(line)
				if got, want := sizes["unsure"], sizes["xorfilter"]; float64(got) > float64(want)*maxFuseSizeRatio {
					t.Errorf("size %d bytes, exceeds %.2fx reference %d", got, maxFuseSizeRatio, want)
				}
			})
		}
	}
}

// Benchmarks

// fuseBenchSizes are chosen so the 8-bit filter (about 1.1 bytes per item)
// fits in L1, fits in L2, and is far larger than any cache.
var fuseBenchSizes = []int{10_000, 1_000_000, 50_000_000}

// fuseNewSizes skip the largest size: building it for every implementation
// takes several GB of temporary memory and seconds per run.
var fuseNewSizes = []int{10_000, 1_000_000}

// fuseBenchKeys is the max number of distinct keys a benchmark loop cycles through.
const fuseBenchKeys = 1_000_000

var sinkFuse fuseFilter

// BenchmarkFuseNew measures building a filter from n items, reported per item.
func BenchmarkFuseNew(b *testing.B) {
	for _, n := range fuseNewSizes {
		for _, im := range fuseImpls {
			b.Run(fmt.Sprintf("n=%d/bits=%d/impl=%s", n, im.bits, im.name), func(b *testing.B) {
				for b.Loop() {
					sinkFuse = im.new(seqKeys(1, n), n)
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n), "ns/item")
			})
		}
	}
}

func benchmarkFuseContains(b *testing.B, probeSeed uint64) {
	for _, n := range fuseBenchSizes {
		// For hits, the first keys of seed 1 are among the n in the filter.
		keys := makeKeys(probeSeed, min(n, fuseBenchKeys))
		for _, im := range fuseImpls {
			b.Run(fmt.Sprintf("n=%d/bits=%d/impl=%s", n, im.bits, im.name), func(b *testing.B) {
				f := im.new(seqKeys(1, n), n)
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

func BenchmarkFuseContainsHit(b *testing.B)  { benchmarkFuseContains(b, 1) }
func BenchmarkFuseContainsMiss(b *testing.B) { benchmarkFuseContains(b, 2) }
