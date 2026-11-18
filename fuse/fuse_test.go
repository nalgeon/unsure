package fuse

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"testing"
	"unsafe"

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

// hashKeys returns the item hashes of n keys from makeKeys(seed, n).
func hashKeys(seed uint64, n int) []uint64 {
	hashes := make([]uint64, n)
	var k [16]byte
	binary.LittleEndian.PutUint64(k[:8], seed)
	for i := range hashes {
		binary.LittleEndian.PutUint64(k[8:], uint64(i))
		hashes[i] = wyhash.Hash(k[:], wyhash.Seed0)
	}
	return hashes
}

func checkNoFalseNegatives[T uint8 | uint16 | uint32](t *testing.T, f *Filter[T], keys [][]byte) {
	t.Helper()
	for _, k := range keys {
		if !f.Contains(k) {
			t.Fatalf("key %x not found", k)
		}
	}
}

func TestNoFalseNegatives(t *testing.T) {
	sizes := []int{0, 1, 2, 3, 4, 5, 10, 100, 1000, 10_000, 100_000, 1_000_000}
	for _, n := range sizes {
		keys := makeKeys(1, n)
		t.Run(fmt.Sprintf("n=%d/T=uint8", n), func(t *testing.T) {
			checkNoFalseNegatives(t, New[uint8](slices.Values(keys), len(keys)), keys)
		})
		t.Run(fmt.Sprintf("n=%d/T=uint16", n), func(t *testing.T) {
			checkNoFalseNegatives(t, New[uint16](slices.Values(keys), len(keys)), keys)
		})
		t.Run(fmt.Sprintf("n=%d/T=uint32", n), func(t *testing.T) {
			checkNoFalseNegatives(t, New[uint32](slices.Values(keys), len(keys)), keys)
		})
	}
}

// TestRetries checks filters that needed a retry, which derive their hashes
// differently (see layout.hash).
func TestRetries(t *testing.T) {
	found := 0
	for n := 1; n < 2000 && found < 5; n++ {
		if _, attempts := build[uint8](hashKeys(uint64(n), n)); attempts == 1 {
			continue
		}
		found++
		keys := makeKeys(uint64(n), n)
		f := New[uint8](slices.Values(keys), n)
		if f.seed == 0 {
			t.Fatalf("n=%d: seed 0 after a retry", n)
		}
		checkNoFalseNegatives(t, f, keys)
	}
	if found == 0 {
		t.Fatal("no size needed a retry")
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
	checkNoFalseNegatives(t, New[uint8](slices.Values(keys), len(keys)), keys)
}

// TestReusedBuffer checks that New doesn't retain the items,
// so the iterator may reuse one buffer.
func TestReusedBuffer(t *testing.T) {
	const n = 10_000
	f := New[uint16](func(yield func([]byte) bool) {
		var buf [16]byte
		binary.LittleEndian.PutUint64(buf[:8], 1)
		for i := range n {
			binary.LittleEndian.PutUint64(buf[8:], uint64(i))
			if !yield(buf[:]) {
				return
			}
		}
	}, n)
	checkNoFalseNegatives(t, f, makeKeys(1, n))
}

func TestDuplicates(t *testing.T) {
	cases := []struct {
		name string
		keys [][]byte
	}{
		{"pair", append(makeKeys(1, 1000), makeKeys(1, 1)...)},
		{"all twice", append(makeKeys(1, 1000), makeKeys(1, 1000)...)},
		{"all 3 times", slices.Repeat(makeKeys(1, 10_000), 3)},
		{"one key 1000 times", slices.Repeat(makeKeys(1, 1), 1000)},
		{"one key 300 times among others", append(makeKeys(2, 1000), slices.Repeat(makeKeys(1, 1), 300)...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := New[uint8](slices.Values(c.keys), len(c.keys))
			checkNoFalseNegatives(t, f, c.keys)
			// Duplicates are removed before the filter is sized,
			// so it's sized for the distinct keys.
			seen := map[string]bool{}
			var distinct [][]byte
			for _, k := range c.keys {
				if !seen[string(k)] {
					seen[string(k)] = true
					distinct = append(distinct, k)
				}
			}
			if want := New[uint8](slices.Values(distinct), len(distinct)).Size(); f.Size() != want {
				t.Errorf("size %d, want %d (distinct keys only)", f.Size(), want)
			}
		})
	}
}

// TestFewDuplicates checks that a few duplicates don't cost an extra attempt
// when removing them keeps the filter size, so the failed peel is resumed.
func TestFewDuplicates(t *testing.T) {
	tested := 0
	for _, n := range []int{100, 1000, 10_000, 300_000} {
		for _, dups := range []int{1, 2, 5} {
			uniq := hashKeys(uint64(n), n)
			_, want := build[uint8](slices.Clone(uniq))
			// The first dups hashes twice, and the first one 3 times.
			hashes := append(slices.Clone(uniq), uniq[:dups]...)
			hashes = append(hashes, uniq[0])
			if newLayout(n) != newLayout(len(hashes)) {
				continue // the fallback sorts, tested in TestDuplicates
			}
			if _, got := build[uint8](hashes); got != want {
				t.Errorf("n=%d dups=%d: %d attempts, want %d", n, dups, got, want)
			}
			tested++
		}
	}
	if tested < 6 {
		t.Errorf("only %d cases kept the layout", tested)
	}
}

func TestDeterministic(t *testing.T) {
	keys := makeKeys(1, 10_000)
	a := New[uint16](slices.Values(keys), len(keys))
	b := New[uint16](slices.Values(keys), len(keys))
	if a.layout != b.layout || !slices.Equal(a.fps, b.fps) {
		t.Fatal("same keys built different filters")
	}
}

// TestSizeHint checks that n only affects preallocation, not the filter.
func TestSizeHint(t *testing.T) {
	keys := makeKeys(1, 10_000)
	want := New[uint8](slices.Values(keys), len(keys))
	for _, n := range []int{-1, 0, 1, len(keys) / 2, len(keys) * 10} {
		got := New[uint8](slices.Values(keys), n)
		if got.layout != want.layout || !slices.Equal(got.fps, want.fps) {
			t.Errorf("n=%d: built a different filter", n)
		}
	}
}

// TestAttempts checks that builds rarely need a retry at any size.
// A failed attempt costs a full rebuild, plus a sort the first time.
func TestAttempts(t *testing.T) {
	var sizes []int
	for n := 1; n < 1000; n++ {
		sizes = append(sizes, n)
	}
	for n := 1000.0; n < 2e6; n *= 1.07 {
		sizes = append(sizes, int(n))
	}
	total, worst, worstN, failed := 0, 0, 0, 0
	for _, n := range sizes {
		_, attempts := build[uint8](hashKeys(uint64(n), n))
		total += attempts
		if attempts > 1 {
			failed++
		}
		if attempts > worst {
			worst, worstN = attempts, n
		}
	}
	mean := float64(total) / float64(len(sizes))
	t.Logf("%d sizes: mean %.3f attempts, %d needed a retry, worst %d at n=%d", len(sizes), mean, failed, worst, worstN)
	if mean > 1.1 || worst > 5 {
		t.Errorf("mean %.3f attempts, worst %d at n=%d", mean, worst, worstN)
	}
}

func testFalsePositiveRate[T uint8 | uint16 | uint32](t *testing.T, n, probes int) {
	f := New[T](slices.Values(makeKeys(1, n)), n)
	fp := 0
	for _, k := range makeKeys(2, probes) {
		if f.Contains(k) {
			fp++
		}
	}
	want := math.Ldexp(1, -8*int(unsafe.Sizeof(T(0))))
	rate := float64(fp) / float64(probes)
	// 5 standard deviations of a binomial.
	slack := 5 * math.Sqrt(want*(1-want)/float64(probes))
	t.Logf("n=%d: want %.3g, got %.3g (%d/%d)", n, want, rate, fp, probes)
	if math.Abs(rate-want) > slack {
		t.Errorf("false positive rate %.3g, want %.3g ± %.2g", rate, want, slack)
	}
}

func TestFalsePositiveRate(t *testing.T) {
	for _, n := range []int{100, 10_000, 1_000_000} {
		t.Run(fmt.Sprintf("n=%d/T=uint8", n), func(t *testing.T) { testFalsePositiveRate[uint8](t, n, 2_000_000) })
		t.Run(fmt.Sprintf("n=%d/T=uint16", n), func(t *testing.T) { testFalsePositiveRate[uint16](t, n, 10_000_000) })
	}
}

// TestSize checks bits per item against the reference sizing:
// 1.125·bits per item for large n, more for small n.
func TestSize(t *testing.T) {
	for _, n := range []int{1000, 10_000, 100_000, 1_000_000, 10_000_000} {
		f := New[uint8](slices.Values(makeKeys(1, n)), n)
		bpi := float64(f.Size()*8) / float64(n)
		factor := max(1.125, 0.875+0.25*math.Log(1e6)/math.Log(float64(n)))
		t.Logf("n=%d: %.2f bits/item (size factor %.3f)", n, bpi, factor)
		// The array is rounded up to whole segments, plus 2 segments.
		if bpi > 8*factor*1.1 {
			t.Errorf("n=%d: %.2f bits/item, want about %.2f", n, bpi, 8*factor)
		}
	}
}

func TestPositions(t *testing.T) {
	for _, n := range []int{0, 1, 2, 10, 1000, 1_000_000, 100_000_000} {
		l := newLayout(n)
		size := uint32(l.size())
		for _, h := range []uint64{0, 1, math.MaxUint64, 1 << 63, 0x123456789abcdef} {
			h0, h1, h2 := l.positions(h)
			if h0 >= size || h1 >= size || h2 >= size {
				t.Fatalf("n=%d h=%x: positions %d %d %d out of %d", n, h, h0, h1, h2, size)
			}
			if h0/l.segLen+1 != h1/l.segLen || h1/l.segLen+1 != h2/l.segLen {
				t.Fatalf("n=%d h=%x: positions %d %d %d not in consecutive segments of %d", n, h, h0, h1, h2, l.segLen)
			}
		}
	}
}

// FuzzFilter builds filters from small item sets with many duplicates, and
// checks that every item is found and that the build is deterministic.
// Each item is 2 bytes: a byte of data and a repetition index mod 7, so there
// are at most 256·7 distinct items, repeated up to 2000 times in total.
func FuzzFilter(f *testing.F) {
	f.Add([]byte{}, uint16(0))
	f.Add([]byte{1}, uint16(0))
	f.Add([]byte{1, 1, 1, 2}, uint16(3))
	f.Add([]byte("hello, world"), uint16(1000))
	f.Fuzz(func(t *testing.T, data []byte, n uint16) {
		var keys [][]byte
		for r := range int(n%2000) + 1 {
			for _, b := range data {
				keys = append(keys, []byte{b, byte(r % 7)})
			}
		}
		fl := New[uint8](slices.Values(keys), len(keys))
		checkNoFalseNegatives(t, fl, keys)
		again := New[uint8](slices.Values(keys), len(keys))
		if fl.layout != again.layout || !slices.Equal(fl.fps, again.fps) {
			t.Fatal("same keys built different filters")
		}
	})
}
