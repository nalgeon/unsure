package bloom

import "testing"

// Upstream test vectors: test_vector.cpp in github.com/wangyi-fudan/wyhash
// (wyhash final 4.3, default secrets).
func TestWyhashVectors(t *testing.T) {
	tests := []struct {
		msg  string
		seed uint64
		want uint64
	}{
		{"", 0, 0x93228a4de0eec5a2},
		{"a", 1, 0xc5bac3db178713c4},
		{"abc", 2, 0xa97f2f7b1d9b3314},
		{"message digest", 3, 0x786d1f1df3801df4},
		{"abcdefghijklmnopqrstuvwxyz", 4, 0xdca5a8138ad37c87},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", 5, 0xb9e734f117cfaf70},
		{"12345678901234567890123456789012345678901234567890123456789012345678901234567890", 6, 0x6cc5eab49a92d617},
	}
	for _, tt := range tests {
		got := wyhash([]byte(tt.msg), wyseed(tt.seed))
		if got != tt.want {
			t.Errorf("wyhash(%q, %d) = %#x, want %#x", tt.msg, tt.seed, got, tt.want)
		}
	}
}

// Upstream outputs for every length 0..100 (covers all branches),
// for b[i] = i*7+1 and seed 0x123456789abcdef0.
func TestWyhashLengths(t *testing.T) {
	want := []uint64{
		0x24fb23c660c73639, 0xeaf261ecc13556f9, 0x66ad8210684d6641, 0x665f889594537fa8,
		0x90c947f0c69d5511, 0xa37cf1a433047bd6, 0xfd4e4406f6efe7fd, 0x0e858bcb2a562bd6,
		0x5d885d8d63bb28c1, 0x1c324835610fe703, 0xd4ea3f398f661a32, 0x219d476cd0f03864,
		0x510fc927a0e8ce77, 0xc214aaa83aee40ce, 0xa9e41b7c9c0ed4f1, 0xd9fbd0e554666b41,
		0xdb960e41a17d55e7, 0x295ff4aca85c6b1d, 0xe4637d9db77466f1, 0x054acef029099b7a,
		0x0eed14e43ac511e2, 0x772f03b22a296ede, 0xe547d10e4442feb6, 0x2e235f83e4b5b7b9,
		0x37fcd59f52668f3f, 0xa120a39b9ee60e2a, 0x194cf8fb63049d69, 0x9ba211a54c1238db,
		0x5bb952d339739795, 0xe59f0151a9f47966, 0xcc516e71ce2e13b9, 0x5fe30ee96a667d5d,
		0xdb1c61b1da15738b, 0xd1525ee9668cc043, 0x4d549d191bfce369, 0x9257191691eab94b,
		0x77418199b9798241, 0x90d6863ecd888724, 0x55982841ad1b2b02, 0x4a7e90f9d6eadf24,
		0xf4db0c2240e51fe0, 0x064415142bee645a, 0xce62b6c22dae0ab9, 0x27708a7ee79eb338,
		0xc73159499fa30299, 0x4e7594e7e0f1a4b6, 0x61aca16185ad88f1, 0x4340b4e51b638698,
		0xb09cbb0e44c3a549, 0xf0f651aa9f315e89, 0xeb6d7df0e198b0a4, 0x6e35c01c1aeb6080,
		0xfc34bbf2d3ac40d9, 0x94b950bfa980b363, 0x949155cff584f4f5, 0xa5dd71b9fdade763,
		0x9dfbea03f4a4de63, 0x600bc84b29808f61, 0x199cbbbcaf6b8677, 0x58e4270a87f25c66,
		0x2ffc8d71a13e1317, 0x61b5c7311d042288, 0x92fccecb0ebb7744, 0xd4e2062065a16c6c,
		0xd7a5781022e276f5, 0x941528953a24280d, 0x30cc5dc22c5a5e58, 0xb0a16bd6a630ab0d,
		0x5682f00e066a88ac, 0x4144096757461c66, 0x48cb9bec8e342db7, 0xe907f161adb3bb53,
		0x927e788446d94d0b, 0x5c14254bbbfa13a6, 0x5642ab916c295bb0, 0x832cb3efc5254abb,
		0xfd81873e9dd8c8e3, 0x78ddb79149c0448e, 0xd8a6ce0bdb60f0f9, 0xe8ba35852af86fae,
		0x75e259e32e9b3393, 0x3745db541e1ef056, 0x5343d62c50b810ad, 0xc0a7cc8d29c2a504,
		0xc11435b4006a5d0e, 0x1f1a6447da2e4c45, 0x8d364061b4bd215b, 0x84797ad86e18a0f7,
		0x50d8fedfa1ae3e1e, 0xab0f1b22216b98f4, 0x41fefbed9818a899, 0x7e185f2f3f4611ed,
		0x842544b2fda2aa4c, 0x16490327c916a23a, 0x2c9b103c21237fb5, 0x4cfa4b330dd073f2,
		0xc1a48b367a330389, 0xb57c8f1758816704, 0xc2bb9e4f64bba86f, 0x50e4d1f73314a2d6,
		0xf1561592e804076a,
	}
	var b [100]byte
	for i := range b {
		b[i] = byte(i*7 + 1)
	}
	seed := wyseed(0x123456789abcdef0)
	for n, w := range want {
		if got := wyhash(b[:n], seed); got != w {
			t.Errorf("len %d: got %#x, want %#x", n, got, w)
		}
	}
}
