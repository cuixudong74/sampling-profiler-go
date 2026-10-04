package samplingprofiler

import (
	"container/list"
	"hash/fnv"
)

// 去重语义（两条判重规则，满足任一即丢弃）：
//  1. (Source, TimestampNanos, 归一化栈指纹) 三元组完全相同——
//     重复上报同一条采样；
//  2. (Source, TimestampNanos) 已出现过但栈指纹不同——
//     该执行单元在该时刻的时间已经被累计过，不得重复累计。
//
// 仅栈相同但时间戳不同，属于不同采样周期的正常采样，照常计入。
//
// 每个执行单元维护两个等容量的有界 LRU 集合（复合键集合、时间戳
// 集合）。容量（DedupCapacityPerSource）不小于该单元不同采样条数
// 时，结果与到达顺序、批次划分、并发度完全无关；容量不足触发 LRU
// 淘汰并计入 Stats.DedupEvictions，淘汰后再次到达的旧标识无法识别
// （内存有界的代价，README 有说明）。

// dedupCache 是单个执行单元的一种有界去重集合（LRU）。
type dedupCache struct {
	capacity int
	ll       *list.List // front 为最近使用；元素 Value 为 uint64
	items    map[uint64]*list.Element
	evicted  int64
}

func newDedupCache(capacity int) *dedupCache {
	if capacity < 1 {
		capacity = 1
	}
	return &dedupCache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[uint64]*list.Element),
	}
}

// has 报告 v 是否在集合中；若在则刷新为最近使用。
func (c *dedupCache) has(v uint64) (*list.Element, bool) {
	el, ok := c.items[v]
	if ok {
		c.ll.MoveToFront(el)
	}
	return el, ok
}

// add 插入 v 并在超容量时淘汰最久未使用项；返回是否发生淘汰。
func (c *dedupCache) add(v uint64) bool {
	if _, ok := c.items[v]; ok {
		return false
	}
	c.items[v] = c.ll.PushFront(v)
	if c.ll.Len() > c.capacity {
		old := c.ll.Back()
		if old != nil {
			c.ll.Remove(old)
			delete(c.items, old.Value.(uint64))
			c.evicted++
			return true
		}
	}
	return false
}

// compositeKey 计算 (Source, TimestampNanos, StackFP) 的复合指纹。
func compositeKey(source string, ts int64, fp uint64) uint64 {
	h := fnv.New64a()
	var b [8]byte
	_, _ = h.Write([]byte(source))
	_, _ = h.Write([]byte{0})
	putU64(b[:], uint64(ts))
	_, _ = h.Write(b[:])
	_, _ = h.Write([]byte{0})
	putU64(b[:], fp)
	_, _ = h.Write(b[:])
	return h.Sum64()
}

// sourceDedup 是一个执行单元的两类去重集合。
type sourceDedup struct {
	composite *dedupCache // 三元组复合键
	ts        *dedupCache // 时间戳
}

// dedupStates 是按执行单元索引的全部去重集合。
type dedupStates struct {
	perSource      int
	maxSources     int
	caches         map[string]*sourceDedup
	totalEvictions int64
}

func newDedupStates(perSource int, maxSources int) *dedupStates {
	if perSource < 1 {
		perSource = 1
	}
	return &dedupStates{
		perSource:  perSource,
		maxSources: maxSources,
		caches:     make(map[string]*sourceDedup),
	}
}

// seenOrAdd 检查并记录采样。dup=true 表示命中上述任一判重规则，
// 此时只刷新 LRU 位置、不插入任何新项；rejected=true 表示新执行
// 单元数量超过 MaxSources（>0 时），不建立任何状态；evicted 表示
// 本次插入是否触发了 LRU 淘汰。
func (d *dedupStates) seenOrAdd(source string, fp uint64, ts int64) (dup, rejected bool, evicted bool) {
	sd, ok := d.caches[source]
	if !ok {
		if d.maxSources > 0 && len(d.caches) >= d.maxSources {
			return false, true, false
		}
		sd = &sourceDedup{
			composite: newDedupCache(d.perSource),
			ts:        newDedupCache(d.perSource),
		}
		d.caches[source] = sd
	}

	ck := compositeKey(source, ts, fp)
	if _, exact := sd.composite.has(ck); exact {
		// 复合键命中 => 时间戳必然同时存在（一起插入、同容量），
		// 一并刷新。
		sd.ts.has(uint64(ts))
		return true, false, false
	}
	if _, tsSeen := sd.ts.has(uint64(ts)); tsSeen {
		return true, false, false
	}

	e1 := sd.composite.add(ck)
	e2 := sd.ts.add(uint64(ts))
	if e1 {
		d.totalEvictions++
	}
	if e2 {
		d.totalEvictions++
	}
	return false, false, e1 || e2
}

func (d *dedupStates) evictions() int64 { return d.totalEvictions }

// exportedDedup 是去重状态的可序列化表示。条目按 LRU 从最近到最久
// 的顺序排列，恢复后淘汰行为与连续运行时一致。
type exportedDedup struct {
	Composite  []uint64 `json:"c"`
	Timestamps []int64  `json:"ts"`
}

func (d *dedupStates) export() map[string]exportedDedup {
	out := make(map[string]exportedDedup, len(d.caches))
	for src, sd := range d.caches {
		e := exportedDedup{}
		for el := sd.composite.ll.Front(); el != nil; el = el.Next() {
			e.Composite = append(e.Composite, el.Value.(uint64))
		}
		for el := sd.ts.ll.Front(); el != nil; el = el.Next() {
			e.Timestamps = append(e.Timestamps, int64(el.Value.(uint64)))
		}
		out[src] = e
	}
	return out
}

// restoreDedupStates 从导出表示重建去重状态；数据不合法时返回错误。
func restoreDedupStates(perSource, maxSources int, data map[string]exportedDedup) (*dedupStates, error) {
	d := newDedupStates(perSource, maxSources)
	for src, e := range data {
		if len(e.Composite) > perSource || len(e.Timestamps) > perSource {
			return nil, errPersist("dedup entry exceeds configured capacity")
		}
		sd := &sourceDedup{
			composite: newDedupCache(perSource),
			ts:        newDedupCache(perSource),
		}
		// 导出顺序为最近在前；从最久未使用到最近回放，重建相同 LRU 序。
		for i := len(e.Composite) - 1; i >= 0; i-- {
			sd.composite.add(e.Composite[i])
		}
		for i := len(e.Timestamps) - 1; i >= 0; i-- {
			sd.ts.add(uint64(e.Timestamps[i]))
		}
		d.caches[src] = sd
	}
	d.totalEvictions = 0
	return d, nil
}
