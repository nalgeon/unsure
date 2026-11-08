package tdigest_test

import (
	"fmt"

	"github.com/nalgeon/unsure/tdigest"
)

func Example() {
	// Response times in milliseconds.
	digest := tdigest.New()
	for i := range 1000 {
		digest.Add(float64(i%100) + 0.5)
	}

	fmt.Printf("median: %.1f ms\n", digest.Quantile(0.5))
	fmt.Printf("p99: %.1f ms\n", digest.Quantile(0.99))
	fmt.Printf("under 10 ms: %.0f%%\n", digest.CDF(10)*100)
	// Output:
	// median: 50.0 ms
	// p99: 99.0 ms
	// under 10 ms: 10%
}

func ExampleNewCompression() {
	// More centroids: 40 KB, about 4× smaller error than the default.
	digest := tdigest.NewCompression(500)
	digest.Add(1)
	digest.Add(2)
	fmt.Println(digest.Count(), digest.Size())
	// Output:
	// 2 40088
}

func ExampleDigest_Merge() {
	// Latencies measured on two servers, summarized separately.
	east := tdigest.New()
	west := tdigest.New()
	for i := range 100 {
		east.Add(float64(i))
		west.Add(float64(i + 100))
	}

	// Latencies across both servers.
	east.Merge(west)
	fmt.Println(east.Count(), east.Quantile(0), east.Quantile(1))
	// Output:
	// 200 0 199
}

func ExampleDigest_Size() {
	digest := tdigest.New()
	fmt.Println(digest.Size(), "bytes") // centroids + buffer
	// Output:
	// 8088 bytes
}
