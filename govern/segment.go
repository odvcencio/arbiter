package govern

import (
	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/vm"
)

// CompiledSegment is a named condition compiled to bytecode.
type CompiledSegment struct {
	Name    string
	Source  string
	Ruleset *compiler.CompiledRuleset
}

// Eval evaluates a compiled segment against a nested context map. A runtime
// error from the underlying condition (a type mismatch, a bad builtin call,
// and so on) is returned rather than silently treated as "did not match", so
// callers can surface it the same way they surface a rule condition error.
func (s *CompiledSegment) Eval(nestedCtx map[string]any) (bool, error) {
	if s == nil || s.Ruleset == nil {
		return false, nil
	}
	sp := vm.NewStringPool(s.Ruleset.Constants.Strings())
	dc := vm.DataFromMap(nestedCtx, sp)
	matched, err := vm.EvalWithPool(s.Ruleset, dc, sp)
	if err != nil {
		return false, err
	}
	return len(matched) > 0, nil
}

// SegmentSet holds compiled segments shared across rules and flags.
type SegmentSet struct {
	segments map[string]*CompiledSegment
}

// NewSegmentSet creates an empty set.
func NewSegmentSet() *SegmentSet {
	return &SegmentSet{segments: make(map[string]*CompiledSegment)}
}

// Add registers a pre-compiled segment.
func (ss *SegmentSet) Add(seg *CompiledSegment) {
	if ss == nil || seg == nil {
		return
	}
	if ss.segments == nil {
		ss.segments = make(map[string]*CompiledSegment)
	}
	ss.segments[seg.Name] = seg
}

// All returns all compiled segments.
func (ss *SegmentSet) All() []*CompiledSegment {
	if ss == nil {
		return nil
	}
	out := make([]*CompiledSegment, 0, len(ss.segments))
	for _, seg := range ss.segments {
		out = append(out, seg)
	}
	return out
}

// Get retrieves a segment by name.
func (ss *SegmentSet) Get(name string) (*CompiledSegment, bool) {
	if ss == nil {
		return nil, false
	}
	seg, ok := ss.segments[name]
	return seg, ok
}
