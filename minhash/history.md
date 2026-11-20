# MinHash history

Benchmarks: `make minhash-bench` (entry 1 from `make minhash-bench-full`), Apple M1, 16-byte keys. ns/op for k = 64 / 256 / 1024. Add on a sketch filled with 100k items; Similarity of two signatures of 10k-item sets with J=0.5; Merge of two 10k-item sketches (nothing changes after the first loop). Signature at k=256 for n = 16 / 64 / 25,600 (k/16, k/4, 100k).
Accuracy: `make minhash-accuracy`, k=256, 5000 pairs of sets A = S ∪ X, B = S ∪ Y. RMSE of the similarity at J=0.5 (stdErr 3.12%) for u = |A ∪ B| = 16 / 256 / 16,384 (k/16, k, 64k), and size at k=256.

## 0. Baseline: dgryski/go-minhash MinWise (2026-11-20)

Classic MinHash: k uint64 minimums, value i = h1 + i·h2 (two hashes per item, given wyhash with seeds 0 and 1). Add O(k), branchy min. Similarity counts equal positions.

| Add            | Similarity     | Merge          | RMSE                | Size   |
| -------------- | -------------- | -------------- | ------------------- | ------ |
| 30 / 115 / 358 | 24 / 103 / 376 | 32 / 132 / 500 | 3.15 / 3.07 / 3.07% | 2.0 KB |

Matches √(J(1−J)/k) at every u, no bias, except J=0.9 at u=16: 2.24% vs 2.07% (+8%, beyond the ±3% noise), likely h1 + i·h2 values correlating on tiny sets. Its BottomK isn't compared: no deduplication on Push or Merge, and |bottom-k(A) ∩ bottom-k(B)|/k as the estimator, which is biased.

## 1. One permutation hashing with optimal densification

Design: k uint32 buckets, k in [1, 2^16], no default. One wyhash per item; `bucket, lo = Mul64(h, k)` (Lemire reduction, any k), value = lo>>32 capped at MaxUint32−1, bucket keeps the min (branchless `min`). MaxUint32 = empty. Merge: element-wise min. Signature: copy, then each empty bucket i probes `Mul64(splitmix64(i<<32 | try), k)` until a non-empty bucket of the sketch (not the copy) and takes its value (Shrivastava 2017); no work if no bucket or all buckets are filled. Empty set → all-empty signature (similarity 1 with another empty set, 0 with any other). Similarity: share of equal positions.

| Add             | Similarity     | Merge          | Signature k=256   | RMSE                | Size   |
| --------------- | -------------- | -------------- | ----------------- | ------------------- | ------ |
| 5.0 / 5.1 / 5.0 | 25 / 104 / 378 | 28 / 106 / 380 | 6.0k / 1.6k / 197 | 3.65 / 3.08 / 3.11% | 1.0 KB |

vs dgryski: Add 6–70× faster (flat in k), Similarity same (identical loop, ~0.37 ns/value), Merge −14 to −24%, half the memory. Add k=64 has a warmup artifact as the first benchmark of the run (11 → 5.0 ns over the first runs of -count 10). Accuracy (5000 trials, ±1%; 1000 trials gave ±2.2% and a misleading drift between rows): no bias anywhere. With all buckets filled, OPH samples without replacement, so RMSE = stdErr·√((u−k)/(u−1)), checked for u ≥ 4k: 2.84 / 2.99 / 3.11% vs predicted 2.71 / 3.03 / 3.10% at u = 4k / 16k / 64k (at 4k, ~2% of buckets are still empty). For u ≤ k/4, densified copies give up to 1.2× stdErr (J=0.5: 3.65 / 3.54% at u = 16 / 64); at u=k the two effects cancel (3.08%). Signature for small sets costs ≈ (k−f)·k/f probes at ~1.6 ns each (f filled buckets): 6 µs at k=256, n=16; 29 µs at k=1024, n=64; about k²·1.6 ns (~2 ms at k=1024) for a single item.
