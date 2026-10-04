package samplingprofiler

import (
	"math"
	"sync"
)

// 单条采样的处理管线（全部在聚合器互斥锁内完成，对外原子可见）：
//  1. Received 计数；
//  2. 栈归一化（未知帧 / 重复帧 / 截断），空栈计入 EmptyStacks 后丢弃；
//  3. 确定性降采样，丢弃计入 Downsampled（先于去重，避免淘汰扰动）；
//  4. 去重（完整重复 / 同单元同时间戳重复 -> Duplicates；
//     来源超限 -> SourcesRejected；淘汰 -> DedupEvictions）；
//  5. 节点上限检查，超限计入 NodesCapDropped，本次写入完全不生效；
//  6. 计入聚合树并刷新时间范围，Accepted++。
// 任何中途丢弃都不会产生“半累计”：查询只能看到上述 1-6 全部完成
// 或完全未发生的状态。

// Aggregator 是线程安全的采样聚合器。
// 多个来源可并发 Add；查询（Snapshot）只能看到某个自洽的完整状态。
type Aggregator struct {
	mu    sync.RWMutex
	cfg   Config
	tree  *profileTree
	dedup *dedupStates
	stats Stats
	minTS int64
	maxTS int64
}

// NewAggregator 创建聚合器。
func NewAggregator(cfg Config) *Aggregator {
	return &Aggregator{
		cfg:   cfg,
		tree:  newProfileTree(),
		dedup: newDedupStates(cfg.DedupCapacityPerSource, cfg.MaxSources),
		minTS: math.MaxInt64,
		maxTS: math.MinInt64,
	}
}

// Add 计入一条采样（支持乱序、重复、迟到、并发）。
func (a *Aggregator) Add(s Sample) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.stats.Received++

	stack, ctr := normalizeStack(a.cfg, s.Stack)
	a.stats.UnknownFrames += ctr.unknown
	a.stats.CollapsedFrames += ctr.collapsed
	if ctr.truncated {
		a.stats.TruncatedStacks++
	}
	if len(stack) == 0 {
		a.stats.EmptyStacks++
		return
	}

	fp := stackFingerprint(stack)
	if !keepSample(a.cfg, s, fp) {
		a.stats.Downsampled++
		return
	}

	dup, rejected, _ := a.dedup.seenOrAdd(s.Source, fp, s.TimestampNanos)
	if rejected {
		a.stats.SourcesRejected++
		return
	}
	if dup {
		a.stats.Duplicates++
		return
	}
	a.stats.DedupEvictions = a.dedup.evictions()

	value := s.ValueNanos
	if value <= 0 {
		value = a.cfg.SamplePeriodNanos
		if value <= 0 {
			value = 1
		}
	}

	if a.tree.add(stack, value, a.cfg.MaxNodes) {
		a.stats.NodesCapDropped++
		return
	}

	if s.TimestampNanos < a.minTS {
		a.minTS = s.TimestampNanos
	}
	if s.TimestampNanos > a.maxTS {
		a.maxTS = s.TimestampNanos
	}
	a.stats.Accepted++
}

// Snapshot 返回当前聚合状态的自洽副本。返回内容全部为新分配，
// 调用方可在不持锁的情况下自由读取与修改。
func (a *Aggregator) Snapshot() Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Snapshot{
		Nodes:             a.tree.snapshot(),
		Config:            a.cfg,
		Stats:             a.stats,
		MinTimestampNanos: a.minTS,
		MaxTimestampNanos: a.maxTS,
	}
}

// Stats 返回可核对的处理计数（快照副本）。
func (a *Aggregator) Stats() Stats {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.stats
}
