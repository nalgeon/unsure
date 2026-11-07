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

## 2. Merge 8 registers at a time, realistic Merge benchmark

Benchmark: the old Merge bench merged the same sketch repeatedly, so after the first loop no register grew (case=same). Added case=disjoint: merge into fresh copies of a filled sketch (prepared untimed in batches; per-loop StopTimer stops the world), rotating between 8 different others (with a single other, the branch predictor learned the growth pattern even at p=14: old Merge 35 µs vs 62 µs rotated). axiom/boom Merge reference re-recorded; entries 0–1 Merge = case=same.

Design: Merge compares 8 registers per uint64. Registers are < 128, so `^((x|0x80…) − y) & 0x80…` flags bytes where other > s without cross-byte borrow; skip the word if none, else update the histogram per flagged byte and blend with `(gt>>7)*0xff`. m is a power of two ≥ 16, so no tail. Tested against per-byte max with 0 and 65-p values.

Merge same / disjoint, ns/op at p = 10 / 14 / 18 (Add, Count, RMSE, size unchanged):

| Merge same         | Merge disjoint     |
| ------------------ | ------------------ |
| 125 / 1.9k / 30.1k | 730 / 20.1k / 341k |

vs old Merge: same 3.1× faster at all p; disjoint 1.2× / 3.1× / 3.0× (old: 0.90k / 62k / 1.03M). vs axiom: same −76%, disjoint −57% / −66% / −64% (axiom 1.71k / 58.6k / 951k; old Merge was +6% / +8% slower than axiom at p=14/18 in this case). At p=10 the histogram updates take about half of the disjoint time (350 ns without them): updates to the same few bins form a dependency chain.

## 3. minP 4 → 8

Measured with 10k trials (random hashes): below p=8 the docs' "RMSE at most stdErr, unbiased for any n" was false. RMSE at large n: p=4 27–28.5% vs 26.0% (1.04–1.09×), p=5 1.02–1.04×, p=6 1.01–1.02×, p=7 ≈1.00×, p=8 0.99×. The α∞/(1+1.079/m) correction is a constant factor (−6.3% at p=4), but the α∞ bias isn't constant (p=4: +3.4% at n ≤ 8, +7.3% at large n), so it fixes large n (+0.5%, not 0.3% as in entry 1) and flips small n to −3.1%. p=6: +0.9% → −0.8% small n, +1.7% → ±0.1% large n. p=8: +0.2% → −0.2%, +0.5% → ±0.1%.

Design: minP = 8 instead of tuning an n-dependent correction. Registers below p=8 saved at most 240 bytes next to the fixed 256-byte histogram, and precision() already clamps, so New(stdErr > 0.065) just gets p=8 (256 B). Correction kept (halves the bias at p=8). TestAccuracy now runs p=8/10/14. Add/Count/Merge, RMSE and size at p=14 unchanged.

Point 3 of the same review: Size() now counts the 256-byte histogram (2^p + 256; it's half the sketch at p=8). TestAccuracy adds p=18 (n up to 1M ≈ 4m). Re-measured the small-n rounding bias from entry 1 (20k trials): worst −2.9% at n=16 for p=8, −1.5% at n=32 for p=10, −0.4% at n=128 for p=14, always under 0.5 in absolute terms. The α correction's part of it is −0.24% at p=8 and ≈0 at p ≥ 14, so it's almost all integer rounding.

## 4. New() and NewPrecision(p), seed removed

API review across bloom/countmin/hll. `New(stdErr)` → `New()` (p=14, the default in Redis and axiomhq) plus `NewPrecision(p)`, p in [8, 18]. Precision only comes in a few values (`New(0.01)` gave 0.81%, `New(0.005)` 0.41% at 4× the memory), so a stdErr parameter suggested a continuous dial that doesn't exist. Merging needs equal p, which is now explicit. The package docs have a p → stdErr/size table. `precision()` removed.

Seed removed like in bloom/countmin: no `Options`/`NewWith`/`ErrSeedMismatch`, always seed 0 premixed as `wyhash.Seed0`. Merge only checks p.

| Add             | Count        | Merge same         | Merge disjoint     |
| --------------- | ------------ | ------------------ | ------------------ |
| 6.8 / 5.6 / 8.3 | 69 / 62 / 57 | 124 / 1.9k / 29.8k | 718 / 19.6k / 335k |

Single `make hll-bench` run, same as entries 2–3 within noise. RMSE and size unchanged (same hashing).
