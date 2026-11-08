# Count-min sketch

A simple and fast count-min sketch implemented in Go.

## What is a count-min sketch

A count-min sketch is a frequency table that trades accuracy for space. It tells you approximately how many times each item has appeared. The estimate is _never less_ than the actual count, but it can be _higher_. The sketch doesn't actually store the items, so it's much more compact than a regular map of per-item counts.

For example, a regular map (`map[string]uint32`) counting 1,000,000 distinct 8-byte strings takes about 64 MB. A count-min sketch for the same strings, with estimates within 0.01% of the total count (with 99% probability), uses only 0.55 MB — that's 116 times smaller. The size of the sketch doesn't depend on the number of distinct items or their size, only on the accuracy you choose, so the difference gets even bigger with more or longer strings.

A count-min sketch is useful when you want to track how many times an item shows up in a large stream of events. It's often used to check if something has happened a certain number of times, like rate-limiting an IP after X requests or sending an alert after an error happens Y times. A sketch can also help decide if a key is worth keeping in the cache given how often it was requested. Finally, a sketch can identify the most common items when used with a small heap of candidates (since the sketch itself can't list the items).

The counting error in a sketch depends on the total of all counts, not on each individual item. Therefore, frequent items are estimated more or less accurately relative to their count, but rare items are not — the absolute error is too high for them.

You can increase the per-item counter in a sketch and get the estimated count for an item, but you cannot decrease a counter or iterate over the items.

## Usage

```
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/countmin"

// Estimates within 0.1% of the total count, with 99% probability.
sketch := countmin.New(0.001, 0.01)

sketch.Add([]byte("alice"))
sketch.Add([]byte("bob"))
sketch.AddN([]byte("alice"), 2)

sketch.Count([]byte("alice")) // 3 (or, rarely, more)
sketch.Count([]byte("bob"))   // 1 (or, rarely, more)
sketch.Count([]byte("carol")) // 0 (or, rarely, more)
sketch.Total()                // 4
```

The sketch uses two settings: epsilon _ε_ and delta _δ_. If the actual count for any given item is X and the total of all counts is N, then the estimated count for this item will usually be between X and X+ε×N. However, rarely (with a probability of δ) the estimate could be even higher. For example, if ε is 0.001, δ is 0.01, and N is 1 million, then the estimate could be off by up to 1,000 with at least 99% probability, and by more than 1,000 with at most 1% probability.

Choose ε to be smaller than the smallest fraction of N that matters to you. A common choice for δ is 0.01. Making δ even smaller is pretty cheap, since the sketch size only grows with ln(1/δ).

Counts are stored as 32-bit numbers. If an item is added more than about 4.3 billion times, its count stays at `math.MaxUint32`.

Hashing is deterministic (wyhash with seed 0), so the same items always produce the same sketch. The sketch is not designed for adversarial input: anyone who knows the seed can craft items that inflate other items' estimates.

## How it works

**The count-min sketch** is a table of counters with d rows and w columns, all starting at zero. Each row uses its own hash function that maps an item to one of the w counters. To add an item, you increment its counter in each row:

```
        ┌──────┐                       ┌───────┐
item ──►│ hash │──► h1, h2, ..., hd ──►│ mod w │──► i1, i2, ..., id
        └──────┘                       └───────┘

       ┌───┬───┬─▼─┬───┬───┬───┬───┬───┬───┬───┐
row 1  │ 0 │ 2 │ 4 │ 0 │ 1 │ 0 │ 0 │ 3 │ 0 │ 1 │  4 → 5
       ├───┼───┼───┼───┼───┼───┼─▼─┼───┼───┼───┤
row 2  │ 1 │ 0 │ 0 │ 0 │ 2 │ 0 │ 3 │ 0 │ 1 │ 0 │  3 → 4
       ├───┼───┼───┼───┼───┼───┼───┼───┼─▼─┼───┤
row d  │ 0 │ 1 │ 0 │ 2 │ 0 │ 0 │ 1 │ 0 │ 5 │ 0 │  5 → 6
       └───┴───┴───┴───┴───┴───┴───┴───┴───┴───┘
                 i1              i2      id
```

To estimate an item's count, you take the smallest value from its d counters:

- Other items might hash to the same counter, so each counter value is ≥ true count.
- The smallest counter is the one with the fewest collisions, so it's the best estimate.

```
       ┌───┬───┬─▼─┬───┬───┬───┬───┬───┬───┬───┐
row 1  │ 0 │ 2 │ 4 │ 0 │ 1 │ 0 │ 0 │ 3 │ 0 │ 1 │  4
       ├───┼───┼───┼───┼───┼───┼─▼─┼───┼───┼───┤
row 2  │ 1 │ 0 │ 0 │ 0 │ 2 │ 0 │ 3 │ 0 │ 1 │ 0 │  3
       ├───┼───┼───┼───┼───┼───┼───┼───┼─▼─┼───┤
row d  │ 0 │ 1 │ 0 │ 2 │ 0 │ 0 │ 1 │ 0 │ 5 │ 0 │  5   min = 3
       └───┴───┴───┴───┴───┴───┴───┴───┴───┴───┘
```

With w = e/ε columns, each counter's error is at most ε×N, with probability at least 1 − 1/e. If you use d = ln(1/δ) rows, the chance that all d of the item's counters exceed its true count by more than ε×N is ≤ δ.

**This implementation** uses a _conservative update_. When you add n to an item, it first finds the current estimate E (the smallest counter), then increases only the counters that are below E+n, leaving the larger ones unchanged. This keeps the same accuracy guarantee, but the actual error is several times lower. The tradeoff is that counts can't be decreased.

```
       ┌───┬───┬─▼─┬───┬───┬───┬───┬───┬───┬───┐
row 1  │ 0 │ 2 │ 4 │ 0 │ 1 │ 0 │ 0 │ 3 │ 0 │ 1 │  4 → 4
       ├───┼───┼───┼───┼───┼───┼─▼─┼───┼───┼───┤
row 2  │ 1 │ 0 │ 0 │ 0 │ 2 │ 0 │ 3 │ 0 │ 1 │ 0 │  3 → 4
       ├───┼───┼───┼───┼───┼───┼───┼───┼─▼─┼───┤
row d  │ 0 │ 1 │ 0 │ 2 │ 0 │ 0 │ 1 │ 0 │ 5 │ 0 │  5 → 5
       └───┴───┴───┴───┴───┴───┴───┴───┴───┴───┘
```

Counters are 32-bit instead of the usual 64-bit, so the sketch uses 2× less memory. The item is hashed once, and each row gets its own position from that hash by salting it (multiplying by a random per-row constant).

```
        ┌──────┐         ┌─────────┐
item ──►│ hash │──► h ──►│ × salts │──► i1, i2, ..., id
        └──────┘         └─────────┘
```

The sketch uses [wyhash](https://github.com/wangyi-fudan/wyhash) for hashing.

## Benchmarks

Compared to [tylertreat/BoomFilters](https://github.com/tylertreat/BoomFilters) (a classic count-min sketch with 64-bit counters). Apple M1, δ=0.01, 16-byte items, ns/op (lower is better):

| Operation |        ε | Size (unsure) |  unsure | BoomFilters |
| --------- | -------: | ------------: | ------: | ----------: |
| Add       |    0.001 |         55 KB |  **13** |          19 |
|           |  0.00001 |        5.4 MB |  **17** |          32 |
|           | 0.000001 |         54 MB |  **81** |          98 |
| Count     |    0.001 |         55 KB | **8.8** |          51 |
|           |  0.00001 |        5.4 MB |  **14** |          64 |
|           | 0.000001 |         54 MB |  **52** |         139 |

Accuracy at ε=0.0001, N=10M additions over 1M distinct items, mean overestimate (lower is better). The error bound ε×N is 1,000:

| Distribution  |     unsure | BoomFilters |
| ------------- | ---------: | ----------: |
| Uniform       |    **139** |         302 |
| Zipf, s = 1.1 |     **23** |          44 |
| Zipf, s = 1.5 |    **0.3** |         0.8 |
| Size          | **536 KB** |     1080 KB |

Overall, unsure is about 2.5× faster than BoomFilters (up to 5× for Count), using 2× less memory with 2–2.7× lower error.

To reproduce, run `make countmin-bench-full` and `make countmin-accuracy` from the repository root.

## Papers

[An improved data stream summary: the count-min sketch and its applications](https://doi.org/10.1016/j.jalgor.2003.12.001) (Graham Cormode, S. Muthukrishnan, 2005): the classic count-min sketch.

[New directions in traffic measurement and accounting](https://doi.org/10.1145/633025.633056) (Cristian Estan, George Varghese, 2002): conservative update.
