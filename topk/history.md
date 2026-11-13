# Top-K history

Benchmarks: `make topk-bench`, Apple M1, 16-byte keys, Zipf s=0.8 stream (sketch warmed up with 1M events). ns/op for Add and µs/op for Top at k = 10 / 100 / 1000.
Accuracy: `make topk-accuracy`, 10M events over 1M items, Zipf with shuffled ranks. p = precision of the reported top k (fraction whose true count ≥ the true k-th count, so boundary ties don't count as misses), at s = 0.6 / 0.8 / 1.0 for k=1000, and retained size at k=1000.

## 0. Baselines (2026-11-09)

**keilerkonzept/topk** (the reference): HeavyKeeper, decay b=0.9, d = max(3, ln k) rows of w = max(256, k·ln k) buckets, min-heap of k with a map index. Random decay via math/rand.

| Add             | Top             | p                  | Size   |
| --------------- | --------------- | ------------------ | ------ |
| 148 / 173 / 222 | 0.64 / 13 / 268 | 0.70 / 0.98 / 1.00 | 414 KB |

Precision at k=100: 0.34–0.41 / 0.76–0.79 / 1.00 (it isn't deterministic, so results vary between runs). Mean count error |estimate − true| / true over the reported items, k=1000: 62% / 1.9% / 0.24%; at k=100: 93% / 46% / 0.14% (includes the wrongly reported items).

**dgryski/go-topk**: Filtered Space-Saving, m = k monitored items, 6k alpha counters.

| Add             | Top             | p                  | Size   |
| --------------- | --------------- | ------------------ | ------ |
| 104 / 126 / 140 | 0.30 / 3.9 / 73 | 0.21 / 0.53 / 0.78 | 133 KB |

With m = k, the tail evicts real top items all the time. Counts are overestimates by 28×–10000× (they inherit the evicted item's count).

## 1. Space-Saving (prototype, rejected)

The original plan: Space-Saving (Metwally 2005) with m = c·k counters in a min-heap plus a hash index. On a miss, evict the minimum and inherit its count as the error. Guarantees: error ≤ N/m, every item with count > N/m is reported. Simple, deterministic, mergeable.

Measured c = 1..8. Even at c = 8, p ≥ 0.9 only for s ≥ 1.0. At s=0.8: p = 0.20 / 0.47 / 0.65 for k = 10 / 100 / 1000. The N/m guarantee is weak: at s=0.8 over 1M items, the k-th count is far below N/m for any reasonable m, so the bottom of the heap is a pool of inherited counts from the tail.

Slow at low skew: 100–210 ns/op, since every tail item is a miss that rewrites the heap root, the map entry and the item buffer. ~100 B per monitored item, so c = 8 already takes ~800 B per k. Rejected: both slower and less precise than the reference.

Also rejected without prototyping:

- Count-min + heap: count-min overestimates (by ε·N), so on flat streams tail items look like top items. Same failure mode as Space-Saving, plus d hashes per add.
- Stream-Summary (Space-Saving with bucket linked lists, O(1) updates): pointer chasing and allocation per bucket. The heap is simpler and the root replacement cost is the same.

## 2. Filtered Space-Saving (prototype, rejected)

FSS (Homem & Carvalho 2010): a filter of alpha counters (hashed) in front of Space-Saving. A miss only enters the monitored set if its alpha counter reaches the set's minimum, so most tail items never touch the heap.

Best variant: m = 2k monitored, 64k alphas. p = 1.00 at s ≥ 0.8, 33–94 ns/op, 445 KB at k=1000. But it collapses at s=0.6: p = 0.20 / 0.57 / 0.95 at k = 10 / 100 / 1000, 100–130 ns/op. Cause: alphas are shared by many tail items and also inherit evicted counts, so on a flat stream they rise to the heap minimum, and tail noise churns the heap. More alphas only delay it. Rejected for the low-skew collapse.

## 3. HeavyKeeper

HeavyKeeper (Gong et al. 2018): d rows of (fingerprint, count) buckets. If a bucket is owned by another item, decay its count with probability b^count, and take it over at 0. Estimate = max over owned buckets. A min-heap of k items with estimates. Counts only include the item's own events (never overestimate, except fingerprint collisions), and frequent items lock their buckets, so tail noise can't reach the heap.

Shape (decay 0.5, averaged over 3 seeds, precision across s = 0.6..1.0):

- 1 row: fastest (~45 ns), but a top item that collides with another top item in its only row loses events. p = 0.90–0.96 at s=1.0.
- 2 rows: p ≥ 0.97 everywhere.
- 3–4 rows: no precision gain, slower (one more random memory access per row).
- Width: 16k buckets per row (256 B per k for 2 rows). Narrower loses precision at s=0.6, wider gains nothing. Min width 256 barely matters, but it's cheap for small k.

Decay base b (2×16k, 3 seeds). p at s=0.6 for k = 10 / 100 / 1000:

| b   | p                     |
| --- | --------------------- |
| 0.5 | 1.000 / 0.977 / 0.990 |
| 0.6 | 0.933 / 0.970 / 0.983 |
| 0.7 | 0.900 / 0.923 / 0.978 |
| 0.8 | 0.833 / 0.873 / 0.959 |

At s ≥ 0.8 all b give p ≥ 0.967. Smaller b locks buckets sooner, so at low skew a top item keeps its bucket against the tail. This is why the reference (b = 0.9) drops to 0.34–0.70 at s=0.6. Speed is ~44–72 ns for all b. Chose b = 0.5: the most precise, and the decay check is just "top c bits of a random uint64 are zero" (no pow or lookup table). Counts ≥ 64 never decay.

Design:

- Two rows of max(16k, 256) 8-byte buckets (uint32 fingerprint, uint32 count) in one slice, widened to use allocator slack.
- One wyhash (seed 0) per item. Row index from h·salt_i via multiply-shift. Fingerprint = h>>32.
- Decay with wyrand, fixed seed (deterministic). AddN with n > 1 samples the attempts until each decrement from a geometric distribution, so it's O(64) instead of O(n).
- Heap: nodes (count, slot) with the count inline, so sifts don't touch the slots. Slots (hash, pos, item) never move. Index map[uint64]uint32 from item hash to slot, so lookups don't compare bytes. On replace, the evicted slot's buffer is reused, so Add doesn't allocate in steady state.
- Skip the heap entirely if the estimate ≤ the heap minimum (the common case for tail items): one hash, two bucket accesses, one compare.
- An item already in the heap keeps its largest estimate (its buckets may decay, but the true count never does).

| Add          | Top             | p                  | Size   |
| ------------ | --------------- | ------------------ | ------ |
| 75 / 77 / 77 | 0.35 / 3.8 / 73 | 0.99 / 1.00 / 1.00 | 359 KB |

p at k=10 / 100: 1.00 / 0.98 at s=0.6, 1.00 / 1.00 at s=0.8, 1.00 / 0.99 at s=1.0. Mean count error ≤ 0.6%.

vs keiler: 2–3× faster Add (at k=1000, 2.9×), 3.6× faster Top, 13% less memory at k=1000 (but 37 vs 27 KB at k=100: our width is 16k, theirs k·ln k), and p ≥ 0.98 at s=0.6 where keiler gets 0.34–0.70. Top is dominated by the sort and the k string allocations.

## 4. Three rows at equal memory (rejected, 2026-11-10)

With 2 rows, an item is locked out for good if both its buckets are owned by heavier items (counts ≥ 64 never decay), e.g. rank 40 of 100 (17k events) missing at s=1.0. Tried 3 rows of 32k/3 buckets (same memory). 20 stream seeds. Lockouts = missing true top-k items with count ≥ 1.05× the k-th:

| s / k      | p (2 rows) | p (3 rows) | err (2 → 3)    | lockouts (2 → 3) |
| ---------- | ---------- | ---------- | -------------- | ---------------- |
| 0.6 / 100  | 0.988      | 0.982      | 0.60 → 0.91%   | 5 → 3            |
| 0.6 / 1000 | 0.990      | 0.989      | 0.55 → 0.69%   | 67 → 24          |
| 0.8 / 100  | 0.999      | 1.000      | 0.02 → 0.03%   | 2 → 1            |
| 0.8 / 1000 | 0.997      | 0.999      | 0.07 → 0.07%   | 58 → 10          |
| 1.0 / 100  | 0.997      | 0.999      | 0.002 → 0.002% | 3 → 1            |
| 1.0 / 1000 | 0.998      | 1.000      | 0.017 → 0.013% | 37 → 13          |

3 rows cut lockouts 2.5–6×, but narrower rows get more tail traffic, so flat-stream count error and precision get worse. Add 75 / 77 / 76 → 97 / 98 / 93 ns (+23–29%), Top unchanged. Kept 2 rows: the precision gain is ≤ 0.2 points, not worth a quarter of Add speed.

## 5. Add benchmark without key-fetch misses (2026-11-10)

The Add benchmark looked up `st.keys[st.events[i]]`: 1M separately allocated strings in random Zipf order, so two cache misses per op (string header, then data) that every implementation paid. Now the keys of events 1M–2M are laid out in event order in one 16 MB string and read sequentially. Reference re-recorded. Sketch code unchanged.

| Add (old → new) | k=10     | k=100    | k=1000    |
| --------------- | -------- | -------- | --------- |
| unsure          | 75 → 30  | 77 → 29  | 77 → 30   |
| keiler          | 149 → 64 | 171 → 84 | 222 → 130 |
| dgryski         | 110 → 58 | 146 → 71 | 124 → 72  |

vs keiler: 2.1 / 2.9 / 4.4× faster Add (was 2.0 / 2.2 / 2.9×). Top unchanged. Earlier entries' Add numbers include ~45 ns of harness overhead; e.g. entry 4's 3-row cost (+~20 ns) is about +70% of the sketch's own time, not +25%.

## 6. Decay base 0.8 via threshold table (2026-11-13)

With b = 0.5, a bucket owned for a long time by a moderately frequent item becomes permanent (decay 2^-c ≈ 1e-6 at c = 20), so an item that becomes frequent late in the stream never gets counted, even if it ends up #2. E.g. k=100, Zipf s=1.0 warmup, then a new item at 1% of traffic for 2M events: missed in 5/5 seeds after a 1M warmup; keiler (b = 0.9) found it every time. New `TestTopKDrift`: after half the stream, k/10 new items with 2× the k-th count each. Fraction found (k = 10 / 100 / 1000) and p at s=0.6:

| b               | found s=0.8        | found s=1.0        | p at s=0.6         | count err s=0.6 |
| --------------- | ------------------ | ------------------ | ------------------ | --------------- |
| 0.5             | 1.00 / 0.60 / 0.55 | 0.00 / 0.40 / 0.52 | 1.00 / 0.98 / 0.99 | ≤ 0.6%          |
| 0.7             | 1.00 / 0.90 / 0.82 | 1.00 / 0.50 / 0.70 | 0.90 / 0.95 / 0.98 | 1–9%            |
| 0.8             | 1.00 / 1.00 / 0.94 | 1.00 / 0.70 / 0.75 | 0.80 / 0.90 / 0.97 | 2–45%           |
| 0.85            | 1.00 / 1.00 / 0.93 | 1.00 / 0.80 / 0.81 | 0.80 / 0.85 / 0.95 | 4–72%           |
| 0.9             | 1.00 / 1.00 / 0.97 | 1.00 / 1.00 / 0.88 | 0.80 / 0.84 / 0.92 | 8–85%           |
| 0.5, floor 1/16 | 1.00 / 0.90 / 0.85 | 1.00 / 0.70 / 0.74 | 1.00 / 0.97 / 0.98 | 8–39%           |
| keiler (0.9)    | 1.00 / 1.00 / 1.00 | 1.00 / 1.00 / 1.00 | 0.80 / 0.34 / 0.70 | 61–94%          |

At s ≥ 0.8, p ≥ 0.99 for all b. A floor on the probability (max(2^-c, p_min)) doesn't help: taking over a bucket costs ~c/p_min attempts, and old owners have large c, while the floor erodes every owner's count. Chose b = 0.8: a general-purpose sketch shouldn't silently miss its #2 item, while the flat-stream misses are near-ties at the k-th count.

Decay check is now `rand() < thr[c]` with thr[c] = b^c·2^64 precomputed (199 entries, 0 beyond: counts ≥ 199 never decay), and ln(1 − b^c) precomputed for AddN. Same speed as the bit check at b = 0.5 (28.7 / 28.8 / 30.0 vs 29.1 / 28.8 / 29.8 ns in one session).

Add 24 / 25 / 33 ns (b = 0.5: 29 / 29 / 30; the dynamics change, not the check), Top 0.34 / 3.7 / 75 µs. Precision s=0.6 / 0.8 / 1.0 at k=1000: 0.97 / 1.00 / 1.00, k=100: 0.90 / 1.00 / 0.99. Size unchanged (364 KB at k=1000).
