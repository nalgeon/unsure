# Bloom filter history

Benchmarks: `make bloom-bench`, Apple M1, p=0.01, 16-byte keys. ns/op for n = 10k / 1M / 50M.
Size: bits/item for p = 0.1 / 0.01 / 0.001.

## 0. Baseline: bits-and-blooms/bloom v3.7.1

Standard Bloom filter, murmur3 128-bit, k scattered bits per item.

| Add           | ContainsHit   | ContainsMiss  | Size             |
| ------------- | ------------- | ------------- | ---------------- |
| 34 / 38 / 178 | 32 / 31 / 131 | 41 / 48 / 115 | 4.8 / 9.6 / 14.4 |

## 0b. Second baseline: jcalabro/gloom v0.1.0

Added later, for comparison. Blocked like ours: 512-bit blocks, xxh3-128 (unseeded),
k positions = hash mod k distinct primes (hardware divide), early-exit Test.

| Add          | ContainsHit  | ContainsMiss | Size                 |
| ------------ | ------------ | ------------ | -------------------- |
| 17 / 19 / 68 | 16 / 18 / 61 | 27 / 33 / 61 | 5.39 / 10.79 / 16.18 |

Overshoots on FP (0.0078 / 0.0072 / 0.00087 at 100k), so it spends memory it doesn't need.
At iteration 3, ours is ~2× faster (geomean 14.9 vs 30.2 ns) and 9–11% smaller.

## 1. Split block Bloom filter

One 64-byte block (cache line) per item, `[]([8]uint64)`; sets 1 bit in each of 8 words (k=8).

Hash: `maphash` (AES-based, random seed, not serializable), one 64-bit hash per item. Block from high 32 bits via multiply-shift; bit positions from low 32 bits × 8 odd salts (Parquet).

Contains: branch-free AND of 8 shifted words.

Sizing: exact FP rate for blocked layout (Poisson block loads), bisect for max load per block.

| Add          | ContainsHit   | ContainsMiss  | Size              |
| ------------ | ------------- | ------------- | ----------------- |
| 10 / 12 / 57 | 9.4 / 11 / 44 | 9.4 / 11 / 40 | 5.9 / 10.1 / 15.7 |

~3.3× faster; +5% memory at p=0.01, +23% at p=0.1 (fixed k=8 too high).
FP rate on target (0.0099 at p=0.01, incl. n=50M).
Profile (10k): maphash ~40%, Contains ~30%.
Open: TestSize fails at n=1000/p=0.1 (1.28× > 1.25×).

## 2. Variable k, bits anywhere in a 512-bit block

k bits anywhere in the 512-bit block (9-bit positions from low 32 bits × salt), k ∈ 1..16.
Modeled better than split blocks with k words (4.83 vs 4.90 bits/item at p=0.1).

New picks k that maximizes items per block for fpProb: p=0.1 → k=3, 0.01 → 6, 0.001 → 9.

Contains stays branch-free (AND over k shifted words); no bounds check on in-block word index.

New cost: ~0.1–0.2 ms (sizing math, stops k search once load drops).

| Add           | ContainsHit    | ContainsMiss   | Size                |
| ------------- | -------------- | -------------- | ------------------- |
| 9.3 / 10 / 50 | 8.3 / 9.1 / 40 | 8.6 / 9.4 / 38 | 4.83 / 9.90 / 15.49 |

~3.7× faster than baseline, ~10% faster than iteration 1 (k=6 vs 8 at p=0.01).

Memory vs baseline: +1% / +3% / +8%. TestSize passes (worst: n=1000/p=0.1, 1.07×, block rounding).

Limit: 512-bit block hurts very low p (1e-6 → k=16, 38.5 bits/item vs 28.8 standard).

## 3. Use allocator slack as extra blocks

Go rounds allocations up (size classes; whole 8KB pages above 32KB). Build blocks with `append([]block(nil), make([]block, n)...)` so cap reports the rounded size, and use all of it.

TestSize now measures real allocation (MemStats.TotalAlloc), not self-reported Size().

| Add           | ContainsHit    | ContainsMiss   | Size                |
| ------------- | -------------- | -------------- | ------------------- |
| 9.1 / 10 / 40 | 8.4 / 9.2 / 37 | 8.4 / 9.2 / 38 | 4.84 / 9.90 / 15.49 |

Speed: from `make bloom-bench-full` (±0–6%); ~3.8× faster than baseline (geomean 14.7 vs 56.4 ns).

Memory unchanged; FP rate lower for free where rounding was large: n=100k → 0.0834 / 0.0078 / 0.00088 (was ~0.1 / 0.01 / 0.001). Only 64 bytes (the struct) of allocation now unused.

## 4. Pure-Go wyhash instead of maphash (reverted)

Hypothesis: maphash is mostly call overhead for short keys. Wrong: on arm64 it's a tight AES asm routine. 16-byte keys: maphash 3.6 ns, wyhash (pure Go, not inlinable) 4.3 ns.

Contains got ~1 ns slower (8.4 → 9.6 at n=10k). Reverted to maphash.

## 5. xxh3 instead of maphash

`zeebo/xxh3` `HashSeed` with a random per-set seed (still not saved; but xxh3 has a fixed spec, so save/load becomes possible). Pure Go on arm64; 16-byte keys: 3.0 ns vs maphash 3.6 ns.

| Add           | ContainsHit    | ContainsMiss   | Size                |
| ------------- | -------------- | -------------- | ------------------- |
| 8.8 / 10 / 41 | 8.3 / 9.2 / 38 | 8.3 / 9.3 / 38 | 4.84 / 9.90 / 15.49 |

A/B, `-count 8`: geomean −0.65% (Add −3–4% in-cache; Contains at 50M +2–4%). Speed-neutral.

## 6. In-house wyhash instead of xxh3

Drop the `zeebo/xxh3` dependency (and its `cpuid` indirect dep). ~50-line `wyhash.go`: same algorithm as Go's runtime fallback hash, fixed public secrets + per-set seed, so output is stable for save/load.

Isolated, latency-bound 16-byte hash: maphash 3.8 ns, wyhash 5.4, xxh3 6.9. Inside Add/Contains (throughput-bound) wyhash still comes out ahead of xxh3.

| Add            | ContainsHit    | ContainsMiss   | Size                |
| -------------- | -------------- | -------------- | ------------------- |
| 8.4 / 9.7 / 40 | 7.8 / 8.8 / 37 | 7.8 / 8.6 / 36 | 4.84 / 9.90 / 15.49 |

A/B vs xxh3, `-count 8`: geomean −3.5% (in-cache −4–5%, ContainsMiss −6% at 1M/50M; rest ~). Size, FP unchanged.

## 7. Exact upstream wyhash (final 4.3)

Iteration 6 was Go's runtime variant (`memHashFallback`), not upstream wyhash: no spec, no test vectors. Now a direct port of final 4.3 with default secrets, checked against upstream outputs (`test_vector.cpp` + every length 0..100). Save/load can say "wyhash final 4.3, seed s".

Seed-only step (`seed ^= wymix(seed^s0, s1)`) split into `wyseed`, done once in New.

Literal port (four 4-byte reads for keys ≤16 bytes) was +14% geomean (+1.8 ns in-cache).
Same words from two 8-byte reads (rotate / combine halves; n=16 has its own layout) → parity.

| Add            | ContainsHit    | ContainsMiss   | Size                |
| -------------- | -------------- | -------------- | ------------------- |
| 8.3 / 9.6 / 41 | 7.9 / 8.6 / 37 | 7.9 / 8.7 / 38 | 4.84 / 9.90 / 15.49 |

A/B vs iteration 6, `-count 8`: geomean −1.0% (all within ±3%). Hash alone, lengths 3..64: ±6%, geomean +0.9%.

## 8. Add returns "was new" flag, like TestAndAdd (reverted)

Idea: `Add` returns `!Contains` from before the call, so dedup needs one call instead of `Contains` + `Add` (instead of a separate method). Costs 2 extra instructions per probe (`LSR`, `AND`) on a word that is already loaded.

In-cache Add looks bound by loop instruction count: k=6 → +12 instructions ≈ +1.1 ns. Add isn't inlined, so callers that ignore the flag pay too. `m |= bit &^ w`: same cost. Read pass then write pass: +47%.

Add, interleaved A/B, `-count 8`: 8.5 / 9.8 / 40.7 → 9.5 / 11.0 / 42.2 (+12% / +12% / +3%). Contains unchanged. Reverted. If needed, add it as a separate method (9.5 ns vs ~16.5 for Contains + Add).

## 9. Fixed default seed, Options

The random seed per set gave no protection: with WYHASH_CONDOM=1, keys built so that `x^wyp1 == 0` (≤16 bytes), or that zero the lanes on the first round (>16 bytes), hash the same under every seed. But it did cost reproducibility and merging. Common practice for persisted filters (Parquet, RocksDB, bits-and-blooms) is a fixed, specified hash with no adversarial claim.

Now: seed 0 by default (so the filter is plain "wyhash final 4.3, seed 0"), `NewWith(n, p, Options{Seed})` to override it. Zero Options means default. Docs say the filter isn't designed for adversarial keys.

| Add            | ContainsHit    | ContainsMiss   | Size                |
| -------------- | -------------- | -------------- | ------------------- |
| 8.5 / 9.6 / 43 | 8.0 / 8.9 / 38 | 7.8 / 8.6 / 45 | 4.84 / 9.90 / 15.49 |

Hot path unchanged (single `make bloom-bench` run; differences are noise).

## 10. Faster sizing in New

New took 100–220 µs at any n: `params` tried every k from 1 up, and bisected each load 40 times from [0, 1024], with a few hundred Poisson terms per step (280 `fpRate` calls at p=0.01, 640 at p≤1e-6).

Now: start k at ⌊log2(1/p)⌋ (the standard filter optimum; ours is 3/6/9 for p=0.1/0.01/0.001) and walk toward the peak. Bracket the load from the standard filter's bits/item (an upper bound, since blocking only raises the FP rate) and bisect to relative 1e-6. ~21 calls per k, ~3 k values. Same k as an exhaustive search at all p tested (TestParams); load never above it, within 1e-6.

New BenchmarkBloomNew (n=10 / 10k / 1M, p=0.01): 125 / 127 / 178 µs → 28 / 29 / 83 µs. bitsblooms (closed form): 0.075 / 0.87 / 51 µs; gloom: 142 / 1198 / ~1200 µs.

| Add            | ContainsHit    | ContainsMiss   | Size                |
| -------------- | -------------- | -------------- | ------------------- |
| 8.5 / 9.7 / 38 | 8.0 / 8.8 / 34 | 8.0 / 8.8 / 34 | 4.84 / 9.90 / 15.49 |

Hot path untouched. Benchmarks from `make bloom-bench-full` (`-count 10`). Geomean vs bitsblooms / gloom: 13.7 vs 57.2 / 29.9 ns (4.2× / 2.2× faster). Further options if needed: a faster root finder (~5 calls per k) or a shorter Poisson sum (currently up to load + 12σ).
