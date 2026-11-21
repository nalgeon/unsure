# MinHash

MinHash estimates how similar two sets are. It summarizes each set in a short fixed-size _signature_ (for example, 256 numbers). Comparing two signatures gives the approximate _Jaccard similarity_ of the sets: |A∩B| / |A∪B|, the proportion of unique items that both sets share. Identical sets have similarity 1, sets with no common items have 0.

The size of a signature doesn't depend on the size of the set, so you can compare two sets of a million items each just by comparing 256 pairs of numbers instead of checking every item. Plus, each set's signature is only calculated once, so comparing lots of sets to each other is relatively cheap.

MinHash is useful for finding near-duplicate documents, like web pages or essays (represented as sets of words or 3-word phrases). It's also useful for finding similar user profiles (as sets of liked items) or overlapping datasets (as sets of keys). With locality-sensitive hashing (LSH), you can index signatures so that similar sets are likely to land in the same bucket, and only compare sets within a bucket.

You can add items to a MinHash sketch, merge sketches, and get and compare signatures. You cannot check if a specific item was added, count the items, remove them, or list them.

## Usage

```text
go get github.com/nalgeon/unsure
```

```go
import "github.com/nalgeon/unsure/minhash"

// Two documents as sets of words.
strA := "the quick brown fox jumps over the lazy dog"
docA := minhash.New(256)
for w := range strings.FieldsSeq(strA) {
    docA.Add([]byte(w))
}

strB := "the quick brown cat jumps over the lazy dog"
docB := minhash.New(256)
for w := range strings.FieldsSeq(strB) {
    docB.Add([]byte(w))
}

sigA, sigB := docA.Signature(), docB.Signature()
sigA.Similarity(sigB) // 0.75 (true value 7/9 = 0.78)
```

The signature is a `[]uint32`, so you can store it, send it, or use its values directly (for example, as LSH band keys).

Merging sketches:

```go
day1 := minhash.New(256) // items seen on day 1
day2 := minhash.New(256) // items seen on day 2
// ...
day1.Merge(day2)
day1.Signature() // signature of the items seen on either day
```

The sketch has one setting: the signature size _k_, which gives a standard error of √(J(1−J)/k) for true similarity J, at most 0.5/√k (at J=0.5). For example, with k=256 and J=0.5, 68% of estimates are within ±3.1% (0.47–0.53), 95% within ±6.2% (2·stdErr), and 99.7% within ±9.4% (3·stdErr).

| k    | Error at J=0.5 | Signature |
| ---- | -------------: | --------: |
| 64   |           6.3% |     256 B |
| 128  |           4.4% |     512 B |
| 256  |           3.1% |      1 KB |
| 512  |           2.2% |      2 KB |
| 1024 |           1.6% |      4 KB |

This error applies to sets that are much larger than k. For smaller sets, the error can be up to 1.2× higher.

Sketches and signatures can only be merged or compared if they have the same k. The sketch is deterministic, so the same items always produce the same signature. The sketch is not designed for adversarial input: anyone who knows the seed can create items that distort the estimate.

## How it works

**Classic MinHash** hashes every item with _k_ different hash functions. Here are two sets, A = {fox, cat, dog, owl} and B = {fox, dog, emu}, and their item hashes for k=4:

```text
item   h1     h2     h3     h4     in A   in B
fox    0.07   0.52   0.66   0.38    ✓      ✓
cat    0.21   0.15   0.80   0.91    ✓
dog    0.48   0.33   0.09   0.74    ✓      ✓
emu    0.63   0.27   0.44   0.12           ✓
owl    0.90   0.71   0.58   0.29    ✓
```

For each hash function, the set keeps only the smallest hash value among its items. These k minimums are the set's _signature_. To estimate the similarity of two sets, compare their signatures position by position, count the number of equal values, and divide by k:

```text
             h1     h2     h3     h4
A signature  0.07   0.15   0.09   0.29
B signature  0.07   0.27   0.09   0.12
equal        ✓             ✓
             → 2 of 4 = 0.5
```

The estimated similarity (0.5) is different from the actual value (2 shared items / 5 distinct items = 0.4). To get a more accurate estimate, k is usually much larger than in this example.

**One permutation hashing** hashes every item once, with a single hash function, and splits the range of hash values into k equal buckets. Each bucket keeps the smallest hash value among its items. Here are the same sets with k=4, using h1 from above:

```text
bucket       0            1            2            3
range        0.00–0.25    0.25–0.50    0.50–0.75    0.75–1.00

A items      fox 0.07     dog 0.48                  owl 0.90
             cat 0.21
B items      fox 0.07     dog 0.48     emu 0.63

A minimums   0.07         0.48         ·            0.90
B minimums   0.07         0.48         0.63         ·
```

Each bucket works like a separate MinHash over the items in its range. So the bucket minimums form a signature, and comparing two signatures works the same way. But there is a catch: when a set isn't much larger than k, some buckets end up empty (shown as · above), so there's nothing to compare in them.

**Optimal densification** solves this by filling each empty bucket with the value from another bucket in the same set. Each empty bucket tries other buckets in a pseudo-random order and copies the value of the first non-empty one:

```text
bucket       0         1         2         3
A minimums   0.07      0.48      ·         0.90
B minimums   0.07      0.48      0.63      ·

A signature  0.07      0.48      0.90 (3)  0.90
B signature  0.07      0.48      0.63  │   0.07 (0)
equal        ✓         ✓               │         │
             → 2 of 4 = 0.5            │         │
                                       │         │
             which bucket the value was copied from
```

This order depends only on the empty bucket's position, so two similar sets tend to fill empty spots with the same values. This makes the estimate work for sets of any size, with about the same error as classic MinHash.

**This implementation** keeps the k bucket minimums in the sketch. `Add` and `Merge` use the original buckets. Densification only happens in `Signature`, and it makes a copy of the buckets without modifying the sketch.

Each item is hashed once with 64-bit [wyhash](https://github.com/wangyi-fudan/wyhash). Multiplying the hash by k once gives both the bucket and the value to store:

```text
┌─────────────────┬─────────────────┬─────────────────┐
│ high 64 bits:   │ next 32 bits:   │ rest:           │
│ bucket in [0,k) │ value           │ unused          │
└─────────────────┴─────────────────┴─────────────────┘
127              64                32                 0
```

The value is the item's position within the bucket's range, so the smallest value in a bucket belongs to the item with the smallest hash. This works for any k, not just powers of two.

Densification fills empty buckets in a coordinated way, spreading the copies evenly across the non-empty buckets. This keeps the error close to classic MinHash, and makes densification fast even for small sets.

## Benchmarks

Compared to [dgryski/go-minhash](https://github.com/dgryski/go-minhash) (classic MinHash), with the same k and the same wyhash hashing. Apple M1, 16-byte items, ns/op (lower is better):

| Operation  |    k |  unsure | dgryski |
| ---------- | ---: | ------: | ------: |
| Add        |   64 |   **5** |      30 |
|            |  256 |   **5** |     115 |
|            | 1024 |   **5** |     358 |
| Similarity |   64 |      25 |      24 |
|            |  256 |     104 |     103 |
|            | 1024 |     378 |     376 |
| Merge      |   64 |  **28** |      32 |
|            |  256 | **106** |     132 |
|            | 1024 | **380** |     500 |

_unsure_ hashes each item once, so `Add` takes the same time for any k, 6–70× faster than _dgryski_, which computes and compares k values per item. Comparing signatures is the same simple loop in both.

`Signature` copies the buckets and fills the empty ones, so its time depends on the set size n. ns/op:

|    k |  n = 1 | n = k/16 | n = k/4 | n = 100·k |
| ---: | -----: | -------: | ------: | --------: |
|   64 |    650 |      675 |     355 |        56 |
|  256 |  4,250 |    1,780 |   1,200 |       199 |
| 1024 | 43,200 |    6,380 |   4,610 |       729 |

For large sets, almost no buckets are empty, and `Signature` just copies k values. For smaller sets, filling the empty buckets takes up to about k·ln k steps, so the time grows as the set gets smaller.

Accuracy of the similarity estimate at k=256 and J=0.5, RMSE over 5,000 pairs of sets (±1%), by the number of distinct items in both sets (lower is better):

| Distinct items |     unsure |   dgryski |
| -------------- | ---------: | --------: |
| 16             |      3.19% | **3.15%** |
| 64             |      3.11% |     3.11% |
| 256            |  **2.89%** |     3.07% |
| 1,024          |  **2.84%** |     3.12% |
| 4,096          |  **2.99%** |     3.09% |
| 16,384         |      3.11% | **3.07%** |
| Memory         | **1.0 KB** |    2.0 KB |

For large sets, both _unsure_ and _dgryski_ roughly match the classic MinHash error (√(J(1−J)/k) = 3.12%). For sets of a few hundred to a few thousand items (256 to 1,024), _unsure_'s error is a bit lower, because each bucket samples a different item, while classic MinHash's hash functions can pick the same one. _dgryski_ stores 64-bit values, so it uses twice as much memory.

Overall, _unsure_ adds items 6–70× faster than _dgryski_, compares signatures at the same speed, merges 1.1–1.3× faster, has the same accuracy, and uses half the memory.

To reproduce, run `make minhash-bench-full` and `make minhash-accuracy` from the repository root.

## Papers

[One permutation hashing](https://papers.nips.cc/paper/2012/hash/eaa32c96f620053cf442ad32258076b9-Abstract.html) (Ping Li, Art Owen, Cun-Hui Zhang, 2012): the bucket scheme.

[Optimal densification for fast and accurate minwise hashing](https://arxiv.org/abs/1703.04664) (Anshumali Shrivastava, 2017): filling the empty buckets.

[On the resemblance and containment of documents](https://www.cs.princeton.edu/courses/archive/spring13/cos598C/broder97resemblance.pdf) (Andrei Broder, 1997): classic MinHash.
