package profiler

import (
	"hash/fnv"
	"sync"
	"sync/atomic"
)

// Aggregator ingests samples and publishes immutable snapshots. Ingest is
// safe for concurrent use; queries always observe a self-consistent snapshot
// (either the state before or after a commit, never a half-built one).
//
// Aggregation is a pure, order-independent reduction (sum over unique
// samples): batching samples into commits differently, reordering arrivals,
// or changing ingest concurrency never changes the result.
type Aggregator struct {
	cfg      Config
	shards   []*shard
	current  atomic.Pointer[Snapshot]
	commitMu sync.Mutex
}

type shard struct {
	mu      sync.Mutex
	pending []Sample
	seen    map[string]struct{}
	// seenOrder supports the bounded LRU used by DedupLossy.
	seenOrder []string
	stats     Stats
}

// New creates an aggregator with the given configuration.
func New(cfg Config) (*Aggregator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	a := &Aggregator{cfg: cfg}
	a.shards = make([]*shard, cfg.ShardCount)
	for i := range a.shards {
		a.shards[i] = &shard{seen: make(map[string]struct{})}
	}
	empty := &Snapshot{
		nodes:  make(map[string]*Node),
		config: cfg,
	}
	a.current.Store(empty)
	return a, nil
}

// Ingest hands samples to the aggregator. It is safe for concurrent use.
// Samples are deduplicated, optionally downsampled, and normalized before
// being buffered for the next commit.
func (a *Aggregator) Ingest(samples ...Sample) {
	for _, s := range samples {
		key := s.key()
		sh := a.shards[hashKey(key)%uint32(len(a.shards))]
		sh.mu.Lock()
		sh.stats.Received++
		if _, dup := sh.seen[key]; dup {
			sh.stats.Duplicates++
			sh.mu.Unlock()
			continue
		}
		if a.cfg.DownsampleK > 1 && hashKey(key)%uint32(a.cfg.DownsampleK) != 0 {
			// Downsampled out: never seen, never aggregated.
			sh.stats.Dropped++
			sh.mu.Unlock()
			continue
		}
		a.markSeen(sh, key)
		s.Stack = normalizeStack(s.Stack, a.cfg)
		sh.pending = append(sh.pending, s)
		sh.stats.Kept++
		sh.mu.Unlock()
	}
}

// markSeen records a sample key as seen, evicting the oldest entry in lossy
// mode when the per-shard capacity is exceeded.
func (a *Aggregator) markSeen(sh *shard, key string) {
	if a.cfg.Dedup == DedupLossy && len(sh.seen) >= a.cfg.DedupCapacity {
		evict := sh.seenOrder[0]
		sh.seenOrder = sh.seenOrder[1:]
		delete(sh.seen, evict)
		sh.stats.SeenEvicted++
	}
	sh.seen[key] = struct{}{}
	if a.cfg.Dedup == DedupLossy {
		sh.seenOrder = append(sh.seenOrder, key)
	}
}

// Commit builds a new snapshot from all ingested samples and publishes it
// atomically. It returns the published snapshot.
func (a *Aggregator) Commit() *Snapshot {
	a.commitMu.Lock()
	defer a.commitMu.Unlock()

	prev := a.Snapshot()

	// Drain pending samples and collect stats. Pending buffers are swapped
	// out under lock; the (potentially slow) trie build happens without
	// blocking ingest.
	batches := make([][]Sample, len(a.shards))
	var stats Stats
	for i, sh := range a.shards {
		sh.mu.Lock()
		batches[i] = sh.pending
		sh.pending = nil
		stats.Received += sh.stats.Received
		stats.Duplicates += sh.stats.Duplicates
		stats.Dropped += sh.stats.Dropped
		stats.Kept += sh.stats.Kept
		stats.SeenEvicted += sh.stats.SeenEvicted
		sh.mu.Unlock()
	}

	nodes := make(map[string]*Node, len(prev.nodes)+len(batches))
	for k, n := range prev.nodes {
		cp := *n
		cp.Path = append([]Frame(nil), n.Path...)
		nodes[k] = &cp
	}
	var weighted int64
	for _, batch := range batches {
		for _, s := range batch {
			applySample(nodes, s)
			weighted += s.weight()
		}
	}
	if a.cfg.PruneMinCount > 0 {
		pruneLeaves(nodes, a.cfg.PruneMinCount, &stats)
	}

	next := &Snapshot{
		nodes:  nodes,
		total:  prev.total + weighted,
		stats:  stats,
		config: a.cfg,
	}
	a.current.Store(next)
	return next
}

// Snapshot returns the latest published snapshot without blocking on ingest.
func (a *Aggregator) Snapshot() *Snapshot {
	return a.current.Load()
}

// applySample adds one normalized sample to the node map.
func applySample(nodes map[string]*Node, s Sample) {
	w := s.weight()
	for d := range s.Stack {
		path := s.Stack[:d+1]
		key := PathKey(path)
		n := nodes[key]
		if n == nil {
			n = &Node{Path: append([]Frame(nil), path...)}
			nodes[key] = n
		}
		n.Cum += w
		if d == 0 {
			n.Self += w
		}
	}
}

// pruneLeaves removes low-frequency leaves. In a leaf-first trie every node
// whose path starts with leaf L belongs exclusively to leaf L (the leaf is
// path[0]); self time lives on the depth-0 node. Pruning a leaf whose self
// count is below threshold therefore removes the leaf and its whole exclusive
// chain. Remaining nodes keep their exact counts; removed self time is
// accounted in Stats.PrunedSamples.
func pruneLeaves(nodes map[string]*Node, minCount int64, stats *Stats) {
	prune := make(map[string]bool)
	for _, n := range nodes {
		if len(n.Path) == 1 && n.Self < minCount {
			prune[n.Path[0].Name] = true
		}
	}
	for key, n := range nodes {
		if len(n.Path) > 0 && prune[n.Path[0].Name] {
			stats.PrunedNodes++
			if len(n.Path) == 1 {
				stats.PrunedSamples += n.Self
			}
			delete(nodes, key)
		}
	}
}

// hashKey computes a deterministic, order-independent hash of a sample key.
func hashKey(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32()
}
