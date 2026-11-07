# HyperLogLog history

Benchmarks: `make hll-bench` (entries 0–1 from `make hll-bench-full`), Apple M1, 16-byte keys, sketch filled with 1M keys. ns/op for p = 10 / 14 / 18 (1 KB / 16 KB / 256 KB of registers).
Accuracy: `make hll-accuracy`, p=14 (stdErr 0.81%), 100 sketches of distinct 16-byte keys. Relative RMSE at n = 1k / 40k (2.4m) / 1M, and size after 1M items.

## 0. Baseline: axiomhq/hyperloglog v0.3.0 (2026-11-07)

8-bit registers, LogLog-Beta estimator (per-precision polynomial), sparse mode (25-bit hashes in a compressed list) until the list outgrows m. MetroHash64. Count scans all registers.

| Add             | Count                | Merge             | RMSE                | Size    |
| --------------- | -------------------- | ----------------- | ------------------- | ------- |
| 7.6 / 7.6 / 7.9 | 18.3k / 350k / 4.71M | 509 / 7.9k / 127k | 0.02 / 0.71 / 0.89% | 16.1 KB |

Sparse mode makes it nearly exact and tiny (1.2 KB) below ~m items. Count scans all registers with `1/math.Pow(2, v)` each, ~20 ns per register.

Also compared: tylertreat/BoomFilters, classic HLL with 32-bit FNV. Add 24–27 ns, Count/Merge ≈ axiom. Badly biased around 1–2.5m (+40–50%) and +10% at 1M, likely from the 32-bit FNV hash on sequential keys.

## 1. Dense registers, Ertl's improved estimator

Design: m = 2^p uint8 registers, p = ⌈log2((1.04/stdErr)²)⌉ in 4..18. One wyhash per item; top p bits = register, `rank = clz(h<<p | 1<<(p-1)) + 1` (branchless, capped at 65-p). Estimate from the register histogram with Ertl's improved estimator (σ/τ series, no linear counting switch, no bias tables), α∞ replaced by α∞/(1+1.079/m): bias at p=4 +7% → 0.3%, p=6 +1.7% → 0.1%, negligible at p ≥ 10. Histogram [64]uint32 updated in Add/Merge only when a register grows, so Count is O(64). Merge: per-byte max + histogram update; error on precision/seed mismatch. No sparse mode.

| Add             | Count        | Merge              | RMSE                | Size    |
| --------------- | ------------ | ------------------ | ------------------- | ------- |
| 5.5 / 5.6 / 8.1 | 68 / 62 / 57 | 381 / 5.9k / 94.1k | 0.63 / 0.64 / 0.79% | 16.3 KB |

vs axiom: Add −27% at p=10/14, +3% at p=18; Count 270–83,000× faster (axiom scans all registers with `math.Pow`, ours reads 64 histogram entries); Merge −26%; equal accuracy at n ≥ m (RMSE ≈ 1.0× stdErr asymptotically, measured with 3000 trials). Costs: +256 bytes for the histogram, and 16 KB from the first item where axiom's sparse mode takes ~1 KB. Integer rounding of Count gives up to −4.6% relative bias at tiny n (e.g. n=7, p=6), at most 0.5 in absolute terms.
