package countmin_test

import (
	"fmt"

	"github.com/nalgeon/unsure/countmin"
)

func Example() {
	// Estimates within 0.1% of the total count, with 99% probability.
	sketch := countmin.New(0.001, 0.01)

	sketch.Add([]byte("alice"), 1)
	sketch.Add([]byte("bob"), 1)
	sketch.Add([]byte("alice"), 2)

	fmt.Println(sketch.Count([]byte("alice")))
	fmt.Println(sketch.Count([]byte("bob")))
	fmt.Println(sketch.Count([]byte("carol")))
	fmt.Println(sketch.Total())
	// Output:
	// 3
	// 1
	// 0
	// 4
}

func ExampleNewWith() {
	// Sketches with the same seed hash items the same way.
	opts := countmin.Options{Seed: 42}
	sketch := countmin.NewWith(0.001, 0.01, opts)

	sketch.Add([]byte("alice"), 5)
	fmt.Println(sketch.Count([]byte("alice")))
	// Output:
	// 5
}

func ExampleSketch_Size() {
	sketch := countmin.New(0.001, 0.01)
	fmt.Printf("%d KB\n", sketch.Size()/1024)
	// Output:
	// 55 KB
}
