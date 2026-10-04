package profiler

// Node is a single call-path node in an aggregated profile.
type Node struct {
	// Path is the leaf-first path from the leaf to this node.
	Path []Frame
	// Self is the number of samples whose leaf is this node.
	Self int64
	// Cum is the number of samples whose stack passes through this node.
	Cum int64
}

// Stats holds counters that account for every ingested sample.
type Stats struct {
	// Received is the total number of samples handed to the aggregator.
	Received int64
	// Duplicates is the number of samples rejected as repeats.
	Duplicates int64
	// Dropped is the number of samples discarded by downsampling.
	Dropped int64
	// Kept is the number of samples aggregated (Received = Duplicates +
	// Dropped + Kept).
	Kept int64
	// PrunedNodes is the number of leaf nodes removed by pruning.
	PrunedNodes int64
	// PrunedSamples is the self time removed by pruning.
	PrunedSamples int64
	// SeenEvicted is the number of dedup keys evicted in lossy mode.
	SeenEvicted int64
}

// Snapshot is an immutable, self-consistent aggregated profile. Snapshots are
// safe for concurrent reads and are never mutated after publication.
type Snapshot struct {
	nodes  map[string]*Node
	total  int64
	stats  Stats
	config Config
}

// Total returns the sum of weights of kept samples represented by the
// snapshot (each sample defaults to a weight of 1).
func (s *Snapshot) Total() int64 { return s.total }

// Stats returns the accounting counters for the snapshot.
func (s *Snapshot) Stats() Stats { return s.stats }

// Config returns the configuration the snapshot was built with.
func (s *Snapshot) Config() Config { return s.config }

// NodeByPath returns the node for a leaf-first path, or nil if absent.
func (s *Snapshot) NodeByPath(path []Frame) *Node {
	return s.nodes[PathKey(path)]
}

// NodeCount returns the number of nodes in the snapshot.
func (s *Snapshot) NodeCount() int { return len(s.nodes) }

// Nodes returns all nodes in unspecified order.
func (s *Snapshot) Nodes() []*Node {
	out := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		cp := *n
		out = append(out, &cp)
	}
	return out
}
