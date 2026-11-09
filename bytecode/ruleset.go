// bytecode/ruleset.go
package bytecode

import (
	"regexp"

	"m31labs.dev/arbiter/intern"
)

// CompiledRuleset is the output of compilation — everything the VM needs.
type CompiledRuleset struct {
	Constants    *intern.Pool
	Instructions []byte
	Rules        []RuleHeader
	Actions      []ActionEntry
	Templates    []TemplateEntry
	Tags         []uint16
	Prereqs      []uint16
	Excludes     []uint16

	// Regexes holds pre-compiled regexes for literal patterns validated at
	// compile time. Key is the string pool index of the pattern.
	Regexes map[uint16]*regexp.Regexp

	// Tables holds compiled table declarations.
	Tables []CompiledTable

	// LookupMetas holds metadata for each OpLookup instruction.
	LookupMetas []LookupMeta
}

// RuleHeader stores metadata for one rule within the compiled ruleset.
type RuleHeader struct {
	NameIdx             uint16 // index into Constants.strings
	Priority            int32
	ConditionOff        uint32 // byte offset into Instructions
	ConditionLen        uint32 // byte length of condition bytecode
	ActionIdx           uint16 // index into Actions table
	FallbackIdx         uint16 // 0 = none
	KillSwitch          KillSwitchState
	HasActiveFrom       bool
	ActiveFromUnixNano  int64
	HasActiveUntil      bool
	ActiveUntilUnixNano int64
	HasRollout          bool
	RolloutBps          uint16
	RolloutSubjectIdx   uint16
	RolloutNamespaceIdx uint16
	HasRolloutSubject   bool
	HasRolloutNamespace bool
	PrereqOff           uint16
	PrereqLen           uint16
	TagOff              uint16
	TagLen              uint16
	ExcludeOff          uint16
	ExcludeLen          uint16
	SegmentNameIdx      uint16
	HasSegment          bool
}

// HasCondition reports whether this rule has an inline condition.
// A rule with ConditionLen==0 is segment-only; its condition is unconditionally satisfied.
func (rh RuleHeader) HasCondition() bool {
	return rh.ConditionLen > 0
}

// RuleMatchesTags reports whether the rule contains all requested tags.
func (rs *CompiledRuleset) RuleMatchesTags(rule RuleHeader, tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	if rs == nil || rule.TagLen == 0 {
		return false
	}
	start := int(rule.TagOff)
	end := start + int(rule.TagLen)
	if start < 0 || start >= len(rs.Tags) {
		return false
	}
	if end > len(rs.Tags) {
		end = len(rs.Tags)
	}
	if end-start < len(tags) {
		return false
	}
	remaining := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		remaining[tag] = struct{}{}
	}
	if len(remaining) == 0 {
		return true
	}
	for _, idx := range rs.Tags[start:end] {
		delete(remaining, rs.Constants.GetString(idx))
		if len(remaining) == 0 {
			return true
		}
	}
	return len(remaining) == 0
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

// CompiledTable is the compiled form of a table declaration.
type CompiledTable struct {
	Name    string
	Columns []string             // column names in order
	Rows    [][]intern.PoolValue // each row is a slice of pool values
}

// LookupMeta stores the metadata for a single OpLookup instruction.
type LookupMeta struct {
	TableIdx uint16   // index into Tables
	WhereOff uint32   // byte offset of where-clause bytecode (0 if none)
	WhereLen uint32   // byte length of where-clause bytecode
	SortCol  string   // column to sort by ("" if none)
	SortDesc bool     // true for descending sort
	ElseOff  uint32   // byte offset of else-value bytecode (0 if none)
	ElseLen  uint32   // byte length of else-value bytecode
	ElseKeys []string // column names for the else row
}
