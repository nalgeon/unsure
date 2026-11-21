package ull

import (
	"math"
	"math/bits"
	"slices"
	"testing"
)

// fuzzOp is an operation of FuzzSketch: add rank k to register i
// of sketch a or b.
type fuzzOp struct {
	i, k int
	toB  bool
}

// decodeFuzzOps returns the operations in data for precision p, 3 bytes
// each: the register (the first two bytes, modulo 2^p), and the rank
// (low 6 bits of the third byte, modulo 65-p, plus one) with the target
// sketch (high bit of the third byte).
func decodeFuzzOps(data []byte, p int) []fuzzOp {
	var ops []fuzzOp
	for len(data) >= 3 && len(ops) < 2000 {
		ops = append(ops, fuzzOp{
			i:   (int(data[0]) | int(data[1])<<8) & (1<<p - 1),
			k:   int(data[2]&63)%(65-p) + 1,
			toB: data[2]&0x80 != 0,
		})
		data = data[3:]
	}
	return ops
}

// fillRanks returns the ranks the fill byte adds to every register:
// none for 0, otherwise rank k = fill&63 (capped at 65-p), plus k-1
// if bit 6 is set and k-2 if bit 7 is set (when they are valid ranks).
func fillRanks(fill uint8, p int) []int {
	k := min(int(fill&63), 65-p)
	if k == 0 {
		return nil
	}
	ranks := []int{k}
	if fill&0x40 != 0 && k > 1 {
		ranks = append(ranks, k-1)
	}
	if fill&0x80 != 0 && k > 2 {
		ranks = append(ranks, k-2)
	}
	return ranks
}

// registerMask computes a register from scratch from the set of ranks
// it has seen, as a bitmask with bit k set for each rank k.
func registerMask(seen uint64) uint8 {
	if seen == 0 {
		return 0
	}
	u := bits.Len64(seen) - 1
	r := 4 * u
	if seen>>(u-1)&1 != 0 {
		r += 2
	}
	if u >= 2 && seen>>(u-2)&1 != 0 {
		r++
	}
	return uint8(r)
}

// FuzzSketch adds fuzzed ranks to fuzzed registers of two sketches
// a and b with a fuzzed precision, optionally after filling every
// register with up to three ranks (to reach the large-range and
// saturated estimators), and checks them against a model of the ranks
// each register has seen:
//   - Each register matches the model, and the histogram matches
//     the registers.
//   - The estimate is non-negative and finite (unless fully saturated),
//     0 only for an empty sketch, and Count is the estimate rounded.
//     The estimate can decrease as ranks are added, since a register
//     forgets ranks below u-2: ranks 1 and 2 (at least two items) give
//     a higher estimate than adding rank 7 on top (looks like one item).
//   - a.Merge(b) gives exactly the sketch that saw both a's and b's
//     ranks (in reverse order), and leaves b unchanged.
//   - Merging a sketch with a copy of itself changes nothing.
func FuzzSketch(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0, 1, 0, 0, 2}, uint8(0), uint8(0))
	// Same register in a and b, every history combination.
	f.Add([]byte{5, 0, 4, 5, 0, 0x83, 5, 0, 2, 6, 0, 0x85, 6, 0, 3, 7, 0, 0x81}, uint8(2), uint8(0))
	// Neighbors within and across 8-register blocks.
	f.Add([]byte{7, 0, 10, 8, 0, 0x8a, 7, 0, 0x89, 8, 0, 9, 15, 0, 1, 16, 0, 0x82}, uint8(1), uint8(0))
	// Full sketch: small-range estimator, then large-range.
	f.Add([]byte{1, 0, 5, 2, 0, 0x86}, uint8(0), uint8(2))
	f.Add([]byte{1, 0, 20, 2, 0, 0x93}, uint8(0), uint8(20))
	// Saturated with all history combinations (p=8: rank 57).
	f.Add([]byte{1, 0, 56, 2, 0, 0xb7, 3, 0, 0x37}, uint8(0), uint8(0xf9))
	f.Add([]byte{0xff, 0xff, 46, 0, 0x80, 0xae}, uint8(10), uint8(0x6f))

	f.Fuzz(func(t *testing.T, data []byte, mode uint8, fill uint8) {
		p := minP + int(mode)%(maxP-minP+1)
		m := 1 << p
		ops := decodeFuzzOps(data, p)

		// Model: the ranks each register has seen, for a, b and both.
		seenA := make([]uint64, m)
		seenB := make([]uint64, m)
		var a, b, both *Sketch
		for _, s := range []**Sketch{&a, &b, &both} {
			*s = NewPrecision(p)
		}
		filled := fillRanks(fill, p)
		for _, k := range filled {
			for i := range m {
				seenA[i] |= 1 << k
				seenB[i] |= 1 << k
				h := hashFor(i, p, k)
				a.addHash(h)
				b.addHash(h)
				both.addHash(h)
			}
		}
		empty := len(filled) == 0

		// both gets the ranks in reverse order, checking the estimate
		// after each one.
		checkEstimate(t, both, both.estimate(), empty)
		for _, op := range slices.Backward(ops) {
			both.addHash(hashFor(op.i, p, op.k))
			checkEstimate(t, both, both.estimate(), false)
		}
		for _, op := range ops {
			h := hashFor(op.i, p, op.k)
			if op.toB {
				seenB[op.i] |= 1 << op.k
				b.addHash(h)
			} else {
				seenA[op.i] |= 1 << op.k
				a.addHash(h)
			}
		}

		checkSketch(t, "a", a, seenA)
		checkSketch(t, "b", b, seenB)
		for i := range seenA {
			seenA[i] |= seenB[i]
		}
		checkSketch(t, "both", both, seenA)

		bRegs, bHist := slices.Clone(b.regs), b.hist
		if err := a.Merge(b); err != nil {
			t.Fatalf("Merge: %v", err)
		}
		if !slices.Equal(b.regs, bRegs) || b.hist != bHist {
			t.Fatal("Merge changed other")
		}
		checkSketch(t, "merged", a, seenA)
		if !slices.Equal(a.regs, both.regs) || a.hist != both.hist {
			t.Fatal("merged sketch differs from the one that saw both")
		}

		self := &Sketch{regs: slices.Clone(a.regs), hist: a.hist, p: a.p}
		if err := a.Merge(self); err != nil {
			t.Fatalf("Merge with self: %v", err)
		}
		if !slices.Equal(a.regs, both.regs) || a.hist != both.hist {
			t.Fatal("Merge with a copy of itself changed the sketch")
		}
	})
}

// checkSketch checks that the registers of s match the ranks they have
// seen, and that the histogram matches the registers.
func checkSketch(t *testing.T, name string, s *Sketch, seen []uint64) {
	t.Helper()
	for i, r := range s.regs {
		if want := registerMask(seen[i]); r != want {
			t.Fatalf("%s: register %d = %d, want %d (ranks %b)", name, i, r, want, seen[i])
		}
	}
	if s.hist != histogram(s) {
		t.Fatalf("%s: histogram doesn't match the registers", name)
	}
}

// checkEstimate checks that est (the estimate of s) is non-negative,
// 0 if s is empty and positive otherwise, and finite unless every
// register is saturated with both history bits set. Count must be est
// rounded, capped at MaxInt.
func checkEstimate(t *testing.T, s *Sketch, est float64, empty bool) {
	t.Helper()
	full := s.hist[4*(65-int(s.p))+3] == uint32(len(s.regs))
	if math.IsNaN(est) || est < 0 || math.IsInf(est, 0) != full {
		t.Fatalf("estimate = %v, fully saturated = %v", est, full)
	}
	if empty != (est == 0) {
		t.Fatalf("estimate = %v, empty = %v", est, empty)
	}
	want := math.MaxInt
	if est < math.MaxInt {
		want = int(math.Round(est))
	}
	if c := s.Count(); c != want {
		t.Fatalf("Count = %d, want %d (estimate %v)", c, want, est)
	}
}
