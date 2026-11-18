# UltraLogLog

UltraLogLog is a unique items counter that trades accuracy for space, like [HyperLogLog](../hll/README.md). It tells you approximately how many _distinct items_ it has seen, no matter how many times each one appeared. UltraLogLog (also called a _sketch_ from here on) doesn't store the items, so it's much more compact than a regular set.

UltraLogLog is a newer design (Otmar Ertl, 2024) that uses the space more efficiently: for the same error, it needs about 43% less memory than HyperLogLog. For example, a HyperLogLog sketch with 0.81% standard error takes 16 KB, while an UltraLogLog sketch with 0.86% error takes 8 KB. However, adding, counting, and merging are slower than with HyperLogLog.

UltraLogLog is useful in the same places as HyperLogLog: counting unique visitors, unique search queries, or distinct values in a database column, possibly across many machines or segments. You can merge sketches to get the total number of unique items from all of them, without counting shared items twice.

You can add items to a sketch, get the estimated count, and merge sketches. You cannot check if a specific item was added, remove items, or list them.

## Usage

```text
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/ull"

// Standard error about 0.9% of the true count.
sketch := ull.New()

sketch.Add([]byte("alice"))
sketch.Add([]byte("bob"))
sketch.Add([]byte("alice"))
sketch.Add([]byte("carol"))

sketch.Count() // 3 (approximately)
```

Merging sketches:

```go
home := ull.New() // visitors to the home page
docs := ull.New() // visitors to the docs
// ...
home.Merge(docs)
home.Count() // visitors to either page
```

The sketch has one setting: the precision _p_, which gives a relative standard error (stdErr) of 0.782/√(2^p). `ull.New()` uses p=13 with 0.86% error in 8 KB, close to the common HyperLogLog default (0.81% in 16 KB).

With p=13 and 1 million distinct items:

- 68% of the time the estimate is within 0.86% (8,600) of the true count,
- 95% within 1.7% (2·stdErr),
- 99.7% within 2.6% (3·stdErr).

The error is relative to the count, so the standard error is the same 0.86% for a thousand items or a billion.

To trade accuracy for memory, use `ull.NewPrecision(p)` with p from 8 to 18. Each +2 to p halves the error and quadruples the memory:

| p   | Error | Memory | HLL memory |
| --- | ----: | -----: | ---------: |
| 8   |  4.9% |  256 B |      450 B |
| 10  |  2.4% |   1 KB |     1.8 KB |
| 12  |  1.2% |   4 KB |     7.1 KB |
| 13  | 0.86% |   8 KB |      14 KB |
| 14  | 0.61% |  16 KB |      28 KB |
| 16  | 0.31% |  64 KB |     113 KB |
| 18  | 0.15% | 256 KB |     452 KB |

The sketch also keeps a fixed 1 KB histogram, so at p=10 and below, the total saving is small.

Sketches can only be merged if they have the same precision. The sketch is deterministic, so the same items always produce the same sketch. The sketch is not designed for adversarial input: anyone who knows the seed can create items that artificially increase the estimate.

## How it works

**HyperLogLog** hashes each item, uses the first _p_ bits of the hash to pick one of m = 2^p _registers_, and keeps in the register the highest _rank_ (the number of leading zeros in the rest of the hash, plus one) it has seen. Seeing a rank of _k_ suggests there were about 2^k items in the register. The count is estimated from all the registers together.

A HyperLogLog register loses a lot of information: it only keeps track of the highest value, not whether any slightly lower values were seen. These lower values show up about as often as the highest one, so they can tell a lot about the count.

**UltraLogLog** uses the registers more effciently. Each register is still one byte, but it stores the highest rank _u_ in the upper 6 bits, and whether _u−1_ and _u−2_ were seen in the lower 2 bits:

```text
              u = 5        u-1 u-2
          ┌───────────────┬───┬───┐
register  │ 0 0 0 1 0 1   │ 1 │ 0 │  saw 5 and 4, but not 3
          └───────────────┴───┴───┘
          ┌───────────────┬───┬───┐
add k=6:  │ 0 0 0 1 1 0   │ 1 │ 1 │  saw 6, 5, 4
          └───────────────┴───┴───┘
```

The count estimate uses the FGRA estimator from the UltraLogLog paper. Each register adds a weight based on its value, and the count is calculated as a power of the sum of the weights. Nearly empty and nearly full sketches get special handling, so the estimate stays accurate from zero to billions of items, without bias tables or a separate formula for small counts.

**This implementation** keeps a histogram of register values (256 four-byte counters, 1 KB) updated as the registers change. This way, `Count` reads the histogram instead of scanning all the registers. `Merge` checks 8 registers at a time using a few bitwise operations to find which ones the other sketch would change, and only updates those.

The item is hashed once with [wyhash](https://github.com/wangyi-fudan/wyhash), and the 64-bit hash is split into the register index and the rank.

## Benchmarks

Compared to _unsure_'s [HyperLogLog](../hll/README.md) and [axiomhq/hyperloglog](https://github.com/axiomhq/hyperloglog) (HyperLogLog with LogLog-Beta estimator and sparse mode), all with the same number of one-byte registers. Apple M1, 16-byte items, ns/op (lower is better):

| Operation |   p |   Size |     ull |         hll |     axiom |
| --------- | --: | -----: | ------: | ----------: | --------: |
| Add       |  10 |   1 KB |     8.1 |     **5.5** |       7.6 |
|           |  14 |  16 KB |     8.0 |     **5.7** |       7.7 |
|           |  18 | 256 KB |     9.4 |         8.3 |   **8.0** |
| Count     |  10 |   1 KB |     165 |      **68** |    17,700 |
|           |  14 |  16 KB |     161 |      **63** |   348,000 |
|           |  18 | 256 KB |     173 |      **57** | 4,650,000 |
| Merge     |  10 |   1 KB |   2,900 |     **720** |     1,740 |
|           |  14 |  16 KB |  51,200 |  **20,200** |    58,500 |
|           |  18 | 256 KB | 818,000 | **341,000** |   955,000 |

_ull_ registers take more work to update, so `Add` is 13–47% slower than _hll_ and 4–18% slower than _axiom_. `Count` reads a larger histogram and has a more complex formula, but still takes constant time, so it's 100–27,000× faster than _axiom_, which scans all the registers.

Accuracy and memory at the default settings, relative RMSE over 1,000 sketches (lower is better):

| Distinct items |   ull p=13 |  hll p=14 | axiom p=14 |
| -------------- | ---------: | --------: | ---------: |
| 1,000          |      0.66% |     0.54% |  **0.01%** |
| 40,000         |      0.73% | **0.68%** |      0.69% |
| 1,000,000      |      0.86% | **0.79%** |      0.82% |
| Memory         | **9.1 KB** |   16.3 KB |    16.1 KB |

With 44% less memory, _ull_'s error is 3–10% higher than _hll_'s above the number of registers (theory predicts 6%: 1.106 vs 1.04 per √(2^14)). _axiom_ starts in a compact sparse mode, which makes it nearly exact and small below ~16,000 items, and switches to full registers above that.

Overall, UltraLogLog provides the same accuracy as HyperLogLog with almost half the memory. However, adding, counting, and merging are noticeably slower.

To reproduce, run `make ull-bench-full` and `make ull-accuracy` from the repository root.

## Papers

[UltraLogLog: a practical and more space-efficient alternative to HyperLogLog for approximate distinct counting](https://arxiv.org/abs/2308.16862) (Otmar Ertl, 2024): the algorithm and the estimator used here. The reference implementation is in [hash4j](https://github.com/dynatrace-oss/hash4j).

[New cardinality estimation algorithms for HyperLogLog sketches](https://arxiv.org/abs/1702.01284) (Otmar Ertl, 2017): the histogram-based estimation used in _unsure_'s HyperLogLog.
