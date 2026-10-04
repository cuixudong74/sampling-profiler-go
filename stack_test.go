package samplingprofiler

import (
	"testing"
)

func TestNormalizeUnknownFrames(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Unknown = UnknownSentinel
	got, ctr := normalizeStack(cfg, []Frame{
		{Function: "main"},
		{Function: ""},
		{Function: "work"},
	})
	if len(got) != 3 || got[1].Function != "<unknown>" || ctr.unknown != 1 {
		t.Fatalf("unexpected: %+v ctr=%+v", got, ctr)
	}

	cfg.Unknown = UnknownDrop
	got, ctr = normalizeStack(cfg, []Frame{{Function: "main"}, {Function: ""}})
	if len(got) != 1 || got[0].Function != "main" || ctr.unknown != 1 {
		t.Fatalf("drop failed: %+v", got)
	}
}

func TestNormalizeRepeatedCollapse(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Repeated = RepeatedCollapse
	got, ctr := normalizeStack(cfg, []Frame{
		{Function: "a", Line: 1},
		{Function: "a", Line: 1},
		{Function: "b"},
	})
	if len(got) != 2 || ctr.collapsed != 1 {
		t.Fatalf("collapse failed: %+v ctr=%+v", got, ctr)
	}
	// 相邻但非完全相同（行号不同）不折叠。
	got, _ = normalizeStack(cfg, []Frame{{Function: "a", Line: 1}, {Function: "a", Line: 2}})
	if len(got) != 2 {
		t.Fatalf("non-identical frames should be kept: %+v", got)
	}
}

func TestNormalizeTruncation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxDepth = 3
	stack := []Frame{{Function: "a"}, {Function: "b"}, {Function: "c"}, {Function: "d"}, {Function: "e"}}

	cfg.Truncation = TruncationSentinel
	got, ctr := normalizeStack(cfg, stack)
	if len(got) != 3 || got[0].Function != "a" || got[1].Function != "b" || got[2].Function != "<truncated>" || !ctr.truncated {
		t.Fatalf("sentinel truncation failed: %+v", got)
	}

	cfg.Truncation = TruncationTailDrop
	got, ctr = normalizeStack(cfg, stack)
	if len(got) != 3 || got[2].Function != "c" {
		t.Fatalf("taildrop failed: %+v", got)
	}
	// 截断不影响其余帧的标识
	if frameKey(got[0]) != frameKey(stack[0]) {
		t.Fatal("prefix frames must remain identical")
	}
}

func TestDeterministicKeys(t *testing.T) {
	a := []Frame{{Function: "f", File: "x.go", Line: 7}}
	b := []Frame{{Function: "f", File: "x.go", Line: 7}}
	if nodeKey(a) != nodeKey(b) || stackFingerprint(a) != stackFingerprint(b) {
		t.Fatal("identical frames must have identical keys/fingerprints")
	}
	c := []Frame{{Function: "f", File: "x.go", Line: 8}}
	if nodeKey(a) == nodeKey(c) {
		t.Fatal("different frames must have different keys")
	}
}
