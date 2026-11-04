# Count-min sketch history

Benchmarks: `make countmin-bench`, Apple M1, δ=0.01, 16-byte keys. ns/op for ε = 1e-3 / 1e-5 / 1e-6 (ours: 55 KB / 5.4 MB / 54 MB).
Accuracy: `make countmin-accuracy`, 10M events over 1M items, ε=1e-4, δ=0.01. Mean error (estimate − true) over present items for uniform / Zipf 1.1 / Zipf 1.5 (εN = 1000), and size.

## 0. Baseline: tylertreat/BoomFilters (2025-11-17)

Classic d×w, uint64 counters, plain increments. FNV-64 split into two 32-bit halves, row i at (lo + i·hi) mod w.

| Add          | Count         | Error          | Size    |
| ------------ | ------------- | -------------- | ------- |
| 19 / 33 / 98 | 50 / 63 / 133 | 302 / 44 / 0.8 | 1080 KB |

Count is slow because of `math.Min` on float64 per row. Max error on Zipf 1.1 at ε=1e-3: 578k (εN = 10k): some items get the same column in every row, probably when hi ≡ 0 mod w.

## 1. Blocked layout (prototype, rejected)

Like bloom: one 64-byte block of 16 uint32 counters per item, d ≤ 8 counters inside it (4-bit slices of the low 32 hash bits), conservative update. Every op touches one cache line.

Compared against a classic d-rows prototype (also with conservative update): min counters to reach (ε, δ) on 10M events over 1M items, best d in 1..8 per layout. Blocked / classic memory, ε = 1e-3 and 1e-4:

| stream  | δ=0.01    | δ=0.001   |
| ------- | --------- | --------- |
| uniform | 1.0–1.04× | 1.0–1.09× |
| Zipf    | 1.5–2.2×  | 4.1–5.2×  |

Speed (prototypes via interface, d=5, ns/op at 16 KB / 1 MB / 64 MB):

| layout  | Add              | Count           |
| ------- | ---------------- | --------------- |
| classic | 16.6 / 18.2 / 92 | 9.9 / 11.0 / 58 |
| blocked | 13.0 / 13.9 / 46 | 8.4 / 9.1 / 34  |

Blocked is ~20% faster in cache, ~2× at 64 MB, but needs up to 5× the memory on skewed data. A heavy hitter sharing an item's block hits all its counters with probability ~(d/16)^d instead of ~(1/w)^d, and more d doesn't help in 16 slots (best d stays 4–5). Uniform streams don't separate the layouts. Rejected in favor of classic rows. Prototypes removed.

## 2. Classic rows, conservative update

Design: d = ⌈ln 1/δ⌉ ≤ 16 rows of w = ⌈e/ε⌉ uint32 counters in one slice, widened to use allocator slack. One wyhash (seed 0) per item; row i index = top 32 bits of h·salt_i (random odd 64-bit), multiply-shift to w. Conservative update: min over rows, then raise counters to min+n (saturating). Total in uint64.

| Add          | Count         | Error          | Size   |
| ------------ | ------------- | -------------- | ------ |
| 13 / 17 / 80 | 8.7 / 12 / 50 | 139 / 23 / 0.3 | 536 KB |

vs baseline: geomean 21.1 vs 54.6 ns (2.6× faster), half the memory (uint32), 2–2.7× lower mean error (conservative update). Add out of cache is d dependent misses (read pass, then write pass); Count is d misses.
