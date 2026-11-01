REF := testdata/bloom-reference.txt

.PHONY: bloom-test bloom-bench bloom-bench-ref bloom-bench-full bloom-size

# Run the tests: our Set, then the size comparison against other libraries.
bloom-test:
	@go test ./bloom
	@go test -C bench -run Bloom .

# Fast benchmark (median of 3 runs) of our Set, compared against the recorded reference results.
bloom-bench:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=unsure' -benchtime 1000000x -count 3 . | benchstat -col /impl bench/$(REF) -

# Record reference (bits-and-blooms, gloom) results. Rerun on a new machine or Go version.
bloom-bench-ref:
	@go test -C bench -run x -bench 'Bloom(Add|Contains).*//impl=(bitsblooms|gloom)' -count 6 . > bench/$(REF)

# Full statistically sound comparison of all implementations (slow, several minutes).
bloom-bench-full:
	@go test -C bench -run x -bench 'Bloom(Add|Contains)' -count 10 . | benchstat -col /impl -

# Memory use (bits/item) of our Set vs the other libraries.
bloom-size:
	@go test -C bench -v -run '^TestBloomSize$$' . | grep 'bits/item' | sed 's/^.*: n=/n=/'
