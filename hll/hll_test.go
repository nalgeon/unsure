package hll

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nalgeon/unsure/internal/wyhash"
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

func TestPrecision(t *testing.T) {
	tests := []struct {
		stdErr float64
		p      int
	}{
		{0.9, 4},
		{0.26, 4},
		{1.04 / 32, 10},
		{0.0325, 10},
		{0.0324, 11},
		{0.01, 14},
		{1.04 / 128, 14},
		{0.0081, 15},
		{1.04 / 512, 18},
		{0.002, 19},
	}
	for _, tt := range tests {
		if p := precision(tt.stdErr); p != tt.p {
			t.Errorf("precision(%g) = %d, want %d", tt.stdErr, p, tt.p)
		}
		if tt.p > maxP {
			continue
		}
		s := New(tt.stdErr)
		if int(s.p) != tt.p || len(s.regs) != 1<<tt.p || s.Size() != 1<<tt.p {
			t.Errorf("New(%g): p = %d, %d registers, size %d, want p = %d",
				tt.stdErr, s.p, len(s.regs), s.Size(), tt.p)
		}
	}
}

func TestNewPanics(t *testing.T) {
	tests := []struct {
		name   string
		stdErr float64
	}{
		{"0", 0},
		{"1", 1},
		{"negative", -0.01},
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"too small", 0.002},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%g) didn't panic", tt.stdErr)
				}
			}()
			New(tt.stdErr)
		})
	}
}

func TestEmpty(t *testing.T) {
	for p := minP; p <= maxP; p++ {
		s := New(1.04 / math.Sqrt(float64(int(1)<<p)))
		if c := s.Count(); c != 0 {
			t.Errorf("p=%d: empty Count = %d", p, c)
		}
	}
}

func TestSmall(t *testing.T) {
	// With 16K registers, the first ~100 items almost never collide,
	// so the estimate should be exact or off by one.
	s := New(0.01)
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
	s := New(0.01)
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
	s := New(0.01)
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
	s := New(1.04 / 128)
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
	s := New(0.26) // p=4
	for range 1000 {
		s.addHash(0) // rank 61 into register 0
	}
	for i := range s.regs {
		s.addHash(uint64(i) << 60)
	}
	if c := s.Count(); c != math.MaxUint64 {
		t.Fatalf("all registers saturated: Count = %d, want MaxUint64", c)
	}
}

func TestHistogram(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	a, b := New(0.05), New(0.05)
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
	a, b, all := New(0.01), New(0.01), New(0.01)
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

func TestMergeMismatch(t *testing.T) {
	a := New(0.01)
	if err := a.Merge(New(0.02)); err != ErrPrecisionMismatch {
		t.Errorf("different precisions: want ErrPrecisionMismatch, got %v", err)
	}
	if err := a.Merge(NewWith(0.01, Options{Seed: 1})); err != ErrSeedMismatch {
		t.Errorf("different seeds: want ErrSeedMismatch, got %v", err)
	}
}

func TestSeed(t *testing.T) {
	keys := makeKeys(1, 1000)
	a := NewWith(0.01, Options{Seed: 1})
	b := NewWith(0.01, Options{Seed: 1})
	c := NewWith(0.01, Options{Seed: 2})
	for _, k := range keys {
		a.Add(k)
		b.Add(k)
		c.Add(k)
	}
	if !slices.Equal(a.regs, b.regs) {
		t.Errorf("same seed, different registers")
	}
	if slices.Equal(a.regs, c.regs) {
		t.Errorf("different seeds, same registers")
	}
	if d := New(0.01); d.seed != wyhash.Seed(0) {
		t.Errorf("default seed = %#x, want wyhash.Seed(0)", d.seed)
	}
}

// TestAccuracy checks the bias and relative standard error of the estimate
// over many independent sketches, at cardinalities from tiny to far beyond
// the number of registers (including the 2.5m–5m range where the original
// HyperLogLog switches estimators). Random hashes stand in for items.
func TestAccuracy(t *testing.T) {
	trials := 200
	if testing.Short() {
		trials = 50
	}
	for _, p := range []int{6, 10, 14} {
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
			s := New(stdErr)
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
	for _, stdErr := range []float64{0.0325, 0.0082, 0.0021} {
		s := New(stdErr)
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
	s := New(0.01)
	other := New(0.01)
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
