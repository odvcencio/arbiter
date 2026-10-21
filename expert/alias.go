package expert

import (
	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/expert/engine"
	"m31labs.dev/arbiter/units"
)

// The expert runtime lives in package engine, which does not import the .arb
// parser. These aliases keep the expert API unchanged. A program that only
// loads precompiled rules should import engine directly.

type (
	ActionKind  = engine.ActionKind
	Activation  = engine.Activation
	Checkpoint  = engine.Checkpoint
	Fact        = engine.Fact
	Options     = engine.Options
	Outcome     = engine.Outcome
	Program     = engine.Program
	Result      = engine.Result
	Rule        = engine.Rule
	Session     = engine.Session
	StopReason  = engine.StopReason
	SyncSummary = engine.SyncSummary
)

const (
	ActionAssert  = engine.ActionAssert
	ActionEmit    = engine.ActionEmit
	ActionRetract = engine.ActionRetract
	ActionModify  = engine.ActionModify

	StopQuiescent        = engine.StopQuiescent
	StopMaxRounds        = engine.StopMaxRounds
	StopMaxMutations     = engine.StopMaxMutations
	StopContextCancelled = engine.StopContextCancelled
)

// D constructs one runtime decimal value for schema-aware fact assertions.
func D(value, unit string) dec.Value { return engine.D(value, unit) }

// Q constructs one runtime quantity value for schema-aware fact assertions.
func Q(value float64, unit string) units.Quantity { return engine.Q(value, unit) }

// NewSession creates a new in-memory expert session.
func NewSession(p *Program, envelope map[string]any, facts []Fact, opts Options) *Session {
	return engine.NewSession(p, envelope, facts, opts)
}
