package fuse_test

import (
	"fmt"
	"slices"

	"github.com/nalgeon/unsure/fuse"
)

func Example() {
	items := [][]byte{[]byte("alice"), []byte("bob")}

	// An 8-bit filter: 0.39% false positive rate.
	filter := fuse.New[uint8](slices.Values(items))

	fmt.Println(filter.Contains([]byte("alice")))
	fmt.Println(filter.Contains([]byte("bob")))
	fmt.Println(filter.Contains([]byte("carol")))
	// Output:
	// true
	// true
	// false
}

func ExampleFilter_Size() {
	// The iterator may reuse its buffer.
	items := func(yield func([]byte) bool) {
		var buf [8]byte
		for i := range 1_000_000 {
			buf[0], buf[1], buf[2] = byte(i), byte(i>>8), byte(i>>16)
			if !yield(buf[:]) {
				return
			}
		}
	}
	filter := fuse.New[uint8](items)
	fmt.Printf("%.1f bits per item\n", float64(filter.Size()*8)/1_000_000)
	// Output:
	// 9.0 bits per item
}
