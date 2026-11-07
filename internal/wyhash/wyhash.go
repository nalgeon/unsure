// Package wyhash is a port of wyhash final 4.3 by Wang Yi,
// Unlicense (github.com/wangyi-fudan/wyhash).
package wyhash

import (
	"encoding/binary"
	"math/bits"
)

// Default secrets (_wyp) of wyhash final 4.3.
const (
	wyp0 = 0x2d358dccaa6c78a5
	wyp1 = 0x8bb84b93962eacc9
	wyp2 = 0x4b33a62ed433d4a3
	wyp3 = 0x4d5a2da51de1aa47
)

// Seed0 is Seed(0): the premixed fixed seed that all structures hash with.
const Seed0 = 0xca813bf4c7abf0a9

// Seed premixes a seed for Hash. It's the part of wyhash that depends
// only on the seed, split out so callers can do it once instead of per item.
func Seed(seed uint64) uint64 {
	return seed ^ wymix(seed^wyp0, wyp1)
}

// Hash returns the 64-bit wyhash of b. seed must come from Seed:
// Hash(b, Seed(s)) equals upstream wyhash(b, len(b), s, _wyp).
//
// A direct port of wyhash final 4.3 (wyhash.h in github.com/wangyi-fudan/wyhash)
// with its defaults: WYHASH_CONDOM=1, WYHASH_32BIT_MUM=0, little-endian reads.
// Checked against upstream outputs in wyhash_test.go.
func Hash(b []byte, seed uint64) uint64 {
	n := len(b)
	var x, y uint64
	if n <= 16 {
		// Upstream reads four 4-byte words: a = r4(0)<<32 | r4(m),
		// b = r4(n-4)<<32 | r4(n-4-m), m = (n>>3)<<2 (0, 4, or 8 for n=16).
		// Equivalent, with fewer loads:
		if n == 16 {
			lo, hi := le64(b, 0), le64(b, 8)
			x = lo<<32 | hi&0xffffffff
			y = hi&^0xffffffff | lo>>32
		} else if n >= 8 {
			x = bits.RotateLeft64(le64(b, 0), 32)
			y = le64(b, n-8)
		} else if n >= 4 {
			x = le32(b, 0) * (1<<32 + 1)
			y = le32(b, n-4) * (1<<32 + 1)
		} else if n > 0 {
			x = uint64(b[0])<<16 | uint64(b[n>>1])<<8 | uint64(b[n-1])
		}
	} else {
		i := 0
		if n >= 48 {
			seed1, seed2 := seed, seed
			for ; n-i >= 48; i += 48 {
				seed = wymix(le64(b, i)^wyp1, le64(b, i+8)^seed)
				seed1 = wymix(le64(b, i+16)^wyp2, le64(b, i+24)^seed1)
				seed2 = wymix(le64(b, i+32)^wyp3, le64(b, i+40)^seed2)
			}
			seed ^= seed1 ^ seed2
		}
		for ; n-i > 16; i += 16 {
			seed = wymix(le64(b, i)^wyp1, le64(b, i+8)^seed)
		}
		// The last 16 bytes (may overlap bytes already mixed in).
		x = le64(b, n-16)
		y = le64(b, n-8)
	}
	hi, lo := bits.Mul64(x^wyp1, y^seed)
	return wymix(lo^wyp0^uint64(n), hi^wyp1)
}

// wymix multiplies a and b (64x64 -> 128 bits) and xors the halves.
func wymix(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	return hi ^ lo
}

func le32(b []byte, i int) uint64 {
	return uint64(binary.LittleEndian.Uint32(b[i:]))
}

func le64(b []byte, i int) uint64 {
	return binary.LittleEndian.Uint64(b[i:])
}
