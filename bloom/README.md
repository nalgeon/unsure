# Bloom filter

A simple and fast Bloom filter implemented in Go.

## What is a Bloom filter

A Bloom filter is a set that trades certainty for space. It can tell you that an item is _definitely not_ in the set, or that it _probably is_ (which might be a false positive). It doesn't actually store the items, so it's much more compact than a regular set — about 10 bits per item for a 1% false positive rate, regardless of the item size.

A Bloom filter often resides in memory and helps minimize expensive I/O operations such as disk reads or network calls. You check if an item is in the filter, and if the answer is "no", you skip the costly lookup. If the answer is "yes", you still have to perform the lookup because it might be a false positive.

You can add an item to a Bloom filter and check if an item is in the filter, but you cannot delete an item, count the number of items, or iterate over them.

## Usage

```
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/bloom"

// A set for 1000 items with a 1% false positive rate.
set := bloom.New(1000, 0.01)

set.Add([]byte("alice"))
set.Add([]byte("bob"))

set.Contains([]byte("alice")) // true
set.Contains([]byte("carol")) // false (or, rarely, true)
```

The set is sized for the given number of items with a given false positive rate. You can add more, but the false positive rate will go up.

Hashing is deterministic (wyhash with a fixed seed), so the same items always produce the same set. The filter is not designed for adversarial input: don't rely on it if an attacker could benefit from false positives.

## How it works

**Classic Bloom filter**. The filter is a large array of bits, all set to zero at first. To add an item, you compute k hash values for it and set the k bits at those positions. To check if an item is in the filter, you compute the same k hashes and check if all k bits are set:

- If any of the bits is not set, the item definitely was never added.
- If all the bits are set, the item was probably added, but it's not guaranteed (the bits could have been set by other items).

The k bits are randomly distributed throughout the entire array. When the filter is larger than the CPU cache, each bit access could result in a cache miss, so a lookup may wait on memory k times.

**This implementation** is a _blocked_ Bloom filter. The bit array is divided into 512-bit blocks, each matching the size of a CPU cache line. When you add or check an item, it is hashed once: part of the hash selects a block, and the rest selects k bits within that block. Each add or check only accesses one cache line, so the filter stays fast even when it's very large.

A blocked Bloom filter uses more memory than a classic one. Some blocks end up with more items than others, which increases the false positive rate. To make up for this, the filter needs to be a bit larger. Compared to a standard Bloom filter, it uses about 3% more memory at a 1% false positive rate, 8% more at 0.1%, and 15% more at 0.01%. The extra memory needed goes up even more at very low false positive rates (see the package docs for details).

The filter uses [wyhash](https://github.com/wangyi-fudan/wyhash) for hashing. It performs roughly the same as [xxh3](https://xxhash.com) but has a much simpler implementation, without SIMD or assembly.

## Benchmarks

Compared to [bits-and-blooms/bloom](https://github.com/bits-and-blooms/bloom) (a classic Bloom filter) and [jcalabro/gloom](https://github.com/jcalabro/gloom) (a blocked one). Apple M1, false positive rate 1%, 16-byte items, ns/op (lower is better):

| Operation      | Items |  unsure | bits-and-blooms | gloom |
| -------------- | ----: | ------: | --------------: | ----: |
| Add            |   10K | **8.5** |              37 |    17 |
|                |    1M | **9.7** |              36 |    19 |
|                |   50M |  **38** |             170 |    63 |
| Contains, hit  |   10K | **8.0** |              32 |    16 |
|                |    1M | **8.8** |              32 |    18 |
|                |   50M |  **34** |             129 |    60 |
| Contains, miss |   10K | **8.0** |              41 |    27 |
|                |    1M | **8.8** |              48 |    33 |
|                |   50M |  **34** |             113 |    60 |

Memory, bits per item at 10M items (lower is better):

| False positive rate | unsure | bits-and-blooms | gloom |
| ------------------- | -----: | --------------: | ----: |
| 10%                 |   4.84 |        **4.80** |  5.39 |
| 1%                  |   9.90 |        **9.59** | 10.79 |
| 0.1%                |  15.49 |       **14.38** | 16.18 |

Overall, unsure is about 4× faster than bits-and-blooms and 2× faster than gloom, using 1–8% more memory than bits-and-blooms and 4–10% less than gloom.

To reproduce, run `make bloom-bench-full` and `make bloom-size` from the repository root.
