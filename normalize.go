package profiler

// normalizeStack applies the configured stack processing pipeline:
//  1. unknown-frame replacement,
//  2. consecutive-repeated-frame folding,
//  3. depth truncation to the leaf-most MaxDepth frames.
//
// The returned stack is a fresh slice; the input is never modified.
func normalizeStack(stack []Frame, cfg Config) []Frame {
	out := make([]Frame, len(stack))
	copy(out, stack)

	// 1. Unknown frame replacement.
	if cfg.Unknown == UnknownPlaceholder && len(cfg.KnownFrames) > 0 {
		for i, f := range out {
			if !cfg.KnownFrames[f.Name] {
				out[i] = Frame{Name: cfg.Placeholder}
			}
		}
	}

	// 2. Consecutive repeated-frame folding.
	if cfg.Recursion == RecursionFold && len(out) > 1 {
		folded := out[:0]
		for _, f := range out {
			if len(folded) == 0 || folded[len(folded)-1].Name != f.Name {
				folded = append(folded, f)
			}
		}
		out = folded
	}

	// 3. Truncation to the leaf-most MaxDepth frames. Leaf-most frames are
	// kept so every retained node keeps the same self/cumulative counts it
	// had before truncation.
	if cfg.MaxDepth > 0 && len(out) > cfg.MaxDepth {
		out = out[:cfg.MaxDepth]
	}

	return out
}
