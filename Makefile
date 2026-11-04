BLOOM_REF := testdata/bloom-reference.txt
CM_REF := testdata/countmin-reference.txt

.PHONY: bloom-test bloom-bench bloom-bench-ref bloom-bench-full bloom-size
.PHONY: countmin-test countmin-bench countmin-bench-ref countmin-bench-full countmin-accuracy

# Run the tests: our Set, then the size comparison against other libraries.
bloom-test:
	@GOOS=linux GOARCH=386 go vet ./bloom
	@go test ./bloom
	@go test -C bench -run Bloom .

# Fast benchmark (median of 3 runs) of our Set, compared against the recorded reference results.
bloom-bench:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=unsure' -benchtime 1000000x -count 3 . | benchstat -col /impl bench/$(BLOOM_REF) -

# Record reference (bits-and-blooms, gloom) results. Rerun on a new machine or Go version.
bloom-bench-ref:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=(bitsblooms|gloom)' -count 6 . > bench/$(BLOOM_REF)

# Full statistically sound comparison of all implementations (slow, several minutes).
bloom-bench-full:
	@go test -C bench -run x -bench 'Bloom(Add|Contains)' -count 10 . | benchstat -col /impl -

# Memory use (bits/item) of our Set vs the other libraries.
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
