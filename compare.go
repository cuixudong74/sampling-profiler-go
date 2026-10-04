package profiler

import "sort"

// Compare aggregates the per-node differences between two snapshots. Node
// matching is deterministic: nodes are first paired by their full normalized
// path; with MatchLeaf, unmatched nodes are additionally paired by leaf frame
// name (exactly one candidate on each side), which handles renamed or moved
// leaves.
func Compare(before, after *Snapshot, cfg CompareConfig) *Comparison {
	bMap := make(map[string]*Node)
	aMap := make(map[string]*Node)
	for _, n := range before.Nodes() {
		bMap[PathKey(normalizePath(n.Path, cfg.Renames))] = n
	}
	for _, n := range after.Nodes() {
		aMap[PathKey(normalizePath(n.Path, cfg.Renames))] = n
	}

	bMatched := make(map[string]bool)
	aMatched := make(map[string]bool)
	var diffs []Diff

	// Pass 1: exact path match.
	for key, bn := range bMap {
		if an, ok := aMap[key]; ok {
			diffs = append(diffs, matchedDiff(bn, an, MatchKindExact, cfg.Renames))
			bMatched[key] = true
			aMatched[key] = true
		}
	}

	// Pass 2: leaf-name match for unmatched nodes (renamed/moved leaves).
	if cfg.Mode == MatchLeaf {
		bByLeaf := make(map[string][]*Node)
		aByLeaf := make(map[string][]*Node)
		for key, n := range bMap {
			if !bMatched[key] {
				leaf := normalizePath(n.Path, cfg.Renames)[0].Name
				bByLeaf[leaf] = append(bByLeaf[leaf], n)
			}
		}
		for key, n := range aMap {
			if !aMatched[key] {
				leaf := normalizePath(n.Path, cfg.Renames)[0].Name
				aByLeaf[leaf] = append(aByLeaf[leaf], n)
			}
		}
		for leaf, bns := range bByLeaf {
			ans := aByLeaf[leaf]
			if len(bns) == 1 && len(ans) == 1 {
				bn, an := bns[0], ans[0]
				bKey := PathKey(normalizePath(bn.Path, cfg.Renames))
				aKey := PathKey(normalizePath(an.Path, cfg.Renames))
				bMatched[bKey] = true
				aMatched[aKey] = true
				diffs = append(diffs, matchedDiff(bn, an, MatchKindLeaf, cfg.Renames))
			}
		}
	}

	// Pass 3: unmatched nodes are added/removed.
	for key, n := range bMap {
		if !bMatched[key] {
			diffs = append(diffs, singleDiff(n, DiffRemoved, cfg.Renames))
		}
	}
	for key, n := range aMap {
		if !aMatched[key] {
			diffs = append(diffs, singleDiff(n, DiffAdded, cfg.Renames))
		}
	}

	sort.Slice(diffs, func(i, j int) bool {
		return PathKey(diffs[i].Path) < PathKey(diffs[j].Path)
	})

	return &Comparison{
		Diffs:       diffs,
		BeforeTotal: before.Total(),
		AfterTotal:  after.Total(),
	}
}

// normalizePath applies the rename map to each frame of a path.
func normalizePath(path []Frame, renames map[string]string) []Frame {
	if len(renames) == 0 {
		return path
	}
	out := make([]Frame, len(path))
	for i, f := range path {
		if r, ok := renames[f.Name]; ok {
			out[i] = Frame{Name: r}
		} else {
			out[i] = f
		}
	}
	return out
}

func matchedDiff(bn, an *Node, kind MatchKind, renames map[string]string) Diff {
	path := normalizePath(an.Path, renames)
	return Diff{
		Path:       append([]Frame(nil), path...),
		Status:     DiffMatched,
		Match:      kind,
		BeforeSelf: bn.Self,
		AfterSelf:  an.Self,
		BeforeCum:  bn.Cum,
		AfterCum:   an.Cum,
		DeltaSelf:  an.Self - bn.Self,
		DeltaCum:   an.Cum - bn.Cum,
	}
}

func singleDiff(n *Node, status DiffStatus, renames map[string]string) Diff {
	path := normalizePath(n.Path, renames)
	d := Diff{
		Path:   append([]Frame(nil), path...),
		Status: status,
	}
	if status == DiffRemoved {
		d.BeforeSelf, d.BeforeCum = n.Self, n.Cum
	} else {
		d.AfterSelf, d.AfterCum = n.Self, n.Cum
	}
	d.DeltaSelf = d.AfterSelf - d.BeforeSelf
	d.DeltaCum = d.AfterCum - d.BeforeCum
	return d
}
