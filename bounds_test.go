package samplingprofiler

import (
	"testing"
)

func TestMemoryBoundedUnderLargeInput(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxNodes = 50
	cfg.DedupCapacityPerSource = 16
	cfg.MaxSources = 2
	a := NewAggregator(cfg)

	// 每个来源产生大量不同栈、不同时间戳的采样。
	const perSource = 20000
	for u := 0; u < 2; u++ {
		for i := 0; i < perSource; i++ {
			a.Add(Sample{
				Source:         string(rune('a' + u)),
				TimestampNanos: int64(u*perSource + i + 1),
				ValueNanos:     1,
				Stack: []Frame{
					{Function: "root"},
					{Function: "leaf", Line: i}, // 每个都不同 -> 新节点
				},
			})
		}
	}
	snap := a.Snapshot()

	// 节点数不得超过上限。
	if len(snap.Nodes) > cfg.MaxNodes {
		t.Fatalf("node cap violated: %d > %d", len(snap.Nodes), cfg.MaxNodes)
	}
	// 计数必须能核对：节点上限丢弃必须被记录。
	if snap.Stats.NodesCapDropped == 0 {
		t.Fatal("expected NodesCapDropped to be reported")
	}
	st := snap.Stats
	if st.Received != 2*perSource {
		t.Fatalf("received wrong: %+v", st)
	}
	accounted := st.Accepted + st.NodesCapDropped + st.Duplicates +
		st.Downsampled + st.EmptyStacks + st.SourcesRejected
	if accounted != st.Received {
		t.Fatalf("counters do not account for all inputs: %+v", st)
	}
	// 去重淘汰必须被计数（容量 16，输入 20000/单元）。
	if st.DedupEvictions == 0 {
		t.Fatal("expected LRU evictions to be reported")
	}

	// 再灌同样规模的数据，节点数依然有界、结果不再增长。
	before := len(a.Snapshot().Nodes)
	for u := 0; u < 2; u++ {
		for i := 0; i < perSource; i++ {
			a.Add(Sample{
				Source:         string(rune('a' + u)),
				TimestampNanos: int64(3*perSource + u*perSource + i + 1),
				ValueNanos:     1,
				Stack:          []Frame{{Function: "root"}, {Function: "leaf", Line: i}},
			})
		}
	}
	after := a.Snapshot()
	if len(after.Nodes) != before || len(after.Nodes) > cfg.MaxNodes {
		t.Fatalf("node set must stay bounded: before=%d after=%d cap=%d", before, len(after.Nodes), cfg.MaxNodes)
	}
}

func TestSourceCapRejection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSources = 2
	a := NewAggregator(cfg)
	a.Add(Sample{Source: "a", TimestampNanos: 1, ValueNanos: 1, Stack: mkStack("f")})
	a.Add(Sample{Source: "b", TimestampNanos: 1, ValueNanos: 1, Stack: mkStack("f")})
	a.Add(Sample{Source: "c", TimestampNanos: 1, ValueNanos: 1, Stack: mkStack("f")})
	st := a.Stats()
	if st.SourcesRejected != 1 || st.Accepted != 2 {
		t.Fatalf("source cap accounting wrong: %+v", st)
	}
	// 被拒来源后来也不应被接纳（没有为它建立任何状态）。
	a.Add(Sample{Source: "c", TimestampNanos: 2, ValueNanos: 1, Stack: mkStack("f")})
	if a.Stats().SourcesRejected != 2 {
		t.Fatalf("rejected source must keep being rejected: %+v", a.Stats())
	}
}
