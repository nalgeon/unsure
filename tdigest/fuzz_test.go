package tdigest

import (
	"encoding/binary"
	"math"
	"slices"
	"testing"
)

// fuzzDeltas are the compressions FuzzDigest picks from. Small ones,
// so that a few thousand values go through many flushes and merges.
var fuzzDeltas = []int{10, 20, 50, 100}

// encodeFloats encodes values as the input of FuzzDigest (with mode&1 == 0).
func encodeFloats(xs ...float64) []byte {
	b := make([]byte, 0, 8*len(xs))
	for _, x := range xs {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
	}
	return b
}

// decodeValues returns the values in data: raw float64 bits (including NaN,
// infinities, subnormals and values beyond ±maxValue) if mode&1 == 0, and one small integer
// per byte otherwise, which gives dense data with many duplicates.
func decodeValues(data []byte, mode uint8) []float64 {
	var xs []float64
	if mode&1 == 0 {
		for len(data) >= 8 && len(xs) < 4096 {
			xs = append(xs, math.Float64frombits(binary.LittleEndian.Uint64(data)))
			data = data[8:]
		}
		return xs
	}
	for _, b := range data[:min(len(data), 4096)] {
		xs = append(xs, float64(int8(b)))
	}
	return xs
}

// FuzzDigest adds fuzzed values to a digest with a fuzzed compression,
// and checks its estimates against the sorted values:
//   - Count is the number of values within ±maxValue, and the centroids
//     are valid (see check).
//   - Quantile(0) and Quantile(1) are the exact min and max, and all
//     estimates are finite, monotonic and within [min, max].
//   - While all centroids have weight 1, the estimates are exact.
//   - CDF is monotonic, within [0, 1], 0 below min and 1 above max.
//   - Merging two parts with different compressions gives a digest
//     with the same properties.
func FuzzDigest(f *testing.F) {
	f.Add(encodeFloats(1, 2, 3, 1000, 1e6), uint8(0), uint16(2))
	f.Add(encodeFloats(0, -1.5, 2.5, math.Copysign(0, -1), -1e-12, 7, 7, 7, math.NaN(), math.Inf(1)), uint8(2), uint16(3))
	f.Add(encodeFloats(math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64/3, 1, 5e-324), uint8(0), uint16(1))
	f.Add([]byte("the quick brown fox jumps over the lazy dog, again and again and again and again"), uint8(1), uint16(30))
	f.Add(slices.Repeat([]byte{0, 1, 2, 3, 250, 128, 127}, 300), uint8(1), uint16(1000))
	// The largest values of both signs, with the largest spread and sums.
	f.Add(slices.Repeat(encodeFloats(maxValue, -maxValue, math.Nextafter(maxValue, 2*maxValue), 1, -1, 5e-324), 50), uint8(0), uint16(100))

	f.Fuzz(func(t *testing.T, data []byte, mode uint8, split uint16) {
		delta := fuzzDeltas[int(mode>>1)%len(fuzzDeltas)]
		var xs []float64
		d := NewCompression(delta)
		for _, x := range decodeValues(data, mode) {
			d.Add(x)
			if math.Abs(x) <= maxValue {
				xs = append(xs, x)
			}
		}
		sorted := slices.Clone(xs)
		slices.Sort(sorted)
		checkFuzz(t, d, delta, sorted)

		// Merge two parts, the second one with another compression.
		p := int(split) % (len(xs) + 1)
		a := NewCompression(delta)
		b := NewCompression(fuzzDeltas[int(mode>>3)%len(fuzzDeltas)])
		fill(a, xs[:p])
		fill(b, xs[p:])
		a.Merge(b)
		checkFuzz(t, a, delta, sorted)
	})
}

// checkFuzz checks the invariants of FuzzDigest for d with compression delta
// and the sorted values added to it (within ±maxValue).
func checkFuzz(t *testing.T, d *Digest, delta int, sorted []float64) {
	t.Helper()
	n := len(sorted)
	if d.Count() != n {
		t.Fatalf("Count = %d, want %d", d.Count(), n)
	}
	check(t, d, delta)
	if n == 0 {
		if !math.IsNaN(d.Quantile(0.5)) || !math.IsNaN(d.CDF(0)) {
			t.Fatalf("empty: Quantile, CDF = %v, %v, want NaN", d.Quantile(0.5), d.CDF(0))
		}
		return
	}
	lo, hi := sorted[0], sorted[n-1]
	if d.Quantile(0) != lo || d.Quantile(1) != hi {
		t.Fatalf("Quantile(0), Quantile(1) = %v, %v, want %v, %v", d.Quantile(0), d.Quantile(1), lo, hi)
	}
	exact := true
	for _, c := range d.centroids {
		exact = exact && c.weight == 1
	}

	// Quantiles at up to ~1000 ranks, in ascending order.
	steps := min(1000, 4*n)
	prev := math.Inf(-1)
	for i := 0; i <= steps; i++ {
		q := float64(i) / float64(steps)
		v := d.Quantile(q)
		if !(v >= prev && v >= lo && v <= hi) {
			t.Fatalf("Quantile(%v) = %v: not monotonic (prev %v) or outside [%v, %v]", q, v, prev, lo, hi)
		}
		prev = v
		if want := sorted[min(int(q*float64(n)), n-1)]; exact && v != want {
			t.Fatalf("Quantile(%v) = %v, want exactly %v", q, v, want)
		}
	}

	// CDF at the values and just outside the range.
	if c := d.CDF(math.Nextafter(lo, math.Inf(-1))); c != 0 {
		t.Fatalf("CDF below min = %v, want 0", c)
	}
	if c := d.CDF(math.Nextafter(hi, math.Inf(1))); c != 1 {
		t.Fatalf("CDF above max = %v, want 1", c)
	}
	step := max(1, n/1000)
	prevC := 0.0
	for i := 0; i < n; i += step {
		x := sorted[i]
		c := d.CDF(x)
		if !(c >= prevC && c <= 1) {
			t.Fatalf("CDF(%v) = %v: not monotonic (prev %v) or outside [0, 1]", x, c, prevC)
		}
		prevC = c
		if want := rank(sorted, x); exact && math.Abs(c-want) > 1e-12 {
			t.Fatalf("CDF(%v) = %v, want exactly %v", x, c, want)
		}
	}
}
