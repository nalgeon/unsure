module github.com/nalgeon/unsure/bench

go 1.25

require (
	github.com/bits-and-blooms/bloom/v3 v3.7.1
	github.com/jcalabro/gloom v0.1.0
	github.com/nalgeon/unsure v0.0.0
)

require (
	github.com/bits-and-blooms/bitset v1.24.2 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
)

replace github.com/nalgeon/unsure => ../
