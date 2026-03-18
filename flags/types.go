package flags

import (
	"encoding/json"
	"time"
)

// FlagType represents boolean or multivariate flags.
type FlagType int

const (
	FlagBoolean FlagType = iota
	FlagMultivariate
)

// SecretResolver resolves secret references at runtime.
// Implement for your KMS: Vault, AWS Secrets Manager, GCP Secret Manager, etc.
type SecretResolver interface {
	Resolve(ref string) (string, error)
}

// SecretValue marks a value as a secret reference, not a literal.
// The Ref is stored in the .arb file. The actual value is resolved at serve time.
type SecretValue struct {
	Ref string // e.g., "stripe/v2/api_key"
}

// ValueType tracks the declared type of a variant payload field.
type ValueType int

const (
	ValueUnknown ValueType = iota
	ValueString
	ValueNumber
	ValueBool
	ValueSecret
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
	Schema        map[string]ValueType    // inferred field types (validated at load time)
	Metadata      FlagMetadata
}

// VariantDef declares a variant's payload schema and values.
type VariantDef struct {
	Name   string
	Values map[string]any // declared payload values (SecretValue for secret refs)
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

// Bool returns the variant name as a boolean. Useful for boolean flags.
func (v ServedVariant) Bool() bool {
	return v.Name == "true"
}

// String returns a string value from the payload, or fallback if not found.
func (v ServedVariant) String(key, fallback string) string {
	if v.Values == nil {
		return fallback
	}
	if s, ok := v.Values[key].(string); ok {
		return s
	}
	return fallback
}

// Number returns a numeric value from the payload, or fallback if not found.
func (v ServedVariant) Number(key string, fallback float64) float64 {
	if v.Values == nil {
		return fallback
	}
	if n, ok := v.Values[key].(float64); ok {
		return n
	}
	return fallback
}

// Int returns a numeric value as int from the payload, or fallback if not found.
func (v ServedVariant) Int(key string, fallback int) int {
	if v.Values == nil {
		return fallback
	}
	if n, ok := v.Values[key].(float64); ok {
		return int(n)
	}
	return fallback
}

// Flag returns a boolean value from the payload, or fallback if not found.
func (v ServedVariant) Flag(key string, fallback bool) bool {
	if v.Values == nil {
		return fallback
	}
	if b, ok := v.Values[key].(bool); ok {
		return b
	}
	return fallback
}

// Decode unmarshals the variant payload into a typed struct.
// The struct fields should be tagged with `json:"key"`.
//
//	type CheckoutConfig struct {
//	    Provider    string `json:"provider"`
//	    ButtonColor string `json:"button_color"`
//	    MaxItems    int    `json:"max_items"`
//	    ShowPromo   bool   `json:"show_promo"`
//	}
//	var cfg CheckoutConfig
//	v.Decode(&cfg)
func (v ServedVariant) Decode(dst any) error {
	if v.Values == nil {
		return nil
	}
	// Round-trip through JSON for struct tag support
	b, err := json.Marshal(v.Values)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
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
