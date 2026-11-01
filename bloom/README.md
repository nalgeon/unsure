# Bloom filter

A fast, zero-dependency Bloom filter implemented in Go.

## What is a Bloom filter

A Bloom filter is a set that trades certainty for space. It can tell you that an item is _definitely not_ in the set, or that it _probably is_ (which might be a false positive). It doesn't store the items themselves, so it uses much less memory than a regular set — about 10 bits per item for a 1% false positive rate, no matter how large the items are.

A common use is to put a filter in front of something expensive, like reading from a disk or making a network call. If the filter says "no", you skip the lookup. If it says "yes", you do the lookup, and it's only wrong occasionally.

Unlike a regular set, a Bloom filter can't iterate, count, or remove items.

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

Hashing is deterministic (wyhash with a fixed seed), so the same items always produce the same set. The filter is not designed for adversarial input: don't rely on it where an attacker could benefit from false positives.

## How it works

**Classic Bloom filter**. The filter is a big array of bits, all zero at first. To add an item, compute k hashes of it and set the k bits they point to. To check an item, compute the same k hashes and see if all k bits are set. If any bit is zero, the item was never added. If all are set, it probably was, but it's not guaranteed, since other items could have set those bits.

The k bits are spread randomly over the whole array. When the filter is larger than the CPU cache, each bit access could result in a cache miss, so a lookup may wait on memory k times.

**This implementation** is a _blocked_ Bloom filter. The bit array is split into 512-bit blocks, each the size of one CPU cache line. An item is hashed once: part of the hash picks a block, and the rest picks k bits inside that block. Each add or check touches exactly one cache line, so it stays fast even for very large filters.

The price is a bit more memory. Some blocks end up with more items than others, which raises the false positive rate, so the filter has to be a little larger to make up for it. The filter accounts for this when sizing itself: it computes the actual false positive rate of the blocked layout and picks the k and size that use the least memory. Compared to a classic filter, it uses about 3% more memory at a 1% false positive rate, 8% more at 0.1%, and up to ~15% down to 0.01%. The overhead grows at very low rates (see the package docs).

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
