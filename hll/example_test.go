package hll_test

import (
	"fmt"

	"github.com/nalgeon/unsure/hll"
)

func Example() {
	// Estimates within about 1% of the true count.
	sketch := hll.New(0.01)

	sketch.Add([]byte("alice"))
	sketch.Add([]byte("bob"))
	sketch.Add([]byte("alice"))
	sketch.Add([]byte("carol"))

	fmt.Println(sketch.Count())
	// Output:
	// 3
}

func ExampleNewWith() {
	// Sketches with the same seed hash items the same way.
	opts := hll.Options{Seed: 42}
	sketch := hll.NewWith(0.01, opts)

	sketch.Add([]byte("alice"))
	fmt.Println(sketch.Count())
	// Output:
	// 1
}

func ExampleSketch_Merge() {
	// Visitors to two pages, counted separately.
	home := hll.New(0.01)
	home.Add([]byte("alice"))
	home.Add([]byte("bob"))

	docs := hll.New(0.01)
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
	sketch := hll.New(0.01)
	fmt.Printf("%d KB\n", sketch.Size()/1024)
	// Output:
	// 16 KB
}
