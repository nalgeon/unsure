# T-digest history

Benchmarks: `make tdigest-bench`, Apple M1, normal values, digest filled with 1M values. ns/op for δ = 50 / 100 / 500. Merge: a 100K-value digest into a 1M+ one.
Accuracy: `make tdigest-accuracy`, δ=100, 5 digests of 1M normal values, single or merged from 10 × 100K. Mean rank error at q = 0.5 / 0.99 / 0.999, and size after 1M values.

## 0. Baseline: influxdata/tdigest v0.0.1 (2026-11-08)

Merging t-digest with k1 scaled to [0, c], run at c = δ/2 (same centroid count as ours). Buffer of 8c centroids; each flush appends the centroids to the buffer and sorts everything with `sort.Sort`. Merge = `AddCentroid` for each of `other.Centroids()`, compressed lazily when the buffer fills (`AddCentroidList` is broken, see below).

| Add           | Quantile           | Merge             | Single                 | Merged                 | Size    |
| ------------- | ------------------ | ----------------- | ---------------------- | ---------------------- | ------- |
| 78 / 87 / 105 | 12.7 / 13.2 / 15.6 | 2.1k / 4.3k / 24k | 0.015 / 0.016 / 0.009% | 0.037 / 0.021 / 0.016% | 14.4 KB |

Corrected after iteration 3: the baseline first merged with `AddCentroidList`, which fills the buffer to exactly 8c and then silently drops every further centroid until a query or a single `Add` triggers processing. That made influx's Merge look 4–11× faster than ours (85 / 147 / 633 ns, mostly copying `Centroids()`) and its merged error 2.2% at the median (44% for sorted input). The "vs influx" Merge and merged-accuracy comparisons in entries 1–3 were against that broken version; see entry 3 for the corrected ones.

Also compared: caio/go-tdigest v4.1.0, the older tree-based t-digest with an RNG, ~1,360 centroids at δ=100 (13× ours). Add 170–1,030 ns, Quantile 410–3,190 ns, Merge 0.12–5.6 ms; rank error 0.0001–0.005%, 29.4 KB.

## 1. Merging t-digest, k1, sort-and-merge flush

Design: centroids {mean, weight float64} in two fixed arrays of δ+2 (current + scratch, swapped after each pass), cumulative weights for binary search, exact min/max. Buffer of 5δ raw float64; flush = `slices.Sort` the buffer, then one two-way merge with the sorted centroids (not concat + re-sort). k1 = δ/(2π)·asin(2q-1): limit q(k(q₀)+1) computed once per output centroid (one asin + one sin). Centroid sums accumulated as offsets from the first mean (values like 1e12 + noise kept means out of order otherwise). Every other flush negates the values, so the pass runs right to left: left-tail error at q=1e-4 1.8e-4 → 1.0e-4, both tails equal. Paper's interpolation (weight-1 centroids exact, min/max at ends), so n ≲ δ/2 is exact. Merge flushes both and runs one compression pass over both centroid lists. NaN/±Inf ignored. Zero allocations.

| Add          | Quantile           | Merge             | Single                 | Merged                 | Size   |
| ------------ | ------------------ | ----------------- | ---------------------- | ---------------------- | ------ |
| 48 / 52 / 62 | 11.2 / 13.1 / 15.4 | 913 / 1.7k / 8.3k | 0.007 / 0.018 / 0.011% | 0.055 / 0.023 / 0.006% | 8.5 KB |

vs influx: Add −38..−41% (sorting 5δ floats instead of re-sorting buffer + centroids with `sort.Sort`), Quantile −0..−12%, CDF −3%, same single-digest accuracy, −41% memory, merged digests correct. Merge 11× slower: it compresses on every call, and the asin/sin per output centroid dominates (~25 ns × ~60 centroids at δ=100), while influx defers to its buffer. Error scales ~1/δ² in the tails and ~1/δ^1.3 at the median (δ=10: 0.7% / 0.9%; δ=1000: 0.003% / 0.001%). k1 is weak at q ≤ 1e-4: the outermost centroid (~500 values at n=1M) is larger than the rank.

## 2. Scale function limit without asin/sin

The centroid limit (sin(asin(2q-1) + 2π/δ) + 1)/2 expanded as sin(a+s) = sin(a)·cos(s) + cos(a)·sin(s), with sin(a) = 2q-1, cos(a) = 2√(q(1-q)), and cos(s), sin(s) precomputed: one sqrt per output centroid instead of asin + sin. Exact rewrite, accuracy unchanged to the digit.

| Add          | Quantile           | Merge            | Single                 | Merged                 | Size   |
| ------------ | ------------------ | ---------------- | ---------------------- | ---------------------- | ------ |
| 45 / 50 / 60 | 11.2 / 13.1 / 15.5 | 319 / 818 / 3.6k | 0.007 / 0.018 / 0.011% | 0.055 / 0.023 / 0.006% | 8.5 KB |

Merge 2.1–2.9× faster (vs influx: 11× → 4–6× slower), Add −2..−6%.

## 3. Simpler compressor check

The compressor keeps the current centroid's max weight (cap = limit − before) and checks `w+weight <= cap` instead of `before+w+weight <= limit`. Accuracy unchanged to the digit.

| Add          | Quantile           | Merge            | Single                 | Merged                 | Size   |
| ------------ | ------------------ | ---------------- | ---------------------- | ---------------------- | ------ |
| 43 / 48 / 58 | 11.0 / 13.1 / 15.5 | 296 / 637 / 2.7k | 0.007 / 0.018 / 0.011% | 0.055 / 0.023 / 0.006% | 8.5 KB |

Numbers from `make tdigest-bench-full`. Merge −5 / −21 / −21%, Add −1..−2%. vs influx (merging correctly): Merge 6.9 / 6.9 / 8.8× faster, since influx re-sorts its buffer and all centroids with `sort.Sort` when the buffer fills, while ours is one linear pass over two sorted lists. Merged accuracy is mixed: worse around the median (0.055 vs 0.037% normal, 0.12 vs 0.056% lognormal), better at q=0.999 (0.006 vs 0.016%). Likely from compressing on every merge (10 passes here) vs influx's ~2: a one-pass merge of all 10 digests gives 1.4–7.5× lower median error, but higher error at the tails. Tried and rejected: inlining `add` (fits the budget with a `rem` field instead of `w`) made Merge 1.7–2.2× slower; a branchless pick in the merge loop (CSET) made it 45–65% slower, since `add` isn't inlined, so i/j round-trip through the stack and each load waits on the previous compare; building `cum` in the compressor instead of `finish` gave +9 / −12 / 0% (noise, plus a reverse pass for descending flushes).
