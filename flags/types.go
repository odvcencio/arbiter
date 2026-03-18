package flags

import "time"

// FlagType represents boolean or multivariate flags.
type FlagType int

const (
	FlagBoolean FlagType = iota
	FlagMultivariate
)

// FlagDef is a parsed flag definition from .arb source.
type FlagDef struct {
	Key           string
	Type          FlagType
	Default       string       // default variant ("false", "control", etc.)
	KillSwitch    bool
	Prerequisites []string     // flag keys that must be enabled
	Rules         []FlagRule   // ordered targeting rules
	Metadata      FlagMetadata
}

// FlagRule is one targeting rule within a flag.
type FlagRule struct {
	SegmentName    string           // reference to a named segment, or ""
	InlineExpr     string           // inline condition source (if no segment name)
	CompiledInline *compiledSegment // precompiled inline condition (nil if segment ref)
	Variant        string           // variant to serve if matched
	Rollout        int              // 0-100, 0 means no rollout (always match)
}

// FlagMetadata holds human-readable info about a flag.
type FlagMetadata struct {
	Owner     string
	Ticket    string
	Rationale string
	Expires   *time.Time
}

// Segment is a named, reusable condition.
type Segment struct {
	Name   string
	Source string // the original condition source text
}

// FlagEvaluation is the rich result of evaluating a flag.
type FlagEvaluation struct {
	Flag      string
	Variant   string
	IsDefault bool
	Reason    string // human-readable one-liner
	Trace     []TraceStep
	Metadata  FlagMetadata
	Elapsed   time.Duration
}

// TraceStep records one check in the evaluation.
type TraceStep struct {
	Check  string // "kill_switch", "requires payments_enabled", "segment internal", "rollout 20%"
	Result bool
	Detail string // "user.email ends_with \"@m31labs.dev\" -> true"
}
