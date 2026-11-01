package bloom_test

import (
	"fmt"

	"github.com/nalgeon/unsure/bloom"
)

func Example() {
	// A set for 1000 items with a 1% false positive rate.
	set := bloom.NewSet(1000, 0.01)

	set.Add([]byte("alice"))
	set.Add([]byte("bob"))

	fmt.Println(set.Contains([]byte("alice")))
	fmt.Println(set.Contains([]byte("bob")))
	fmt.Println(set.Contains([]byte("carol")))
	// Output:
	// true
	// true
	// false
}

func ExampleNewSetOptions() {
	// Sets with the same seed hash items the same way.
	opts := bloom.Options{Seed: 42}
	set := bloom.NewSetOptions(1000, 0.01, opts)

	set.Add([]byte("alice"))
	fmt.Println(set.Contains([]byte("alice")))
	// Output:
	// true
}

func ExampleSet_Size() {
	set := bloom.NewSet(1_000_000, 0.01)
	fmt.Printf("%.1f bits per item\n", float64(set.Size()*8)/1_000_000)
	// Output:
	// 9.9 bits per item
}
