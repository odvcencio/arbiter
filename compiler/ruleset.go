// compiler/ruleset.go
package compiler

import "github.com/odvcencio/arbiter/intern"

// CompiledRuleset is the output of compilation — everything the VM needs.
type CompiledRuleset struct {
	Constants    *intern.Pool
	Instructions []byte
	Rules        []RuleHeader
	Actions      []ActionEntry
	Templates    []TemplateEntry
}

// RuleHeader stores metadata for one rule within the compiled ruleset.
type RuleHeader struct {
	NameIdx      uint16 // index into Constants.strings
	Priority     int32
	ConditionOff uint32 // byte offset into Instructions
	ConditionLen uint32 // byte length of condition bytecode
	ActionIdx    uint16 // index into Actions table
	FallbackIdx  uint16 // 0 = none
}

// ActionEntry stores a rule's action or fallback.
type ActionEntry struct {
	NameIdx uint16        // action name → Constants.strings index
	Params  []ActionParam // resolved parameters
}

// ActionParam stores one parameter of an action.
type ActionParam struct {
	KeyIdx   uint16 // param name → Constants.strings index
	ValueOff uint32 // byte offset into Instructions for value expression
	ValueLen uint32 // byte length of value expression bytecode
}

// TemplateEntry stores a shared condition subtree.
type TemplateEntry struct {
	Hash     uint64 // structural hash of the subtree
	InstrOff uint32 // byte offset into Instructions
	InstrLen uint16 // byte length
}
