# unsure: Probabilistic data structures in Go

Probabilistic data structures give you approximate answers to questions about the data they handle. Does the set contain a certain item? How many unique items are in the collection? Which items appear most often? Unlike regular data structures, the answers might not be exact, and you can choose the acceptable error rate.

The `unsure` module implements probabilistic data structures in Go. It's **simple** (the core logic for each data structure is 100–300 LOC), **fast** (often several times faster than other implementations), and has **no dependencies** except for the standard library.

## Data structures

[Bloom filter](./bloom/README.md) and [Binary fuse filter](./fuse/README.md) check if an item might be in a set. The binary fuse is more compact, but unlike Bloom, it can't be changed after creation.

[Count-min sketch](./countmin/README.md) estimates how many times each item has occurred.

[HyperLogLog](./hll/README.md) and [UltraLogLog](./ull/README.md) estimate the number of distinct items. UltraLogLog is more compact, but HyperLogLog is faster.

[T-digest](./tdigest/README.md) and [Log-linear histogram](./loghist/README.md) estimate quantiles of a stream of numbers. The t-digest is more compact, but the histogram is more accurate.

[Top-k sketch](./topk/README.md) finds the most frequent items in a stream.

All data structures use the local [wyhash](https://github.com/wangyi-fudan/wyhash) implementation for hashing. It performs roughly the same as [xxh3](https://github.com/Cyan4973/xxHash) but has a much simpler implementation, without SIMD or assembly.

## Contributing

If there's a feature you need that's missing, please open an issue to discuss it. Only open issues for real use cases you actually have, not for hypothetical situations or features that would just be nice to have.

If you find a bug, open an issue or send a PR. Please don't send PRs with new features — open an issue instead.

## License

Created by [Anton Zhiyanov](https://antonz.org/). Released under the MIT License.
