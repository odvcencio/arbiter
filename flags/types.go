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
	Default       string                  // default variant name ("false", "control", etc.)
	KillSwitch    bool
	Prerequisites []string                // flag keys that must be enabled
	Rules         []FlagRule              // ordered targeting rules
	Variants      map[string]*VariantDef  // declared variant definitions (nil if undeclared)
	DefaultValues map[string]any          // defaults { ... } block — inherited by all variants
	Metadata      FlagMetadata
}

// VariantDef declares a variant's payload schema and values.
type VariantDef struct {
	Name   string
	Values map[string]any // declared payload values
}

// FlagRule is one targeting rule within a flag.
type FlagRule struct {
	SegmentName    string           // reference to a named segment, or ""
	InlineExpr     string           // inline condition source (if no segment name)
	CompiledInline *compiledSegment // precompiled inline condition (nil if segment ref)
	Variant        string           // variant name to serve if matched
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

// ServedVariant is the result of evaluating a flag variant.
// For boolean flags without payloads, Values is nil.
// For multivariate flags with declared variants, Values contains the payload.
type ServedVariant struct {
	Name   string         `json:"name"`            // variant name ("treatment", "true", etc.)
	Values map[string]any `json:"values,omitempty"` // payload (nil if no variant block declared)
}

// FlagEvaluation is the rich result of evaluating a flag with full trace.
type FlagEvaluation struct {
	Flag      string        `json:"flag"`
	Variant   ServedVariant `json:"variant"`
	IsDefault bool          `json:"is_default"`
	Reason    string        `json:"reason"`
	Trace     []TraceStep   `json:"trace"`
	Metadata  FlagMetadata  `json:"metadata"`
	Elapsed   time.Duration `json:"elapsed"`
}

// TraceStep records one check in the evaluation.
type TraceStep struct {
	Check  string `json:"check"`  // "kill_switch", "requires payments_enabled", "segment internal"
	Result bool   `json:"result"`
	Detail string `json:"detail"` // "user.email ends_with \"@m31labs.dev\" -> true"
}
