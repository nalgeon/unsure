// Wyhash algorithm by Wang Yi, Unlicense (github.com/wangyi-fudan/wyhash).
// Implementation by Go Authors, BSD 3-Clause license (github.com/golang/go).

package bloom

import (
	"encoding/binary"
	"math/bits"
)

// wyhash constants. wy0..wy3 are the secrets (_wyp) of wyhash final 4
// (old_versions/wyhash_final4.h in github.com/wangyi-fudan/wyhash).
// wy4 is m5 from the Go runtime (also _wyp4 in wyhash v1..v3).
const (
	wy0 = 0xa0761d6478bd642f
	wy1 = 0xe7037ed1a0b428db
	wy2 = 0x8ebc6af09c88c6e3
	wy3 = 0x589965cc75374cc3
	wy4 = 0x1d8e4e27c47d124f
)

// wyhash returns a 64-bit hash of b. The output depends only on b and seed,
// so it's stable across processes and platforms.
//
// Origin: a port of memHashFallback from the Go runtime
// (src/internal/runtime/maps/runtime_hash64.go, Go 1.27.3), a
// Go-specific variant of wyhash. The logic is the same; the only
// change is that the runtime's random per-process hashkey[0..3] are
// replaced with the fixed wy0..wy3.
//
// This does not match any upstream wyhash version (no test vectors apply):
//   - seed init is seed ^= wy0 (upstream final 4.x: seed ^= wymix(seed^s0, s1));
//   - keys of 4..16 bytes use separate r4/r8 cases (upstream: four r4 reads);
//   - the finish folds the 128-bit product with hi^lo before the last mix
//     (upstream keeps both halves via _wymum);
//   - the 48-byte loop runs while n > 48 (upstream final 4.2+: n >= 48).
//
// It also differs from Go's pure-Go maphash (hash/maphash/maphash_purego.go,
// Go <= 1.26), which reads 4..16-byte keys the upstream way and doesn't
// re-read already mixed bytes for the tail of longer keys.
func wyhash(b []byte, seed uint64) uint64 {
	var x, y uint64
	n := len(b)
	seed ^= wy0
	switch {
	case n == 0:
		return seed
	case n < 4:
		x = uint64(b[0]) | uint64(b[n>>1])<<8 | uint64(b[n-1])<<16
	case n == 4:
		x = uint64(binary.LittleEndian.Uint32(b))
		y = x
	case n < 8:
		x = uint64(binary.LittleEndian.Uint32(b))
		y = uint64(binary.LittleEndian.Uint32(b[n-4:]))
	case n == 8:
		x = binary.LittleEndian.Uint64(b)
		y = x
	case n <= 16:
		x = binary.LittleEndian.Uint64(b)
		y = binary.LittleEndian.Uint64(b[n-8:])
	default:
		i := 0
		if n > 48 {
			seed1, seed2 := seed, seed
			for ; n-i > 48; i += 48 {
				seed = wymix(le64(b, i)^wy1, le64(b, i+8)^seed)
				seed1 = wymix(le64(b, i+16)^wy2, le64(b, i+24)^seed1)
				seed2 = wymix(le64(b, i+32)^wy3, le64(b, i+40)^seed2)
			}
			seed ^= seed1 ^ seed2
		}
		for ; n-i > 16; i += 16 {
			seed = wymix(le64(b, i)^wy1, le64(b, i+8)^seed)
		}
		// The last 16 bytes (may overlap bytes already mixed in).
		x = le64(b, n-16)
		y = le64(b, n-8)
	}
	return wymix(wy4^uint64(n), wymix(x^wy1, y^seed))
}

func wymix(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	return hi ^ lo
}

func le64(b []byte, i int) uint64 {
	return binary.LittleEndian.Uint64(b[i:])
}
