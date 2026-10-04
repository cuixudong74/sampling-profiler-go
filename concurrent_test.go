package profiler

import (
	"sync"
	"testing"
)

// Concurrent ingest, commit and query must never expose a half-built snapshot,
// and the final result must match a serial aggregation of the same samples.
func TestConcurrentAggregation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ShardCount = 16
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	const units = 8
	const perUnit = 200
	var wg sync.WaitGroup
	for u := 0; u < units; u++ {
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			unit := string(rune('A' + u))
			for i := 0; i < perUnit; i++ {
				a.Ingest(sample(unit, int64(i), "leaf", "mid", "root"))
				if i%50 == 0 {
					a.Commit()
				}
			}
		}(u)
	}

	// Concurrent readers.
	stop := make(chan struct{})
	var rw sync.WaitGroup
	for r := 0; r < 4; r++ {
		rw.Add(1)
		go func() {
			defer rw.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s := a.Snapshot()
				if s == nil {
					t.Error("nil snapshot")
					return
				}
				if err := checkSnapshotInvariants(s); err != nil {
					t.Errorf("snapshot invariant violated: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rw.Wait()
	final := a.Commit()
	if err := checkSnapshotInvariants(final); err != nil {
		t.Fatalf("final snapshot invariant violated: %v", err)
	}

	// Serial reference aggregation of the same samples.
	var all []Sample
	for u := 0; u < units; u++ {
		unit := string(rune('A' + u))
		for i := 0; i < perUnit; i++ {
			all = append(all, sample(unit, int64(i), "leaf", "mid", "root"))
		}
	}
	ref := runAgg(t, cfg, all, 1, 1)
	snapshotsEqual(t, ref, final)
}

func checkSnapshotInvariants(s *Snapshot) error {
	st := s.Stats()
	if st.Received != st.Duplicates+st.Dropped+st.Kept {
		return errString("accounting invariant broken")
	}
	if s.Total() != st.Kept {
		return errString("total != kept")
	}
	var selfSum int64
	for _, n := range s.Nodes() {
		if n.Self > n.Cum {
			return errString("self > cum for node " + PathKey(n.Path))
		}
		if len(n.Path) == 1 {
			selfSum += n.Self
		}
	}
	if selfSum != s.Total()-st.PrunedSamples {
		return errString("self sum mismatch")
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }
