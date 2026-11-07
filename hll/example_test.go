package hll_test

import (
	"fmt"

	"github.com/nalgeon/unsure/hll"
)

func Example() {
	// Estimates within about 0.8% of the true count.
	sketch := hll.New()

	sketch.Add([]byte("alice"))
	sketch.Add([]byte("bob"))
	sketch.Add([]byte("alice"))
	sketch.Add([]byte("carol"))

	fmt.Println(sketch.Count())
	// Output:
	// 3
}

func ExampleNewPrecision() {
	// 2^10 registers: 1 KB, estimates within about 3% of the true count.
	sketch := hll.NewPrecision(10)

	sketch.Add([]byte("alice"))
	sketch.Add([]byte("bob"))
	fmt.Println(sketch.Count(), sketch.Size())
	// Output:
	// 2 1280
}

func ExampleSketch_Merge() {
	// Visitors to two pages, counted separately.
	home := hll.New()
	home.Add([]byte("alice"))
	home.Add([]byte("bob"))

	docs := hll.New()
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
	sketch := hll.New()
	fmt.Println(sketch.Size(), "bytes") // 16 KB of registers + 256 B of histogram
	// Output:
	// 16640 bytes
}
