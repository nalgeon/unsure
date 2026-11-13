# Top-K

A simple and fast Top-K sketch (HeavyKeeper) implemented in Go.

## What is a Top-K sketch

A Top-K sketch finds the _k most frequent_ items in a stream (the "heavy hitters"), along with their approximate counts. It uses a fixed amount of memory based only on k, no matter how many different items are in the stream.

To find the exact top items, you'd have to count every unique item, for example with a map of per-item counts. With millions of distinct items, that takes tens or hundreds of megabytes. A Top-K sketch for the top 1000 items takes about 360 KB, and it usually finds all of them.

A Top-K sketch is useful when you want to identify the "hottest" data: the most requested URLs, the most active users, or the most common search queries. Unlike a [count-min sketch](../countmin/README.md), it keeps the actual items, so it can list them.

The sketch works best when the stream is skewed, meaning a few items appear much more often than the others. This is common in real situations. If all items are about equally frequent, there is no clear top k, and the results become less accurate.

You can add items to the sketch and get the top items with their counts, but you can't decrease counts or check items that aren't in the top k.

## Usage

```text
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
sketch.Count([]byte("alice")) // 5
sketch.Count([]byte("bob"))   // 0 (not in the top 2)
sketch.Total()                // 9
```

`Top` returns up to k items, from the most frequent to the least frequent. Counts are estimates: they're usually exact for the most frequent items, but may be slightly lower than the actual count for less common items. Very rarely, a count can be higher. Counts are stored as 32-bit numbers, so if an item is added more than about 4.3 billion times, its count stays at `math.MaxUint32`.

The sketch is deterministic, so the same items added in the same order always produce the same sketch. The sketch is not designed for adversarial input.

## How it works

**HeavyKeeper** is a table of buckets that estimates item counts. It has _d_ rows and _w_ columns, and each item maps to one bucket in each row. Each bucket holds a fingerprint of the item and a count. To add an item, you update its bucket in each row:

- If the bucket is empty or owned by the item, increase its count.
- If the bucket is owned by another item, _decay_ it: decrease its count by 1 with probability b^count (b < 1). When the count reaches 0, the new item takes over the bucket with a count of 1.

```text
          ┌──────┐
add x ──► │ hash │──► h ──► high 32 bits ──► fingerprint = 7
          └──────┘    │
                      │ map to buckets
                   ┌──┴────────────┐
       ┌───────┬───▼───┬───────┬───│───┬───────┐
row 1  │       │ fp=7  │ fp=3  │   │   │ fp=5  │  column 2 is owned by x:
       │       │  41   │   2   │   │   │  980  │  41 → 42
       ├───────┼───────┼───────┼───▼───┼───────┤
row 2  │ fp=9  │       │       │ fp=2  │ fp=4  │  column 4 is owned by another:
       │  17   │       │       │   3   │   1   │  3 → 2 with probability b^3
       └───────┴───────┴───────┴───────┴───────┘
         col 1   col 2   col 3   col 4   col 5
```

The item's estimated count is the largest count among its buckets. Frequent items quickly reach counts that make decay very unlikely, so they keep their buckets. Rare items keep replacing each other without affecting the frequent ones. An item that becomes frequent later in the stream can still outnumber a less frequent owner and take over its bucket. Unlike some other Top-K implementations, rare items don't artificially increase the counts of frequent items.

Next to the table, the sketch keeps a min-heap with the k items that have the largest estimates so far. After an item's buckets are updated, if its estimate is larger than the smallest count in the heap, the item is added to the heap, and the smallest item is removed. If the item is already in the heap, its count is increased.

```text
                   ┌───────────┐
add x (15) ───────►│ bob    12 │  the root has the smallest count
                   └─────┬─────┘
              ┌──────────┴────────┐
        ┌─────┴─────┐       ┌─────┴─────┐
        │ alice  30 │       │ dave   14 │  15 > 12: x replaces bob at the root
        └─────┬─────┘       └───────────┘  15 > 14: x moves down, dave moves up
      ┌───────┴──────┐
┌─────┴─────┐  ┌─────┴─────┐
│ eve   980 │  │ fay    41 │
└───────────┘  └───────────┘
```

**This implementation** uses d = 2 rows of max(16k, 256) buckets, and a decay base of b = 0.8. The probabilities b^count are precomputed as 64-bit thresholds, so decay is cheap: the count _c_ decreases if a random number is below the c-th threshold.

The original HeavyKeeper uses b = 1.08⁻¹ ≈ 0.93 and more rows. A smaller b lets frequent items lock their buckets faster, which makes the sketch more precise on flat streams. However, if b is too small (like 0.5), the first items to claim buckets keep them permanently, so items that become frequent later are never counted, even if they end up being the most frequent. Using b = 0.8 keeps most of the precision and still catches most late-arriving frequent items. As for the rows, two are enough to protect frequent items from collisions in a single row, and adding more rows only makes the sketch slower.

The item is hashed once. The hash is mapped to a position in each row by salting it (multiplying by a random per-row constant). The fingerprint is the top 32 bits of the hash. The heap uses the 64-bit hash to identify items, so it doesn't compare the actual item bytes. The heap only copies an item when it moves into the top k.

Each bucket is 8 bytes (a 32-bit fingerprint and a 32-bit count), so the table takes 256·k bytes. The heap takes about 100 bytes per item plus the item itself.

The sketch uses [wyhash](https://github.com/wangyi-fudan/wyhash) for hashing and wyrand for random numbers.

## Benchmarks

Compared to [keilerkonzept/topk](https://github.com/keilerkonzept/topk) (HeavyKeeper with b = 0.9, d = ln k rows of k·ln k buckets, default settings) and [dgryski/go-topk](https://github.com/dgryski/go-topk) (Filtered Space-Saving). Space-Saving needs more monitored items than k to be accurate, so _dgryski_ monitors 3k items and reports the top k of them. Apple M1, 10M events over 1M distinct 16-byte items with a Zipf distribution.

Speed on the Zipf s=0.8 stream (lower is better):

| Operation |    k |      unsure |  keiler | dgryski |
| --------- | ---: | ----------: | ------: | ------: |
| Add       |   10 |   **24 ns** |   64 ns |   59 ns |
|           |  100 |   **25 ns** |   84 ns |   70 ns |
|           | 1000 |   **33 ns** |  130 ns |   73 ns |
| Top       |   10 | **0.34 µs** | 0.64 µs | 0.77 µs |
|           |  100 |  **3.7 µs** |   13 µs |   14 µs |
|           | 1000 |   **75 µs** |  270 µs |  292 µs |

Precision is the fraction of the reported top k that is in the true top k (higher is better):

| k    | Zipf s |     unsure |    keiler |  dgryski |
| ---- | -----: | ---------: | --------: | -------: |
| 100  |    0.6 |   **0.90** |      0.38 |     0.19 |
|      |    0.8 |   **1.00** |      0.78 |     0.85 |
|      |    1.0 |       0.99 |  **1.00** | **1.00** |
|      |   Size |      39 KB | **27 KB** |    39 KB |
| 1000 |    0.6 |   **0.97** |      0.70 |     0.52 |
|      |    0.8 |   **1.00** |      0.98 | **1.00** |
|      |    1.0 |   **1.00** |  **1.00** | **1.00** |
|      |   Size | **364 KB** |    414 KB |   453 KB |

Count error is the mean of |estimate − true| / true over the reported items (lower is better). It includes the wrongly reported items, so it's high when precision is low:

| k    | Zipf s |    unsure | keiler |   dgryski |
| ---- | -----: | --------: | -----: | --------: |
| 100  |    0.6 |   **14%** |    94% |      428× |
|      |    0.8 | **0.10%** |    46% |      230% |
|      |    1.0 |     0.01% |  0.12% |  **0.0%** |
| 1000 |    0.6 |  **2.4%** |    62% |       18× |
|      |    0.8 | **0.23%** |   1.9% |     0.31% |
|      |    1.0 |     0.05% |  0.24% | **0.02%** |

Overall, _unsure_ is 2–4× faster than the others for Add and 1.8–4× faster for Top. On flat streams (s=0.6), it finds the true top k with 90–97% precision, while _keiler_ drops to 38–70% precision with a count error of 62–94%, and _dgryski_ to 19–52% with counts 18× or more too high. On skewed streams (s=1.0), all three are about equally accurate.

To reproduce, run `make topk-bench-full` and `make topk-accuracy` from the repository root.

## Papers

[HeavyKeeper: An Accurate Algorithm for Finding Top-k Elephant Flows](https://www.usenix.org/conference/atc18/presentation/gong) (Junzhi Gong et al., 2018): the HeavyKeeper sketch.

[Efficient Computation of Frequent and Top-k Elements in Data Streams](https://doi.org/10.1007/978-3-540-30570-5_27) (Ahmed Metwally, Divyakant Agrawal, Amr El Abbadi, 2005): Space-Saving.
