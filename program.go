package arbiter

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/strategy"
	"m31labs.dev/arbiter/vm"
)

// DiagnosticSeverity indicates the severity level of a Diagnostic.
type DiagnosticSeverity int

const (
	// DiagWarning is a non-fatal warning diagnostic.
	DiagWarning DiagnosticSeverity = iota
	// DiagInfo is an informational diagnostic.
	DiagInfo
)

// Diagnostic is a non-fatal compiler message with file and position data.
type Diagnostic struct {
	Severity DiagnosticSeverity
	Message  string
	File     string
	Line     int
	Col      int
}

// Program is the compiled output of an Arbiter source. It contains all
// artifacts needed for evaluation: bytecode, segments, strategies, etc.
type Program struct {
	Ruleset    *compiler.CompiledRuleset
	Segments   *govern.SegmentSet
	Strategies *strategy.Strategies
	Expert     interface{} // *expert.Program when expert declarations exist; nil otherwise
	IR         *ir.Program
	Workers    map[string]WorkerDeclaration
	Arbiters   []ArbiterDeclaration
	Input      *ir.InputSchema
	Warnings   []Diagnostic   // non-fatal diagnostics collected during compilation
	pool       *vm.StringPool // internal, sealed from public API
	poolOnce   sync.Once
	selections atomic.Pointer[ruleSelections] // tag set -> selected rules, built on first use
}

// stringPool returns the string pool, lazily initializing from the ruleset if needed.
func (p *Program) stringPool() *vm.StringPool {
	if p == nil {
		return nil
	}
	p.poolOnce.Do(func() {
		if p.Ruleset != nil {
			p.pool = vm.NewStringPool(p.Ruleset.Constants.Strings())
		}
	})
	return p.pool
}

// maxCachedSelections bounds the tag sets one Program remembers. A program
// declares a handful of tags, but tag sets can come from callers (a service
// may pass them through from a request), so the cache must not grow with
// every distinct set it is shown. Past the bound a miss is computed and not
// stored, which is what Eval did before the cache existed.
const maxCachedSelections = 64

// ruleSelections caches, per tag set, the rules Eval selects from one ruleset.
// The table is immutable; a miss publishes a copy with the new entry.
type ruleSelections struct {
	rs    *compiler.CompiledRuleset
	byTag map[string]*ruleSelection
}

// ruleSelection is the rules one tag set selects, in declaration order, and
// the error plain evaluation returns when one of them needs governance.
type ruleSelection struct {
	rules []compiler.RuleHeader
	err   error
}

// selection returns the rules the tag set selects. Eval used to test every
// rule's tags on every call, building a map per rule; a program's rules and
// tags do not change, so the answer is computed once per tag set.
func (p *Program) selection(rs *compiler.CompiledRuleset, tags []string) *ruleSelection {
	key := ""
	if len(tags) == 1 {
		key = tags[0]
	} else if len(tags) > 1 {
		key = strings.Join(tags, "\x00")
	}
	table := p.selections.Load()
	if table != nil && table.rs == rs {
		if sel, ok := table.byTag[key]; ok {
			return sel
		}
	}
	sel := &ruleSelection{}
	if len(tags) == 0 {
		sel.rules = rs.Rules
	}
	for _, rule := range rs.Rules {
		if !rs.RuleMatchesTags(rule, tags) {
			continue
		}
		if sel.err == nil && ruleRequiresGovernance(rule) {
			sel.err = fmt.Errorf("rule %s: %w", rs.Constants.GetString(rule.NameIdx), ErrGovernanceRequired)
		}
		if len(tags) != 0 {
			sel.rules = append(sel.rules, rule)
		}
	}
	if table != nil && table.rs == rs && len(table.byTag) >= maxCachedSelections {
		return sel
	}
	next := &ruleSelections{rs: rs, byTag: map[string]*ruleSelection{key: sel}}
	if table != nil && table.rs == rs {
		for k, v := range table.byTag {
			if _, ok := next.byTag[k]; !ok {
				next.byTag[k] = v
			}
		}
	}
	// A lost race only drops a cache entry; the next call rebuilds it.
	p.selections.CompareAndSwap(table, next)
	return sel
}

// Option configures optional Compile behavior.
type Option func(*compileOptions)

type compileOptions struct {
	manifest    *string
	resolver    IncludeResolver
	inputSchema *ir.InputSchema
	baseDir     string // directory used to resolve `input from proto "<rel>"` refs
}

// withBaseDir sets the directory that relative `input from proto` paths resolve
// against. Set internally by the file-backed compile entrypoints.
func withBaseDir(dir string) Option {
	return func(o *compileOptions) { o.baseDir = dir }
}

// WithManifest sets the manifest path for module resolution.
func WithManifest(path string) Option {
	return func(o *compileOptions) { o.manifest = &path }
}

// WithResolver sets the include resolver for import resolution.
func WithResolver(r IncludeResolver) Option {
	return func(o *compileOptions) { o.resolver = r }
}

// WithInputSchema injects an externally-derived input schema — for example one
// synthesized from a protobuf message descriptor via protoschema.FromMessage,
// or from a Go struct — so that .arb field references are type-checked against
// it at compile time, with zero schema duplication in the .arb source. It
// merges with any in-source input{} block; conflicting types on the same path
// are reported as a compile error.
func WithInputSchema(schema *ir.InputSchema) Option {
	return func(o *compileOptions) { o.inputSchema = schema }
}

// toCompileResult converts a Program to the legacy CompileResult type.
func (p *Program) toCompileResult() *CompileResult {
	if p == nil {
		return nil
	}
	return &CompileResult{
		Ruleset:    p.Ruleset,
		Segments:   p.Segments,
		Strategies: p.Strategies,
		Workers:    p.Workers,
		Arbiters:   p.Arbiters,
		Program:    p.IR,
	}
}

// programFromResult converts a CompileResult to a Program.
func programFromResult(cr *CompileResult) *Program {
	if cr == nil {
		return nil
	}
	return &Program{
		Ruleset:    cr.Ruleset,
		Segments:   cr.Segments,
		Strategies: cr.Strategies,
		IR:         cr.Program,
		Workers:    cr.Workers,
		Arbiters:   cr.Arbiters,
	}
}
