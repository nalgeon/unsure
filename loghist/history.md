# Log-linear histogram history

Benchmarks: `make loghist-bench`, Apple M1, lognormal latencies in integer ns (median 1 ms), histogram filled with 1M values. ns/op for relative error 1% / 0.4% / 0.1%. Merge: a 100K-value histogram into a 1M+ one.
Accuracy: `make loghist-accuracy`, relative error 0.4%, 5 histograms of 1M lognormal values. Mean relative value error at q = 0.5 / 0.99 / 0.999, max error over all quantiles and trials, and size after 1M values.

## 0. Baseline: HdrHistogram/hdrhistogram-go v1.4.0 (2026-11-14)

Log-linear over int64, range [1, 1 hour in ns] fixed at creation, precision in significant digits d = ⌈−log10(err)⌉ (2 / 3 / 3), so 0.4% runs with 3 digits (1024 sub-buckets per octave, ~0.1% error). Dense array over the whole range; Quantile returns the highest value of the bucket; Merge iterates all recorded values.

| Add             | Quantile          | Merge             | Error (mean)           | Max    | Size     |
| --------------- | ----------------- | ----------------- | ---------------------- | ------ | -------- |
| 4.0 / 4.0 / 4.0 | 283 / 1.7k / 1.7k | 16k / 115k / 115k | 0.019 / 0.034 / 0.019% | 0.068% | 264.1 KB |

Also compared: DataDog/sketches-go v1.4.8 (DDSketch), logarithmic mapping γ = (1+α)/(1−α) (one `math.Log` per Add), buffered paginated float64 store. Add 19 ns, Quantile 1.7k / 3.1k / 12k, Merge 302 / 808 / 2.9k; error 0.280 / 0.187 / 0.216%, max 0.399%, 12.7 KB.

## 1. Float-bit buckets, dense octave-aligned stores

Design: bucket = float64 bits >> (52−m), i.e. exponent + top m mantissa bits, 2^m equal buckets per octave; m from the requested error, max error 1/(2^(m+1)+1) (m=6 / 7 / 9: 0.78 / 0.39 / 0.098%). Estimate 2ab/(a+b) for bucket [a, b), clamped to exact [min, max]. Separate dense uint64 stores for positive and |negative| values, plus a zero count (zero and subnormals); stores grow to cover whole octaves, no slack. Quantile = linear scan negatives → zero → positives to rank ⌊q(n−1)⌋. CDF interpolates within the bucket, clamped to [min, max]. Merge adds counts after covering the other's range: exact and order-independent. Equal precision required.

| Add             | Quantile         | Merge            | Error (mean)           | Max    | Size    |
| --------------- | ---------------- | ---------------- | ---------------------- | ------ | ------- |
| 3.9 / 3.7 / 3.6 | 180 / 333 / 1.3k | 334 / 657 / 3.2k | 0.095 / 0.112 / 0.158% | 0.251% | 14.1 KB |

vs HDR: Add −0..−8%, Quantile −26..−81%, Merge 35–175× faster, 5–21% of HDR's memory (it allocates the whole 1 ns – 1 h range upfront). At 1% and 0.1%, same max error (0.54 / 0.068%): HDR buckets are 2× narrower, but it returns their upper end; at 0.4% HDR uses 3 digits, so it's 4× more accurate with 19× more memory. vs DDSketch: Add 5× faster, Quantile 9–10× faster, Merge about the same, error 1.4–3× lower but memory +11..+93% (log mapping needs ln 2/ln γ buckets per octave, ~1.44× fewer, and its paginated store skips empty pages).

Our t-digest (δ=100, 8.5 KB) for comparison, mean relative value error at q = 0.5 / 0.99 / 0.999 / 0.9999: lognormal 0.13 / 1.7 / 8.8 / 82%, Pareto α=1.5 0.10 / 5.8 / 68 / 871%, uniform 0.008 / 0.015 / 0.009 / 0.004%. Small rank error doesn't bound the value error in a heavy tail; loghist at 1% (8.1 KB) stays under 0.62% everywhere.
