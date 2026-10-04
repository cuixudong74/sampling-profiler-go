package samplingprofiler

import "sort"

// RenameRule 描述节点重命名/移动规则：左侧版本中的节点路径
// （NodeKey）在右侧版本中对应 Right 路径。
type RenameRule struct {
	Left  string
	Right string
}

// MatchPolicy 节点匹配策略。
type MatchPolicy int

const (
	// MatchExactPath 仅当两版本完整归一化路径（NodeKey）相同才匹配。
	MatchExactPath MatchPolicy = iota
	// MatchRenames 先按完整路径匹配，再应用 RenameRules。
	MatchRenames
	// MatchLeaf 先按完整路径、重命名匹配，最后按最终帧 frameKey
	// 唯一匹配（用于处理位置移动）。
	MatchLeaf
)

// CompareConfig 控制版本对比。
type CompareConfig struct {
	Policy  MatchPolicy
	Renames []RenameRule
}

// NodeDiff 是单个匹配节点的差异。
type NodeDiff struct {
	NodeKey   string
	LeftPath  []Frame
	RightPath []Frame
	SelfLeft  int64
	SelfRight int64
	SelfDelta int64 // 右 - 左
	CumLeft   int64
	CumRight  int64
	CumDelta  int64  // 右 - 左
	MatchedBy string // "exact" | "rename" | "leaf"
}

// Diff 是两份剖析结果的对比结果。
type Diff struct {
	Matched   []NodeDiff
	OnlyLeft  []AggregatedNode // 左有右无（按 NodeKey 排序）
	OnlyRight []AggregatedNode // 右有左无（按 NodeKey 排序）
}

// 节点匹配顺序（CompareConfig.Policy 为最高一级策略时按顺序尝试）：
//  1. exact：两版本归一化完整路径 NodeKey 相同；
//  2. rename：按 RenameRules 显式给出的左->右 NodeKey 映射匹配；
//  3. leaf：最终帧 frameKey（函数名/文件/行）在双方“未匹配集合”中
//     都恰好唯一时匹配；任何一方为 0 个或多于 1 个候选都不匹配，
//     节点留在 OnlyLeft/OnlyRight。该规则可确定地处理“位置移动
//     但函数身份不变”的节点；重命名函数需通过 RenameRules 处理。
// 匹配与遍历均按 NodeKey 字典序推进，结果可复现。

// leafKey 返回节点最终帧（叶子帧）的帧标识。
func leafKey(n AggregatedNode) string {
	if len(n.Path) == 0 {
		return ""
	}
	return frameKey(n.Path[len(n.Path)-1])
}

// Compare 对两个快照做确定性对比。
func Compare(left, right Snapshot, cc CompareConfig) Diff {
	lm := make(map[string]AggregatedNode, len(left.Nodes))
	rm := make(map[string]AggregatedNode, len(right.Nodes))
	for _, n := range left.Nodes {
		lm[n.NodeKey] = n
	}
	for _, n := range right.Nodes {
		rm[n.NodeKey] = n
	}

	matchedL := make(map[string]bool)
	matchedR := make(map[string]bool)
	diff := Diff{}

	matchPair := func(l, r AggregatedNode, by string) {
		diff.Matched = append(diff.Matched, NodeDiff{
			NodeKey:   l.NodeKey,
			LeftPath:  l.Path,
			RightPath: r.Path,
			SelfLeft:  l.Stats.SelfNanos,
			SelfRight: r.Stats.SelfNanos,
			SelfDelta: r.Stats.SelfNanos - l.Stats.SelfNanos,
			CumLeft:   l.Stats.CumulativeNanos,
			CumRight:  r.Stats.CumulativeNanos,
			CumDelta:  r.Stats.CumulativeNanos - l.Stats.CumulativeNanos,
			MatchedBy: by,
		})
		matchedL[l.NodeKey] = true
		matchedR[r.NodeKey] = true
	}

	// 1. 精确路径匹配（按左侧 key 排序，保证顺序确定）。
	leftKeys := make([]string, 0, len(lm))
	for k := range lm {
		leftKeys = append(leftKeys, k)
	}
	sort.Strings(leftKeys)
	for _, k := range leftKeys {
		if r, ok := rm[k]; ok {
			matchPair(lm[k], r, "exact")
		}
	}

	// 2. 重命名规则匹配。
	if cc.Policy == MatchRenames || cc.Policy == MatchLeaf {
		for _, rule := range cc.Renames {
			l, lok := lm[rule.Left]
			r, rok := rm[rule.Right]
			if !lok || !rok {
				continue
			}
			if matchedL[rule.Left] || matchedR[rule.Right] {
				continue
			}
			matchPair(l, r, "rename")
		}
	}

	// 3. 叶子帧唯一匹配。
	if cc.Policy == MatchLeaf {
		// 反复扫描直到一轮没有新增匹配，处理顺序按 key 排序。
		for {
			lcand := map[string][]string{} // leafKey -> 未匹配左节点
			rcand := map[string][]string{}
			for _, k := range leftKeys {
				if !matchedL[k] {
					lk := leafKey(lm[k])
					lcand[lk] = append(lcand[lk], k)
				}
			}
			rKeys := make([]string, 0, len(rm))
			for k := range rm {
				rKeys = append(rKeys, k)
			}
			sort.Strings(rKeys)
			for _, k := range rKeys {
				if !matchedR[k] {
					lk := leafKey(rm[k])
					rcand[lk] = append(rcand[lk], k)
				}
			}
			progressed := false
			leafKeys := make([]string, 0)
			for lk := range lcand {
				if _, ok := rcand[lk]; ok {
					leafKeys = append(leafKeys, lk)
				}
			}
			sort.Strings(leafKeys)
			for _, lk := range leafKeys {
				ls := lcand[lk]
				rs := rcand[lk]
				if len(ls) == 1 && len(rs) == 1 {
					matchPair(lm[ls[0]], rm[rs[0]], "leaf")
					progressed = true
				}
			}
			if !progressed {
				break
			}
		}
	}

	for _, k := range leftKeys {
		if !matchedL[k] {
			diff.OnlyLeft = append(diff.OnlyLeft, lm[k])
		}
	}
	rKeys := make([]string, 0, len(rm))
	for k := range rm {
		rKeys = append(rKeys, k)
	}
	sort.Strings(rKeys)
	for _, k := range rKeys {
		if !matchedR[k] {
			diff.OnlyRight = append(diff.OnlyRight, rm[k])
		}
	}
	sort.Slice(diff.OnlyLeft, func(i, j int) bool {
		return diff.OnlyLeft[i].NodeKey < diff.OnlyLeft[j].NodeKey
	})
	sort.Slice(diff.Matched, func(i, j int) bool {
		return diff.Matched[i].NodeKey < diff.Matched[j].NodeKey
	})
	return diff
}
