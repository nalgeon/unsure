# HyperLogLog

A simple and fast HyperLogLog implemented in Go.

## What is HyperLogLog

HyperLogLog is a unique items counter that trades accuracy for space. It tells you approximately how many _different_ items it has seen, no matter how many times each one appeared. HyperLogLog (also called a _sketch_ from here on) doesn't actually store the items, so it's much more compact than a regular set.

For example, a regular set of 1,000,000 distinct 8-byte strings takes about 64 MB. A HyperLogLog sketch counting the same strings with about 0.8% error uses only 16 KB — that's almost 4,000 times smaller. The size of the sketch doesn't depend on the number of items or their size, only on the accuracy you choose. A 16 KB sketch counts a billion items as accurately as a thousand.

HyperLogLog is useful when you need to count unique things in a large stream: unique visitors to a website, unique IP addresses hitting a server, unique search queries per day, or distinct values in a database column. You can merge sketches to gather data from multiple machines or segments (like by hour, page, or region). When you merge two sketches, you get the total number of unique items from both, without counting shared items twice.

You can add items to a sketch, get the estimated count, and merge sketches. You cannot check if a specific item was added, remove items, or list them.

## Usage

```
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/hll"

// Estimates within about 0.8% of the true count.
sketch := hll.New()

sketch.Add([]byte("alice"))
sketch.Add([]byte("bob"))
sketch.Add([]byte("alice"))
sketch.Add([]byte("carol"))

sketch.Count() // 3 (approximately)
```

Merging sketches:

```go
home := hll.New() // visitors to the home page
docs := hll.New() // visitors to the docs
// ...
home.Merge(docs)
home.Count() // visitors to either page
```

The sketch has one setting: the precision _p_, which gives 2^p registers and a relative standard error (stdErr) of 1.04/√(2^p). `hll.New()` uses p=14, a common default, with 0.81% error. With 1 million distinct items, about 68% of the time the estimate is within 0.81% (8,100) of the true count, 95% within 1.6% (2×stdErr), and 99.7% within 2.4% (3×stdErr). The error is relative to the count, so it's the same 0.81% upper bound for a thousand items or a billion.

To trade accuracy for memory, use `hll.NewPrecision(p)` with p from 8 to 18. Each +2 to p halves the error and quadruples the memory:

| p   | Error | Registers |
| --- | ----- | --------- |
| 8   | 6.5%  | 256 B     |
| 10  | 3.3%  | 1 KB      |
| 12  | 1.6%  | 4 KB      |
| 14  | 0.81% | 16 KB     |
| 16  | 0.41% | 64 KB     |
| 18  | 0.20% | 256 KB    |

The sketch also has a fixed 256-byte histogram (see below).

Sketches can only be merged if they have the same precision. The sketch is deterministic, so the same items always produce the same sketch. The sketch is not designed for adversarial input: anyone who knows the seed can create items that artificially increase the estimate.

## How it works

**The HyperLogLog** is based on a simple observation. If you hash items to random bit strings, about half of them start with 1, a quarter with 01, an eighth with 001, and so on. Seeing a hash with k leading zeros suggests there were about 2^(k+1) distinct items. Duplicates hash to the same value, so they don't affect the result.

One such observation is unreliable, so the sketch splits the items into m = 2^p groups. The first p bits of the hash pick a group (a _register_), and the register keeps the highest _rank_ (leading zeros plus one) it has seen in the rest of the hash:

```
        ┌──────┐   ┌──────────┬────────────────────────┐
item ──►│ hash │──►│ 01101001 │ 0001101...             │
        └──────┘   └────┬─────┴───────────┬────────────┘
                        │ register i      └ rank = 3 zeros + 1 = 4
                        ▼
          ┌───┬───┬───┬───┬───┬───┬───┬───┬───┬───┐
registers │ 3 │ 0 │ 5 │ 2 │ 1 │ 0 │ 4 │ 2 │ 0 │ 1 │  2 → 4
          └───┴───┴───┴───┴───┴───┴───┴───┴───┴───┘
```

The count is estimated from all the registers together, using a harmonic mean of 2^register. The standard error is about 1.04/√m. Merging two sketches is just taking the maximum value from each pair of registers.

The original estimator is biased when dealing with small counts (up to a few times m), so it uses a different formula called _linear counting_ in those cases. This switch creates a sudden change in error, and some implementations (like HyperLogLog++) address it by using large tables of bias corrections based on real data.

**This implementation** uses the improved estimator by Otmar Ertl (also used by Redis). Instead of a harmonic mean of the registers, it works from a _histogram_: how many registers have value 0, value 1, and so on. A formula applied to the histogram gives an accurate estimate for any number of items, from zero to billions, so there's no need for a separate formula for small counts or for correction tables.

The sketch updates the histogram whenever registers change. Most adds don't change any registers, so this is almost free. Counting always takes the same amount of time, no matter how big the sketch is. Other implementations scan all the registers on every count, which takes from microseconds to milliseconds.

Registers are one byte each, and the histogram has 64 four-byte counters (256 bytes). The item is hashed once with [wyhash](https://github.com/wangyi-fudan/wyhash), and the 64-bit hash is split into the register index and the rank.

## Benchmarks

Compared to [axiomhq/hyperloglog](https://github.com/axiomhq/hyperloglog) (HyperLogLog with LogLog-Beta estimator and sparse mode) and [tylertreat/BoomFilters](https://github.com/tylertreat/BoomFilters) (classic HyperLogLog). Apple M1, 16-byte items, ns/op (lower is better):

| Operation |   p |   Size |      unsure |     axiom | BoomFilters |
| --------- | --: | -----: | ----------: | --------: | ----------: |
| Add       |  10 |   1 KB |     **5.5** |       7.6 |          27 |
|           |  14 |  16 KB |     **5.6** |       7.6 |          27 |
|           |  18 | 256 KB |         8.1 |   **7.9** |          24 |
| Count     |  10 |   1 KB |      **68** |    18,300 |      18,100 |
|           |  14 |  16 KB |      **62** |   350,000 |     328,000 |
|           |  18 | 256 KB |      **57** | 4,710,000 |   4,520,000 |
| Merge     |  10 |   1 KB |     **730** |     1,710 |       1,600 |
|           |  14 |  16 KB |  **20,100** |    58,600 |      54,300 |
|           |  18 | 256 KB | **341,000** |   951,000 |     847,000 |

The `Count` gap comes from how the estimate is computed. _axiomhq_ and _BoomFilters_ scan all the registers on every count, computing 2^-register with `math.Pow` for each one (about 20 ns per register), so their `Count` grows with the sketch size. _unsure_ keeps a histogram of register values up to date as registers change, and `Count` loops over its 64 entries regardless of the sketch size. The cost of keeping the histogram moves to `Add`, which updates it only when a register grows (often while the sketch fills up, rarely after).

`Merge` combines two sketches filled with different items, so about half of the registers grow.

Accuracy at p=14 (0.81% standard error), relative RMSE over 100 sketches (lower is better):

| Distinct items |    unsure |     axiom | BoomFilters |
| -------------- | --------: | --------: | ----------: |
| 1,000          |     0.63% | **0.02%** |       4.04% |
| 40,000         | **0.64%** |     0.71% |       50.8% |
| 1,000,000      | **0.79%** |     0.89% |        9.9% |

_axiomhq_ is nearly exact below ~16,000 items thanks to its sparse mode. Above that, _unsure_ and _axiomhq_ are equally accurate. _BoomFilters_ is far off (up to 50%) around 20,000–40,000 items.

Memory at p=14 after adding the given number of distinct items (lower is better):

| Distinct items | unsure |      axiom | BoomFilters |
| -------------- | -----: | ---------: | ----------: |
| 100            |  16 KB | **1.2 KB** |       16 KB |
| 1,000          |  16 KB | **4.2 KB** |       16 KB |
| 10,000         |  16 KB |      16 KB |       16 KB |
| 1,000,000      |  16 KB |      16 KB |       16 KB |

_unsure_ and _BoomFilters_ allocate all the registers up front, so their size doesn't depend on the number of items. _axiomhq_ starts in sparse mode, keeping the hashes in a compressed list, and switches to registers once the list would outgrow them (between 3,000 and 10,000 items at p=14). The gap is larger for bigger sketches: at p=18, where the registers take 256 KB, _axiomhq_ takes 8.2 KB at 1,000 items, 40 KB at 10,000, and 208 KB at 100,000.

Overall, _unsure_ adds about as fast as _axiomhq_ (27% faster when the sketch fits in L1, 3% slower at 256 KB), counts 270–83,000× faster, and merges 2.3–2.9× faster, with the same accuracy and memory for counts above the number of registers. When there are fewer items than registers, _axiomhq_'s sparse mode makes it more accurate and compact.

To reproduce, run `make hll-bench-full` and `make hll-accuracy` from the repository root.

## Papers

[HyperLogLog: the analysis of a near-optimal cardinality estimation algorithm](https://doi.org/10.46298/dmtcs.3545) (Philippe Flajolet, Éric Fusy, Olivier Gandouet, Frédéric Meunier, 2007): the classic HyperLogLog.

[HyperLogLog in practice](https://doi.org/10.1145/2452376.2452456) (Stefan Heule, Marc Nunkesser, Alexander Hall, 2013): HyperLogLog++ with bias correction tables.

[New cardinality estimation algorithms for HyperLogLog sketches](https://arxiv.org/abs/1702.01284) (Otmar Ertl, 2017): the improved estimator used here.
