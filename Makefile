BLOOM_REF := testdata/bloom-reference.txt
CM_REF := testdata/countmin-reference.txt
FUSE_REF := testdata/fuse-reference.txt
HLL_REF := testdata/hll-reference.txt
LH_REF := testdata/loghist-reference.txt
MH_REF := testdata/minhash-reference.txt
TD_REF := testdata/tdigest-reference.txt
TK_REF := testdata/topk-reference.txt
ULL_REF := testdata/ull-reference.txt

# Fuzzing time per target, e.g. make loghist-fuzz FUZZTIME=10m.
FUZZTIME ?= 2m

.PHONY: bloom-test bloom-bench bloom-bench-ref bloom-bench-full bloom-size
.PHONY: fuse-test fuse-bench fuse-bench-ref fuse-bench-full fuse-size fuse-fuzz
.PHONY: countmin-test countmin-bench countmin-bench-ref countmin-bench-full countmin-accuracy
.PHONY: hll-test hll-bench hll-bench-ref hll-bench-full hll-accuracy
.PHONY: minhash-test minhash-bench minhash-bench-ref minhash-bench-full minhash-accuracy
.PHONY: loghist-test loghist-bench loghist-bench-ref loghist-bench-full loghist-accuracy loghist-fuzz
.PHONY: tdigest-test tdigest-bench tdigest-bench-ref tdigest-bench-full tdigest-accuracy tdigest-fuzz
.PHONY: topk-test topk-bench topk-bench-ref topk-bench-full topk-accuracy topk-fuzz
.PHONY: ull-test ull-bench ull-bench-ref ull-bench-full ull-accuracy ull-fuzz

# Typical tasks for each data structure:
# - "test" - functional and accuracy tests.
# - "size" and "accuracy" - memory usage and accuracy tests.
# - "fuzz" - fuzzing for correctness.
# - "bench" - fast benchmarks compared against the recorded reference results.
# - "bench-ref" - record reference results. Rerun on a new machine or Go version.
# - "bench-full" - slow, statistically sound comparisons of all implementations.

# Bloom filter.

bloom-test:
	@GOOS=linux GOARCH=386 go vet ./bloom
	@go test ./bloom
	@go test -C bench -run Bloom .

bloom-bench:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=unsure' -benchtime 1000000x -count 3 . | benchstat -col /impl bench/$(BLOOM_REF) -

bloom-bench-ref:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=(bitsblooms|gloom)' -count 6 . > bench/$(BLOOM_REF)

bloom-bench-full:
	@go test -C bench -run x -bench 'Bloom(Add|Contains)' -count 10 . | benchstat -col /impl -

bloom-size:
	@go test -C bench -v -run '^TestBloomSize$$' . | grep 'bits/item' | sed 's/^.*: n=/n=/'

# Binary fuse filter.

fuse-test:
	@GOOS=linux GOARCH=386 go vet ./fuse
	@go test ./fuse
	@go test -C bench -run Fuse .

fuse-bench:
	@(go test -C bench -run x -bench 'FuseContains.*///impl=unsure' -count 3 . && \
	  go test -C bench -run x -bench 'FuseNew///impl=unsure' -count 3 .) | benchstat -col /impl bench/$(FUSE_REF) -

fuse-bench-ref:
	@(go test -C bench -run x -bench 'FuseContains.*///impl=(xorfilter|bloom)' -count 6 . && \
	  go test -C bench -run x -bench 'FuseNew///impl=(xorfilter|bloom)' -count 6 .) > bench/$(FUSE_REF)

fuse-bench-full:
	@(go test -C bench -run x -bench 'FuseContains' -count 10 . && \
	  go test -C bench -run x -bench 'FuseNew' -count 10 .) | benchstat -col /impl -

fuse-size:
	@go test -C bench -v -run '^TestFuseSize$$' . | grep 'bits/item' | sed 's/^.*: n=/n=/'

fuse-fuzz:
	@go test ./fuse -run '^$$' -fuzz '^FuzzFilter$$' -fuzztime $(FUZZTIME)

# Count-min sketch.

countmin-test:
	@GOOS=linux GOARCH=386 go vet ./countmin
	@go test ./countmin
	@go test -C bench -run CountMin .

countmin-bench:
	@go test -C bench -run x -bench 'CountMin(Add|Count).*//impl=unsure' -benchtime 1000000x -count 3 . | benchstat -col /impl bench/$(CM_REF) -

countmin-bench-ref:
	@go test -C bench -run x -bench 'CountMin(Add|Count).*//impl=boom' -count 6 . > bench/$(CM_REF)

countmin-bench-full:
	@go test -C bench -run x -bench 'CountMin(Add|Count)' -count 10 . | benchstat -col /impl -

countmin-accuracy:
	@go test -C bench -v -run '^TestCountMinAccuracy$$' . | grep 'err mean' | sed 's/^.*_test.go:[0-9]*: //'

# HyperLogLog.

hll-test:
	@GOOS=linux GOARCH=386 go vet ./hll
	@go test ./hll
	@go test -C bench -run HLL .

hll-bench:
	@(go test -C bench -run x -bench 'HLLAdd.*//impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'HLLCount//impl=unsure' -benchtime 200ms -count 3 . && \
	  go test -C bench -run x -bench 'HLLMerge///impl=unsure' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(HLL_REF) -

hll-bench-ref:
	@(go test -C bench -run x -bench 'HLLAdd.*//impl=(axiom|boom)' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'HLLCount//impl=(axiom|boom)' -count 6 . && \
	  go test -C bench -run x -bench 'HLLMerge///impl=(axiom|boom)' -count 6 .) > bench/$(HLL_REF)

hll-bench-full:
	@(go test -C bench -run x -bench 'HLLAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'HLL(Count|Merge)' -count 10 .) | benchstat -col /impl -

hll-accuracy:
	@go test -C bench -v -run '^TestHLL(Accuracy|Size)$$' . | grep -E 'rmse=|KB' | sed 's/^.*_test.go:[0-9]*: //'

# UltraLogLog.

ull-test:
	@GOOS=linux GOARCH=386 go vet ./ull
	@go test ./ull
	@go test -C bench -run ULL .

ull-bench:
	@(go test -C bench -run x -bench 'ULLAdd.*//impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'ULLCount//impl=unsure' -benchtime 200ms -count 3 . && \
	  go test -C bench -run x -bench 'ULLMerge///impl=unsure' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(ULL_REF) -

ull-bench-ref:
	@(go test -C bench -run x -bench 'ULLAdd.*//impl=(hll|axiom)' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'ULLCount//impl=(hll|axiom)' -count 6 . && \
	  go test -C bench -run x -bench 'ULLMerge///impl=(hll|axiom)' -count 6 .) > bench/$(ULL_REF)

ull-bench-full:
	@(go test -C bench -run x -bench 'ULLAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'ULL(Count|Merge)' -count 10 .) | benchstat -col /impl -

ull-accuracy:
	@go test -C bench -v -run '^TestULL(Accuracy|Size)$$' . | grep -E 'rmse=|KB' | sed 's/^.*_test.go:[0-9]*: //'

ull-fuzz:
	@go test ./ull -run '^$$' -fuzz '^FuzzSketch$$' -fuzztime $(FUZZTIME)

# MinHash.

minhash-test:
	@GOOS=linux GOARCH=386 go vet ./minhash
	@go test ./minhash
	@go test -C bench -run MinHash .

minhash-bench:
	@(go test -C bench -run x -bench 'MinHashAdd//impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'MinHash(Similarity|Merge)//impl=unsure' -benchtime 200ms -count 3 . && \
	  go test -C bench -run x -bench 'MinHashSignature' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(MH_REF) -

minhash-bench-ref:
	@(go test -C bench -run x -bench 'MinHashAdd//impl=dgryski' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'MinHash(Similarity|Merge)//impl=dgryski' -count 6 .) > bench/$(MH_REF)

minhash-bench-full:
	@(go test -C bench -run x -bench 'MinHashAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'MinHash(Signature|Similarity|Merge)' -count 10 .) | benchstat -col /impl -

minhash-accuracy:
	@go test -C bench -v -run '^TestMinHash(Accuracy|Size)$$' . | grep -E 'rmse=|KB' | sed 's/^.*_test.go:[0-9]*: //'

# Log-linear histogram.

loghist-test:
	@GOOS=linux GOARCH=386 go vet ./loghist
	@go test ./loghist
	@go test -C bench -run Loghist .

loghist-bench:
	@(go test -C bench -run x -bench 'LoghistAdd.*//impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'Loghist(Quantile|Merge).*//impl=unsure' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(LH_REF) -

loghist-bench-ref:
	@(go test -C bench -run x -bench 'LoghistAdd.*//impl=(hdr|dd)' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'Loghist(Quantile|Merge).*//impl=(hdr|dd)' -count 6 .) > bench/$(LH_REF)

loghist-bench-full:
	@(go test -C bench -run x -bench 'LoghistAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'Loghist(Quantile|Merge)' -count 10 .) | benchstat -col /impl -

loghist-accuracy:
	@go test -C bench -v -run '^TestLoghist(Accuracy|Size)$$' . | grep -E 'rel err|KB' | sed 's/^.*_test.go:[0-9]*: //'

loghist-fuzz:
	@go test ./loghist -run '^$$' -fuzz '^FuzzHistogram$$' -fuzztime $(FUZZTIME)

# T-Digest.

tdigest-test:
	@GOOS=linux GOARCH=386 go vet ./tdigest
	@go test ./tdigest
	@go test -C bench -run TDigest .

tdigest-bench:
	@(go test -C bench -run x -bench 'TDigestAdd.*//impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'TDigest(Quantile|CDF|Merge).*//impl=unsure' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(TD_REF) -

tdigest-bench-ref:
	@(go test -C bench -run x -bench 'TDigestAdd.*//impl=(influx|caio)' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'TDigest(Quantile|CDF|Merge).*//impl=(influx|caio)' -count 6 .) > bench/$(TD_REF)

tdigest-bench-full:
	@(go test -C bench -run x -bench 'TDigestAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'TDigest(Quantile|CDF|Merge)' -count 10 .) | benchstat -col /impl -

tdigest-accuracy:
	@go test -C bench -v -run '^TestTDigest(Accuracy|Size)$$' . | grep -E 'rank err|KB' | sed 's/^.*_test.go:[0-9]*: //'

tdigest-fuzz:
	@go test ./tdigest -run '^$$' -fuzz '^FuzzDigest$$' -fuzztime $(FUZZTIME)

# Top-K sketch.

topk-test:
	@GOOS=linux GOARCH=386 go vet ./topk
	@go test ./topk
	@go test -C bench -run TopK .

topk-bench:
	@(go test -C bench -run x -bench 'TopKAdd/.*/impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'TopKTop/.*/impl=unsure' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(TK_REF) -

topk-bench-ref:
	@(go test -C bench -run x -bench 'TopKAdd/.*/impl=(keiler|dgryski)' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'TopKTop/.*/impl=(keiler|dgryski)' -count 6 .) > bench/$(TK_REF)

topk-bench-full:
	@(go test -C bench -run x -bench 'TopKAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'TopKTop' -count 10 .) | benchstat -col /impl -

topk-accuracy:
	@go test -C bench -v -run '^TestTopK(Accuracy|Drift)$$' . | grep 'impl=' | sed 's/^.*_test.go:[0-9]*: //'

topk-fuzz:
	@go test ./topk -run '^$$' -fuzz '^FuzzSketch$$' -fuzztime $(FUZZTIME)
