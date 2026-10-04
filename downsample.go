package samplingprofiler

import "hash/fnv"

// 降采样语义：
//   - 仅 DownsampleDeterministic 策略实际丢弃采样；
//   - 决策哈希的输入只有 (Source, TimestampNanos, 归一化栈指纹)，
//     因此同一条采样在任何到达顺序、批次划分、并发度乃至跨进程
//     重放下的取舍结果完全一致；
//   - 保留比例 = KeepNumerator/KeepDenominator（分母为 0 或
//     分子>=分母时全部保留）；
//   - 被丢弃的采样计入 Stats.Downsampled，可与 Accepted 核对。

// keepSample 按策略决定一条采样是否保留。
func keepSample(cfg Config, s Sample, stackFP uint64) bool {
	if cfg.Downsample != DownsampleDeterministic {
		return true
	}
	den := cfg.DownsampleKeepDenominator
	num := cfg.DownsampleKeepNumerator
	if den == 0 || num == 0 {
		return den == 0 // den==0 视为未配置：全保留；num==0 且 den>0：全丢弃
	}
	if num >= den {
		return true
	}
	h := fnv.New64a()
	var buf [8]byte
	writeStr := func(p []byte) { _, _ = h.Write(p) }
	writeStr([]byte(s.Source))
	h.Write([]byte{0})
	putU64(buf[:], uint64(s.TimestampNanos))
	h.Write(buf[:])
	h.Write([]byte{0})
	putU64(buf[:], stackFP)
	h.Write(buf[:])
	return h.Sum64()%den < num
}

func putU64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (uint(i) * 8))
	}
}
