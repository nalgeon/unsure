# unsure: Probabilistic data structures in Go

Probabilistic data structures give you approximate answers to questions about the data they handle. Does the set contain a certain item? How many unique items are in the collection? Which items appear most often? Unlike regular data structures, the answers might not be exact, and you can choose the acceptable error rate.

The `unsure` module implements these structures in Go, with each structure in its own package. It's designed to be fast and simple, and it has no third-party dependencies.

## Roadmap

- ✓ [Bloom filter](./bloom/README.md): estimates if an item is in a set.
- ✓ [Count-min sketch](./countmin/README.md): estimates how many times each item has occurred.
- ✗ HyperLogLog: estimates the number of distinct items.
- ✗ T-digest: estimates quantiles (like the median or 99th percentile) of a stream of numbers.
- ✗ Top-K: finds the most frequent items in a stream.

## Contributing

If there's a feature you need that's missing, please open an issue to discuss it. Only open issues for real use cases you actually have, not for hypothetical situations or features that would just be nice to have.

If you find a bug, open an issue or send a PR. Please don't send PRs with new features — open an issue instead.

## License

Created by [Anton Zhiyanov](https://antonz.org/). Released under the MIT License.
