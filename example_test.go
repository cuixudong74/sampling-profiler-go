package samplingprofiler_test

import (
	"fmt"

	sp "github.com/cuixudong74/sampling-profiler-go"
)

func Example() {
	cfg := sp.DefaultConfig()
	agg := sp.NewAggregator(cfg)

	// 乱序、重复、多来源并发上报都安全。
	agg.Add(sp.Sample{
		Source:         "worker-1",
		TimestampNanos: 20_000_000,
		ValueNanos:     10_000_000,
		Stack: []sp.Frame{
			{Function: "main.main"},
			{Function: "main.serve"},
			{Function: "main.handle"},
		},
	})
	agg.Add(sp.Sample{
		Source:         "worker-1",
		TimestampNanos: 10_000_000, // 迟到
		ValueNanos:     10_000_000,
		Stack: []sp.Frame{
			{Function: "main.main"},
			{Function: "main.serve"},
			{Function: "main.handle"},
		},
	})
	agg.Add(sp.Sample{
		Source:         "worker-1",
		TimestampNanos: 20_000_000, // 重复
		ValueNanos:     10_000_000,
		Stack: []sp.Frame{
			{Function: "main.main"},
			{Function: "main.serve"},
			{Function: "main.handle"},
		},
	})

	snap := agg.Snapshot()
	for _, n := range snap.Nodes {
		fmt.Printf("%s self=%d cum=%d\n", n.Path[len(n.Path)-1].Function,
			n.Stats.SelfNanos, n.Stats.CumulativeNanos)
	}
	fmt.Printf("accepted=%d duplicates=%d\n", snap.Stats.Accepted, snap.Stats.Duplicates)
	// Unordered output:
	// main.main self=0 cum=20000000
	// main.serve self=0 cum=20000000
	// main.handle self=20000000 cum=20000000
	// accepted=2 duplicates=1
}
