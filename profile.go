package samplingprofiler

import (
	"sort"
	"strings"
)

// 聚合口径（对每条被接受的采样，时间值为 valueNanos）：
//   - 路径上每个节点累计耗时 cum += valueNanos；
//   - 仅叶子节点自身耗时 self += valueNanos，采样数 samples++；
//   - 因而对任意节点恒有 cum >= self；同一栈重复到达按加法合并。
// 树的更新只依赖“已归一化栈 + 加法”，天然与到达顺序无关。

// profileNode 是聚合树的内部节点。
type profileNode struct {
	frame    Frame
	key      string // 从根到本节点的完整路径标识
	parent   *profileNode
	depth    int
	children map[string]*profileNode // 以 frameKey 索引
	self     int64
	cum      int64
	samples  int64
}

// profileTree 是一棵调用栈聚合树。
type profileTree struct {
	root      *profileNode // 虚拟根，depth=0，不参与统计与输出
	nodes     map[string]*profileNode
	nodeCount int
}

func newProfileTree() *profileTree {
	return &profileTree{
		root:  &profileNode{children: make(map[string]*profileNode)},
		nodes: make(map[string]*profileNode),
	}
}

// add 将一条已归一化栈计入树。若 MaxNodes>0 且该栈需要新建节点
// 会导致超限，则整条采样丢弃（droppedByCap=true），已有节点与
// 本次路径上的任何统计都不改变。
func (t *profileTree) add(stack []Frame, valueNanos int64, maxNodes int) (droppedByCap bool) {
	if len(stack) == 0 {
		return false
	}

	// 预演：找出需要新建的节点，判断是否会超上限。
	cur := t.root
	var prefix strings.Builder
	newOnes := 0
	for i, f := range stack {
		fk := frameKey(f)
		next, ok := cur.children[fk]
		if !ok {
			newOnes++
			// 预演阶段不创建节点，只沿“预期”路径继续。
			cur = &profileNode{frame: f, depth: i + 1}
			continue
		}
		cur = next
	}
	if maxNodes > 0 && t.nodeCount+newOnes > maxNodes {
		return true
	}

	// 正式写入。
	cur = t.root
	for i, f := range stack {
		fk := frameKey(f)
		next, ok := cur.children[fk]
		if !ok {
			if i > 0 {
				prefix.WriteByte(0x1e)
			}
			prefix.WriteString(fk)
			next = &profileNode{
				frame:    f,
				key:      prefix.String(),
				parent:   cur,
				depth:    cur.depth + 1,
				children: make(map[string]*profileNode),
			}
			cur.children[fk] = next
			t.nodes[next.key] = next
			t.nodeCount++
		} else {
			prefix.Reset()
			prefix.WriteString(next.key)
		}
		cur = next
		cur.cum += valueNanos
	}
	cur.self += valueNanos
	cur.samples++
	return false
}

// pathOf 返回从根到节点的帧序列（新分配切片）。
func (n *profileNode) pathOf() []Frame {
	path := make([]Frame, n.depth)
	for i, x := n.depth-1, n; x != nil && i >= 0; i, x = i-1, x.parent {
		path[i] = x.frame
	}
	return path
}

// snapshot 导出按 NodeKey 字典序排列的全部真实节点。
func (t *profileTree) snapshot() []AggregatedNode {
	out := make([]AggregatedNode, 0, len(t.nodes))
	for key, n := range t.nodes {
		out = append(out, AggregatedNode{
			Path:    n.pathOf(),
			NodeKey: key,
			Stats: NodeStats{
				SelfNanos:       n.self,
				CumulativeNanos: n.cum,
				Samples:         n.samples,
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeKey < out[j].NodeKey })
	return out
}
