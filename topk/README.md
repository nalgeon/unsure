# Top-K

A simple and fast Top-K sketch (HeavyKeeper) implemented in Go.

## What is a Top-K sketch

A Top-K sketch finds the k most frequent items in a stream (the "heavy hitters"), along with their approximate counts. It uses a fixed amount of memory based only on k, no matter how many different items are in the stream.

To find the exact top items, you'd have to count every unique item, for example with a map of per-item counts. With millions of distinct items, that takes tens or hundreds of megabytes. A Top-K sketch for the top 1000 items takes about 360 KB, and it usually finds all of them.

A Top-K sketch is useful when you want to identify the "hottest" data: the most requested URLs, the most active users or IP addresses, or the most common search queries. Unlike a [count-min sketch](../countmin/README.md), it keeps the actual items, so it can list them.

The sketch works best when the stream is skewed, meaning a few items appear much more often than the others. This is common in real situations. If all items are about equally frequent, there is no clear top k, and the results become less accurate.

You can add items to the sketch and get the top items with their counts, but you can't decrease counts or check items that aren't in the top k.

## Usage

```
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/topk"

// Track the 2 most frequent items.
sketch := topk.New(2)

sketch.Add([]byte("alice"))
sketch.Add([]byte("bob"))
sketch.Add([]byte("carol"))
sketch.AddN([]byte("alice"), 4)
sketch.AddN([]byte("carol"), 2)

sketch.Top()                  // [{alice 5} {carol 3}]
sketch.Count([]byte("alice")) // 5, true
sketch.Count([]byte("bob"))   // 0, false (not in the top 2)
sketch.Total()                // 9
```

`Top` returns up to k items, from the most frequent to the least frequent. Counts are estimates: they're usually exact for the most frequent items, but may be slightly lower than the actual count for less common items (an item can lose some events while competing for a bucket). Very rarely, a count can be higher, if two items share a bucket and a 32-bit fingerprint. Counts are stored as 32-bit numbers, so if an item is added more than about 4.3 billion times, its count stays at `math.MaxUint32`.

The sketch is deterministic: it hashes items with wyhash final 4.3 (seed 0) and decays buckets with a fixed-seed generator (see below), so the same items added in the same order always produce the same sketch. The sketch is not designed for adversarial input.

## How it works

**HeavyKeeper** is a table of buckets that estimates item counts. It has d rows and w columns, and each item maps to one bucket in each row. Each bucket holds a fingerprint of the item and a count. To add an item, you update its bucket in each row:

- If the bucket is empty or owned by the item, increase its count.
- If the bucket is owned by another item, _decay_ it: decrease its count by 1 with probability b^count (b < 1). When the count reaches 0, the new item takes over the bucket with a count of 1.

```
add x ──► hash ──► fingerprint 7 ──┐
                   │               │
       ┌───────┬───▼───┬───────┬───│───┬───────┐
row 1  │       │ fp=7  │ fp=3  │   │   │ fp=5  │  column 2 is owned by x:
       │       │  41   │   2   │   │   │  980  │  41 → 42
       ├───────┼───────┼───────┼───▼───┼───────┤
row 2  │ fp=9  │       │       │ fp=2  │ fp=4  │  column 4 is owned by another item:
       │  17   │       │       │   3   │   1   │  3 → 2 with probability b^3
       └───────┴───────┴───────┴───────┴───────┘
         col 1   col 2   col 3   col 4   col 5
```

The item's estimated count is the largest count among its buckets. Frequent items quickly reach counts that make decay almost impossible, so they keep their buckets. Rare items keep replacing each other without affecting the frequent ones. Unlike some other Top-K implementations, rare items don't inflate the counts of frequent items.

Next to the table, the sketch keeps a min-heap with the k items that have the largest estimates so far. After an item's buckets are updated, if its estimate is larger than the smallest count in the heap, the item is added to the heap (or its count is increased if it's already there), and the smallest item is removed.

**This implementation** uses d = 2 rows of max(16k, 256) buckets, and a decay base of b = 0.5. With b = 0.5, decay is cheap: the count c decreases if the top c bits of a random number are all zero. Counts of 64 and above never decay. Each bucket is 8 bytes (a 32-bit fingerprint and a 32-bit count), so the table takes 256×k bytes. The heap takes about 100 bytes per item plus the item itself.

The item is hashed once. The hash is mapped to a position in each row by salting it (multiplying by a random per-row constant). The fingerprint is the top 32 bits of the hash. The heap uses the 64-bit hash to identify items, so it doesn't compare the actual item bytes. The heap only copies an item when it moves into the top k.

The original HeavyKeeper uses b = 1.08⁻¹ ≈ 0.93 and more rows. A smaller b lets frequent items lock their buckets faster, which makes the sketch much more precise on flat streams. Two rows are enough to protect frequent items from collisions in a single row, and more rows only make the sketch slower.

The sketch uses [wyhash](https://github.com/wangyi-fudan/wyhash) for hashing and wyrand for random numbers.

## Benchmarks

Compared to [keilerkonzept/topk](https://github.com/keilerkonzept/topk) (HeavyKeeper with b = 0.9, d = ln k rows of k·ln k buckets) and [dgryski/go-topk](https://github.com/dgryski/go-topk) (Filtered Space-Saving). All with default settings. Apple M1, 10M events over 1M distinct 16-byte items with a Zipf distribution.

Precision is the fraction of the reported top k that is in the true top k (higher is better):

| k    | Zipf s |     unsure |    keiler | dgryski |
| ---- | -----: | ---------: | --------: | ------: |
| 100  |    0.6 |   **0.98** |      0.34 |    0.08 |
|      |    0.8 |   **1.00** |      0.76 |    0.42 |
|      |    1.0 |   **0.99** |  **1.00** |    0.73 |
|      |   Size |      37 KB | **27 KB** |   15 KB |
| 1000 |    0.6 |   **0.99** |      0.70 |    0.21 |
|      |    0.8 |   **1.00** |      0.98 |    0.53 |
|      |    1.0 |   **1.00** |  **1.00** |    0.78 |
|      |   Size | **359 KB** |    414 KB |  133 KB |

Count error is the mean of |estimate − true| / true over the reported items (lower is better). It includes the wrongly reported items, so it's high when precision is low. Space-Saving (_dgryski_) overestimates: a new item inherits the count of the item it evicts.

| k    | Zipf s |    unsure | keiler | dgryski |
| ---- | -----: | --------: | -----: | ------: |
| 100  |    0.6 |  **0.6%** |    93% |   1600× |
|      |    0.8 | **0.02%** |    46% |    670× |
|      |    1.0 |  **0.0%** |  0.14% |    420× |
| 1000 |    0.6 |  **0.5%** |    62% |    122× |
|      |    0.8 | **0.06%** |   1.9% |     65× |
|      |    1.0 | **0.02%** |  0.24% |     28× |

Speed on the Zipf s=0.8 stream (lower is better):

| Operation |    k |     unsure |  keiler |     dgryski |
| --------- | ---: | ---------: | ------: | ----------: |
| Add       |   10 |  **75 ns** |  148 ns |      104 ns |
|           |  100 |  **77 ns** |  173 ns |      126 ns |
|           | 1000 |  **77 ns** |  222 ns |      140 ns |
| Top       |   10 |    0.35 µs | 0.64 µs | **0.30 µs** |
|           |  100 | **3.8 µs** |   13 µs |  **3.9 µs** |
|           | 1000 |  **73 µs** |  268 µs |   **73 µs** |

Overall, _unsure_ is 2–3× faster than _keiler_, and finds the true top k with ≥97% precision and counts with <1% error even on flat streams. _keiler_ drops to 34–70% precision with counts off by 62–93%. _dgryski_ is smaller, but misses most of the top items and overestimates their counts by 28× or more.

To reproduce, run `make topk-bench-full` and `make topk-accuracy` from the repository root.

## Papers

[HeavyKeeper: An Accurate Algorithm for Finding Top-k Elephant Flows](https://www.usenix.org/conference/atc18/presentation/gong) (Junzhi Gong et al., 2018): the HeavyKeeper sketch.

[Efficient Computation of Frequent and Top-k Elements in Data Streams](https://doi.org/10.1007/978-3-540-30570-5_27) (Ahmed Metwally, Divyakant Agrawal, Amr El Abbadi, 2005): Space-Saving.
