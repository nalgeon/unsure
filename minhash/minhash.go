// Package minhash implements MinHash signatures for estimating the
// similarity of sets.
//
// A sketch summarizes a set of items in k small values. Its signature can be
// compared with another set's signature to estimate their Jaccard similarity
// |A ∩ B| / |A ∪ B|, the proportion of unique items that both sets share.
//
// Signatures are what you store and compare, for example to find
// near-duplicate documents (as sets of words or shingles), or to bucket them
// with locality-sensitive hashing (LSH).
//
// The sketch uses one permutation hashing (Li, Owen & Zhang, 2012): each
// item's hash picks one of k buckets, and the bucket keeps the smallest hash
// it has seen. For sets much larger than k, almost all buckets are
// filled, and the estimate has the same standard error as classic MinHash:
// stdErr = √(J(1−J)/k), at most 0.5/√k, where J is the true similarity.
//
//	k      stdErr at J=0.5   signature
//	64     6.3%              256 B
//	128    4.4%              512 B
//	256    3.1%              1 KB
//	512    2.2%              2 KB
//	1024   1.6%              4 KB
//
// When the set is not much larger than k, some buckets stay empty.
// The signature fills each empty bucket by copying the value of another,
// non-empty bucket ("optimal densification", Shrivastava, 2017).
// But since copied values don't add any new information, the error
// is higher for small sets.
//
// Sketches with the same k can be merged: the result is the same as a single
// sketch that saw the items of both.
//
// Hashing is deterministic, so the same items always produce the same
// signature. The sketch is not designed for adversarially chosen items:
// anyone who knows the seed can create items that distort the estimate.
package minhash

import (
	"errors"
	"math"
	"math/bits"

	"github.com/nalgeon/unsure/internal/wyhash"
)

// maxK is the largest supported number of buckets. Densifying a sketch with
// a single item takes about k² steps, so much larger k gets impractical.
const maxK = 1 << 16

// empty marks a bucket that has no items. Hash values are capped below it.
const empty = math.MaxUint32

// ErrMismatch is returned by [Sketch.Merge] when
// the sketches have different numbers of buckets.
var ErrMismatch = errors.New("minhash: sketches have different sizes")

// Sketch collects the items of a set to compute its [Signature].
//
// Allocates memory at creation and never grows or shrinks.
// Not safe for concurrent use.
type Sketch struct {
	// vals[i] is the smallest hash value in bucket i, or empty.
	vals []uint32
}

// New creates a sketch with k buckets, which gives signatures of k values
// (4k bytes), and a similarity estimate with standard error at most 0.5/√k.
// See the package docs for common values. Panics if k is not in [1, 65536].
func New(k int) *Sketch {
	if k < 1 || k > maxK {
		panic("minhash: k must be in [1, 65536]")
	}
	vals := make([]uint32, k)
	for i := range vals {
		vals[i] = empty
	}
	return &Sketch{vals: vals}
}

// Add adds an item to the sketch.
func (s *Sketch) Add(item []byte) {
	s.addHash(wyhash.Hash(item, wyhash.Seed0))
}

// addHash adds an item with hash h.
func (s *Sketch) addHash(h uint64) {
	// h·k as a 128-bit number: the high half is the bucket, uniform in
	// [0, k) (Lemire's reduction), and the low half is the position of h
	// within the bucket's range, uniform and independent of the bucket.
	i, lo := bits.Mul64(h, uint64(len(s.vals)))
	v := min(uint32(lo>>32), empty-1)
	s.vals[i] = min(s.vals[i], v)
}

// Merge adds the items of other to s, so that s describes the union of
// both sets. Other is unchanged.
// Returns [ErrMismatch] if the sketches have different numbers of buckets.
func (s *Sketch) Merge(other *Sketch) error {
	if len(s.vals) != len(other.vals) {
		return ErrMismatch
	}
	vals := s.vals[:len(other.vals)]
	for i, v := range other.vals {
		vals[i] = min(vals[i], v)
	}
	return nil
}

// Signature returns the signature of the items added so far.
// The sketch is unchanged and can be added to or merged later.
func (s *Sketch) Signature() Signature {
	sig := make(Signature, len(s.vals))
	copy(sig, s.vals)
	filled := 0
	for _, v := range s.vals {
		if v != empty {
			filled++
		}
	}
	if filled == 0 || filled == len(s.vals) {
		// An empty set has an all-empty signature: equal to other empty
		// sets, with no values in common with non-empty ones.
		return sig
	}
	// Optimal densification: each empty bucket i takes the value of the first
	// non-empty bucket in its own pseudo-random sequence of buckets. With f
	// filled buckets, it takes about k/f tries per empty bucket.
	k := uint64(len(s.vals))
	for i, v := range s.vals {
		if v != empty {
			continue
		}
		for try := uint64(0); ; try++ {
			j, _ := bits.Mul64(mix(uint64(i)<<32|try), k)
			if s.vals[j] != empty {
				sig[i] = s.vals[j]
				break
			}
		}
	}
	return sig
}

// Size returns the memory used by the sketch's data, in bytes.
func (s *Sketch) Size() int {
	return 4 * len(s.vals)
}

// Signature is a compact summary of a set: k hash values, one per bucket.
// Signatures from sketches with the same k can be compared with
// [Signature.Similarity]. The values can also be used directly,
// for example as LSH band keys.
type Signature []uint32

// Similarity returns the estimated Jaccard similarity of the sets
// with signatures a and b: from 0 (no common items) to 1 (the same items).
// Two empty sets have similarity 1. Panics if the signatures have
// different lengths.
func (a Signature) Similarity(b Signature) float64 {
	if len(a) != len(b) {
		panic("minhash: signatures have different sizes")
	}
	if len(a) == 0 {
		return 0
	}
	b = b[:len(a)]
	n := 0
	for i, v := range a {
		if v == b[i] {
			n++
		}
	}
	return float64(n) / float64(len(a))
}

// mix is the splitmix64 finalizer, a bijective 64-bit mixer.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}
