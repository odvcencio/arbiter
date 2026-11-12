package arbiter

import (
	"fmt"
	"slices"
	"strconv"
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
//
// Treat a Program's ruleset as read-only once Eval has run. Eval remembers
// which rules each tag set selects, so a rule, tag or header changed in place
// afterwards is not seen. Assigning a different ruleset to Ruleset is
// detected and starts a new selection cache.
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
// stored, which is what Eval did before the cache existed. Only sets made of
// tags that rules carry count toward the bound: see selection.
const maxCachedSelections = 64

// ruleSelections caches, per tag set, the rules Eval selects from one ruleset.
// A published table is never modified; a miss publishes a copy with the new
// entry. The copies share known, which is built once per ruleset.
type ruleSelections struct {
	rs    *compiler.CompiledRuleset
	known map[string]struct{}       // every tag at least one rule carries
	all   *ruleSelection            // the selection for no tags: every rule
	byKey map[string]*ruleSelection // appendSelectionKey of a tag set -> its selection
}

// ruleSelection is the rules one tag set selects, in declaration order, and
// the error plain evaluation returns when one of them needs governance.
// Selections are shared between calls and goroutines and are never modified.
type ruleSelection struct {
	rules []compiler.RuleHeader
	err   error
}

// noRulesSelected is the selection of every tag set that names a tag no rule
// carries.
var noRulesSelected = &ruleSelection{}

// selection returns the rules the tag set selects. Eval used to test every
// rule's tags on every call, building a map per rule; a program's rules and
// tags do not change, so the answer is computed once per tag set.
//
// tags must hold no duplicates and no empty strings, which is what
// uniqueEvalTags returns. Their order does not matter: a rule is selected when
// it carries every requested tag.
func (p *Program) selection(rs *compiler.CompiledRuleset, tags []string) *ruleSelection {
	table := p.selections.Load()
	if table == nil || table.rs != rs {
		// First use, or Program.Ruleset was reassigned. A selection holds rule
		// headers of the ruleset it was computed for, so a table for another
		// ruleset is dropped whole. Losing the race to publish only costs a
		// rebuild on a later call.
		fresh := &ruleSelections{rs: rs, known: ruleTagSet(rs)}
		p.selections.CompareAndSwap(table, fresh)
		table = fresh
	}
	if len(tags) == 0 {
		if table.all != nil {
			return table.all
		}
		sel := selectRules(rs, nil)
		next := *table
		next.all = sel
		p.selections.CompareAndSwap(table, &next)
		return sel
	}
	var buf [96]byte
	key := appendSelectionKey(buf[:0], tags)
	if sel, ok := table.byKey[string(key)]; ok {
		return sel
	}
	// A rule is selected only when it carries every requested tag, so one tag
	// that no rule carries selects nothing and cannot refuse. Such a set is
	// answered without a slot: the tags callers make up are unbounded, and
	// storing them would fill the table and leave no room for the real ones.
	for _, tag := range tags {
		if _, ok := table.known[tag]; !ok {
			return noRulesSelected
		}
	}
	sel := selectRules(rs, tags)
	if len(table.byKey) >= maxCachedSelections {
		return sel
	}
	next := *table
	next.byKey = make(map[string]*ruleSelection, len(table.byKey)+1)
	for k, v := range table.byKey {
		next.byKey[k] = v
	}
	next.byKey[string(key)] = sel
	// A lost race only drops a cache entry; the next call rebuilds it.
	p.selections.CompareAndSwap(table, &next)
	return sel
}

// selectRules computes the selection for one tag set.
func selectRules(rs *compiler.CompiledRuleset, tags []string) *ruleSelection {
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
	return sel
}

// ruleTagSet returns every tag at least one rule of rs carries. It reads the
// same tag ranges as CompiledRuleset.RuleMatchesTags, so a tag outside the set
// matches no rule.
func ruleTagSet(rs *compiler.CompiledRuleset) map[string]struct{} {
	known := make(map[string]struct{})
	for i := range rs.Rules {
		start := int(rs.Rules[i].TagOff)
		end := min(start+int(rs.Rules[i].TagLen), len(rs.Tags))
		for j := start; j < end; j++ {
			known[rs.Constants.GetString(rs.Tags[j])] = struct{}{}
		}
	}
	return known
}

// appendSelectionKey appends the cache key of a tag set to dst: the tags in
// sorted order, each written as its length in bytes, a colon and the tag.
// Sorting makes the key independent of the order the caller gave, because tag
// matching treats the tags as a set. The length prefix makes the key
// injective: two different sets never share a key, whatever bytes the tags
// hold. Joining the tags with a separator is not injective, because a tag may
// contain the separator.
func appendSelectionKey(dst []byte, tags []string) []byte {
	if len(tags) > 1 {
		var buf [8]string
		sorted := append(buf[:0], tags...)
		slices.Sort(sorted)
		tags = sorted
	}
	for _, tag := range tags {
		dst = strconv.AppendInt(dst, int64(len(tag)), 10)
		dst = append(dst, ':')
		dst = append(dst, tag...)
	}
	return dst
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
