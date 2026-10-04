package profiler

import "fmt"

// RecursionMode controls how consecutive repeated frames are handled.
type RecursionMode int

const (
	// RecursionKeep keeps repeated frames verbatim.
	RecursionKeep RecursionMode = iota
	// RecursionFold collapses consecutive repeated frames into one.
	RecursionFold
)

// UnknownMode controls frames that are not present in the KnownFrames set.
type UnknownMode int

const (
	// UnknownKeep keeps unknown frames verbatim.
	UnknownKeep UnknownMode = iota
	// UnknownPlaceholder replaces unknown frames with Placeholder.
	UnknownPlaceholder
)

// DedupMode controls how duplicate samples are recognized.
type DedupMode int

const (
	// DedupExact tracks every seen sample key. Exact, but memory grows with
	// the number of unique samples.
	DedupExact DedupMode = iota
	// DedupLossy keeps a bounded LRU set of seen keys. Exact within the
	// capacity; evicted keys may be counted again (see Stats.SeenEvicted).
	DedupLossy
)

// Config holds all tunable aggregation parameters.
type Config struct {
	// MaxDepth truncates stacks to this many leaf-most frames (0 = unlimited).
	// Leaf-most frames are kept so the self/cumulative counts of every
	// retained node are unaffected by truncation.
	MaxDepth int
	// Recursion controls consecutive repeated-frame folding.
	Recursion RecursionMode
	// Unknown controls unknown-frame handling.
	Unknown UnknownMode
	// KnownFrames is the set of recognized frame names; nil means all frames
	// are known.
	KnownFrames map[string]bool
	// Placeholder is the replacement name used with UnknownPlaceholder.
	Placeholder string
	// DownsampleK keeps one out of every K samples deterministically
	// (1 = disabled).
	DownsampleK int
	// Dedup selects the duplicate-recognition strategy.
	Dedup DedupMode
	// DedupCapacity is the LRU size per shard for DedupLossy.
	DedupCapacity int
	// PruneMinCount drops leaf nodes whose self count is below this threshold
	// at snapshot build time (0 = disabled).
	PruneMinCount int64
	// ShardCount controls ingest parallelism (0 = default).
	ShardCount int
}

// DefaultConfig returns a configuration with safe, deterministic defaults.
func DefaultConfig() Config {
	return Config{
		MaxDepth:      0,
		Recursion:     RecursionKeep,
		Unknown:       UnknownKeep,
		Placeholder:   "<unknown>",
		DownsampleK:   1,
		Dedup:         DedupExact,
		DedupCapacity: 1 << 16,
		PruneMinCount: 0,
		ShardCount:    16,
	}
}

// Validate checks the configuration for invalid values.
func (c Config) Validate() error {
	if c.MaxDepth < 0 {
		return fmt.Errorf("profiler: MaxDepth must be >= 0")
	}
	if c.DownsampleK < 1 {
		return fmt.Errorf("profiler: DownsampleK must be >= 1")
	}
	if c.DedupCapacity < 1 {
		return fmt.Errorf("profiler: DedupCapacity must be >= 1")
	}
	if c.PruneMinCount < 0 {
		return fmt.Errorf("profiler: PruneMinCount must be >= 0")
	}
	if c.ShardCount < 1 {
		return fmt.Errorf("profiler: ShardCount must be >= 1")
	}
	switch c.Recursion {
	case RecursionKeep, RecursionFold:
	default:
		return fmt.Errorf("profiler: invalid Recursion mode %d", c.Recursion)
	}
	switch c.Unknown {
	case UnknownKeep, UnknownPlaceholder:
	default:
		return fmt.Errorf("profiler: invalid Unknown mode %d", c.Unknown)
	}
	switch c.Dedup {
	case DedupExact, DedupLossy:
	default:
		return fmt.Errorf("profiler: invalid Dedup mode %d", c.Dedup)
	}
	return nil
}
