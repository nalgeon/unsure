# T-digest history

Benchmarks: `make tdigest-bench`, Apple M1, normal values, digest filled with 1M values. ns/op for δ = 50 / 100 / 500. Merge: a 100K-value digest into a 1M+ one.
Accuracy: `make tdigest-accuracy`, δ=100, 5 digests of 1M normal values, single or merged from 10 × 100K. Mean rank error at q = 0.5 / 0.99 / 0.999, and size after 1M values.

## 0. Baseline: influxdata/tdigest v0.0.1 (2026-11-08)

Merging t-digest with k1 scaled to [0, c], run at c = δ/2 (same centroid count as ours). Buffer of 8c centroids; each flush appends the centroids to the buffer and sorts everything with `sort.Sort`. Merge = `AddCentroidList(other.Centroids())`, compressed lazily when the buffer fills.

| Add           | Quantile           | Merge          | Single                 | Merged               | Size    |
| ------------- | ------------------ | -------------- | ---------------------- | -------------------- | ------- |
| 78 / 87 / 105 | 12.7 / 13.2 / 15.6 | 85 / 147 / 633 | 0.015 / 0.016 / 0.009% | 2.2 / 0.084 / 0.011% | 14.4 KB |

Merged digests are badly off (2.2% at the median, up to 44% for sorted input): `AddCentroidList` skips the centroid at the point where the buffer fills.

Also compared: caio/go-tdigest v4.1.0, the older tree-based t-digest with an RNG, ~1,360 centroids at δ=100 (13× ours). Add 170–1,030 ns, Quantile 410–3,190 ns, Merge 0.12–5.6 ms; rank error 0.0001–0.005%, 29.4 KB.

## 1. Merging t-digest, k1, sort-and-merge flush

Design: centroids {mean, weight float64} in two fixed arrays of δ+2 (current + scratch, swapped after each pass), cumulative weights for binary search, exact min/max. Buffer of 5δ raw float64; flush = `slices.Sort` the buffer, then one two-way merge with the sorted centroids (not concat + re-sort). k1 = δ/(2π)·asin(2q-1): limit q(k(q₀)+1) computed once per output centroid (one asin + one sin). Centroid sums accumulated as offsets from the first mean (values like 1e12 + noise kept means out of order otherwise). Every other flush negates the values, so the pass runs right to left: left-tail error at q=1e-4 1.8e-4 → 1.0e-4, both tails equal. Paper's interpolation (weight-1 centroids exact, min/max at ends), so n ≲ δ/2 is exact. Merge flushes both and runs one compression pass over both centroid lists. NaN/±Inf ignored. Zero allocations.

| Add          | Quantile           | Merge             | Single                 | Merged                 | Size   |
| ------------ | ------------------ | ----------------- | ---------------------- | ---------------------- | ------ |
| 48 / 52 / 62 | 11.2 / 13.1 / 15.4 | 913 / 1.7k / 8.3k | 0.007 / 0.018 / 0.011% | 0.055 / 0.023 / 0.006% | 8.5 KB |

vs influx: Add −38..−41% (sorting 5δ floats instead of re-sorting buffer + centroids with `sort.Sort`), Quantile −0..−12%, CDF −3%, same single-digest accuracy, −41% memory, merged digests correct. Merge 11× slower: it compresses on every call, and the asin/sin per output centroid dominates (~25 ns × ~60 centroids at δ=100), while influx defers to its buffer. Error scales ~1/δ² in the tails and ~1/δ^1.3 at the median (δ=10: 0.7% / 0.9%; δ=1000: 0.003% / 0.001%). k1 is weak at q ≤ 1e-4: the outermost centroid (~500 values at n=1M) is larger than the rank.
