package profiler

import "testing"

func profile(t *testing.T, samples []Sample) *Snapshot {
	t.Helper()
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(samples...)
	return a.Commit()
}

func TestCompareExact(t *testing.T) {
	before := profile(t, []Sample{
		sample("A", 1, "leaf", "mid", "root"),
		sample("A", 2, "leaf", "mid", "root"),
		sample("A", 3, "old", "root"),
	})
	after := profile(t, []Sample{
		sample("A", 1, "leaf", "mid", "root"),
		sample("A", 2, "leaf", "mid", "root"),
		sample("A", 3, "leaf", "mid", "root"),
		sample("A", 4, "new", "root"),
	})

	c := Compare(before, after, CompareConfig{Mode: MatchExact})
	if c.BeforeTotal != 3 || c.AfterTotal != 4 {
		t.Fatalf("totals = %d / %d", c.BeforeTotal, c.AfterTotal)
	}

	byPath := map[string]*Diff{}
	for i := range c.Diffs {
		byPath[PathKey(c.Diffs[i].Path)] = &c.Diffs[i]
	}

	// Matched node: self grew 2 -> 3.
	leaf := byPath[PathKey(frames("leaf"))]
	if leaf == nil || leaf.Status != DiffMatched || leaf.Match != MatchKindExact {
		t.Fatalf("leaf match wrong: %v", leaf)
	}
	if leaf.BeforeSelf != 2 || leaf.AfterSelf != 3 || leaf.DeltaSelf != 1 {
		t.Fatalf("leaf self delta wrong: %+v", leaf)
	}
	if leaf.BeforeCum != 2 || leaf.AfterCum != 3 || leaf.DeltaCum != 1 {
		t.Fatalf("leaf cum delta wrong: %+v", leaf)
	}

	// Removed node.
	old := byPath[PathKey(frames("old"))]
	if old == nil || old.Status != DiffRemoved || old.BeforeSelf != 1 || old.AfterSelf != 0 || old.DeltaSelf != -1 {
		t.Fatalf("removed node wrong: %+v", old)
	}

	// Added node.
	new := byPath[PathKey(frames("new"))]
	if new == nil || new.Status != DiffAdded || new.AfterSelf != 1 || new.DeltaSelf != 1 {
		t.Fatalf("added node wrong: %+v", new)
	}

	// Determinism: re-running gives identical results.
	c2 := Compare(before, after, CompareConfig{Mode: MatchExact})
	if len(c2.Diffs) != len(c.Diffs) {
		t.Fatal("non-deterministic comparison")
	}
	for i := range c.Diffs {
		if PathKey(c.Diffs[i].Path) != PathKey(c2.Diffs[i].Path) {
			t.Fatal("diff order non-deterministic")
		}
	}
}

func TestCompareRenames(t *testing.T) {
	before := profile(t, []Sample{
		sample("A", 1, "oldname", "root"),
		sample("A", 2, "oldname", "root"),
	})
	after := profile(t, []Sample{
		sample("A", 1, "newname", "root"),
		sample("A", 2, "newname", "root"),
		sample("A", 3, "newname", "root"),
	})
	c := Compare(before, after, CompareConfig{
		Mode:    MatchExact,
		Renames: map[string]string{"oldname": "newname"},
	})
	if len(c.Diffs) != 2 {
		t.Fatalf("expected 2 diffs (matched pair + root), got %d", len(c.Diffs))
	}
	for _, d := range c.Diffs {
		if d.Status != DiffMatched {
			t.Fatalf("expected matched, got status %d for %v", d.Status, d.Path)
		}
	}
}

func TestCompareMovedLeaf(t *testing.T) {
	// Leaf "hot" moved from under rootA to under rootB; leaf-name matching
	// should pair it.
	before := profile(t, []Sample{
		sample("A", 1, "hot", "rootA"),
		sample("A", 2, "hot", "rootA"),
		sample("A", 3, "cold", "rootA"),
	})
	after := profile(t, []Sample{
		sample("A", 1, "hot", "rootB"),
		sample("A", 2, "hot", "rootB"),
		sample("A", 3, "cold", "rootA"),
	})
	c := Compare(before, after, CompareConfig{Mode: MatchLeaf})
	byPath := map[string]*Diff{}
	for i := range c.Diffs {
		byPath[PathKey(c.Diffs[i].Path)] = &c.Diffs[i]
	}
	var hot *Diff
	for i := range c.Diffs {
		if c.Diffs[i].Path[0].Name == "hot" {
			hot = &c.Diffs[i]
		}
	}
	if hot == nil {
		t.Fatal("hot node missing")
	}
	if hot.Status != DiffMatched || hot.Match != MatchKindLeaf {
		t.Fatalf("moved leaf should be matched by leaf name, got status=%d match=%d", hot.Status, hot.Match)
	}
	// The moved node is the depth-1 path; its cumulative count is unchanged.
	if hot.BeforeCum != 2 || hot.AfterCum != 2 || hot.DeltaCum != 0 {
		t.Fatalf("moved leaf counts wrong: %+v", hot)
	}
	// The leaf itself (depth 0) matches exactly with unchanged self time.
	leaf := byPath[PathKey(frames("hot"))]
	if leaf == nil || leaf.Status != DiffMatched || leaf.BeforeSelf != 2 || leaf.AfterSelf != 2 {
		t.Fatalf("leaf node mismatch: %+v", leaf)
	}
}
