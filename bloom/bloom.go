// Package bloom implements a fast blocked Bloom filter.
//
// A Bloom filter is a compact set that answers "is this item in the set?".
// A "no" is always correct. A "yes" is wrong with probability about fpProb
// (the false positive rate) once n items are added, where n and fpProb are
// the parameters passed to [New]. Items can't be removed or listed.
//
// A standard Bloom filter sets k bits per item spread across the whole
// filter. This one maps each item to a single 512-bit block (one 64-byte
// cache line) and sets k bits within it. So every Add or Contains reads
// one cache line, and stays fast even when the filter is much larger than
// the CPU cache. k is chosen to minimize memory for the given fpProb.
//
// The tradeoff is memory at low false positive rates. Some blocks get more
// items than average, and at low rates most false positives come from them,
// so the filter has to keep the average lower. Down to fpProb = 1e-4, it
// uses at most ~15% more memory than a standard Bloom filter. Below that,
// the overhead grows: +34% at 1e-6, +70% at 1e-8.
//
// To choose n, use the largest number of items you expect. Adding more
// still works, but the false positive rate grows quickly: with fpProb = 0.01,
// it's ~5% at 1.5n and ~12% at 2n. To choose fpProb, weigh the cost of a
// false positive (usually an unneeded lookup) against memory: each 10× lower
// rate costs about 5–6 more bits per item. The filter takes about 4.8 bits per
// item at fpProb = 0.1, 9.9 bits at 0.01, and 15.5 bits at 0.001.
//
// Hashing is deterministic: wyhash final 4.3 with seed 0, so the same items
// always produce the same filter. The filter is not designed for adversarially
// chosen items: anyone who knows the seed (or crafts keys that cancel it out)
// can produce false positives at will.
package bloom

import (
	"math"

	"github.com/nalgeon/unsure/internal/wyhash"
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

// Filter is a probabilistic set: Contains may report false positives
// (with probability around fpProb once n items are added),
// but never false negatives.
//
// Filter is not safe for concurrent use if any goroutine calls Add.
type Filter struct {
	blocks []block
	salts  []uint32 // salts[:k]
}

// New creates a filter for n items with the target false positive
// probability fpProb.
// Panics if n is negative or fpProb is not in (0, 1).
func New(n int, fpProb float64) *Filter {
	if n < 0 {
		panic("bloom: n must not be negative")
	}
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
		panic("bloom: filter is too large")
	}
	// The allocator rounds the allocation up to its size class (or whole
	// pages for large sizes). Appending to a nil slice reports the rounded
	// capacity, so use it all as extra blocks: lower FP rate for free.
	blocks := append([]block(nil), make([]block, nblocks)...)
	// Block index math needs nblocks <= 2^32 (see Add), so cap it.
	blocks = blocks[:min(uint64(cap(blocks)), math.MaxUint32)]
	return &Filter{
		blocks: blocks,
		salts:  salts[:k],
	}
}

// Add adds an item to the filter.
func (f *Filter) Add(item []byte) {
	h := wyhash.Hash(item, wyhash.Seed0)
	b := &f.blocks[(h>>32)*uint64(len(f.blocks))>>32]
	x := uint32(h)
	for _, salt := range f.salts {
		pos := (x * salt) >> 23 // 9 bits: 0..511
		b[pos>>6] |= 1 << (pos & 63)
	}
}

// Contains reports whether the item may be in the filter.
// False means the item is definitely not in the filter.
func (f *Filter) Contains(item []byte) bool {
	h := wyhash.Hash(item, wyhash.Seed0)
	b := &f.blocks[(h>>32)*uint64(len(f.blocks))>>32]
	x := uint32(h)
	m := uint64(1)
	for _, salt := range f.salts {
		pos := (x * salt) >> 23
		m &= b[pos>>6] >> (pos & 63)
	}
	return m != 0
}

// Size returns the memory used by the filter's data, in bytes.
func (f *Filter) Size() int {
	return len(f.blocks) * 64
}

// params returns the number of bits per item k and the max average
// number of items per block that minimize memory for the given fpProb.
func params(fpProb float64) (k int, load float64) {
	// The max load first increases with k, then decreases. The peak is near
	// log2(1/fpProb), the best k for a standard Bloom filter, so start there
	// and walk up while the load improves. If it doesn't, walk down instead.
	k = min(max(int(math.Log2(1/fpProb)), 1), maxK)
	load = maxLoad(fpProb, k)
	for _, dir := range []int{1, -1} {
		for k+dir >= 1 && k+dir <= maxK {
			l := maxLoad(fpProb, k+dir)
			if l <= load {
				break
			}
			k, load = k+dir, l
		}
	}
	return k, load
}

// maxLoad returns the largest average number of items per block
// that keeps the false positive rate at or below fpProb with k bits per item,
// rounded down to within a relative 1e-6. Returns 0 if there's no such load
// above 1e-9.
func maxLoad(fpProb float64, k int) float64 {
	// A standard Bloom filter with k bits per item needs -k/ln(1-p^(1/k))
	// bits per item. Blocking only raises the FP rate, so the same bits
	// spread over 512-bit blocks give a load just above the answer.
	hi := 512 * math.Log1p(-math.Pow(fpProb, 1/float64(k))) / -float64(k)
	for fpRate(hi, k) <= fpProb {
		hi *= 1.25
	}
	lo := hi / 2
	for fpRate(lo, k) > fpProb {
		hi, lo = lo, lo/2
		if lo < 1e-9 {
			return 0
		}
	}
	// fpRate is increasing in load, so bisect.
	for hi-lo > lo*1e-6 {
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
// probability (1-(511/512)^(j*k))^k.
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
