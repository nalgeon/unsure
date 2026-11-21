# Log-linear histogram

A log-linear histogram is a summary of a stream of numbers that trades accuracy for space. It can give you an approximate median, 99th percentile, or any other _quantile_ of the data. It can also estimate what fraction of the numbers are below a certain value (the CDF). The histogram doesn't actually store the numbers, so it's much more compact than a sorted list.

For example, to find the exact 99.9th percentile of 1,000,000 response times, you'd have to keep all of them. With 64-bit floats, that takes 8 MB. A log-linear histogram estimates the same percentile within 0.39% of its true value using about 14 KB — almost 600 times less.

The error is relative to the value: if the true 99.9th percentile is 200 ms, the histogram returns a value between 199.2 and 200.8 ms. This guarantee applies to every quantile, from the median to the most extreme outliers. The size of the histogram doesn't depend on the number of values, only on the accuracy you choose and on how wide the range of the values is.

A log-linear histogram is useful when you need percentiles for positive values that cover a wide range, like latencies, file sizes, or prices. You can merge histograms to gather data from multiple machines or segments (like by hour, endpoint, or region). Merging is exact: a merged histogram is the same as if it had received all the values directly.

You can add numbers to a histogram, estimate quantiles and the CDF, and merge histograms. You cannot remove numbers or list them.

## Usage

```text
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/loghist"

// Response times in milliseconds.
hist := loghist.New()

hist.Add(12.5)
hist.Add(8.1)
hist.Add(130.0)
// ...

hist.Quantile(0.5)   // median
hist.Quantile(0.999) // 99.9th percentile
hist.CDF(100)        // fraction of responses faster than 100 ms
```

Merging histograms:

```go
east := loghist.New() // latencies on one server
west := loghist.New() // latencies on another
// ...
east.Merge(west)
east.Quantile(0.99) // 99th percentile across both servers
```

The histogram has two settings: the relative error and the range of values. `loghist.New()` uses the default error of 0.0039 (0.39%) and the default range from 1e-9 to 1e18. To trade accuracy for memory, use `loghist.NewError(e)` with the error value from 0.0001 to 0.1. The histogram doesn't use the given value exactly, but picks the largest error from the series 5.9%, 3%, 1.5%, 0.78%, 0.39%, 0.2%, 0.098%, ... (about half the previous one each time) that doesn't exceed the requested error. Only histograms with the same error and range can be merged.

Memory depends on the error and on the range of the values. For example, for values from 1 to 1,000,000:

| Requested error | Actual error | Memory |
| --------------: | -----------: | -----: |
|             0.1 |         5.9% | 1.3 KB |
|            0.01 |        0.78% |  10 KB |
|           0.005 |        0.39% |  20 KB |
|           0.001 |       0.098% |  80 KB |
|          0.0001 |      0.0061% | 1.3 MB |

Memory grows when you add a value that's outside the current range, and it never shrinks. The range of values limits how much it can grow. By default, the error guarantee applies to absolute values from 1e-9 to 1e18. This range covers latencies measured in seconds or nanoseconds, as well as sizes in bytes.

With the default settings, values smaller than 1e-9 are treated as zero. Values ≤−1e18 or ≥1e18 are treated as overflow: if a quantile lands on them, `Quantile` returns the exact maximum (or minimum). This way, the default histogram never uses more than 90 KB for positive values and 90 KB for negative values, no matter what you add.

To set a different range, use `loghist.NewRange(e, lo, hi)`. For example, `NewRange(0.01, 1e-6, 3600)` is for response times in seconds, from 1 µs to 1 hour, with a 1% error. `NewRange(e, 0, math.Inf(1))` covers all float64 values, but then memory is unbounded: adding 1e-300 to a set of values from 1 to 1,000,000 uses about 1 MB more memory.

`Quantile(q)` returns the estimated value below which about q of the data falls. `Quantile(0)` and `Quantile(1)` return the exact minimum and maximum.

NaN and infinite values are ignored. The order in which values are added doesn't matter: the same values always produce the same histogram.

## How it works

A **histogram** splits the range of values into _buckets_ and counts how many values are in each one. To estimate a quantile, it adds up the counts starting from the lowest bucket until it reaches the requested rank, then returns a value from that bucket. The accuracy of the estimate depends on how wide the bucket is.

With buckets that have equal widths, like 0–10, 10–20, and so on, the error is very large for small values (5 ± 5) and unnecessarily small for large values (995 ± 5). A **logarithmic histogram** solves this by making each bucket's width proportional to its value. For example, if the bucket boundaries are powers of 1.02 (1, 1.02, 1.0404, ...), the end of each bucket is 2% larger than its start, so any value in the bucket is within about 1% of the bucket's midpoint. _DDSketch_ uses this method. The downside is that finding the right bucket for a value means you have to calculate a logarithm, which is relatively slow.

A **log-linear histogram** gets almost the same result with cheaper math. First, it splits the values into ranges between powers of two: 1–2, 2–4, 4–8, and so on (these are called _octaves_). Then it splits every octave into the same number of equal-width buckets. For example, here are three octaves with 4 buckets each:

```text
1       2               4                               8
├─┬─┬─┬─┼───┬───┬───┬───┼───────┬───────┬───────┬───────┤
4 buckets   4 buckets               4 buckets
```

Each octave is twice as long as the previous one, so its buckets are twice as wide, and the bucket width grows with the values, as in a logarithmic histogram. But within an octave, the bucket width stays the same, so a bucket at the start of an octave is twice as wide (relative to its values) as a bucket at the end. To get the same maximum error, a log-linear histogram needs about 1.44× more buckets than a logarithmic one. In return, finding the right bucket only takes a few bit operations instead of calculating a logarithm.

_HdrHistogram_ is the best-known log-linear histogram. It records integers: the position of the highest 1 bit of a number determines its octave, and the next few bits determine the bucket within the octave. The lowest and highest values are set when the histogram is created, and it allocates the buckets for this whole range upfront. The precision is set in significant decimal digits, and the histogram returns the highest value of a bucket as the estimate.

**This implementation** works with float64 numbers and finds the bucket in a similar way to _HdrHistogram_. A float64 is stored as three parts: a sign bit, an 11-bit exponent _e_, and a 52-bit mantissa _f_. The number's value is 1.f × 2^e, where 1.f is a binary fraction between 1 and 2. The exponent determines the octave, from 2^e to 2^(e+1). The _m_ bits of the mantissa determine one of the 2^m equal buckets within the octave.

For example, here's the number 5.25 with m=3 (8 buckets per octave). In binary, 5.25 = 1.0101₂ × 2², so the exponent is 2 (stored as 2 + 1023 = 1025, because float64 adds 1023 to the exponent to avoid negative values), and the mantissa starts with 0101:

```text
5.25 = 1.0101₂ × 2²

sign   exponent     top m   rest of the mantissa
┌───┬─────────────┬─────┬─────────────────────────┐
│ 0 │ 10000000001 │ 010 │ 1000000000 ... 00000000 │
└───┴──────┬──────┴──┬──┴──┬──────────────────────┘
           │         │     └── ignored
           │         └── bucket 2 of 8
           └── 1025 = 2 + 1023: octave [4, 8)

4                               8
├───┬───┬───┬───┬───┬───┬───┬───┤
  0   1   2   3   4   5   6   7
          └ 5.25 lands in bucket 2: [5, 5.5)
```

`Add` finds the bucket for the given number and increases the count. If the bucket is outside the bucket array, it grows the array. `Merge` adds the counts bucket by bucket.

`Quantile` adds up the counts from the closer end (the most negative bucket for q ≤ 0.5, the most positive one otherwise) until it reaches the rank. It returns the estimate for that bucket, limited to the exact minimum and maximum. `CDF` adds up the counts in the buckets below x, plus part of x's own bucket, assuming its values are spread evenly.

The number of mantissa bits m comes from the requested error: `NewError(e)` picks the smallest m for which the maximum error doesn't exceed e. The default is m=7: 128 buckets per octave and an error of 0.39%.

The counts are 64-bit numbers in two arrays: one for positive values, and one for negative values by their absolute value. Out-of-range values are counted separately. Unlike _HdrHistogram_, the arrays don't cover a fixed range. Instead, they start empty and grow by whole octaves to include the values that are actually added. So the histogram accepts any float64 and only uses memory for the range of the data: 2^m counts of 8 bytes per octave (1 KB for m=7).

## Log-linear histogram vs. t-digest

A [t-digest](../tdigest/README.md) also estimates quantiles, but it limits the error in _ranks_: `Quantile(0.99)` returns a value whose rank is close to 99%. A log-linear histogram limits the error in _values_: `Quantile(0.99)` returns a value close to the true 99th percentile.

In the middle of the data, the two are pretty similar. But in a long tail, values at nearby ranks can be very different, so even a small error in rank can lead to a big difference in value. Here is the mean relative error of an 8.5 KB t-digest (δ=100) on 1M values from three distributions: lognormal (as in the benchmarks), Pareto with α=1.5 (a longer tail), and uniform between 1,000,000 and 2,000,000 (no tail):

| Quantile | Lognormal | Pareto | Uniform |
| -------- | --------: | -----: | ------: |
| 0.5      |     0.12% |  0.09% |  0.009% |
| 0.99     |      1.7% |   5.6% |  0.015% |
| 0.999    |      8.7% |    67% |  0.009% |
| 0.9999   |       83% |   868% |  0.004% |

A log-linear histogram of about the same size (1% error, 8 KB) has an error below 0.78% for all these cases.

So a log-linear histogram is better for latencies, sizes, and other positive measurements with long tails. A t-digest is better when you need a fixed size no matter what the values are, or when the values can be arbitrarily close to zero.

## Benchmarks

Compared to [HdrHistogram/hdrhistogram-go](https://github.com/HdrHistogram/hdrhistogram-go) (log-linear, integers only, fixed range) and [DataDog/sketches-go](https://github.com/DataDog/sketches-go) (DDSketch, logarithmic buckets).

The input values are random integers from a lognormal distribution: the median is 1,000,000, and 99.7% of the values are between 50,000 and 20,000,000. They are integers because _hdr_ only records integers. _hdr_'s range is set from 1 to 3.6·10¹².

The _Error_ column is the requested relative error. _hdr_'s precision is set in significant decimal digits rather than as an error, so it uses 2 digits for 1%, and 3 digits for 0.4% and 0.1%.

Speed on Apple M1, ns/op (lower is better):

| Operation | Error |  unsure |     hdr |        dd |
| --------- | ----: | ------: | ------: | --------: |
| Add       |    1% |   **4** |   **4** |        19 |
|           |  0.4% |   **4** |   **4** |        19 |
|           |  0.1% |   **4** |   **4** |        19 |
| Quantile  |    1% |  **48** |     283 |     1,750 |
|           |  0.4% |  **90** |   1,708 |     3,100 |
|           |  0.1% | **337** |   1,712 |    11,967 |
| Merge     |    1% | **334** |  16,369 |   **302** |
|           |  0.4% | **657** | 115,052 |       808 |
|           |  0.1% |   3,249 | 115,018 | **2,862** |

`Add` is measured on a histogram that already has 1M values. `Merge` merges histograms of 100K values each, one after another, into a histogram that starts with 1M values.

Mean relative error at the requested 0.1%, over 5 histograms of 1M values each, lower is better:

| Quantile |     unsure |        hdr |     dd |
| -------- | ---------: | ---------: | -----: |
| 0.5      | **0.033%** | **0.033%** | 0.050% |
| 0.99     |     0.038% | **0.037%** | 0.048% |
| 0.999    |     0.027% | **0.019%** | 0.039% |
| 0.9999   |     0.016% | **0.002%** | 0.025% |

Memory after adding 1M values (lower is better):

| Error | unsure |    hdr |        dd |
| ----: | -----: | -----: | --------: |
|    1% |   8 KB |  40 KB |  **6 KB** |
|  0.4% |  14 KB | 264 KB | **13 KB** |
|  0.1% |  56 KB | 264 KB | **40 KB** |

Overall, _unsure_ adds values as fast as _hdr_ and 5× faster than _dd_, queries 5–19× faster than _hdr_ and about 35× faster than _dd_, and merges about as fast as _dd_ and 35–175× faster than _hdr_. It's about as accurate as _hdr_ with 5× less memory, and 1.3–1.6× more accurate than _dd_ with 40% more memory.

To reproduce, run `make loghist-bench-full` and `make loghist-accuracy` from the repository root.

## Papers

[DDSketch: A fast and fully-mergeable quantile sketch with relative-error guarantees](https://arxiv.org/abs/1908.10693) (Charles Masson, Jee E. Rim, Homin K. Lee, 2019): relative-error quantile sketches, including the log-linear mapping used here.

[HdrHistogram: A High Dynamic Range (HDR) Histogram](https://github.com/HdrHistogram/HdrHistogram).
