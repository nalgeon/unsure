module github.com/nalgeon/unsure/bench

go 1.25

require (
	github.com/axiomhq/hyperloglog v0.3.0
	github.com/bits-and-blooms/bloom/v3 v3.7.1
	github.com/caio/go-tdigest/v4 v4.1.0
	github.com/dgryski/go-topk v0.0.0-20191119021947-593b4f2374c9
	github.com/influxdata/tdigest v0.0.1
	github.com/jcalabro/gloom v0.1.0
	github.com/keilerkonzept/topk v1.1.4
	github.com/nalgeon/unsure v0.0.0
	github.com/tylertreat/BoomFilters v0.0.0-20251117164519-53813c36cc1b
)

require (
	github.com/OneOfOne/xxhash v1.2.8 // indirect
	github.com/bits-and-blooms/bitset v1.24.2 // indirect
	github.com/d4l3k/messagediff v1.2.1 // indirect
	github.com/dgryski/go-metro v0.0.0-20250106013310-edb8663e5e33 // indirect
	github.com/dgryski/go-sip13 v0.0.0-20200911182023-62edffca9245 // indirect
	github.com/kamstrup/intmap v0.5.2 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
)

replace github.com/nalgeon/unsure => ../
