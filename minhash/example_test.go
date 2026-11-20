package minhash_test

import (
	"fmt"
	"strings"

	"github.com/nalgeon/unsure/minhash"
)

// shingles returns the sketch of a text's word 3-grams.
func shingles(text string) *minhash.Sketch {
	words := strings.Fields(text)
	sketch := minhash.New(128)
	for i := 0; i+3 <= len(words); i++ {
		sketch.Add([]byte(strings.Join(words[i:i+3], " ")))
	}
	return sketch
}

func Example() {
	a := shingles("the quick brown fox jumps over the lazy dog and runs away into the dark forest")
	b := shingles("the quick brown fox jumps over the lazy cat and runs away into the dark forest")
	c := shingles("a completely different sentence that shares no three word phrases with the others")

	sigA, sigB, sigC := a.Signature(), b.Signature(), c.Signature()

	// 11 of 17 distinct 3-grams are shared: J ≈ 0.65.
	fmt.Printf("a~b: %.1f\n", sigA.Similarity(sigB))
	fmt.Printf("a~c: %.1f\n", sigA.Similarity(sigC))
	// Output:
	// a~b: 0.6
	// a~c: 0.0
}

func ExampleSketch_Merge() {
	// Items seen by two machines, sketched separately.
	a := minhash.New(256)
	b := minhash.New(256)
	for i := range 1000 {
		a.Add(fmt.Appendf(nil, "item-%d", i))
		b.Add(fmt.Appendf(nil, "item-%d", 1000+i))
	}

	// The union of both: items 0..1999.
	if err := a.Merge(b); err != nil {
		panic(err)
	}
	all := minhash.New(256)
	for i := range 2000 {
		all.Add(fmt.Appendf(nil, "item-%d", i))
	}
	fmt.Println(a.Signature().Similarity(all.Signature()))
	// Output:
	// 1
}

func ExampleSketch_Size() {
	sketch := minhash.New(256)
	fmt.Println(sketch.Size(), "bytes")
	// Output:
	// 1024 bytes
}
