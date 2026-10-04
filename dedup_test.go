package samplingprofiler

import "testing"

func TestDedupExactAndTimestamp(t *testing.T) {
	d := newDedupStates(4, 0)
	dup, rej, _ := d.seenOrAdd("u1", 100, 10)
	if dup || rej {
		t.Fatal("first sample must be accepted")
	}
	dup, _, _ = d.seenOrAdd("u1", 100, 10)
	if !dup {
		t.Fatal("identical resend must be duplicate")
	}
	// 同单元同时间戳但栈不同，也算重复。
	dup, _, _ = d.seenOrAdd("u1", 200, 10)
	if !dup {
		t.Fatal("same source+timestamp must be duplicate")
	}
	// 不同单元互不影响。
	dup, _, _ = d.seenOrAdd("u2", 100, 10)
	if dup {
		t.Fatal("different sources must not share dedup state")
	}
}

func TestDedupEviction(t *testing.T) {
	d := newDedupStates(2, 0)
	d.seenOrAdd("u", 1, 1)
	d.seenOrAdd("u", 2, 2)
	_, _, evicted := d.seenOrAdd("u", 3, 3)
	if !evicted || d.evictions() != 2 {
		t.Fatalf("expected two evictions, got %d", d.evictions())
	}
}

func TestDedupMaxSources(t *testing.T) {
	d := newDedupStates(8, 1)
	if _, rej, _ := d.seenOrAdd("u1", 1, 1); rej {
		t.Fatal("first source accepted")
	}
	if _, rej, _ := d.seenOrAdd("u2", 1, 1); !rej {
		t.Fatal("second source must be rejected")
	}
}
