package minhash

import (
	"encoding/binary"
	"math"
	"math/bits"
	"slices"
	"testing"
)

// fuzzOp is an operation of FuzzSketch: add hash h to sketch a or b.
type fuzzOp struct {
	h   uint64
	toB bool
}

// decodeFuzzOps returns the operations in data, 9 bytes each:
// the hash (little-endian) and the target sketch (low bit of the 9th byte).
func decodeFuzzOps(data []byte) []fuzzOp {
	var ops []fuzzOp
	for len(data) >= 9 && len(ops) < 2000 {
		ops = append(ops, fuzzOp{
			h:   binary.LittleEndian.Uint64(data),
			toB: data[8]&1 != 0,
		})
		data = data[9:]
	}
	return ops
}

// encodeFuzzOps encodes hashes as the input of FuzzSketch,
// alternating between sketches a and b.
func encodeFuzzOps(hs ...uint64) []byte {
	var b []byte
	for i, h := range hs {
		b = binary.LittleEndian.AppendUint64(b, h)
		b = append(b, byte(i&1))
	}
	return b
}

// fuzzK returns the number of buckets for the raw value:
// 65536 for 0xffff, otherwise in [1, 1024].
func fuzzK(raw uint16) int {
	if raw == math.MaxUint16 {
		return maxK
	}
	return int(raw)%1024 + 1
}

// naiveSignature computes the signature of bucket values vals from
// the definition of optimal densification: each empty bucket i takes
// the value of the first non-empty bucket among i + b_1, i + b_2, ...
// (mod k), with the same offsets b_t for all buckets.
func naiveSignature(vals []uint32) Signature {
	k := len(vals)
	sig := slices.Clone(vals)
	if isEmpty(vals) {
		return sig
	}
	var offsets []int
	for i := range sig {
		for t := 0; sig[i] == empty; t++ {
			if t == len(offsets) {
				x := uint64(t+1) * wyrandStep
				b, _ := bits.Mul64(wyrand(x), uint64(k))
				offsets = append(offsets, int(b))
			}
			sig[i] = vals[(i+offsets[t])%k]
		}
	}
	return sig
}

// FuzzSketch adds fuzzed hashes to two sketches a and b with a fuzzed k
// (from 1 to 1024, or 65536), and checks them against a model of the
// smallest value in each bucket:
//   - The buckets match the model, and no added hash leaves a bucket empty.
//   - Signature doesn't change the sketch, and keeps the values of
//     filled buckets. An empty sketch gives an all-empty signature,
//     a non-empty one has no empty values.
//   - The signature matches naiveSignature (for k ≤ 1024).
//   - a.Merge(b) gives exactly the sketch that saw both a's and b's
//     hashes (in reverse order), and leaves b unchanged.
//   - Similarity is 1 for a signature with itself, symmetric, within
//     [0, 1], and 0 between an empty and a non-empty set.
func FuzzSketch(f *testing.F) {
	f.Add(encodeFuzzOps(1, 2, 3), uint16(63))
	f.Add(encodeFuzzOps(0, math.MaxUint64, 1<<63), uint16(0))
	// Values capped below the empty marker (k=1: the value is h>>32).
	f.Add(encodeFuzzOps(math.MaxUint64, math.MaxUint64-1, 0xffffffff00000000), uint16(0))
	// One item in a large sketch: thousands of densification rounds.
	f.Add(encodeFuzzOps(12345), uint16(1023))
	f.Add(encodeFuzzOps(12345, 1<<40), uint16(math.MaxUint16))
	// Push, then pull: about half the buckets filled.
	f.Add(encodeFuzzOps(1<<60, 3<<60, 5<<60, 7<<60, 9<<60, 11<<60, 13<<60, 15<<60, 1<<59), uint16(15))
	// Non-power-of-two k.
	f.Add(encodeFuzzOps(1<<62, 1<<61, 1<<60, 3<<62, 1<<63), uint16(299))

	f.Fuzz(func(t *testing.T, data []byte, kraw uint16) {
		k := fuzzK(kraw)
		ops := decodeFuzzOps(data)

		// Model: the smallest value in each bucket, for a, b and both.
		modelA := slices.Repeat([]uint32{empty}, k)
		modelB := slices.Clone(modelA)
		a, b, both := New(k), New(k), New(k)
		for _, op := range slices.Backward(ops) {
			both.addHash(op.h)
		}
		for _, op := range ops {
			i, lo := bits.Mul64(op.h, uint64(k))
			v := uint32(lo >> 32)
			if v == empty {
				v--
			}
			if op.toB {
				modelB[i] = min(modelB[i], v)
				b.addHash(op.h)
			} else {
				modelA[i] = min(modelA[i], v)
				a.addHash(op.h)
			}
		}
		if !slices.Equal(a.vals, modelA) {
			t.Fatalf("a: buckets differ from the model")
		}
		if !slices.Equal(b.vals, modelB) {
			t.Fatalf("b: buckets differ from the model")
		}

		sigA := checkSignature(t, "a", a)
		sigB := checkSignature(t, "b", b)
		sigBoth := checkSignature(t, "both", both)

		bVals := slices.Clone(b.vals)
		if err := a.Merge(b); err != nil {
			t.Fatalf("Merge: %v", err)
		}
		if !slices.Equal(b.vals, bVals) {
			t.Fatal("Merge changed other")
		}
		if !slices.Equal(a.vals, both.vals) {
			t.Fatal("merged buckets differ from the sketch that saw both")
		}
		if !slices.Equal(a.Signature(), sigBoth) {
			t.Fatal("merged signature differs from the one that saw both")
		}

		for _, sig := range []Signature{sigA, sigB, sigBoth} {
			if s := sig.Similarity(sig); s != 1 {
				t.Fatalf("similarity with itself = %v", s)
			}
		}
		for _, p := range [][2]Signature{{sigA, sigB}, {sigA, sigBoth}, {sigB, sigBoth}} {
			s1, s2 := p[0].Similarity(p[1]), p[1].Similarity(p[0])
			if s1 != s2 || s1 < 0 || s1 > 1 {
				t.Fatalf("similarity = %v and %v", s1, s2)
			}
			empty1, empty2 := isEmpty(p[0]), isEmpty(p[1])
			if empty1 != empty2 && s1 != 0 {
				t.Fatalf("empty and non-empty set: similarity = %v", s1)
			}
		}
	})
}

// checkSignature checks the signature of s and returns it.
func checkSignature(t *testing.T, name string, s *Sketch) Signature {
	t.Helper()
	vals := slices.Clone(s.vals)
	sig := s.Signature()
	if !slices.Equal(s.vals, vals) {
		t.Fatalf("%s: Signature changed the sketch", name)
	}
	if len(sig) != len(vals) {
		t.Fatalf("%s: len(Signature) = %d, want %d", name, len(sig), len(vals))
	}
	emptySet := isEmpty(vals)
	for i, v := range sig {
		if vals[i] != empty && v != vals[i] {
			t.Fatalf("%s: filled bucket %d = %d in the signature, want %d", name, i, v, vals[i])
		}
		if (v == empty) != emptySet {
			t.Fatalf("%s: signature value %d at %d, empty set = %v", name, v, i, emptySet)
		}
	}
	if len(vals) <= 1024 {
		want := naiveSignature(vals)
		for i := range sig {
			if sig[i] != want[i] {
				t.Fatalf("%s: signature value %d at %d, want %d", name, sig[i], i, want[i])
			}
		}
	}
	return sig
}

// isEmpty reports whether all values are empty.
func isEmpty(vals []uint32) bool {
	return !slices.ContainsFunc(vals, func(v uint32) bool { return v != empty })
}
