package govern

import (
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/vm"
)

// CompiledSegment is a named condition compiled to bytecode.
type CompiledSegment struct {
	Name    string
	Source  string
	Ruleset *compiler.CompiledRuleset
}

// Eval evaluates a compiled segment against a nested context map.
func (s *CompiledSegment) Eval(nestedCtx map[string]any) bool {
	if s == nil || s.Ruleset == nil {
		return false
	}
	sp := vm.NewStringPool(s.Ruleset.Constants.Strings())
	dc := vm.DataFromMap(nestedCtx, sp)
	matched, err := vm.EvalWithPool(s.Ruleset, dc, sp)
	return err == nil && len(matched) > 0
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
