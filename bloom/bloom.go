// Package bloom implements a fast blocked Bloom filter.
//
// Each item maps to a single 512-bit block (one 64-byte cache line)
// and sets k bits anywhere within it. Every Add or Contains touches exactly
// one cache line, so performance holds up even when the filter is much
// larger than the CPU cache. k is chosen per set to minimize memory
// for the requested false positive rate.
package bloom

import (
	"math"
	"math/rand/v2"
)

// block is one cache line: 8 words of 64 bits.
type block [8]uint64

// maxK is the max number of bits set per item.
const maxK = 16

// salts are odd constants used to derive independent bit positions
// from one 32-bit hash value. The first 8 are from Parquet's split block
// Bloom filter, the rest are random.
var salts = [maxK]uint32{
	0x47b6137b, 0x44974d91, 0x8824ad5b, 0xa2b7289d,
	0x705495c7, 0x2df1424b, 0x9efc4947, 0x5c6bfb31,
	0x52e6b439, 0xf2a74de5, 0x269e0d37, 0x6513270f,
	0xa6a3a451, 0x0c5c7fd1, 0x128b2f33, 0xd23f0825,
}

// Set is a probabilistic set: Contains may report false positives
// (with probability around fpProb once n items are added),
// but never false negatives.
//
// Set is not safe for concurrent use if any goroutine calls Add.
type Set struct {
	blocks  []block
	nblocks uint64
	salts   []uint32 // salts[:k]
	seed    uint64   // premixed with wyseed
}

// NewSet creates a set for n items with the target false positive probability fpProb.
// Panics if fpProb is not in (0, 1).
func NewSet(n int, fpProb float64) *Set {
	if !(fpProb > 0 && fpProb < 1) {
		panic("bloom: fpProb must be in (0, 1)")
	}
	n = max(n, 1)
	k, load := params(fpProb)
	if load == 0 {
		panic("bloom: fpProb is too small")
	}
	nblocks := uint64(math.Ceil(float64(n) / load))
	if nblocks > math.MaxUint32 {
		panic("bloom: set is too large")
	}
	// The allocator rounds the allocation up to its size class (or whole
	// pages for large sizes). Appending to a nil slice reports the rounded
	// capacity, so use it all as extra blocks: lower FP rate for free.
	blocks := append([]block(nil), make([]block, nblocks)...)
	// Block index math needs nblocks <= 2^32 (see Add), so cap it.
	blocks = blocks[:min(uint64(cap(blocks)), math.MaxUint32)]
	nblocks = uint64(len(blocks))
	return &Set{
		blocks:  blocks,
		nblocks: nblocks,
		salts:   salts[:k],
		seed:    wyseed(rand.Uint64()),
	}
}

// Add adds an item to the set.
func (s *Set) Add(item []byte) {
	h := wyhash(item, s.seed)
	b := &s.blocks[(h>>32)*s.nblocks>>32]
	x := uint32(h)
	for _, salt := range s.salts {
		pos := (x * salt) >> 23 // 9 bits: 0..511
		b[pos>>6] |= 1 << (pos & 63)
	}
}

// Contains reports whether the item may be in the set.
// False means the item is definitely not in the set.
func (s *Set) Contains(item []byte) bool {
	h := wyhash(item, s.seed)
	b := &s.blocks[(h>>32)*s.nblocks>>32]
	x := uint32(h)
	m := uint64(1)
	for _, salt := range s.salts {
		pos := (x * salt) >> 23
		m &= b[pos>>6] >> (pos & 63)
	}
	return m != 0
}

// Size returns the memory used by the set's data, in bytes.
func (s *Set) Size() int {
	return len(s.blocks) * 64
}

// params returns the number of bits per item k and the max average
// number of items per block that minimize memory for the given fpProb.
func params(fpProb float64) (k int, load float64) {
	// The max load first increases with k, then decreases,
	// so stop at the first k that does worse than the previous one.
	for kk := 1; kk <= maxK; kk++ {
		l := maxLoad(fpProb, kk)
		if kk > 1 && l <= load {
			break
		}
		k, load = kk, l
	}
	return k, load
}

// maxLoad returns the largest average number of items per block
// that keeps the false positive rate at or below fpProb with k bits per item.
func maxLoad(fpProb float64, k int) float64 {
	// fpRate is increasing in load, so bisect.
	lo, hi := 0.0, 1024.0
	for range 40 {
		mid := (lo + hi) / 2
		if fpRate(mid, k) <= fpProb {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// fpRate returns the expected false positive rate of a filter
// with the given average number of items per block and k bits per item.
//
// Items per block follow (approximately) a Poisson distribution.
// A block with j items has a given bit set with probability
// 1-(511/512)^(j*k), so a lookup falsely matches all k bits with
// probability (1-(511/512)^(j*k))^k. Poisson slightly overestimates
// the spread of the actual binomial distribution, so the result
// is a bit conservative.
func fpRate(load float64, k int) float64 {
	if load <= 0 {
		return 0
	}
	if load > 700 {
		// A block this full matches everything (and exp(-load) underflows).
		return 1
	}
	jmax := int(load + 12*math.Sqrt(load) + 20)
	pmf := math.Exp(-load) // Poisson pmf at j=0
	q := math.Pow(511.0/512.0, float64(k))
	qj := 1.0 // q^j
	rate := 0.0
	for j := 1; j <= jmax; j++ {
		pmf *= load / float64(j)
		qj *= q
		rate += pmf * powInt(1-qj, k)
	}
	return rate
}

// powInt returns x**n for n >= 0.
func powInt(x float64, n int) float64 {
	r := 1.0
	for ; n > 0; n >>= 1 {
		if n&1 == 1 {
			r *= x
		}
		x *= x
	}
	return r
}
