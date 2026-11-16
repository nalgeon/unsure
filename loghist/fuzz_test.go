package loghist

import (
	"encoding/binary"
	"math"
	"slices"
	"testing"
)

// fuzzErrs are the relative errors FuzzHistogram picks from (m = 3, 6, 7, 9, 13).
var fuzzErrs = []float64{0.1, 0.01, 0.004, 0.001, 0.0001}

// encodeFloats encodes values as the input of FuzzHistogram.
func encodeFloats(xs ...float64) []byte {
	b := make([]byte, 0, 8*len(xs))
	for _, x := range xs {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
	}
	return b
}

// FuzzHistogram adds values from raw float64 bits (including NaN, infinities
// and subnormals) to a histogram with a fuzzed error and range, and checks
// its estimates against the sorted values:
//   - Count is the number of finite values.
//   - Quantile(0) and Quantile(1) are the exact min and max, and all
//     estimates are monotonic and within [min, max].
//   - The estimate at every checked rank is within the relative error
//     of the true value if it's in [lo, hi), closer to zero than lo if
//     the true value is, and at or beyond hi on the same side otherwise.
//   - CDF is monotonic, within [0, 1], 0 below min and 1 above max.
//   - Memory stays within the octaves of [lo, hi).
//   - Merging two parts, and adding the values in sorted order,
//     give exactly the same estimates.
func FuzzHistogram(f *testing.F) {
	f.Add(encodeFloats(1, 2, 3, 1000, 1e6), uint8(2), 1e-9, 1e18, uint16(2))
	f.Add(encodeFloats(0, -1.5, 2.5, math.Copysign(0, -1), -1e-12, 7, 7, 7), uint8(1), 1e-9, 1e18, uint16(3))
	f.Add(encodeFloats(math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64/3, 1, 5e-324), uint8(0), 0.0, math.Inf(1), uint16(1))
	f.Add(encodeFloats(0.4, 0.5, 0.999, 1, 1.5, 2, 2.0001, -0.5, -2, math.NaN(), math.Inf(-1)), uint8(3), 0.5, 2.0, uint16(4))
	f.Add(encodeFloats(1e-9, 9.99e-10, 1e18, 9.99e17, -1e18, -1e-9), uint8(4), 1e-9, 1e18, uint16(0))
	// Buckets above 2^1023, where 2a overflows (m=7), including
	// the last one, which ends at +Inf.
	f.Add(encodeFloats(1, 0x1.fcp1023, math.MaxFloat64), uint8(2), 0.0, math.Inf(1), uint16(1))
	f.Add(encodeFloats(1, 0x1.fep1023, 0x1.ffp1023, math.MaxFloat64), uint8(2), 0.0, math.Inf(1), uint16(2))
	// Ranges of subnormals, which become empty.
	f.Add(encodeFloats(1, 1e300, -2, 1e-316, 5e-324, 0x1p-1022), uint8(2), 0.0, 1e-310, uint16(1))
	f.Add(encodeFloats(1e-316, 1e-318, 0x1p-1022, 0x1.0000000000001p-1022), uint8(4), 1e-320, 0x1p-1022*(1-0x1p-52), uint16(2))

	f.Fuzz(func(t *testing.T, data []byte, mode uint8, lo, hi float64, split uint16) {
		if !(lo >= 0 && lo < hi) {
			return
		}
		relErr := fuzzErrs[int(mode)%len(fuzzErrs)]
		h := NewRange(relErr, lo, hi)
		m := 52 - int(h.shift)

		// Octaves of [lo, hi) per sign. Skip ranges that could take
		// more than 2 MB per sign, so the fuzzer doesn't run out of memory.
		loOct := int(h.lo >> 52)
		hiOct := min(int((h.hi-1)>>52), 2046)
		octaves := hiOct - loOct + 1
		if octaves<<m > 1<<18 {
			return
		}

		var xs []float64
		for len(data) >= 8 && len(xs) < 4096 {
			x := math.Float64frombits(binary.LittleEndian.Uint64(data))
			data = data[8:]
			h.Add(x)
			if !math.IsNaN(x) && !math.IsInf(x, 0) {
				xs = append(xs, x)
			}
		}
		n := len(xs)
		if h.Count() != n {
			t.Fatalf("Count = %d, want %d", h.Count(), n)
		}
		if limit := 2 * octaves * 8 << m; h.Size() > limit {
			t.Fatalf("Size = %d > %d for %d octaves", h.Size(), limit, octaves)
		}
		if n == 0 {
			if !math.IsNaN(h.Quantile(0.5)) || !math.IsNaN(h.CDF(0)) {
				t.Fatalf("empty: Quantile, CDF = %v, %v, want NaN", h.Quantile(0.5), h.CDF(0))
			}
			return
		}
		sorted := slices.Clone(xs)
		slices.Sort(sorted)
		lo0, hi0 := sorted[0], sorted[n-1]
		if h.Quantile(0) != lo0 || h.Quantile(1) != hi0 {
			t.Fatalf("Quantile(0), Quantile(1) = %v, %v, want %v, %v", h.Quantile(0), h.Quantile(1), lo0, hi0)
		}

		// Quantiles at up to ~500 ranks, in ascending order.
		bound := maxError(m) * (1 + 1e-12)
		step := max(1, n/500)
		prev := math.Inf(-1)
		for k := 0; k < n; k += step {
			q := 1.0
			if n > 1 {
				q = float64(k) / float64(n-1)
			}
			est := h.Quantile(q)
			// The rank Quantile uses, computed the same way.
			k0 := int(uint64(float64(q * float64(n-1))))
			want := sorted[k0]
			if est < prev || est < lo0 || est > hi0 {
				t.Fatalf("Quantile(%v) = %v: not monotonic (prev %v) or outside [%v, %v]", q, est, prev, lo0, hi0)
			}
			prev = est
			wantAbs := math.Float64bits(want) &^ signBit
			estAbs := math.Float64bits(est) &^ signBit
			switch {
			case wantAbs < h.lo:
				if estAbs >= h.lo {
					t.Fatalf("Quantile(%v) = %v, want closer to zero than lo (true %v)", q, est, want)
				}
			case wantAbs >= h.hi:
				if estAbs < h.hi || math.Signbit(est) != math.Signbit(want) {
					t.Fatalf("Quantile(%v) = %v, want overflow on the same side (true %v)", q, est, want)
				}
			default:
				if err := relError(est, want); err > bound {
					t.Fatalf("Quantile(%v) = %v, want %v (error %.3e > %.3e)", q, est, want, err, bound)
				}
			}
		}

		// CDF at the values and just outside the range.
		if c := h.CDF(math.Nextafter(lo0, math.Inf(-1))); c != 0 {
			t.Fatalf("CDF below min = %v, want 0", c)
		}
		if c := h.CDF(math.Nextafter(hi0, math.Inf(1))); c != 1 {
			t.Fatalf("CDF above max = %v, want 1", c)
		}
		prevC := 0.0
		for i := 0; i < n; i += step {
			c := h.CDF(sorted[i])
			if !(c >= prevC && c <= 1) {
				t.Fatalf("CDF(%v) = %v: not monotonic (prev %v) or outside [0, 1]", sorted[i], c, prevC)
			}
			prevC = c
		}

		// Merging two parts and adding in sorted order give the same result.
		p := int(split) % (n + 1)
		a, b := NewRange(relErr, lo, hi), NewRange(relErr, lo, hi)
		fill(a, xs[:p])
		fill(b, xs[p:])
		if err := a.Merge(b); err != nil {
			t.Fatal(err)
		}
		same(t, h, a)
		s := NewRange(relErr, lo, hi)
		fill(s, sorted)
		same(t, h, s)
	})
}
