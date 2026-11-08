BLOOM_REF := testdata/bloom-reference.txt
CM_REF := testdata/countmin-reference.txt
HLL_REF := testdata/hll-reference.txt
TD_REF := testdata/tdigest-reference.txt

.PHONY: bloom-test bloom-bench bloom-bench-ref bloom-bench-full bloom-size
.PHONY: countmin-test countmin-bench countmin-bench-ref countmin-bench-full countmin-accuracy
.PHONY: hll-test hll-bench hll-bench-ref hll-bench-full hll-accuracy
.PHONY: tdigest-test tdigest-bench tdigest-bench-ref tdigest-bench-full tdigest-accuracy

# Run the tests: our Filter, then the size comparison against other libraries.
bloom-test:
	@GOOS=linux GOARCH=386 go vet ./bloom
	@go test ./bloom
	@go test -C bench -run Bloom .

# Fast benchmark (median of 3 runs) of our Filter, compared against the recorded reference results.
bloom-bench:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=unsure' -benchtime 1000000x -count 3 . | benchstat -col /impl bench/$(BLOOM_REF) -

# Record reference (bits-and-blooms, gloom) results. Rerun on a new machine or Go version.
bloom-bench-ref:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=(bitsblooms|gloom)' -count 6 . > bench/$(BLOOM_REF)

# Full statistically sound comparison of all implementations (slow, several minutes).
bloom-bench-full:
	@go test -C bench -run x -bench 'Bloom(Add|Contains)' -count 10 . | benchstat -col /impl -

# Memory use (bits/item) of our Filter vs the other libraries.
bloom-size:
	@go test -C bench -v -run '^TestBloomSize$$' . | grep 'bits/item' | sed 's/^.*: n=/n=/'

# Run the tests: our Sketch, then the accuracy comparison against other libraries.
countmin-test:
	@GOOS=linux GOARCH=386 go vet ./countmin
	@go test ./countmin
	@go test -C bench -run CountMin .

# Fast benchmark (median of 3 runs) of our Sketch, compared against the recorded reference results.
countmin-bench:
	@go test -C bench -run x -bench 'CountMin(Add|Count).*//impl=unsure' -benchtime 1000000x -count 3 . | benchstat -col /impl bench/$(CM_REF) -

# Record reference (BoomFilters) results. Rerun on a new machine or Go version.
countmin-bench-ref:
	@go test -C bench -run x -bench 'CountMin(Add|Count).*//impl=boom' -count 6 . > bench/$(CM_REF)

# Full statistically sound comparison of all implementations (slow, several minutes).
countmin-bench-full:
	@go test -C bench -run x -bench 'CountMin(Add|Count)' -count 10 . | benchstat -col /impl -

# Memory and error distribution of our Sketch vs the other libraries.
countmin-accuracy:
	@go test -C bench -v -run '^TestCountMinAccuracy$$' . | grep 'err mean' | sed 's/^.*_test.go:[0-9]*: //'

# Run the tests: our Sketch, then the accuracy and size comparison against other libraries.
hll-test:
	@GOOS=linux GOARCH=386 go vet ./hll
	@go test ./hll
	@go test -C bench -run HLL .

# Fast benchmark (median of 3 runs) of our Sketch, compared against the recorded reference results.
# Add runs a fixed number of iterations to cycle through all keys; Count and Merge run for a fixed time.
hll-bench:
	@(go test -C bench -run x -bench 'HLLAdd.*//impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'HLLCount//impl=unsure' -benchtime 200ms -count 3 . && \
	  go test -C bench -run x -bench 'HLLMerge///impl=unsure' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(HLL_REF) -

# Record reference (axiomhq, BoomFilters) results. Rerun on a new machine or Go version.
hll-bench-ref:
	@(go test -C bench -run x -bench 'HLLAdd.*//impl=(axiom|boom)' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'HLL(Count|Merge).*//impl=(axiom|boom)' -count 6 .) > bench/$(HLL_REF)

# Full statistically sound comparison of all implementations (slow, several minutes).
hll-bench-full:
	@(go test -C bench -run x -bench 'HLLAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'HLL(Count|Merge)' -count 10 .) | benchstat -col /impl -

# Error distribution and memory of our Sketch vs the other libraries.
hll-accuracy:
	@go test -C bench -v -run '^TestHLL(Accuracy|Size)$$' . | grep -E 'rmse=|KB' | sed 's/^.*_test.go:[0-9]*: //'

# Run the tests: our Digest, then the accuracy and size comparison against other libraries.
tdigest-test:
	@GOOS=linux GOARCH=386 go vet ./tdigest
	@go test ./tdigest
	@go test -C bench -run TDigest .

# Fast benchmark (median of 3 runs) of our Digest, compared against the recorded reference results.
# Add runs a fixed number of iterations to cycle through all values; the rest run for a fixed time.
tdigest-bench:
	@(go test -C bench -run x -bench 'TDigestAdd.*//impl=unsure' -benchtime 1000000x -count 3 . && \
	  go test -C bench -run x -bench 'TDigest(Quantile|CDF|Merge).*//impl=unsure' -benchtime 200ms -count 3 .) | benchstat -col /impl bench/$(TD_REF) -

# Record reference (influxdata, caio) results. Rerun on a new machine or Go version.
tdigest-bench-ref:
	@(go test -C bench -run x -bench 'TDigestAdd.*//impl=(influx|caio)' -benchtime 1000000x -count 6 . && \
	  go test -C bench -run x -bench 'TDigest(Quantile|CDF|Merge).*//impl=(influx|caio)' -count 6 .) > bench/$(TD_REF)

# Full statistically sound comparison of all implementations (slow, several minutes).
tdigest-bench-full:
	@(go test -C bench -run x -bench 'TDigestAdd' -benchtime 1000000x -count 10 . && \
	  go test -C bench -run x -bench 'TDigest(Quantile|CDF|Merge)' -count 10 .) | benchstat -col /impl -

# Rank error and memory of our Digest vs the other libraries.
tdigest-accuracy:
	@go test -C bench -v -run '^TestTDigest(Accuracy|Size)$$' . | grep -E 'rank err|KB' | sed 's/^.*_test.go:[0-9]*: //'
