package profiler

// MatchMode controls how nodes are matched across two profiles.
type MatchMode int

const (
	// MatchExact matches nodes by their full normalized path.
	MatchExact MatchMode = iota
	// MatchLeaf first tries an exact path match; unmatched nodes are then
	// matched by leaf frame name (for renamed/moved leaves).
	MatchLeaf
)

// DiffStatus classifies a node's presence across the two profiles.
type DiffStatus int

const (
	// DiffMatched exists in both profiles.
	DiffMatched DiffStatus = iota
	// DiffAdded exists only in the after profile.
	DiffAdded
	// DiffRemoved exists only in the before profile.
	DiffRemoved
)

// MatchKind explains how a matched pair was determined.
type MatchKind int

const (
	// MatchKindExact matched by full path.
	MatchKindExact MatchKind = iota
	// MatchKindLeaf matched by leaf frame name.
	MatchKindLeaf
)

// CompareConfig controls profile comparison.
type CompareConfig struct {
	// Renames maps old frame names to new names; applied before matching.
	Renames map[string]string
	// Mode selects the matching strategy.
	Mode MatchMode
}

// Diff is a single node's before/after comparison.
type Diff struct {
	Path       []Frame
	Status     DiffStatus
	Match      MatchKind
	BeforeSelf int64
	AfterSelf  int64
	BeforeCum  int64
	AfterCum   int64
	DeltaSelf  int64 // AfterSelf - BeforeSelf
	DeltaCum   int64 // AfterCum - BeforeCum
}

// Comparison is the result of comparing two snapshots.
type Comparison struct {
	Diffs       []Diff
	BeforeTotal int64
	AfterTotal  int64
}
