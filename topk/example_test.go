package topk_test

import (
	"fmt"

	"github.com/nalgeon/unsure/topk"
)

func Example() {
	// Track the 2 most frequent items.
	sketch := topk.New(2)

	sketch.Add([]byte("alice"))
	sketch.Add([]byte("bob"))
	sketch.Add([]byte("carol"))
	sketch.AddN([]byte("alice"), 4)
	sketch.AddN([]byte("carol"), 2)

	for _, e := range sketch.Top() {
		fmt.Println(e.Item, e.Count)
	}
	fmt.Println(sketch.Count([]byte("alice")))
	fmt.Println(sketch.Count([]byte("bob")))
	fmt.Println(sketch.Total())
	// Output:
	// alice 5
	// carol 3
	// 5 true
	// 0 false
	// 9
}

func ExampleSketch_Size() {
	sketch := topk.New(1000)
	fmt.Printf("%d KB\n", sketch.Size()/1024)
	// Output:
	// 330 KB
}
