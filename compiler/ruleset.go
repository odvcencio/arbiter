// compiler/ruleset.go
package compiler

import "m31labs.dev/arbiter/bytecode"

// The compiled program types live in package bytecode so that the VM and the
// bundle codec can use them without importing the compiler or the parser.

// CompiledRuleset is the output of compilation — everything the VM needs.
type CompiledRuleset = bytecode.CompiledRuleset

// RuleHeader stores metadata for one rule within the compiled ruleset.
type RuleHeader = bytecode.RuleHeader

// ActionEntry stores a rule's action or fallback.
type ActionEntry = bytecode.ActionEntry

// ActionParam stores one parameter of an action.
type ActionParam = bytecode.ActionParam

// TemplateEntry stores a shared condition subtree.
type TemplateEntry = bytecode.TemplateEntry

// CompiledTable is the compiled form of a table declaration.
type CompiledTable = bytecode.CompiledTable

// LookupMeta stores the metadata for a single OpLookup instruction.
type LookupMeta = bytecode.LookupMeta
