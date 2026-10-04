package samplingprofiler

import "testing"

func TestDownsampleDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Downsample = DownsampleDeterministic
	cfg.DownsampleKeepNumerator = 1
	cfg.DownsampleKeepDenominator = 3
	s := Sample{Source: "u", TimestampNanos: 42}
	fp := stackFingerprint([]Frame{{Function: "f"}})
	first := keepSample(cfg, s, fp)
	for i := 0; i < 10; i++ {
		if keepSample(cfg, s, fp) != first {
			t.Fatal("decision must be stable across repeated evaluations")
		}
	}

	// 大致比例：大量不同采样应接近 1/3。
	kept := 0
	n := 6000
	for i := 0; i < n; i++ {
		ss := Sample{Source: "u", TimestampNanos: int64(i)}
		if keepSample(cfg, ss, fp) {
			kept++
		}
	}
	ratio := float64(kept) / float64(n)
	if ratio < 0.28 || ratio > 0.38 {
		t.Fatalf("keep ratio out of expected band: %f", ratio)
	}
}
