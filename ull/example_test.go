package ull_test

import (
	"fmt"

	"github.com/nalgeon/unsure/ull"
)

func Example() {
	// Standard error about 0.9% of the true count, in 9 KB.
	sketch := ull.New()

	sketch.Add([]byte("alice"))
	sketch.Add([]byte("bob"))
	sketch.Add([]byte("alice"))
	sketch.Add([]byte("carol"))

	fmt.Println(sketch.Count())
	// Output:
	// 3
}

func ExampleNewPrecision() {
	// 2^10 registers: 1 KB, estimates within about 2.4% of the true count.
	sketch := ull.NewPrecision(10)

	sketch.Add([]byte("alice"))
	sketch.Add([]byte("bob"))
	fmt.Println(sketch.Count(), sketch.Size())
	// Output:
	// 2 2048
}

func ExampleSketch_Merge() {
	// Visitors to two pages, counted separately.
	home := ull.New()
	home.Add([]byte("alice"))
	home.Add([]byte("bob"))

	docs := ull.New()
	docs.Add([]byte("bob"))
	docs.Add([]byte("carol"))

	// Unique visitors to either page.
	if err := home.Merge(docs); err != nil {
		panic(err)
	}
	fmt.Println(home.Count())
	// Output:
	// 3
}

func ExampleSketch_Size() {
	sketch := ull.New()
	fmt.Println(sketch.Size(), "bytes") // 8 KB of registers + 1 KB of histogram
	// Output:
	// 9216 bytes
}
