# UltraLogLog history

Benchmarks: `make ull-bench` (entry 1 from `make ull-bench-full`), Apple M1, 16-byte keys, sketch filled with 1M keys. ns/op for p = 10 / 14 / 18 (1 KB / 16 KB / 256 KB of registers).
Accuracy: `make ull-accuracy`, 300 sketches of distinct 16-byte keys (1000 from entry 3). Relative RMSE at n = 1k / 40k / 1M, and size after 1M items, at p=14 (stdErr 0.61%) in entries 0–1 and at the default p=13 (stdErr 0.86%) from entry 2.

## 0. Baselines: our hll, axiomhq/hyperloglog v0.3.0 (2026-11-18)

Same number of one-byte registers (HLL stdErr 0.81% at p=14). See `hll/history.md` for their design.

| Impl  | Add             | Count                | Merge same / disjoint                   | RMSE                | Size    |
| ----- | --------------- | -------------------- | --------------------------------------- | ------------------- | ------- |
| hll   | 5.5 / 5.7 / 8.3 | 68 / 63 / 57         | 125 / 1.9k / 30.1k, 724 / 20.2k / 341k  | 0.56 / 0.70 / 0.79% | 16.3 KB |
| axiom | 7.6 / 7.7 / 8.0 | 17.7k / 348k / 4.65M | 510 / 8.0k / 127k, 1.74k / 58.5k / 955k | 0.02 / 0.68 / 0.83% | 16.1 KB |

## 1. UltraLogLog with FGRA estimator

Design: m = 2^p uint8 registers, p in 8..18 (default 14). One wyhash per item; top p bits = register, `k = clz(h<<p | 1<<(p-1)) + 1` (as in hll). Register = `4u + 2·seen(u-1) + seen(u-2)`, 0 = empty (the paper's encoding, without hash4j's 4p−8 offset). Updates go through `unpack` (register → bitmask of seen values, bit 0 always set so pack/unpack are total) and `pack` (bitmask → register), each a few shifts. Estimate: FGRA (hash4j constants), histogram [256]uint32 updated in Add/Merge, Count sums ~4w table-weighted bins with 4 accumulators plus small/large-range terms and one Pow, precomputed m-dependent factor. Add always writes the register and both histogram bins (if unchanged, they cancel out): the early return for unchanged registers was −9% at p=14 but +45% at p=18 (unpredictable branch). Merge: per 8 registers, SWAR test of whether b changes a: u_b > u_a, or d = u_a − u_b ≤ 2 and `(window_b >> d) &^ window_a != 0` with 3-bit windows (u, u−1, u−2), selected per byte by d; only those bytes take the pack/unpack slow path. Tested exhaustively over all register pairs at all byte positions.

| Add             | Count           | Merge same / disjoint                   | RMSE                | Size    |
| --------------- | --------------- | --------------------------------------- | ------------------- | ------- |
| 8.1 / 8.0 / 9.4 | 165 / 161 / 173 | 492 / 7.8k / 125k, 2.90k / 51.2k / 818k | 0.48 / 0.51 / 0.59% | 17.1 KB |

RMSE at n ≥ m is 0.70–0.75× hll's (theory 0.752); 3000 trials give 0.75–0.76/√m asymptotically vs the paper's 0.782/√m, no bias. Small-n rounded bias (e.g. −0.2% at n=100) is integer rounding, as in hll. vs hll: Add +13–47%, Count 2.4–3× slower (176–208 bins + Pow vs 64), Merge 2.4–4× slower. vs axiom: Add +4–18%, Count 100–27,000× faster, Merge same ≈, disjoint −12% / −14% at p=14/18 but +67% at p=10 (histogram updates to a few bins form a dependency chain, as in hll). Size: +768 B over hll for the larger histogram. Merge before the exact SWAR test (only skipping words with x == y or u_b < u_a − 2): same 1.7k / 30.0k / 587k, disjoint 3.06k / 54.5k / 870k.

## 2. Default p=14 → 13

The point of ULL next to our faster hll is memory, so the default now matches hll's accuracy rather than its memory: p=13, 0.86% in 8 KB (+1 KB histogram) vs hll p=14, 0.81% in 16 KB. TestULLAccuracy adds ours at p=13 next to all impls at p=14: within stdErr, and at most 1.2× hll's RMSE at n ≥ 2^14. Add/Count/Merge unchanged (benchmarked per p).

| RMSE p=13           | Size   |
| ------------------- | ------ |
| 0.63 / 0.72 / 0.89% | 9.1 KB |

vs hll p=14 (0.56 / 0.70 / 0.79%, 16.3 KB): −44% memory, RMSE 1.03–1.13× at n ≥ 2^14 (theory 1.106/1.04 = 1.063), 1.13× at n=1k (both collision-dominated there).

## 3. Accuracy with 1000 trials

300 trials gave each RMSE only about ±4%, enough to flip rankings at single points (axiom 0.68 vs hll 0.70% at 40k; 0.68 vs 0.68% over 2000 trials). Now 1000 trials (±2%) via the shared `hllAccuracy` helper in parallel, ~10 s; checks tightened: p=14 at most 0.85× hll (was 0.9×), p=13 at most 1.15× hll (was 1.2×). No code change.

| RMSE p=13           | RMSE p=14           | Size p=13 |
| ------------------- | ------------------- | --------- |
| 0.66 / 0.73 / 0.86% | 0.48 / 0.49 / 0.60% | 9.1 KB    |

vs hll p=14 (0.54 / 0.68 / 0.79%): p=13 1.03–1.10× at n ≥ 2^14 (theory 1.063; was 1.03–1.13× over 300 trials), p=14 0.69–0.77× (theory 0.752).
