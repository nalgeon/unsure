REF := bloom/testdata/bitsblooms.txt

.PHONY: bloom-test bloom-bench bloom-bench-ref bloom-bench-full

# Run the tests.
bloom-test:
	go test ./bloom

# Fast single-run benchmark of our Set, compared against the recorded reference results.
bloom-bench:
	go test -run x -bench '//impl=unsure' -benchtime 1000000x ./bloom | benchstat -col /impl $(REF) -

# Record reference (bits-and-blooms) results. Rerun on a new machine or Go version.
bloom-bench-ref:
	go test -run x -bench '//impl=bitsblooms' -count 6 ./bloom > $(REF)

# Full statistically sound comparison of both implementations (slow, several minutes).
bloom-bench-full:
	go test -run x -bench . -count 10 ./bloom | benchstat -col /impl -
