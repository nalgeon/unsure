package hll

import (
	"encoding/binary"
	"fmt"
	"math"
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

// histogram recomputes the register histogram from scratch.
func histogram(s *Sketch) [64]uint32 {
	var h [64]uint32
	for _, r := range s.regs {
		h[r]++
	}
	return h
}

func TestNew(t *testing.T) {
	s := New()
	if s.p != defaultP || len(s.regs) != 1<<defaultP {
		t.Errorf("New: p = %d, %d registers, want p = %d", s.p, len(s.regs), defaultP)
	}
	for p := minP; p <= maxP; p++ {
		s := NewPrecision(p)
		if int(s.p) != p || len(s.regs) != 1<<p || s.Size() != 1<<p+256 || s.hist[0] != 1<<p {
			t.Errorf("NewPrecision(%d): p = %d, %d registers, size %d, hist[0] = %d",
				p, s.p, len(s.regs), s.Size(), s.hist[0])
		}
	}
}

func TestNewPrecisionPanics(t *testing.T) {
	for _, p := range []int{-1, 0, minP - 1, maxP + 1, 64} {
		t.Run(fmt.Sprint(p), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewPrecision(%d) didn't panic", p)
				}
			}()
			NewPrecision(p)
		})
	}
}

func TestEmpty(t *testing.T) {
	for p := minP; p <= maxP; p++ {
		s := NewPrecision(p)
		if c := s.Count(); c != 0 {
			t.Errorf("p=%d: empty Count = %d", p, c)
		}
	}
}

func TestSmall(t *testing.T) {
	// With 16K registers, the first ~100 items almost never collide,
	// so the estimate should be exact or off by one.
	s := New()
	for i, k := range makeKeys(1, 100) {
		s.Add(k)
		n := uint64(i + 1)
		if c := s.Count(); c+1 < n || c > n+1 {
			t.Fatalf("after %d items: Count = %d", n, c)
		}
	}
}

func TestDuplicates(t *testing.T) {
	keys := makeKeys(1, 10_000)
	s := New()
	for _, k := range keys {
		s.Add(k)
	}
	regs := slices.Clone(s.regs)
	c := s.Count()
	for range 3 {
		for _, k := range keys {
			s.Add(k)
		}
	}
	if !slices.Equal(s.regs, regs) || s.Count() != c {
		t.Fatalf("adding duplicates changed the sketch")
	}
}

func TestEdgeKeys(t *testing.T) {
	keys := [][]byte{
		nil,
		{0},
		{0, 0},
		[]byte("a"),
		[]byte("hello, world"),
		make([]byte, 1<<16),
	}
	s := New()
	for _, k := range keys {
		s.Add(k)
	}
	if c := s.Count(); c != uint64(len(keys)) {
		t.Fatalf("Count = %d, want %d", c, len(keys))
	}
	// nil and empty are the same item.
	s.Add([]byte{})
	if c := s.Count(); c != uint64(len(keys)) {
		t.Fatalf("after adding empty key: Count = %d, want %d", c, len(keys))
	}
}

func TestRank(t *testing.T) {
	const p = 14
	s := NewPrecision(p)
	tests := []struct {
		h    uint64
		i    int
		rank uint8
	}{
		{0, 0, 64 - p + 1}, // all zeros: max rank
		{1, 0, 64 - p},     // only the last bit
		{1 << 49, 0, 1},    // first bit after the index
		{1 << 48, 0, 2},    // second bit
		{^uint64(0), 1<<p - 1, 1},
		{0xabcd << 48, 0xabcd >> 2, 2}, // index 0x2af3, next bits 01
	}
	for _, tt := range tests {
		clear(s.regs)
		s.addHash(tt.h)
		if r := s.regs[tt.i]; r != tt.rank {
			t.Errorf("hash %#x: regs[%d] = %d, want %d", tt.h, tt.i, r, tt.rank)
		}
	}
}

func TestSaturated(t *testing.T) {
	s := NewPrecision(minP)
	for range 1000 {
		s.addHash(0) // max rank 65-p into register 0
	}
	for i := range s.regs {
		s.addHash(uint64(i) << (64 - s.p))
	}
	if c := s.Count(); c != math.MaxUint64 {
		t.Fatalf("all registers saturated: Count = %d, want MaxUint64", c)
	}
}

func TestHistogram(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	a, b := NewPrecision(9), NewPrecision(9)
	for range 5000 {
		a.addHash(r.Uint64())
	}
	for range 20000 {
		b.addHash(r.Uint64())
	}
	if a.hist != histogram(a) {
		t.Fatalf("after Add: hist = %v, want %v", a.hist, histogram(a))
	}
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	if a.hist != histogram(a) {
		t.Fatalf("after Merge: hist = %v, want %v", a.hist, histogram(a))
	}
}

func TestMerge(t *testing.T) {
	keysA := makeKeys(1, 30_000)
	keysB := makeKeys(2, 50_000)
	a, b, all := New(), New(), New()
	for _, k := range keysA {
		a.Add(k)
		all.Add(k)
	}
	for _, k := range keysB {
		b.Add(k)
		all.Add(k)
	}
	// Overlap: b also gets some of a's keys.
	for _, k := range keysA[:10_000] {
		b.Add(k)
	}
	bRegs := slices.Clone(b.regs)
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.regs, all.regs) || a.hist != all.hist {
		t.Fatalf("merged sketch differs from a sketch of the union")
	}
	if !slices.Equal(b.regs, bRegs) {
		t.Fatalf("Merge changed other")
	}
	// Merging with itself is a no-op.
	if err := a.Merge(a); err != nil || !slices.Equal(a.regs, all.regs) {
		t.Fatalf("self-merge changed the sketch (err %v)", err)
	}
	t.Logf("union of 30000+50000 (10000 shared): Count = %d", a.Count())
}

// TestMergeRegisters checks the 8-at-a-time Merge against a plain per-byte
// max on random registers, including 0 and the maximum value 65-p.
func TestMergeRegisters(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, p := range []int{minP, 10, maxP} {
		maxR := 65 - p
		// fill sets registers to random values, keeping the histogram
		// consistent. Each source picks a value for register i.
		fill := func(s *Sketch, val func(i int) uint8) {
			for i := range s.regs {
				s.regs[i] = val(i)
			}
			s.hist = histogram(s)
		}
		randVal := func(int) uint8 {
			switch r.IntN(4) {
			case 0:
				return 0
			case 1:
				return uint8(maxR)
			default:
				return uint8(r.IntN(maxR + 1))
			}
		}
		cases := []struct {
			name string
			a, b func(i int) uint8
		}{
			{"random", randVal, randVal},
			{"empty other", randVal, func(int) uint8 { return 0 }},
			{"empty s", func(int) uint8 { return 0 }, randVal},
			{"all max", func(int) uint8 { return 0 }, func(int) uint8 { return uint8(maxR) }},
			{"alternating", func(i int) uint8 { return uint8(i % 2 * maxR) }, func(i int) uint8 { return uint8((i + 1) % 2 * maxR) }},
		}
		for _, c := range cases {
			a, b := NewPrecision(p), NewPrecision(p)
			fill(a, c.a)
			fill(b, c.b)
			want := slices.Clone(a.regs)
			for i, v := range b.regs {
				want[i] = max(want[i], v)
			}
			bRegs := slices.Clone(b.regs)
			if err := a.Merge(b); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(a.regs, want) {
				t.Errorf("p=%d %s: registers differ from per-byte max", p, c.name)
			}
			if a.hist != histogram(a) {
				t.Errorf("p=%d %s: hist = %v, want %v", p, c.name, a.hist, histogram(a))
			}
			if !slices.Equal(b.regs, bRegs) {
				t.Errorf("p=%d %s: Merge changed other", p, c.name)
			}
			// Merging the same sketch again changes nothing.
			regs, hist := slices.Clone(a.regs), a.hist
			if err := a.Merge(b); err != nil || !slices.Equal(a.regs, regs) || a.hist != hist {
				t.Errorf("p=%d %s: second merge changed the sketch (err %v)", p, c.name, err)
			}
		}
	}
}

func TestMergeMismatch(t *testing.T) {
	a := New()
	if err := a.Merge(NewPrecision(defaultP + 1)); err != ErrPrecisionMismatch {
		t.Errorf("different precisions: want ErrPrecisionMismatch, got %v", err)
	}
}

// TestAccuracy checks the bias and relative standard error of the estimate
// over many independent sketches, at cardinalities from tiny to far beyond
// the number of registers (including the 2.5m–5m range where the original
// HyperLogLog switches estimators). Random hashes stand in for items.
func TestAccuracy(t *testing.T) {
	const trials = 200
	for _, p := range []int{minP, 10, 14, maxP} {
		m := 1 << p
		stdErr := 1.04 / math.Sqrt(float64(m))
		var points []int
		for n := 1; n <= 1_000_000; n = int(float64(n)*1.5) + 1 {
			points = append(points, n)
		}
		sum := make([]float64, len(points))  // Σ relative error
		sum2 := make([]float64, len(points)) // Σ relative error²
		for trial := range trials {
			r := rand.New(rand.NewPCG(uint64(p), uint64(trial)))
			s := NewPrecision(p)
			n := 0
			for j, pt := range points {
				for ; n < pt; n++ {
					s.addHash(r.Uint64())
				}
				e := float64(s.Count())/float64(n) - 1
				sum[j] += e
				sum2[j] += e * e
			}
		}
		t.Run(fmt.Sprintf("p=%d", p), func(t *testing.T) {
			worst := 0.0
			for j, n := range points {
				bias := sum[j] / float64(trials)
				rmse := math.Sqrt(sum2[j] / float64(trials))
				worst = max(worst, rmse/stdErr)
				// Allowed: the noise of the mean over trials (rmse/√trials),
				// a residual bias of stdErr/10, and rounding to an integer.
				if math.Abs(bias) > 4*rmse/math.Sqrt(float64(trials))+stdErr/10+0.5/float64(n) {
					t.Errorf("n=%d (%.2fm): bias %+.4f, rmse %.4f", n, float64(n)/float64(m), bias, rmse)
				}
				// The RMSE estimate from 200 trials is within ±15% at 3σ.
				// It's well below stdErr at small n, where collisions are rare.
				if rmse > stdErr*1.2 {
					t.Errorf("n=%d (%.2fm): rmse %.4f, want <= %.4f", n, float64(n)/float64(m), rmse, stdErr)
				}
			}
			t.Logf("p=%d: worst rmse = %.2f × stdErr (%.4f)", p, worst, stdErr)
		})
	}
}

// TestAccuracyKeys checks the estimate with real keys and wyhash, to catch
// problems in how the hash bits are split into index and rank.
func TestAccuracyKeys(t *testing.T) {
	const n = 1_000_000
	keys := makeKeys(1, n)
	for _, p := range []int{10, 14, 18} {
		stdErr := 1.04 / math.Sqrt(float64(int(1)<<p))
		s := NewPrecision(p)
		for _, k := range keys {
			s.Add(k)
		}
		c := s.Count()
		e := float64(c)/n - 1
		t.Logf("p=%d: n=%d, Count = %d (%+.4f)", s.p, n, c, e)
		if math.Abs(e) > 4*stdErr {
			t.Errorf("p=%d: Count = %d, off by %+.4f, more than 4 × stdErr (%.4f)", s.p, c, e, stdErr)
		}
	}
}

func TestNoAllocs(t *testing.T) {
	s := New()
	other := New()
	key := []byte("alice")
	if n := testing.AllocsPerRun(100, func() { s.Add(key) }); n != 0 {
		t.Errorf("Add allocates %v times, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() { s.Count() }); n != 0 {
		t.Errorf("Count allocates %v times, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() { _ = s.Merge(other) }); n != 0 {
		t.Errorf("Merge allocates %v times, want 0", n)
	}
}
