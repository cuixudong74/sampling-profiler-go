package samplingprofiler

import (
	"hash/fnv"
	"strconv"
	"strings"
)

// 归一化管线（顺序固定，全部为确定性变换）：
//  1. 未知帧处理：Function 为空视为未知帧，按 Unknown 策略
//     保留 / 丢弃 / 替换为统一 "<unknown>" 帧；
//  2. 相邻重复帧：按 Repeated 策略折叠（只影响被折叠的帧，
//     不改变其余帧）；
//  3. 超深截断：超过 MaxDepth 时按 Truncation 策略在靠近根处
//     截断，TailDrop 直接丢弃深层帧，Sentinel 追加 "<truncated>"
//     哨兵帧。截断只作用于被截掉的尾部，其余节点统计不受影响。

// 哨兵帧是全局统一的，保证不同来源的同类帧合并到同一节点。
var (
	sentinelUnknownFrame   = Frame{Function: "<unknown>"}
	sentinelTruncatedFrame = Frame{Function: "<truncated>"}
)

// stackTransformCounters 记录一次栈归一化产生的计数。
type stackTransformCounters struct {
	unknown   int64
	collapsed int64
	truncated bool
}

// normalizeStack 按配置对原始栈做确定性归一化。
// 返回的切片永远是新分配的，不与入参共享底层数组。
func normalizeStack(cfg Config, stack []Frame) ([]Frame, stackTransformCounters) {
	var ctr stackTransformCounters

	// 1. 未知帧处理。
	out := make([]Frame, 0, len(stack))
	for _, f := range stack {
		if f.Function == "" {
			switch cfg.Unknown {
			case UnknownDrop:
				ctr.unknown++
				continue
			case UnknownSentinel:
				ctr.unknown++
				f = sentinelUnknownFrame
			case UnknownKeep:
			}
		}
		out = append(out, f)
	}

	// 2. 相邻重复帧折叠。
	if cfg.Repeated == RepeatedCollapse && len(out) > 1 {
		collapsed := make([]Frame, 0, len(out))
		collapsed = append(collapsed, out[0])
		for i := 1; i < len(out); i++ {
			if frameKey(out[i]) == frameKey(out[i-1]) {
				ctr.collapsed++
				continue
			}
			collapsed = append(collapsed, out[i])
		}
		out = collapsed
	}

	// 3. 超深截断（始终保留靠近根的前缀，保证截断策略可解释）。
	if cfg.MaxDepth > 0 && len(out) > cfg.MaxDepth {
		ctr.truncated = true
		switch cfg.Truncation {
		case TruncationTailDrop:
			out = append([]Frame(nil), out[:cfg.MaxDepth]...)
		default: // TruncationSentinel
			kept := cfg.MaxDepth - 1
			if kept < 0 {
				kept = 0
			}
			trimmed := make([]Frame, 0, kept+1)
			trimmed = append(trimmed, out[:kept]...)
			trimmed = append(trimmed, sentinelTruncatedFrame)
			out = trimmed
		}
	}

	return out, ctr
}

// frameKey 返回单个帧的稳定字符串标识。
// 采用长度前缀避免分隔符歧义：fnLen:function \x00 line \x00 fileLen:file。
func frameKey(f Frame) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(len(f.Function)))
	b.WriteByte(':')
	b.WriteString(f.Function)
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(f.Line))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(len(f.File)))
	b.WriteByte(':')
	b.WriteString(f.File)
	return b.String()
}

// nodeKey 返回从根开始整条路径的稳定标识，各帧以记录分隔符连接。
func nodeKey(stack []Frame) string {
	if len(stack) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range stack {
		if i > 0 {
			b.WriteByte(0x1e)
		}
		b.WriteString(frameKey(f))
	}
	return b.String()
}

// stackFingerprint 返回归一化栈的稳定 64 位指纹（FNV-1a）。
func stackFingerprint(stack []Frame) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(nodeKey(stack)))
	return h.Sum64()
}
