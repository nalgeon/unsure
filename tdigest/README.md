# T-digest

A simple and fast t-digest implemented in Go.

## What is a t-digest

A t-digest is a summary of a stream of numbers that trades accuracy for space. It can give you an approximate median, 99th percentile, or any other quantile of the data. It can also estimate what fraction of the numbers are below a certain value (the CDF). The digest doesn't actually store the numbers, so it's much more compact than a sorted list.

For example, finding the exact 99th percentile of 1,000,000 numbers means keeping all of them: a `[]float64` takes 8 MB. A t-digest estimates the same percentile within about 0.03% of the rank (between the 98.97th and the 99.03rd percentile) using only 8 KB — 1,000 times smaller. The size of the digest doesn't depend on the number of values, only on the accuracy you choose. An 8 KB digest summarizes a billion values as well as a million.

A t-digest is useful when you need percentiles over a large stream: latency percentiles for a service, the distribution of request sizes or prices, or the outlier thresholds in sensor data. You can merge digests to gather data from multiple machines or segments (like by hour, endpoint, or region). A merged digest estimates the percentiles of all values, but with less accuracy than individual digests do.

You can add numbers to a digest, estimate quantiles and the CDF, and merge digests. You cannot remove numbers or list them.

## Usage

```
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/tdigest"

// Response times in milliseconds.
digest := tdigest.New()

digest.Add(12.5)
digest.Add(8.1)
digest.Add(130.0)
// ...

digest.Quantile(0.5)  // median
digest.Quantile(0.99) // 99th percentile
digest.CDF(100)       // fraction of responses faster than 100 ms
```

Merging digests:

```go
east := tdigest.New() // latencies on one server
west := tdigest.New() // latencies on another
// ...
east.Merge(west)
east.Quantile(0.99) // 99th percentile across both servers
```

The digest has one setting: the compression _δ_, which limits the number of centroids (see the _How it works_ section) to δ+2. `tdigest.New()` uses δ=100, a common default, which takes 8 KB. To trade accuracy for memory, use `tdigest.NewCompression(δ)` with δ from 10 to 10,000. The error is measured in ranks: a rank error of 0.03% at quantile q=0.99 means the value returned by `Quantile(0.99)` is somewhere between the 98.97th and the 99.03rd percentile of the actual values.

Here's the mean rank error for 1M random values:

| δ    | Memory | q=0.5  | q=0.99 | q=0.999 |
| ---- | ------ | ------ | ------ | ------- |
| 50   | 4 KB   | 0.06%  | 0.08%  | 0.04%   |
| 100  | 8 KB   | 0.02%  | 0.03%  | 0.02%   |
| 200  | 16 KB  | 0.012% | 0.011% | 0.007%  |
| 500  | 40 KB  | 0.005% | 0.002% | 0.001%  |
| 1000 | 80 KB  | 0.003% | 0.001% | 0.0005% |

The worst-case error is about twice the mean. Numbers added in sorted order give about 10× smaller errors, and merged digests give about 2× larger errors. At extreme quantiles, like q=0.0001 with δ=100, the error can be as large as q itself, so use a larger δ if you need them.

`Quantile(0)` and `Quantile(1)` return the exact minimum and maximum of all added numbers. With few values (up to about δ/2), the estimates are exact. NaN and infinite values are ignored. The digest is deterministic: the same numbers added in the same order always produce the same digest.

`Quantile` and `CDF` change the digest's internal state (see below), so a digest is not safe for concurrent use, even if all goroutines only read from it.

## How it works

**The t-digest** summarizes the numbers as a sorted list of _centroids_. Each centroid has a mean and a weight — the number of values it represents. To estimate a quantile, the digest finds the centroids around the requested rank and interpolates between their means.

The trick is in how big the centroids are allowed to grow. A _scale function_ k(q) maps a quantile q to a number from -δ/4 to δ/4, and each centroid may only span 1 unit of k (if a centroid covers the quantiles from q₁ to q₂, then k(q₂) − k(q₁) ≤ 1). This implementation uses the arcsine scale function, k(q) = δ/(2π)·asin(2q-1), which is steep near q=0 and q=1 and flat near the median. So the centroids are large in the middle and small at the tails:

```
weight
  │                ┌──┐
  │             ┌──┤  ├──┐
  │          ┌──┤  │  │  ├──┐
  │       ┌──┤  │  │  │  │  ├──┐
  │  ┌─┬──┤  │  │  │  │  │  │  ├──┬─┐
  └──┴─┴──┴──┴──┴──┴──┴──┴──┴──┴──┴─┴──► value
    min      q=0.25   median  q=0.75     max
```

This keeps the error near the tails small, where it matters most. With 1M values and δ=100, the centroids around the median hold about 27,000 values each, those around the 99th percentile about 5,000, and the outermost ones about 500.

**This implementation** is the merging t-digest from the Dunning&Ertl 2019 paper. New values go to a buffer of 5·δ numbers. When the buffer is full, the digest sorts it and merges it with the centroids (already sorted) in a single pass, building new centroids from left to right as long as each one fits within its span of k. Every other pass goes right to left, so that neither tail ends up with fuller centroids. `Quantile`, `CDF` and `Merge` flush the buffer first.

The centroids are kept in two fixed arrays of δ+2 entries (the current list and the next one being built), along with the cumulative weights for a binary search in `Quantile` and `CDF`. With δ=100, that's 16·102·2 + 8·103 bytes for the centroids, and 8·500 bytes for the buffer, about 8 KB in total. Adding a value doesn't allocate memory.

Centroids with weight 1 are treated as exact values rather than spread around their mean, and the exact minimum and maximum are used at the ends. So digests with few values give exact results, and `Quantile(0)` and `Quantile(1)` are always exact.

## Benchmarks

Compared to [influxdata/tdigest](https://github.com/influxdata/tdigest) (merging t-digest with the same scale function) and [caio/go-tdigest](https://github.com/caio/go-tdigest) (tree-based t-digest). Apple M1, normal values, ns/op (lower is better):

| Operation |   δ | unsure |  influx |      caio |
| --------- | --: | -----: | ------: | --------: |
| Add       |  50 | **43** |      77 |       169 |
|           | 100 | **48** |      86 |       275 |
|           | 500 | **58** |     104 |     1,020 |
| Quantile  |  50 | **11** |      13 |       407 |
|           | 100 | **13** |  **13** |       752 |
|           | 500 | **15** |  **16** |     3,160 |
| CDF       |  50 | **31** |  **33** |       351 |
|           | 100 | **36** |  **38** |       670 |
|           | 500 | **48** |  **49** |     2,820 |
| Merge     |  50 |    296 |  **85** |   119,000 |
|           | 100 |    637 | **146** |   413,000 |
|           | 500 |  2,730 | **616** | 5,570,000 |

`Add` is measured on a digest that already holds 1M values, and includes the amortized cost of flushing the buffer. `Merge` merges a digest of 100K values into one of 1M or more.

`Merge` in _unsure_ is much slower than in _influxdata_, because _influxdata_ appends the other digest's centroids to its buffer and compresses later, while _unsure_ compresses on every merge.

Accuracy at δ=100, mean rank error over 5 digests of 1M normal values (lower is better). _Merged_ digests are built by merging 10 digests of 100K values each:

| Digest | Quantile | unsure | influx |        caio |
| ------ | -------- | -----: | -----: | ----------: |
| Single | 0.5      | 0.007% | 0.015% | **0.0024%** |
|        | 0.99     | 0.018% | 0.016% | **0.0004%** |
|        | 0.999    | 0.011% | 0.009% | **0.0001%** |
| Merged | 0.5      | 0.055% |   2.2% | **0.0047%** |
|        | 0.99     | 0.023% | 0.084% | **0.0009%** |
|        | 0.999    | 0.006% | 0.011% | **0.0001%** |

Memory at δ=100 (lower is better): _unsure_ 8.5 KB, _influxdata_ 14.4 KB, _caio_ 29.4 KB.

_unsure_ and _influxdata_ are equally accurate for a single digest, since they have the same number of centroids. _influxdata_ is much less accurate after merging (2.2% at the median, and up to 44% for values added in sorted order). _caio_ is 3–100× more accurate thanks to its 13× more centroids, but it takes 3.5× more memory and is 4–18× slower to add values and 11–210× slower to query.

Overall, _unsure_ adds values 44–45% faster than _influxdata_, queries at the same speed, merges 3.5–4.5× slower (with much better accuracy), and uses 40% less memory at the same accuracy.

To reproduce, run `make tdigest-bench-full` and `make tdigest-accuracy` from the repository root.

## Papers

[Computing extremely accurate quantiles using t-digests](https://arxiv.org/abs/1902.04023) (Ted Dunning, Otmar Ertl, 2019): the t-digest, including the merging variant used here.
