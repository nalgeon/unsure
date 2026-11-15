package loghist_test

import (
	"fmt"

	"github.com/nalgeon/unsure/loghist"
)

func Example() {
	// Response times in milliseconds.
	hist := loghist.New()
	for i := range 1000 {
		hist.Add(float64(i%100) + 0.5)
	}

	fmt.Printf("median: %.1f ms\n", hist.Quantile(0.5))
	fmt.Printf("p99: %.1f ms\n", hist.Quantile(0.99))
	fmt.Printf("under 10 ms: %.0f%%\n", hist.CDF(10)*100)
	// Output:
	// median: 49.6 ms
	// p99: 98.7 ms
	// under 10 ms: 10%
}

func ExampleNewError() {
	// At most 0.1% relative error (0.098% actual).
	hist := loghist.NewError(0.001)
	hist.Add(123.456)
	hist.Add(1000)
	fmt.Println(hist.Count(), hist.Quantile(0.5))
	// Output:
	// 2 123.456
}

func ExampleNewRange() {
	// Response times in seconds: 1 µs to 1 hour with 1% error.
	// Faster responses count as zero, slower ones as overflow.
	hist := loghist.NewRange(0.01, 1e-6, 3600)
	hist.Add(0.25)
	hist.Add(1e-9) // counts as zero
	hist.Add(5000) // overflow
	fmt.Printf("min %g, median %.2f, max %g\n",
		hist.Quantile(0), hist.Quantile(0.5), hist.Quantile(1))
	// Output:
	// min 1e-09, median 0.25, max 5000
}

func ExampleHistogram_Merge() {
	// Latencies measured on two servers, summarized separately.
	east := loghist.New()
	west := loghist.New()
	for i := range 100 {
		east.Add(float64(i + 1))
		west.Add(float64(i + 101))
	}

	// Latencies across both servers.
	if err := east.Merge(west); err != nil {
		panic(err)
	}
	fmt.Println(east.Count(), east.Quantile(0), east.Quantile(1))
	// Output:
	// 200 1 200
}

func ExampleHistogram_Size() {
	hist := loghist.New()
	for i := range 1000 {
		hist.Add(float64(i + 1)) // 1 to 1000: 10 octaves
	}
	fmt.Println(hist.Size(), "bytes")
	// Output:
	// 10240 bytes
}
