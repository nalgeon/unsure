package bloom_test

import (
	"fmt"

	"github.com/nalgeon/unsure/bloom"
)

func Example() {
	// A filter for 1000 items with a 1% false positive rate.
	filter := bloom.New(1000, 0.01)

	filter.Add([]byte("alice"))
	filter.Add([]byte("bob"))

	fmt.Println(filter.Contains([]byte("alice")))
	fmt.Println(filter.Contains([]byte("bob")))
	fmt.Println(filter.Contains([]byte("carol")))
	// Output:
	// true
	// true
	// false
}

func ExampleFilter_Size() {
	filter := bloom.New(1_000_000, 0.01)
	fmt.Printf("%.1f bits per item\n", float64(filter.Size()*8)/1_000_000)
	// Output:
	// 9.9 bits per item
}
