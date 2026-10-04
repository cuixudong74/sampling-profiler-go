package samplingprofiler

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"testing"
)

func mkStack(names ...string) []Frame {
	fs := make([]Frame, len(names))
	for i, n := range names {
		fs[i] = Frame{Function: n}
	}
	return fs
}

func nodeMap(sn Snapshot) map[string]NodeStats {
	m := make(map[string]NodeStats, len(sn.Nodes))
	for _, n := range sn.Nodes {
		m[n.NodeKey] = n.Stats
	}
	return m
}

// genSamples 构造一批覆盖多来源、重复、乱序的采样。
func genSamples(seed int64) []Sample {
	rng := rand.New(rand.NewSource(seed))
	paths := [][]string{
		{"main", "serve", "handle"},
		{"main", "serve", "query", "scan"},
		{"main", "bg", "gc"},
	}
	var all []Sample
	for u := 0; u < 4; u++ {
		for i := 0; i < 200; i++ {
			p := paths[rng.Intn(len(paths))]
			all = append(all, Sample{
				Source:         fmt.Sprintf("u%d", u),
				TimestampNanos: int64(i + 1),
				Stack:          mkStack(p...),
				ValueNanos:     10,
			})
		}
	}
	// 注入完整重复（同 source+ts+栈），打乱到达顺序。
	dups := append([]Sample(nil), all[:25]...)
	all = append(all, dups...)
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	return all
}

func TestOrderIndependent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DedupCapacityPerSource = 1000

	samples := genSamples(1)
	// 三次聚合：原始乱序 / 再打乱 / 逆序，结果必须逐节点一致。
	results := make([]map[string]NodeStats, 0, 3)
	statsSeen := make([]Stats, 0, 3)
	for round := 0; round < 3; round++ {
		ss := append([]Sample(nil), samples...)
		if round == 1 {
			rand.New(rand.NewSource(99)).Shuffle(len(ss), func(i, j int) { ss[i], ss[j] = ss[j], ss[i] })
		} else if round == 2 {
			for i, j := 0, len(ss)-1; i < j; i, j = i+1, j-1 {
				ss[i], ss[j] = ss[j], ss[i]
			}
		}
		a := NewAggregator(cfg)
		for _, s := range ss {
			a.Add(s)
		}
		snap := a.Snapshot()
		results = append(results, nodeMap(snap))
		statsSeen = append(statsSeen, snap.Stats)
	}
	for k := range results[0] {
		if results[0][k] != results[1][k] || results[0][k] != results[2][k] {
			t.Fatalf("node %q differs across arrival orders: %v %v %v", k, results[0][k], results[1][k], results[2][k])
		}
	}
	for i := 1; i < len(statsSeen); i++ {
		if statsSeen[0] != statsSeen[i] {
			t.Fatalf("stats differ across orders: %+v vs %+v", statsSeen[0], statsSeen[i])
		}
	}
}

func TestBatchingIndependent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DedupCapacityPerSource = 1000
	samples := genSamples(7)

	single := NewAggregator(cfg)
	for _, s := range samples {
		single.Add(s)
	}
	// 切成大小不一的批次，逐批处理。
	batched := NewAggregator(cfg)
	i := 0
	batch := 1
	for i < len(samples) {
		end := i + batch
		if end > len(samples) {
			end = len(samples)
		}
		for _, s := range samples[i:end] {
			batched.Add(s)
		}
		i = end
		batch = (batch % 7) + 1
	}
	if !reflect.DeepEqual(single.Snapshot().Nodes, batched.Snapshot().Nodes) {
		t.Fatal("batching must not change aggregation")
	}
}

func TestSelfAndCumulative(t *testing.T) {
	cfg := DefaultConfig()
	a := NewAggregator(cfg)
	a.Add(Sample{Source: "u1", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("main", "serve", "handle")})
	a.Add(Sample{Source: "u1", TimestampNanos: 2, ValueNanos: 10, Stack: mkStack("main", "serve", "query")})
	a.Add(Sample{Source: "u1", TimestampNanos: 3, ValueNanos: 10, Stack: mkStack("main", "serve", "handle")})
	snap := a.Snapshot()
	m := nodeMap(snap)
	find := func(names ...string) NodeStats {
		return m[nodeKey(mkStack(names...))]
	}
	main := find("main")
	serve := find("main", "serve")
	handle := find("main", "serve", "handle")
	query := find("main", "serve", "query")
	if main.cum() != 30 || serve.cum() != 30 {
		t.Fatalf("cum wrong: main=%d serve=%d", main.cum(), serve.cum())
	}
	if handle.SelfNanos != 20 || query.SelfNanos != 10 || main.SelfNanos != 0 {
		t.Fatalf("self wrong: handle=%d query=%d main=%d", handle.SelfNanos, query.SelfNanos, main.SelfNanos)
	}
	if handle.Samples != 2 || query.Samples != 1 {
		t.Fatal("leaf sample counts wrong")
	}
	st := snap.Stats
	if st.Received != 3 || st.Accepted != 3 || st.Duplicates != 0 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func (s NodeStats) cum() int64 { return s.CumulativeNanos }

func TestLateSamplesAccepted(t *testing.T) {
	cfg := DefaultConfig()
	a := NewAggregator(cfg)
	a.Add(Sample{Source: "u", TimestampNanos: 1000, ValueNanos: 5, Stack: mkStack("a")})
	a.Add(Sample{Source: "u", TimestampNanos: 50, ValueNanos: 5, Stack: mkStack("b")}) // 迟到
	snap := a.Snapshot()
	if snap.MinTimestampNanos != 50 || snap.MaxTimestampNanos != 1000 {
		t.Fatalf("time range wrong: %d..%d", snap.MinTimestampNanos, snap.MaxTimestampNanos)
	}
	if snap.Stats.Accepted != 2 {
		t.Fatal("late sample must be accepted")
	}
}

func TestDuplicateCounters(t *testing.T) {
	cfg := DefaultConfig()
	a := NewAggregator(cfg)
	s := Sample{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("a", "b")}
	a.Add(s)
	a.Add(s)                                                                                // 完整重复
	a.Add(Sample{Source: "u", TimestampNanos: 1, ValueNanos: 10, Stack: mkStack("a", "c")}) // 同单元同时间戳
	st := a.Stats()
	if st.Received != 3 || st.Accepted != 1 || st.Duplicates != 2 {
		t.Fatalf("counters wrong: %+v", st)
	}
}

func TestEmptyAndUnknownCounters(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Unknown = UnknownDrop
	a := NewAggregator(cfg)
	a.Add(Sample{Source: "u", TimestampNanos: 1, ValueNanos: 1, Stack: mkStack("", "")})
	st := a.Stats()
	if st.EmptyStacks != 1 || st.UnknownFrames != 2 || st.Accepted != 0 {
		t.Fatalf("counters wrong: %+v", st)
	}
}

func TestNodeCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxNodes = 2
	a := NewAggregator(cfg)
	a.Add(Sample{Source: "u", TimestampNanos: 1, ValueNanos: 1, Stack: mkStack("a", "b")}) // 2 节点
	before := a.Snapshot()
	a.Add(Sample{Source: "u", TimestampNanos: 2, ValueNanos: 1, Stack: mkStack("a", "c")}) // 需要新节点，拒绝
	after := a.Snapshot()
	if after.Stats.NodesCapDropped != 1 {
		t.Fatalf("expected node-cap drop, got %+v", after.Stats)
	}
	if !reflect.DeepEqual(before.Nodes, after.Nodes) {
		t.Fatal("dropped sample must not partially mutate tree")
	}
}

func TestDownsampleCountersAndReproducibility(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Downsample = DownsampleDeterministic
	cfg.DownsampleKeepNumerator = 1
	cfg.DownsampleKeepDenominator = 2
	var all []Sample
	for i := 0; i < 500; i++ {
		all = append(all, Sample{Source: "u", TimestampNanos: int64(i + 1), ValueNanos: 1, Stack: mkStack(fmt.Sprintf("f%d", i))})
	}
	run := func() (Stats, []AggregatedNode) {
		a := NewAggregator(cfg)
		sh := append([]Sample(nil), all...)
		sort.Slice(sh, func(i, j int) bool { return sh[i].TimestampNanos > sh[j].TimestampNanos })
		for _, s := range sh {
			a.Add(s)
		}
		snap := a.Snapshot()
		return snap.Stats, snap.Nodes
	}
	st1, n1 := run()
	st2, n2 := run()
	if st1.Downsampled+st1.Accepted != 500 {
		t.Fatalf("downsampled accounting wrong: %+v", st1)
	}
	if !reflect.DeepEqual(n1, n2) || st1 != st2 {
		t.Fatal("downsampling result must be reproducible regardless of order")
	}
	if st1.Accepted == 0 || st1.Downsampled == 0 {
		t.Fatalf("expected both kept and dropped samples, got %+v", st1)
	}
}

func TestConcurrentAggregate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DedupCapacityPerSource = 2000
	a := NewAggregator(cfg)

	var writers sync.WaitGroup
	stop := make(chan struct{})
	var readerDone sync.WaitGroup

	// 持续查询：快照内部必须自洽（每节点 cum>=self，计数关系成立）。
	readerDone.Add(1)
	go func() {
		defer readerDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
				snap := a.Snapshot()
				for _, n := range snap.Nodes {
					if n.Stats.CumulativeNanos < n.Stats.SelfNanos {
						t.Errorf("inconsistent snapshot: cum<self at %s", n.NodeKey)
						return
					}
				}
				if snap.Stats.Accepted > snap.Stats.Received {
					t.Errorf("impossible stats: %+v", snap.Stats)
					return
				}
			}
		}
	}()

	for w := 0; w < 8; w++ {
		writers.Add(1)
		go func(id int) {
			defer writers.Done()
			for i := 0; i < 300; i++ {
				a.Add(Sample{
					Source:         fmt.Sprintf("u%d", id),
					TimestampNanos: int64(id*1000 + i + 1),
					ValueNanos:     1,
					Stack:          mkStack("main", "work", fmt.Sprintf("leaf%d", i)),
				})
			}
		}(w)
	}
	writers.Wait()
	close(stop)
	readerDone.Wait()

	st := a.Stats()
	if st.Accepted != 8*300 || st.Received != 8*300 {
		t.Fatalf("expected %d accepted, got %+v", 8*300, st)
	}
}

// TestConcurrentDuplicates 验证多个 goroutine 并发上报同一条采样，
// 只有一条被计入（去重判定在互斥区内完成）。
func TestConcurrentDuplicates(t *testing.T) {
	cfg := DefaultConfig()
	a := NewAggregator(cfg)
	var wg sync.WaitGroup
	s := Sample{Source: "u", TimestampNanos: 1, ValueNanos: 1, Stack: mkStack("a", "b")}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.Add(s) }()
	}
	wg.Wait()
	st := a.Stats()
	if st.Accepted != 1 || st.Duplicates != 49 {
		t.Fatalf("exact-once expected, got %+v", st)
	}
}
