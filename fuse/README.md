# Binary fuse filter

A binary fuse filter is a set that trades certainty for space, just like a [Bloom filter](../bloom/README.md). It can tell you that an item is _definitely not_ in the set, or that it _probably is_ (which might be a false positive). The filter doesn't store the items, so it's much more compact than a regular set.

Unlike a Bloom filter, a binary fuse filter is _static_: you build it once from all the items, and you can't add more later. In exchange, it uses 24–36% less memory than a Bloom filter with the same false positive rate. For example, a filter for 1,000,000 items with a 0.39% false positive rate takes 1.13 MB (9 bits per item), while a Bloom filter takes 1.51 MB (12 bits per item).

This makes binary fuse filters a good fit for data that is written once and read many times, like the key set of an immutable file in a database (to skip files that don't have the key), or a list of known bad items, such as leaked passwords or malicious URLs.

## Usage

```text
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/fuse"

items := [][]byte{[]byte("alice"), []byte("bob")}

// An 8-bit filter: 0.39% false positive rate.
filter := fuse.New[uint8](slices.Values(items), len(items))

filter.Contains([]byte("alice")) // true
filter.Contains([]byte("carol")) // false (or, rarely, true)
```

The fingerprint type determines the false positive rate and the memory use:

| Type     | False positive rate | Bits per item |
| -------- | ------------------: | ------------: |
| `uint8`  |               0.39% |             9 |
| `uint16` |             0.0015% |            18 |
| `uint32` |             2.3e-10 |            36 |

`New` takes an iterator of items to add to the filter and the expected number of items (0 if unknown). The number is only used to preallocate memory, so a good estimate makes the build faster.

The filter is deterministic, so the same items always produce the same filter. The filter is not designed for adversarial input: anyone who knows the hash function can artificially increase false positives.

## How it works

The filter is an array of _b_-bit fingerprints (for example, b = 8 for `uint8`). Each item is hashed to a 64-bit value _h_, which gives the item's fingerprint _f_ (b bits computed from h) and three positions in the array (cells). The array is filled so that for every item, the XOR of its cells equals f.

```text
        ┌──────┐               ┌──► f  (b bits)
item ──►│ hash │──► h ─────────┤
        └──────┘               └──► i0, i1, i2

           i0          i1          i2
┌───┬───┬──▼──┬───┬───┬──▼──┬───┬──▼──┬───┐
│   │   │ F0  │   │   │ F1  │   │ F2  │   │   F0 ^ F1 ^ F2 == f
└───┴───┴─────┴───┴───┴─────┴───┴─────┴───┘
```

To check an item, you compute h, look up its three cells, and compare their XOR with f. For an item in the set, they always match. For any other item, the XOR is a random b-bit value, which matches f with probability 2^-b.

Each check reads three cells, so for large filters it's three cache misses, against one for a blocked Bloom filter. However, since the reads are independent, the CPU can do them concurrently, so the checks are about as fast as with a Bloom filter.

The filter uses [wyhash](https://github.com/wangyi-fudan/wyhash) for hashing.

## Benchmarks

Compared to [FastFilter/xorfilter](https://github.com/FastFilter/xorfilter) (binary fuse by the authors of the paper) and _unsure_'s [Bloom filter](../bloom/README.md) at the same false positive rate. xorfilter takes 64-bit keys, so items are hashed with the same wyhash before calling it. Apple M1, 16-byte items, ns/op (lower is better).

8-bit fingerprints (false positive rate 0.39%):

| Operation        | Items |  unsure | xorfilter |  bloom |
| ---------------- | ----: | ------: | --------: | -----: |
| Build (per item) |   10K |      23 |        25 | **13** |
|                  |    1M |      30 |        31 | **12** |
| Contains, hit    |   10K | **6.2** |       8.0 |    9.3 |
|                  |    1M | **7.0** |       9.3 |   10.2 |
|                  |   50M |  **39** |        42 |     40 |
| Contains, miss   |   10K | **6.2** |       8.0 |    9.2 |
|                  |    1M | **6.9** |       9.1 |   10.2 |
|                  |   50M |  **39** |        42 |     41 |

16-bit fingerprints (false positive rate 0.0015%):

| Operation        | Items |  unsure | xorfilter |  bloom |
| ---------------- | ----: | ------: | --------: | -----: |
| Build (per item) |   10K |      24 |        25 | **19** |
|                  |    1M |      30 |        32 | **19** |
| Contains, hit    |   10K | **6.1** |       8.1 |   13.3 |
|                  |    1M | **7.1** |       9.3 |   16.3 |
|                  |   50M |  **41** |        46 |     58 |
| Contains, miss   |   10K | **6.3** |       8.0 |   13.2 |
|                  |    1M | **7.2** |       9.3 |   16.2 |
|                  |   50M |  **40** |        46 |     59 |

Memory, bits per item (lower is better):

| Items | Fingerprint |    unsure | xorfilter | bloom |
| ----: | ----------- | --------: | --------: | ----: |
|    1K | 8-bit       | **11.26** | **11.26** | 12.29 |
|  100K | 8-bit       |  **9.50** |  **9.50** | 12.45 |
|   10M | 8-bit       |  **9.02** |  **9.02** | 12.10 |
|   10M | 16-bit      | **18.04** | **18.04** | 27.99 |

Overall, _unsure_ is the same size as _xorfilter_ (same layout), with 22–25% faster checks (8–13% for 50M items, where memory access dominates) and 4–6% faster builds. Compared to the Bloom filter, it uses 24–36% less memory (8% for 1K items), with checks as fast or faster, but 1.2–2.6× slower builds.

To reproduce, run `make fuse-bench-full` and `make fuse-size` from the repository root.

## Papers

[Binary Fuse Filters: Fast and Smaller Than Xor Filters](https://doi.org/10.1145/3510449) (Thomas Mueller Graf, Daniel Lemire, 2022): the binary fuse filter.

[Xor Filters: Faster and Smaller Than Bloom and Cuckoo Filters](https://doi.org/10.1145/3376122) (Thomas Mueller Graf, Daniel Lemire, 2020): the xor filter, the predecessor.
