package samplingprofiler

import "testing"

func buildSnapshot(cfg Config, samples []Sample) Snapshot {
	a := NewAggregator(cfg)
	for _, s := range samples {
		a.Add(s)
	}
	return a.Snapshot()
}

func TestCompareExact(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DedupCapacityPerSource = 1000
	left := buildSnapshot(cfg, []Sample{
		{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "a")},
		{Source: "u", TimestampNanos: 2, ValueNanos: 10, Stack: mkStack("main", "a")},
		{Source: "u", TimestampNanos: 3, ValueNanos: 10, Stack: mkStack("main", "b")},
	})
	right := buildSnapshot(cfg, []Sample{
		{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "a")},
		{Source: "u", TimestampNanos: 2, ValueNanos: 10, Stack: mkStack("main", "b")},
		{Source: "u", TimestampNanos: 3, ValueNanos: 10, Stack: mkStack("main", "b")},
	})
	d := Compare(left, right, CompareConfig{Policy: MatchExactPath})
	byKey := map[string]NodeDiff{}
	for _, m := range d.Matched {
		byKey[m.NodeKey] = m
	}
	da := byKey[nodeKey(mkStack("main", "a"))]
	if da.SelfLeft != 20 || da.SelfRight != 10 || da.SelfDelta != -10 || da.MatchedBy != "exact" {
		t.Fatalf("unexpected diff for a: %+v", da)
	}
	if len(d.OnlyLeft) != 0 || len(d.OnlyRight) != 0 {
		t.Fatalf("exact match should cover all nodes, got onlyL=%d onlyR=%d", len(d.OnlyLeft), len(d.OnlyRight))
	}
	// 累计口径：main 在两边都累计了 30。
	dm := byKey[nodeKey(mkStack("main"))]
	if dm.CumLeft != 30 || dm.CumRight != 30 || dm.CumDelta != 0 {
		t.Fatalf("main cumulative wrong: %+v", dm)
	}
}

func TestCompareRenames(t *testing.T) {
	cfg := DefaultConfig()
	left := buildSnapshot(cfg, []Sample{
		{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "oldName")},
	})
	right := buildSnapshot(cfg, []Sample{
		{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "newName")},
	})
	// 默认精确策略：两边节点互不匹配。
	d := Compare(left, right, CompareConfig{Policy: MatchExactPath})
	if len(d.Matched) != 1 || len(d.OnlyLeft) != 1 || len(d.OnlyRight) != 1 {
		t.Fatalf("exact policy unexpected: matched=%d L=%d R=%d", len(d.Matched), len(d.OnlyLeft), len(d.OnlyRight))
	}
	d = Compare(left, right, CompareConfig{
		Policy: MatchRenames,
		Renames: []RenameRule{{
			Left:  nodeKey(mkStack("main", "oldName")),
			Right: nodeKey(mkStack("main", "newName")),
		}},
	})
	var leaf *NodeDiff
	for i := range d.Matched {
		if d.Matched[i].MatchedBy == "rename" {
			leaf = &d.Matched[i]
		}
	}
	if leaf == nil || leaf.SelfDelta != 0 {
		t.Fatalf("rename match missing: %+v", d.Matched)
	}
	if len(d.OnlyLeft) != 0 || len(d.OnlyRight) != 0 {
		t.Fatal("renamed nodes must not appear as only-one-side")
	}
}

func TestCompareLeafMove(t *testing.T) {
	cfg := DefaultConfig()
	// 同一函数身份移动到了不同父路径。
	left := buildSnapshot(cfg, []Sample{
		{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "svcA", "parse")},
	})
	right := buildSnapshot(cfg, []Sample{
		{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "svcB", "parse")},
	})
	d := Compare(left, right, CompareConfig{Policy: MatchLeaf})
	var leaf *NodeDiff
	for i := range d.Matched {
		if d.Matched[i].MatchedBy == "leaf" {
			leaf = &d.Matched[i]
		}
	}
	if leaf == nil {
		t.Fatalf("expected leaf match, got %+v", d)
	}
	if leaf.LeftPath[1].Function != "svcA" || leaf.RightPath[1].Function != "svcB" {
		t.Fatalf("moved-node paths not preserved: %+v", leaf)
	}

	// 叶子帧在一方不唯一时不允许匹配：再加一个同样叫 parse 的左节点。
	left2 := buildSnapshot(cfg, []Sample{
		{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "svcA", "parse")},
		{Source: "u", TimestampNanos: 2, ValueNanos: 10, Stack: mkStack("main", "svcC", "parse")},
	})
	d2 := Compare(left2, right, CompareConfig{Policy: MatchLeaf})
	for _, m := range d2.Matched {
		if m.MatchedBy == "leaf" {
			t.Fatal("ambiguous leaf must not be matched")
		}
	}
}

func TestCompareDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	mk := func() Snapshot {
		return buildSnapshot(cfg, []Sample{
			{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("z", "y", "x")},
			{Source: "u", TimestampNanos: 2, ValueNanos: 10, Stack: mkStack("a", "b")},
		})
	}
	d1 := Compare(mk(), mk(), CompareConfig{Policy: MatchExactPath})
	d2 := Compare(mk(), mk(), CompareConfig{Policy: MatchExactPath})
	if len(d1.Matched) != len(d2.Matched) {
		t.Fatal("compare length unstable")
	}
	for i := range d1.Matched {
		if d1.Matched[i].NodeKey != d2.Matched[i].NodeKey {
			t.Fatal("compare output order must be deterministic")
		}
	}
}
