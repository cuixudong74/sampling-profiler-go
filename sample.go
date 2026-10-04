package profiler

import "strconv"

// Sample is a single profiling observation. Stack is ordered leaf-first
// (index 0 is the innermost frame where the sample was observed).
type Sample struct {
	// ID is an optional globally unique sample identifier. When empty, the
	// key is derived from Unit and TS.
	ID string
	// Unit identifies the execution unit (thread/goroutine/process).
	Unit string
	// TS is the sample timestamp. Together with Unit it identifies the
	// sample when ID is empty.
	TS int64
	// Stack is the leaf-first call stack.
	Stack []Frame
	// Weight defaults to 1 when zero.
	Weight int64
}

// key returns the deduplication key for the sample.
func (s Sample) key() string {
	if s.ID != "" {
		return s.ID
	}
	return s.Unit + "@" + strconv.FormatInt(s.TS, 16)
}

func (s Sample) weight() int64 {
	if s.Weight > 0 {
		return s.Weight
	}
	return 1
}
