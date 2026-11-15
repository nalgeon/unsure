package topk

import (
	"fmt"
	"math"
	"math/bits"
	"slices"
	"testing"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// fuzzKs are the k values FuzzSketch picks from.
var fuzzKs = []int{1, 2, 3, 5, 10}

// fuzzItems are the items FuzzSketch adds: few, so they compete
// for buckets and heap places, with different lengths, so that
// replaced items reuse or grow the buffers.
var fuzzItems = func() [][]byte {
	items := make([][]byte, 32)
	for i := range items {
		items[i] = fmt.Appendf(nil, "%d", i*i*i*7919)
	}
	return items
}()

// op is an operation of FuzzSketch: add n to the item (n=0 is a no-op).
type op struct {
	item int
	n    int
}

// decodeOps returns the operations in data, 2 bytes each: the item
// (low 5 bits) and the kind (high 3 bits) of the operation, and its
// argument. Kinds 0–3 are Add, 4–5 AddN with n up to 255,
// 6 AddN with n = 2^(arg mod 63), and 7 AddN with n = 0.
func decodeOps(data []byte) []op {
	var ops []op
	for len(data) >= 2 && len(ops) < 2000 {
		b, arg := data[0], data[1]
		data = data[2:]
		o := op{item: int(b & 31), n: 1}
		switch b >> 5 {
		case 4, 5:
			o.n = int(arg)
		case 6:
			o.n = int(min(uint64(1)<<(arg%63), math.MaxInt))
		case 7:
			o.n = 0
		}
		ops = append(ops, o)
	}
	return ops
}

// apply adds the operations to s, using Add for n=1 and AddN otherwise.
func apply(s *Sketch, ops []op) {
	for _, o := range ops {
		if o.n == 1 {
			s.Add(fuzzItems[o.item])
		} else {
			s.AddN(fuzzItems[o.item], o.n)
		}
	}
}

// FuzzSketch adds fuzzed operations to a sketch with a fuzzed k,
// optionally narrowed to one bucket per row, and checks it against
// the true counts:
//   - The heap and index are consistent (see checkInvariants).
//   - Top has at most k distinct items, sorted by count descending
//     (ties by item), with counts of at least 1.
//   - Count agrees with Top, and is 0 for items not in it.
//   - Total is the sum of all counts added, capped at MaxInt.
//   - No estimate exceeds the true count (capped at MaxUint32).
//   - If there are at most k items and each has a bucket of its own,
//     the estimates are exact.
//   - The same operations give the same Top.
func FuzzSketch(f *testing.F) {
	f.Add([]byte{0, 0, 1, 0, 1, 0, 2, 0, 2, 0, 2, 0}, uint8(1))
	f.Add([]byte{0, 0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 0, 0, 0, 0}, uint8(8))
	f.Add([]byte{0x80, 200, 0x81, 100, 0x82, 150, 0xe0, 0, 3, 0, 0x83, 255}, uint8(2))
	// Huge counts: capped at MaxUint32, and a sum beyond MaxInt.
	f.Add([]byte{0xc0, 40, 0xc1, 62, 0xc1, 62, 0xc2, 62, 0, 0}, uint8(0))
	f.Add([]byte{0xc0, 9, 0xc1, 62, 0x02, 0, 0x02, 0, 0xc3, 5}, uint8(9))

	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		k := fuzzKs[int(mode&7)%len(fuzzKs)]
		narrowed := mode&8 != 0
		newSketch := func() *Sketch {
			s := New(k)
			if narrowed {
				narrow(s)
			}
			return s
		}
		ops := decodeOps(data)
		s := newSketch()
		apply(s, ops)

		// True counts and total, saturated.
		counts := make([]uint64, len(fuzzItems))
		var total uint64
		for _, o := range ops {
			counts[o.item] = satAdd(counts[o.item], uint64(o.n))
			total = satAdd(total, uint64(o.n))
		}

		checkInvariants(t, s)
		if want := int(min(total, math.MaxInt)); s.Total() != want {
			t.Fatalf("Total = %d, want %d", s.Total(), want)
		}
		top := s.Top()
		if len(top) > k {
			t.Fatalf("len(Top) = %d > k = %d", len(top), k)
		}
		inTop := make(map[string]int)
		for i, e := range top {
			if _, ok := inTop[e.Item]; ok {
				t.Fatalf("Top has %q twice", e.Item)
			}
			inTop[e.Item] = e.Count
			if e.Count < 1 {
				t.Fatalf("Top[%d] = %v, want count >= 1", i, e)
			}
			if i > 0 {
				prev := top[i-1]
				if prev.Count < e.Count || prev.Count == e.Count && prev.Item >= e.Item {
					t.Fatalf("Top not sorted: %v before %v", prev, e)
				}
			}
		}

		// Items with a bucket of their own in some row (in the narrowed
		// table, only if they are the only item).
		distinct := 0
		for _, c := range counts {
			if c > 0 {
				distinct++
			}
		}
		own := ownBuckets(s, counts)
		for i, item := range fuzzItems {
			c := s.Count(item)
			want, ok := inTop[string(item)]
			if !ok {
				want = 0
			}
			if c != want {
				t.Fatalf("Count(%s) = %d, Top has %d", item, c, want)
			}
			if limit := min(counts[i], math.MaxUint32); uint64(c) > limit {
				t.Fatalf("Count(%s) = %d > true count %d", item, c, limit)
			}
			if distinct <= k && own[i] && uint64(c) != min(counts[i], math.MaxUint32) {
				t.Fatalf("Count(%s) = %d, want exactly %d (%d items, k=%d)", item, c, counts[i], distinct, k)
			}
		}

		// Deterministic.
		s2 := newSketch()
		apply(s2, ops)
		if !slices.Equal(top, s2.Top()) {
			t.Fatalf("Top = %v, then %v for the same operations", top, s2.Top())
		}
	})
}

// ownBuckets reports for each item whether it has a bucket that
// no other item with a nonzero count maps to.
func ownBuckets(s *Sketch, counts []uint64) []bool {
	pos := make([][2]uint64, len(fuzzItems))
	used := make(map[uint64]int)
	for i, item := range fuzzItems {
		h := wyhash.Hash(item, wyhash.Seed0)
		for r, salt := range salts {
			j, _ := bits.Mul64(h*salt, s.width)
			pos[i][r] = uint64(r)*s.width + j
			if counts[i] > 0 {
				used[pos[i][r]]++
			}
		}
	}
	own := make([]bool, len(fuzzItems))
	for i := range fuzzItems {
		own[i] = counts[i] > 0 && (used[pos[i][0]] == 1 || used[pos[i][1]] == 1)
	}
	return own
}

// satAdd returns a+b, or MaxUint64 if it overflows.
func satAdd(a, b uint64) uint64 {
	if s, c := bits.Add64(a, b, 0); c == 0 {
		return s
	}
	return math.MaxUint64
}
