package ull

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
func histogram(s *Sketch) [256]uint32 {
	var h [256]uint32
	for _, r := range s.regs {
		h[r]++
	}
	return h
}

// stdErr is the theoretical relative standard error for 2^p registers.
func stdErr(p int) float64 {
	return math.Sqrt(v / float64(int(1)<<p))
}

// register computes a register from scratch from the set of ranks
// it has seen, as defined in the paper.
func register(seen map[int]bool) uint8 {
	u := 0
	for k := range seen {
		u = max(u, k)
	}
	if u == 0 {
		return 0
	}
	r := 4 * u
	if seen[u-1] {
		r += 2
	}
	if seen[u-2] {
		r++
	}
	return uint8(r)
}

// hashFor returns a hash that adds rank k (in [1, 65-p])
// to register i of a sketch with precision p.
func hashFor(i, p, k int) uint64 {
	h := uint64(i) << (64 - p)
	if k < 65-p {
		h |= 1 << (64 - p - k)
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
		if int(s.p) != p || len(s.regs) != 1<<p || s.Size() != 1<<p+1024 || s.hist[0] != 1<<p {
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
	// With 8K registers, the first ~100 items rarely collide,
	// so the estimate should be exact or off by one.
	s := New()
	for i, k := range makeKeys(1, 100) {
		s.Add(k)
		n := i + 1
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
	if c := s.Count(); c != len(keys) {
		t.Fatalf("Count = %d, want %d", c, len(keys))
	}
	// nil and empty are the same item.
	s.Add([]byte{})
	if c := s.Count(); c != len(keys) {
		t.Fatalf("after adding empty key: Count = %d, want %d", c, len(keys))
	}
}

func TestRank(t *testing.T) {
	const p = 14
	s := NewPrecision(p)
	tests := []struct {
		h uint64
		i int
		k int
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
		if r := s.regs[tt.i]; r != uint8(4*tt.k) {
			t.Errorf("hash %#x: regs[%d] = %d, want %d", tt.h, tt.i, r, 4*tt.k)
		}
	}
}

// TestRegister adds random sequences of ranks to a single register
// and checks it against the definition after each one.
func TestRegister(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, p := range []int{minP, 14, maxP} {
		s := NewPrecision(p)
		w := 65 - p
		for range 2000 {
			clear(s.regs)
			s.hist = histogram(s)
			seen := map[int]bool{}
			for range 1 + r.IntN(8) {
				// Mostly ranks close to each other, sometimes the max.
				k := 1 + r.IntN(6)
				if r.IntN(4) == 0 {
					k = w - r.IntN(4)
				}
				seen[k] = true
				s.addHash(hashFor(0, p, k))
				if got, want := s.regs[0], register(seen); got != want {
					t.Fatalf("p=%d, seen %v: register = %d, want %d", p, seen, got, want)
				}
			}
			if s.hist != histogram(s) {
				t.Fatalf("p=%d: hist differs from registers", p)
			}
		}
	}
}

// TestPackUnpack checks that every valid register survives unpack and pack.
func TestPackUnpack(t *testing.T) {
	for u := 1; u <= 65-minP; u++ {
		for j := range 4 {
			if u == 1 && j != 0 || u == 2 && j&1 != 0 {
				continue // ranks below 1 don't exist
			}
			r := uint8(4*u + j)
			x := unpack(r)
			if x&1 == 0 || x>>(u+1) != 0 {
				t.Errorf("unpack(%d) = %b: want bit 0 and bits in [1, %d]", r, x, u)
			}
			if got := pack(x); got != r {
				t.Errorf("pack(unpack(%d)) = %d", r, got)
			}
		}
	}
	if unpack(0) != 1 || pack(1) != 0 {
		t.Errorf("unpack(0) = %b, pack(1) = %d, want 1 and 0", unpack(0), pack(1))
	}
}

func TestSaturated(t *testing.T) {
	s := NewPrecision(minP)
	for i := range s.regs {
		// All ranks from 1 to 65-p into each register.
		for k := 1; k <= 65-minP; k++ {
			s.addHash(hashFor(i, minP, k))
		}
	}
	if r := s.regs[0]; r != 4*(65-minP)+3 {
		t.Fatalf("register = %d, want %d", r, 4*(65-minP)+3)
	}
	if c := s.Count(); c != math.MaxInt {
		t.Fatalf("all registers saturated: Count = %d, want MaxInt", c)
	}
}

// TestNearlySaturated checks the large range terms with sketches where many
// registers reached the max rank and the rest are high.
func TestNearlySaturated(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	const p = 10
	w := 65 - p
	for range 100 {
		s := NewPrecision(p)
		for i := range s.regs {
			for range 3 {
				s.addHash(hashFor(i, p, w-r.IntN(5)))
			}
		}
		// About 2^w·m items: the estimate must be finite, positive and huge.
		c := s.Count()
		if c <= 1<<(w+p-4) {
			t.Fatalf("nearly saturated: Count = %d", c)
		}
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

// TestMergeRegisters merges random registers and checks the result against
// the definition applied to the union of the ranks.
func TestMergeRegisters(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, p := range []int{minP, 10, maxP} {
		w := 65 - p
		// fill adds random ranks to each register, close to
		// a per-register base so the history bits matter.
		fill := func(s *Sketch, seen []map[int]bool) {
			for i := range s.regs {
				base := 1 + r.IntN(w)
				for range r.IntN(4) {
					k := min(w, max(1, base+r.IntN(5)-2))
					seen[i][k] = true
					s.addHash(hashFor(i, p, k))
				}
			}
		}
		newSeen := func(m int) []map[int]bool {
			seen := make([]map[int]bool, m)
			for i := range seen {
				seen[i] = map[int]bool{}
			}
			return seen
		}
		m := 1 << p
		a, b := NewPrecision(p), NewPrecision(p)
		seenA, seenB := newSeen(m), newSeen(m)
		fill(a, seenA)
		fill(b, seenB)
		bRegs := slices.Clone(b.regs)
		if err := a.Merge(b); err != nil {
			t.Fatal(err)
		}
		for i := range a.regs {
			for k := range seenB[i] {
				seenA[i][k] = true
			}
			if want := register(seenA[i]); a.regs[i] != want {
				t.Fatalf("p=%d: register %d = %d, want %d", p, i, a.regs[i], want)
			}
		}
		if a.hist != histogram(a) {
			t.Errorf("p=%d: hist differs from registers", p)
		}
		if !slices.Equal(b.regs, bRegs) {
			t.Errorf("p=%d: Merge changed other", p)
		}
		// Merging the same sketch again changes nothing.
		regs := slices.Clone(a.regs)
		if err := a.Merge(b); err != nil || !slices.Equal(a.regs, regs) {
			t.Errorf("p=%d: second merge changed the sketch (err %v)", p, err)
		}
	}
}

// TestMergeAllPairs merges every pair of valid registers, each pair at
// every position in an 8-byte word.
func TestMergeAllPairs(t *testing.T) {
	const p = minP
	var valid []uint8
	for r := range 4*(65-p) + 4 {
		if pack(unpack(uint8(r))) == uint8(r) {
			valid = append(valid, uint8(r))
		}
	}
	a, b := NewPrecision(p), NewPrecision(p)
	for _, ra := range valid {
		for off := range 8 {
			for j := range a.regs {
				a.regs[j] = ra
				b.regs[j] = valid[(j+off)%len(valid)]
			}
			a.hist, b.hist = histogram(a), histogram(b)
			if err := a.Merge(b); err != nil {
				t.Fatal(err)
			}
			for j, rb := range b.regs {
				if want := pack(unpack(ra) | unpack(rb)); a.regs[j] != want {
					t.Fatalf("merge(%d, %d) = %d, want %d", ra, rb, a.regs[j], want)
				}
			}
			if a.hist != histogram(a) {
				t.Fatalf("a=%d off=%d: hist differs from registers", ra, off)
			}
		}
	}
}

func TestMergeMismatch(t *testing.T) {
	a := New()
	if err := a.Merge(NewPrecision(defaultP + 1)); err != ErrMismatch {
		t.Errorf("different precisions: want ErrMismatch, got %v", err)
	}
}

// TestAccuracy checks the bias and relative standard error of the estimate
// over many independent sketches, at cardinalities from tiny to far beyond
// the number of registers. Random hashes stand in for items.
func TestAccuracy(t *testing.T) {
	const trials = 200
	for _, p := range []int{minP, 10, 14, maxP} {
		m := 1 << p
		se := stdErr(p)
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
				// Unrounded: at tiny n, rounding to an integer adds a bias
				// of up to 0.5/n and makes the error discrete, so 200 trials
				// can't measure the RMSE there.
				e := s.estimate()/float64(n) - 1
				sum[j] += e
				sum2[j] += e * e
			}
		}
		t.Run(fmt.Sprintf("p=%d", p), func(t *testing.T) {
			worst := 0.0
			for j, n := range points {
				bias := sum[j] / float64(trials)
				rmse := math.Sqrt(sum2[j] / float64(trials))
				worst = max(worst, rmse/se)
				// Allowed: the noise of the mean over trials (rmse/√trials)
				// and a residual bias of stdErr/10.
				if math.Abs(bias) > 4*rmse/math.Sqrt(float64(trials))+se/10 {
					t.Errorf("n=%d (%.2fm): bias %+.4f, rmse %.4f", n, float64(n)/float64(m), bias, rmse)
				}
				// The RMSE estimate from 200 trials is within ±15% at 3σ.
				// It's well below stdErr at small n, where collisions are rare,
				// but there a trial with a collision is off by about 1/n, and
				// a few such trials dominate the RMSE (at p=14, n=17: 0.83%
				// in 200 trials, 0.49% in 20k). Allow for 4 of them.
				if limit := se*1.2 + 2/(float64(n)*math.Sqrt(trials)); rmse > limit {
					t.Errorf("n=%d (%.2fm): rmse %.4f, want <= %.4f", n, float64(n)/float64(m), rmse, limit)
				}
			}
			t.Logf("p=%d: worst rmse = %.2f × stdErr (%.4f)", p, worst, se)
		})
	}
}

// TestAccuracyKeys checks the estimate with real keys and wyhash, to catch
// problems in how the hash bits are split into index and rank.
func TestAccuracyKeys(t *testing.T) {
	const n = 1_000_000
	keys := makeKeys(1, n)
	for _, p := range []int{10, 14, 18} {
		se := stdErr(p)
		s := NewPrecision(p)
		for _, k := range keys {
			s.Add(k)
		}
		c := s.Count()
		e := float64(c)/n - 1
		t.Logf("p=%d: n=%d, Count = %d (%+.4f)", s.p, n, c, e)
		if math.Abs(e) > 4*se {
			t.Errorf("p=%d: Count = %d, off by %+.4f, more than 4 × stdErr (%.4f)", s.p, c, e, se)
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
