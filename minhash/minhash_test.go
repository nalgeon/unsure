package minhash

import (
	"encoding/binary"
	"math"
	"math/bits"
	"math/rand/v2"
	"slices"
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

// sketchOf returns a sketch with k buckets of the given keys.
func sketchOf(k int, keys ...[][]byte) *Sketch {
	s := New(k)
	for _, ks := range keys {
		for _, key := range ks {
			s.Add(key)
		}
	}
	return s
}

func TestNew(t *testing.T) {
	for _, k := range []int{1, 7, 64, 100, 256, maxK} {
		s := New(k)
		if len(s.vals) != k || s.Size() != 4*k {
			t.Errorf("New(%d): %d buckets, size %d", k, len(s.vals), s.Size())
		}
		for i, v := range s.vals {
			if v != empty {
				t.Fatalf("New(%d): bucket %d = %d, want empty", k, i, v)
			}
		}
	}
	for _, k := range []int{-1, 0, maxK + 1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%d): no panic", k)
				}
			}()
			New(k)
		}()
	}
}

func TestAdd(t *testing.T) {
	keys := makeKeys(1, 1000)
	a := sketchOf(256, keys)

	t.Run("duplicates", func(t *testing.T) {
		b := sketchOf(256, keys, keys, keys[:500])
		if !slices.Equal(a.vals, b.vals) {
			t.Error("adding items again changed the sketch")
		}
	})
	t.Run("order", func(t *testing.T) {
		shuffled := slices.Clone(keys)
		rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		b := sketchOf(256, shuffled)
		if !slices.Equal(a.vals, b.vals) {
			t.Error("adding items in a different order changed the sketch")
		}
	})
	t.Run("max hash", func(t *testing.T) {
		// With k=1, the value is the top 32 bits of the hash,
		// which must not collide with the empty marker.
		s := New(1)
		s.addHash(math.MaxUint64)
		if s.vals[0] != empty-1 {
			t.Errorf("bucket = %d, want %d", s.vals[0], uint32(empty-1))
		}
	})
	t.Run("minimum", func(t *testing.T) {
		// Each bucket keeps the smallest value among the items that hit it.
		const k = 100
		want := make([]uint32, k)
		for i := range want {
			want[i] = empty
		}
		s := New(k)
		r := rand.New(rand.NewPCG(3, 4))
		for range 1000 {
			h := r.Uint64()
			hi, lo := bits.Mul64(h, k)
			want[hi] = min(want[hi], uint32(lo>>32))
			s.addHash(h)
		}
		if !slices.Equal(s.vals, want) {
			t.Error("buckets differ from per-bucket minimums")
		}
	})
}

func TestMerge(t *testing.T) {
	a, b := makeKeys(1, 300), makeKeys(2, 300)
	for _, k := range []int{1, 64, 100, 256} {
		s := sketchOf(k, a, b[:100])
		other := sketchOf(k, b)
		if err := s.Merge(other); err != nil {
			t.Fatal(err)
		}
		want := sketchOf(k, a, b)
		if !slices.Equal(s.vals, want.vals) {
			t.Errorf("k=%d: merged sketch differs from the sketch of the union", k)
		}
		if !slices.Equal(other.vals, sketchOf(k, b).vals) {
			t.Errorf("k=%d: merge changed the other sketch", k)
		}
	}
	if err := New(64).Merge(New(128)); err != ErrMismatch {
		t.Errorf("Merge with different k: err = %v, want ErrMismatch", err)
	}
}

func TestSignature(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		a, b := New(64).Signature(), New(64).Signature()
		for _, v := range a {
			if v != empty {
				t.Fatalf("empty sketch: signature value %d", v)
			}
		}
		if got := a.Similarity(b); got != 1 {
			t.Errorf("two empty sets: similarity %v, want 1", got)
		}
		c := sketchOf(64, makeKeys(1, 1)).Signature()
		if got := a.Similarity(c); got != 0 {
			t.Errorf("empty and non-empty set: similarity %v, want 0", got)
		}
	})
	t.Run("filled", func(t *testing.T) {
		// Every value comes from a non-empty bucket.
		for _, n := range []int{1, 2, 10, 100, 1000} {
			s := sketchOf(256, makeKeys(1, n))
			sig := s.Signature()
			for i, v := range sig {
				if v == empty || !slices.Contains(s.vals, v) {
					t.Fatalf("n=%d: signature value %d at %d is not a bucket value", n, v, i)
				}
				if s.vals[i] != empty && v != s.vals[i] {
					t.Fatalf("n=%d: non-empty bucket %d changed", n, i)
				}
			}
		}
	})
	t.Run("unchanged", func(t *testing.T) {
		s := sketchOf(256, makeKeys(1, 10))
		before := slices.Clone(s.vals)
		s.Signature()
		if !slices.Equal(s.vals, before) {
			t.Error("Signature changed the sketch")
		}
	})
	t.Run("same sources", func(t *testing.T) {
		// Densification depends only on which buckets are filled: two
		// sketches with the same filled buckets copy from the same places.
		const k = 300
		r := rand.New(rand.NewPCG(5, 6))
		for _, filled := range []int{1, 3, 30, 299} {
			a, b := New(k), New(k)
			for _, j := range r.Perm(k)[:filled] {
				a.vals[j] = uint32(j)
				b.vals[j] = uint32(j + 1000)
			}
			sa, sb := a.Signature(), b.Signature()
			for i := range sa {
				if sb[i] != sa[i]+1000 {
					t.Fatalf("filled=%d: bucket %d copies from %d and %d", filled, i, sa[i], sb[i]-1000)
				}
			}
		}
	})
	t.Run("deterministic", func(t *testing.T) {
		a := sketchOf(256, makeKeys(1, 50)).Signature()
		b := sketchOf(256, makeKeys(1, 50)).Signature()
		if !slices.Equal(a, b) {
			t.Error("same items, different signatures")
		}
	})
}

func TestSimilarity(t *testing.T) {
	a := sketchOf(256, makeKeys(1, 1000)).Signature()
	if got := a.Similarity(a); got != 1 {
		t.Errorf("same set: similarity %v, want 1", got)
	}
	b := sketchOf(256, makeKeys(2, 1000)).Signature()
	if got := a.Similarity(b); got != 0 {
		t.Errorf("disjoint sets: similarity %v, want 0", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("different lengths: no panic")
		}
	}()
	a.Similarity(b[:100])
}

// TestAccuracy checks the bias and error of the estimate for sets much larger
// than k, where they should match classic MinHash. Accuracy for smaller sets
// and the comparison with other implementations are in the bench module.
func TestAccuracy(t *testing.T) {
	const k = 256
	const trials = 200
	for _, j := range []float64{0.1, 0.5, 0.9} {
		// A = S ∪ X, B = S ∪ Y, with |S| = j·u and |X| = |Y|.
		const u = 20 * k
		shared := int(j * u)
		diff := (u - shared) / 2
		want := float64(shared) / float64(shared+2*diff)
		var sum, sum2 float64
		for trial := range uint64(trials) {
			s := makeKeys(3*trial, shared)
			a := sketchOf(k, s, makeKeys(3*trial+1, diff)).Signature()
			b := sketchOf(k, s, makeKeys(3*trial+2, diff)).Signature()
			e := a.Similarity(b) - want
			sum += e
			sum2 += e * e
		}
		bias, rmse := sum/trials, math.Sqrt(sum2/trials)
		stdErr := math.Sqrt(want * (1 - want) / k)
		t.Logf("J=%.2f: bias %+.4f, rmse %.4f, stdErr %.4f", want, bias, rmse, stdErr)
		// With 200 trials, the bias is within ±stdErr/5 (≈ 3 standard
		// deviations of the mean) and the RMSE within ±15% of stdErr.
		if math.Abs(bias) > stdErr/5 {
			t.Errorf("J=%.2f: bias %+.4f, more than stdErr/5 (%.4f)", want, bias, stdErr/5)
		}
		if rmse > stdErr*1.15 {
			t.Errorf("J=%.2f: rmse %.4f, more than 1.15 × stdErr (%.4f)", want, rmse, stdErr)
		}
	}
}
