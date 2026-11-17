# Binary fuse filter history

Benchmarks: `make fuse-bench`, Apple M1, 8-bit fingerprints, 16-byte keys. New in ns/item for n = 10k / 1M, Contains in ns/op for n = 10k / 1M / 50M.
Size: bits/item for n = 1k / 100k / 10M, 8-bit fingerprints.

## 0. Baseline: FastFilter/xorfilter v0.5.1

BinaryFuse[uint8], 3-wise, uint64 keys (hashed with our wyhash in the adapter), murmur64 mix with a per-attempt seed. Places hashes into segment buckets by linear probing, tags each cell's count with the XOR of position indices, handles duplicates inside the counting loop.

| New     | ContainsHit    | ContainsMiss   | Size                |
| ------- | -------------- | -------------- | ------------------- |
| 25 / 32 | 8.1 / 9.2 / 44 | 8.0 / 9.1 / 43 | 11.26 / 9.50 / 9.02 |

Our blocked Bloom filter at p = 1/256, for context: New (Add per item) 13 / 12, Contains 9.2 / 10.2 / 41, size 12.29 / 12.45 / 12.10.

## 1. First version

Same layout and sizing as the reference (so identical size). Takes `iter.Seq[[]byte]`, hashes with wyhash, then mixes `h + seed` per attempt with murmur's fmix64, so retries don't rehash the items. Exact counting sort into buckets. Counts are plain uint8 (a key's 3 cells are always distinct, so the peeled cell tells which one it was); a wrap to 0 fails the attempt. Duplicates: on a failed attempt, sort and dedupe the hashes once, then retry. Peeling updates only the other 2 cells, with branchless queue pushes (36 → 30 / 41 → 36 ns/item New over a version with a loop over all 3 cells and appends).

| New     | ContainsHit    | ContainsMiss   | Size                |
| ------- | -------------- | -------------- | ------------------- |
| 30 / 36 | 7.6 / 8.4 / 39 | 7.6 / 8.4 / 39 | 11.26 / 9.50 / 9.02 |

From `make fuse-bench-full`. Contains 6–13% faster than the reference. New 11–19% slower: about half is growing the hash slice (n is unknown with an iterator), half the build itself. Build attempts over 1112 sizes from 1 to 2M: mean 1.03, worst 4, so the reference's segment-length fallback for small n isn't needed.

## 2. Size hint

`New(items, n)`: n is the expected number of items, used only to preallocate the hash slice (0 if unknown; the filter is still sized by the items actually read). Removes the slice regrowth and its GC churn: 30 → 28 / 36 → 34 ns/item. Chunked hash storage (no copying, no API change) gave only 30 → 29 / 36 → 34.

| New     | ContainsHit    | ContainsMiss   | Size                |
| ------- | -------------- | -------------- | ------------------- |
| 28 / 34 | 7.6 / 8.4 / 39 | 7.6 / 8.4 / 39 | 11.26 / 9.50 / 9.02 |

New is now 5–12% slower than the reference, all in the build itself.

## 3. Build loop

The low 2 bits of each cell's count XOR which of its 3 cells (0–2) that is for every hash in it (count += 4 per hash, as in the reference). A peeled hash is removed from all 3 of its cells uniformly, with no comparisons to find which one was peeled (a branch that mispredicted often). Caps a cell at 63 hashes, still only reachable with many duplicates. Build only (prehashed): 22.8 → 21.3 / 28.7 → 27.5 ns/item. Tried and dropped: branchless selects for the other 2 cells (no gain), min() for the overflow check (1% slower), mixing each hash once in the counting sort by keeping the mixed hashes in the xor buffer (3% faster, not worth the buffer doing double duty).

| New     | ContainsHit    | ContainsMiss   | Size                |
| ------- | -------------- | -------------- | ------------------- |
| 26 / 32 | 7.6 / 8.4 / 39 | 7.6 / 8.4 / 39 | 11.26 / 9.50 / 9.02 |

New is now 6% slower than the reference at 10k and 2% slower at 1M.
