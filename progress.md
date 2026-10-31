# Bloom filter progress

Benchmarks: `make bloom-bench`, Apple M1, p=0.01, 16-byte keys. ns/op for n = 10k / 1M / 50M.
Size: bits/item for p = 0.1 / 0.01 / 0.001.

## 0. Baseline: bits-and-blooms/bloom v3.7.1

Standard Bloom filter, murmur3 128-bit, k scattered bits per item.

| Add           | HasHit        | HasMiss       | Size             |
| ------------- | ------------- | ------------- | ---------------- |
| 34 / 38 / 178 | 32 / 31 / 131 | 41 / 48 / 115 | 4.8 / 9.6 / 14.4 |

## 1. Split block Bloom filter

- One 64-byte block (cache line) per item, `[]([8]uint64)`; sets 1 bit in each of 8 words (k=8).
- Hash: `maphash` (AES-based, random seed, not serializable), one 64-bit hash per item.
  Block from high 32 bits via multiply-shift; bit positions from low 32 bits × 8 odd salts (Parquet).
- Has: branch-free AND of 8 shifted words.
- Sizing: exact FP rate for blocked layout (Poisson block loads), bisect for max load per block.

| Add          | HasHit        | HasMiss       | Size              |
| ------------ | ------------- | ------------- | ----------------- |
| 10 / 12 / 57 | 9.4 / 11 / 44 | 9.4 / 11 / 40 | 5.9 / 10.1 / 15.7 |

~3.3× faster; +5% memory at p=0.01, +23% at p=0.1 (fixed k=8 too high).
FP rate on target (0.0099 at p=0.01, incl. n=50M).
Profile (10k): maphash ~40%, Has ~30%.
Open: TestSize fails at n=1000/p=0.1 (1.28× > 1.25×).

## 2. Variable k, bits anywhere in a 512-bit block

- k bits anywhere in the 512-bit block (9-bit positions from low 32 bits × salt), k ∈ 1..16.
  Modeled better than split blocks with k words (4.83 vs 4.90 bits/item at p=0.1).
- NewSet picks k that maximizes items per block for fpProb: p=0.1 → k=3, 0.01 → 6, 0.001 → 9.
- Has stays branch-free (AND over k shifted words); no bounds check on in-block word index.
- NewSet cost: ~0.1–0.2 ms (sizing math, stops k search once load drops).

| Add           | HasHit         | HasMiss        | Size                |
| ------------- | -------------- | -------------- | ------------------- |
| 9.3 / 10 / 50 | 8.3 / 9.1 / 40 | 8.6 / 9.4 / 38 | 4.83 / 9.90 / 15.49 |

~3.7× faster than baseline, ~10% faster than iteration 1 (k=6 vs 8 at p=0.01).
Memory vs baseline: +1% / +3% / +8%. TestSize passes (worst: n=1000/p=0.1, 1.07×, block rounding).
Limit: 512-bit block hurts very low p (1e-6 → k=16, 38.5 bits/item vs 28.8 standard).
