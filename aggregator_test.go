package profiler

import (
	"math/rand"
	"testing"
)

func frames(names ...string) []Frame {
	out := make([]Frame, len(names))
	for i, n := range names {
		out[i] = Frame{Name: n}
	}
	return out
}

func sample(unit string, ts int64, stack ...string) Sample {
	return Sample{Unit: unit, TS: ts, Stack: frames(stack...)}
}

// makeSamples builds a fixed corpus of samples across several units.
func makeSamples() []Sample {
	var out []Sample
	for u := 0; u < 4; u++ {
		unit := string(rune('A' + u))
		for t := int64(0); t < 25; t++ {
			out = append(out, sample(unit, t, "leaf", "mid", "root"))
			if t%3 == 0 {
				out[len(out)-1].Stack = frames("leaf", "mid", "root", "deep")
			}
		}
	}
	return out
}

func snapshotsEqual(t *testing.T, a, b *Snapshot) {
	t.Helper()
	if a.Total() != b.Total() {
		t.Fatalf("total mismatch: %d vs %d", a.Total(), b.Total())
	}
	if a.NodeCount() != b.NodeCount() {
		t.Fatalf("node count mismatch: %d vs %d", a.NodeCount(), b.NodeCount())
	}
	for _, n := range a.Nodes() {
		m := b.NodeByPath(n.Path)
		if m == nil {
			t.Fatalf("node %v missing in second snapshot", n.Path)
		}
		if m.Self != n.Self || m.Cum != n.Cum {
			t.Fatalf("node %v mismatch: (%d,%d) vs (%d,%d)", n.Path, n.Self, n.Cum, m.Self, m.Cum)
		}
	}
	for _, n := range b.Nodes() {
		if a.NodeByPath(n.Path) == nil {
			t.Fatalf("extra node %v in second snapshot", n.Path)
		}
	}
}

func runAgg(t *testing.T, cfg Config, samples []Sample, chunks int, seed int64) *Snapshot {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	order := make([]Sample, len(samples))
	copy(order, samples)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	if chunks <= 1 {
		a.Ingest(order...)
		return a.Commit()
	}
	// Split into chunks, committing between chunks.
	for i := 0; i < len(order); i += chunks {
		end := i + chunks
		if end > len(order) {
			end = len(order)
		}
		a.Ingest(order[i:end]...)
		a.Commit()
	}
	return a.Snapshot()
}

// Aggregation must be identical regardless of arrival order, batching or
// concurrency.
func TestAggregateDeterminism(t *testing.T) {
	samples := makeSamples()
	cfg := DefaultConfig()
	cfg.ShardCount = 16

	base := runAgg(t, cfg, samples, 1, 1)
	for _, seed := range []int64{2, 3, 4} {
		got := runAgg(t, cfg, samples, 1, seed)
		snapshotsEqual(t, base, got)
	}
	// Different batch boundaries.
	for _, chunks := range []int{1, 7, 13, 50} {
		got := runAgg(t, cfg, samples, chunks, 99)
		snapshotsEqual(t, base, got)
	}
	// Different shard counts.
	for _, shards := range []int{1, 4, 32} {
		c := cfg
		c.ShardCount = shards
		got := runAgg(t, c, samples, 1, 7)
		snapshotsEqual(t, base, got)
	}
}

// Out-of-order arrival, duplicates and late samples must be handled exactly:
// duplicates are counted once, late samples are never discarded.
func TestOutOfOrderDuplicatesLate(t *testing.T) {
	cfg := DefaultConfig()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s1 := sample("A", 1, "leaf", "root")
	s2 := sample("A", 2, "leaf", "root")
	s3 := sample("B", 1, "leaf", "root")

	// Send out of order, with duplicates interleaved.
	a.Ingest(s2, s1, s1, s3, s2, s1)
	snap := a.Commit()
	if snap.Stats().Kept != 3 {
		t.Fatalf("expected 3 kept, got %d", snap.Stats().Kept)
	}
	if snap.Stats().Duplicates != 3 {
		t.Fatalf("expected 3 duplicates, got %d", snap.Stats().Duplicates)
	}
	if snap.Stats().Received != 6 {
		t.Fatalf("expected 6 received, got %d", snap.Stats().Received)
	}
	if snap.Total() != 3 {
		t.Fatalf("expected total 3, got %d", snap.Total())
	}

	// Late arrival after a commit: must be aggregated, not discarded.
	late := sample("A", 3, "leaf", "root")
	a.Ingest(late)
	snap2 := a.Commit()
	if snap2.Total() != 4 {
		t.Fatalf("late sample discarded: total %d", snap2.Total())
	}
	if snap2.Stats().Kept != 4 {
		t.Fatalf("expected 4 kept, got %d", snap2.Stats().Kept)
	}

	// Re-sending the same late sample is a duplicate.
	a.Ingest(late)
	snap3 := a.Commit()
	if snap3.Stats().Duplicates != snap2.Stats().Duplicates+1 {
		t.Fatalf("expected 1 new duplicate, got %d", snap3.Stats().Duplicates-snap2.Stats().Duplicates)
	}
	if snap3.Total() != 4 {
		t.Fatalf("total changed after duplicate: %d", snap3.Total())
	}
}

// Deep stacks are truncated to the leaf-most frames; retained nodes keep
// identical counts.
func TestDeepStackTruncation(t *testing.T) {
	samples := []Sample{
		sample("A", 1, "l0", "l1", "l2", "l3", "l4", "l5"),
		sample("A", 2, "l0", "l1", "l2", "l3", "l4", "l5"),
		sample("A", 3, "l0", "l1", "l2"),
	}
	full := runAgg(t, DefaultConfig(), samples, 1, 1)
	cfg := DefaultConfig()
	cfg.MaxDepth = 3
	trunc := runAgg(t, cfg, samples, 1, 1)

	if trunc.NodeCount() != 3 {
		t.Fatalf("expected 3 truncated nodes, got %d", trunc.NodeCount())
	}
	// Every retained node must match the full profile exactly.
	for _, n := range trunc.Nodes() {
		m := full.NodeByPath(n.Path)
		if m == nil {
			t.Fatalf("truncated node %v not in full profile", n.Path)
		}
		if m.Self != n.Self || m.Cum != n.Cum {
			t.Fatalf("node %v changed by truncation: (%d,%d) vs (%d,%d)", n.Path, n.Self, n.Cum, m.Self, m.Cum)
		}
	}
	// Deep-only frames must not appear.
	for _, n := range trunc.Nodes() {
		for _, f := range n.Path {
			if f.Name == "l3" || f.Name == "l4" || f.Name == "l5" {
				t.Fatalf("deep frame leaked into truncated profile: %v", n.Path)
			}
		}
	}
}

// Unknown frames are replaced per policy and repeated frames folded; other
// nodes are unaffected.
func TestUnknownAndRecursion(t *testing.T) {
	known := map[string]bool{"leaf": true, "mid": true, "root": true}
	samples := []Sample{
		sample("A", 1, "leaf", "libc_unknown", "root"),
		sample("A", 2, "leaf", "libc_unknown", "root"),
		sample("A", 3, "leaf", "mid", "root"),
	}
	cfg := DefaultConfig()
	cfg.Unknown = UnknownPlaceholder
	cfg.Placeholder = "<unk>"
	cfg.KnownFrames = known
	got := runAgg(t, cfg, samples, 1, 1)

	// The two unknown frames merge into one placeholder node.
	placeholder := got.NodeByPath(frames("leaf", "<unk>", "root"))
	if placeholder == nil {
		t.Fatal("placeholder node missing")
	}
	if placeholder.Cum != 2 {
		t.Fatalf("placeholder cum = %d, want 2", placeholder.Cum)
	}
	// The known path is untouched.
	mid := got.NodeByPath(frames("leaf", "mid", "root"))
	if mid == nil || mid.Cum != 1 {
		t.Fatalf("known node affected: %v", mid)
	}
	// No unknown frame names remain.
	for _, n := range got.Nodes() {
		for _, f := range n.Path {
			if f.Name == "libc_unknown" {
				t.Fatalf("unknown frame leaked: %v", n.Path)
			}
		}
	}

	// Recursion folding: [leaf,leaf,leaf,root] collapses to [leaf,root].
	rec := []Sample{
		sample("B", 1, "leaf", "leaf", "leaf", "root"),
		sample("B", 2, "leaf", "root"),
	}
	rcfg := DefaultConfig()
	rcfg.Recursion = RecursionFold
	rgot := runAgg(t, rcfg, rec, 1, 1)
	leaf := rgot.NodeByPath(frames("leaf"))
	if leaf == nil || leaf.Self != 2 {
		t.Fatalf("folded leaf self = %v, want 2", leaf)
	}
	folded := rgot.NodeByPath(frames("leaf", "root"))
	if folded == nil || folded.Cum != 2 {
		t.Fatalf("folded node cum = %v, want 2", folded)
	}
	if rgot.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes after folding, got %d", rgot.NodeCount())
	}
}

// Downsampling is deterministic and the dropped count is verifiable:
// Received = Duplicates + Dropped + Kept.
func TestDownsample(t *testing.T) {
	samples := makeSamples()
	cfg := DefaultConfig()
	cfg.DownsampleK = 2

	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(samples...)
	snap := a.Commit()
	st := snap.Stats()
	if st.Received != int64(len(samples)) {
		t.Fatalf("received = %d, want %d", st.Received, len(samples))
	}
	if st.Received != st.Duplicates+st.Dropped+st.Kept {
		t.Fatalf("accounting invariant broken: %d != %d+%d+%d", st.Received, st.Duplicates, st.Dropped, st.Kept)
	}
	if st.Kept == 0 || st.Dropped == 0 {
		t.Fatalf("expected both kept and dropped, got kept=%d dropped=%d", st.Kept, st.Dropped)
	}

	// Re-running with the same input yields the identical kept set.
	got := runAgg(t, cfg, samples, 1, 1)
	snapshotsEqual(t, snap, got)

	// Duplicates of kept samples are recognized; duplicates of dropped
	// samples are dropped again (never aggregated).
	a.Ingest(samples...)
	snap2 := a.Commit()
	st2 := snap2.Stats()
	if st2.Received != 2*int64(len(samples)) {
		t.Fatalf("received = %d", st2.Received)
	}
	if st2.Received != st2.Duplicates+st2.Dropped+st2.Kept {
		t.Fatalf("accounting invariant broken on rerun")
	}
	if st2.Kept != st.Kept {
		t.Fatalf("kept changed after duplicate rerun: %d vs %d", st2.Kept, st.Kept)
	}
}

// Lossy dedup stays bounded and reports evictions.
func TestLossyDedupBounded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dedup = DedupLossy
	cfg.DedupCapacity = 4
	cfg.ShardCount = 1
	cfg.DownsampleK = 1
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var samples []Sample
	for i := 0; i < 20; i++ {
		samples = append(samples, sample("A", int64(i), "leaf", "root"))
	}
	a.Ingest(samples...)
	snap := a.Commit()
	if snap.Stats().Kept != 20 {
		t.Fatalf("kept = %d, want 20", snap.Stats().Kept)
	}
	if snap.Stats().SeenEvicted == 0 {
		t.Fatal("expected evictions to be reported")
	}
	// Re-send everything: most keys were evicted and may be counted again,
	// but the accounting invariant still holds.
	a.Ingest(samples...)
	snap2 := a.Commit()
	st := snap2.Stats()
	if st.Received != st.Duplicates+st.Dropped+st.Kept {
		t.Fatalf("accounting invariant broken")
	}
}

// Pruning removes only low-count leaves and reports the removed samples.
func TestPruneLeaves(t *testing.T) {
	samples := []Sample{
		sample("A", 1, "hot", "root"),
		sample("A", 2, "hot", "root"),
		sample("A", 3, "cold", "root"),
	}
	cfg := DefaultConfig()
	cfg.PruneMinCount = 2
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(samples...)
	snap := a.Commit()
	if snap.NodeByPath(frames("cold")) != nil {
		t.Fatal("cold leaf should have been pruned")
	}
	if snap.NodeByPath(frames("cold", "root")) != nil {
		t.Fatal("cold chain should have been pruned")
	}
	if snap.NodeByPath(frames("hot")) == nil {
		t.Fatal("hot leaf should remain")
	}
	if snap.Stats().PrunedNodes != 2 || snap.Stats().PrunedSamples != 1 {
		t.Fatalf("pruning stats = %d nodes / %d samples", snap.Stats().PrunedNodes, snap.Stats().PrunedSamples)
	}
}

// An empty aggregator produces a valid empty snapshot.
func TestEmptySnapshot(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := a.Commit()
	if s.Total() != 0 || s.NodeCount() != 0 {
		t.Fatalf("empty snapshot: total=%d nodes=%d", s.Total(), s.NodeCount())
	}
	if err := checkSnapshotInvariants(s); err != nil {
		t.Fatal(err)
	}
}

// Sample weights are honored.
func TestWeightedSamples(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(Sample{Unit: "A", TS: 1, Stack: frames("leaf", "root"), Weight: 3})
	snap := a.Commit()
	n := snap.NodeByPath(frames("leaf"))
	if n == nil || n.Self != 3 || n.Cum != 3 {
		t.Fatalf("weighted node: %+v", n)
	}
	if snap.Total() != 3 {
		t.Fatalf("total = %d, want 3", snap.Total())
	}
}

// Explicit sample IDs take precedence over (unit, ts).
func TestIDDedup(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(
		Sample{ID: "x", Unit: "A", TS: 1, Stack: frames("leaf", "root")},
		Sample{ID: "x", Unit: "A", TS: 999, Stack: frames("other", "root")},
	)
	snap := a.Commit()
	if snap.Stats().Kept != 1 || snap.Stats().Duplicates != 1 {
		t.Fatalf("kept=%d dup=%d", snap.Stats().Kept, snap.Stats().Duplicates)
	}
	if snap.NodeByPath(frames("other")) != nil {
		t.Fatal("duplicate ID must not be counted")
	}
}

// UnknownKeep (default) leaves unknown frames untouched.
func TestUnknownKeep(t *testing.T) {
	cfg := DefaultConfig()
	cfg.KnownFrames = map[string]bool{"leaf": true}
	cfg.Unknown = UnknownKeep
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(sample("A", 1, "leaf", "weird_frame", "root"))
	snap := a.Commit()
	if snap.NodeByPath(frames("leaf", "weird_frame", "root")) == nil {
		t.Fatal("unknown frame should be kept verbatim")
	}
}

// Repeated frames are kept verbatim when folding is disabled.
func TestRecursionKeep(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	a.Ingest(sample("A", 1, "leaf", "leaf", "root"))
	snap := a.Commit()
	if snap.NodeByPath(frames("leaf", "leaf", "root")) == nil {
		t.Fatal("repeated frame should be kept")
	}
}

// Invalid configuration is rejected.
func TestInvalidConfig(t *testing.T) {
	for _, cfg := range []Config{
		{MaxDepth: -1},
		{DownsampleK: 0},
		{DedupCapacity: 0},
		{ShardCount: 0},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("expected error for %+v", cfg)
		}
	}
}
