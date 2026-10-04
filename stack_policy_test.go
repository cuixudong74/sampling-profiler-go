package samplingprofiler

import "testing"

// TestDeepStackAndUnknown 覆盖超深栈与未知帧：策略只影响被处理的帧，
// 其余节点的统计结果与“没有这些异常帧”时完全一致。
func TestDeepStackAndUnknown(t *testing.T) {
	deep := func(n int) []Frame {
		fs := make([]Frame, n)
		for i := range fs {
			fs[i] = Frame{Function: "f", Line: i}
		}
		return fs
	}

	cfg := DefaultConfig()
	cfg.MaxDepth = 5
	cfg.Truncation = TruncationSentinel
	a := NewAggregator(cfg)
	for i := 0; i < 100; i++ {
		a.Add(Sample{Source: "u", TimestampNanos: int64(i + 1), ValueNanos: 1, Stack: deep(50)})
	}
	snap := a.Snapshot()
	if snap.Stats.TruncatedStacks != 100 {
		t.Fatalf("truncated count wrong: %d", snap.Stats.TruncatedStacks)
	}
	for _, n := range snap.Nodes {
		if n.Stats.CumulativeNanos != 100 {
			t.Fatalf("every surviving node must see all 100 samples, got %d at %s", n.Stats.CumulativeNanos, n.NodeKey)
		}
	}
	leaf := snap.Nodes[len(snap.Nodes)-1]
	if leaf.Path[len(leaf.Path)-1].Function != "<truncated>" || leaf.Stats.SelfNanos != 100 {
		t.Fatalf("sentinel leaf wrong: %+v", leaf)
	}

	// 未知帧策略之间，对“其余已知节点”的统计互不影响。
	run := func(p UnknownFramePolicy) Snapshot {
		c := DefaultConfig()
		c.Unknown = p
		ag := NewAggregator(c)
		ag.Add(Sample{Source: "u", TimestampNanos: 1, ValueNanos: 10,
			Stack: []Frame{{Function: "root"}, {Function: ""}, {Function: "known"}}})
		return ag.Snapshot()
	}
	sKeep := run(UnknownKeep)
	sDrop := run(UnknownDrop)
	sSent := run(UnknownSentinel)
	findLeaf := func(sn Snapshot, fn string) (NodeStats, bool) {
		for _, n := range sn.Nodes {
			if n.Path[len(n.Path)-1].Function == fn {
				return n.Stats, true
			}
		}
		return NodeStats{}, false
	}
	// "root" 节点是公共前缀，三种策略下累计耗时必须一致。
	for _, sn := range []Snapshot{sKeep, sDrop, sSent} {
		root, ok := findLeaf(sn, "root")
		if !ok || root.CumulativeNanos != 10 {
			t.Fatalf("root cum must be 10 under every policy: %+v", root)
		}
	}
	// "known" 帧是叶子，三种策略下自身耗时必须一致。
	for _, sn := range []Snapshot{sKeep, sDrop, sSent} {
		known, ok := findLeaf(sn, "known")
		if !ok || known.SelfNanos != 10 || known.CumulativeNanos != 10 {
			t.Fatalf("known leaf must be counted identically: %+v ok=%v", known, ok)
		}
	}
}
