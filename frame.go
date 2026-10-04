package profiler

import "strings"

// Frame identifies a single call stack frame. Frames are compared by Name.
type Frame struct {
	Name string
}

// String returns the frame name.
func (f Frame) String() string { return f.Name }

// pathSep is the internal separator used when encoding a leaf-first stack
// path as a map key. It is the ASCII unit separator, which is expected to
// never appear in frame names.
const pathSep = "\x1f"

// PathKey encodes a leaf-first stack (index 0 is the leaf) as a comparable
// string key.
func PathKey(stack []Frame) string {
	parts := make([]string, len(stack))
	for i, f := range stack {
		parts[i] = f.Name
	}
	return strings.Join(parts, pathSep)
}

// ParsePathKey decodes a key produced by PathKey.
func ParsePathKey(key string) []Frame {
	parts := strings.Split(key, pathSep)
	out := make([]Frame, len(parts))
	for i, p := range parts {
		out[i] = Frame{Name: p}
	}
	return out
}
