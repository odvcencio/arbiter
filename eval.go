package arbiter

import (
	"fmt"
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"

	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/overrides"
	"m31labs.dev/arbiter/strategy"
	"m31labs.dev/arbiter/vm"
)

// Compile compiles .arb source into a Program containing all evaluation artifacts.
func Compile(source []byte, opts ...Option) (*Program, error) {
	parsed, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	program, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		return nil, err
	}

	// Check for imports — error if present without resolver.
	if len(program.Imports) > 0 {
		program, err = resolveProgramImportsForPath(program, "", false, opts...)
		if err != nil {
			return nil, err
		}
	}

	return compileProgramOpts(program, opts...)
}

// CompileResult includes compiled rule/runtime artifacts for one .arb program.
//
// Deprecated: use *Program instead. Will be removed in v2.0.0.
type CompileResult struct {
	Ruleset    *compiler.CompiledRuleset
	Segments   *govern.SegmentSet
	Strategies *strategy.Strategies
	Workers    map[string]WorkerDeclaration
	Arbiters   []ArbiterDeclaration
	Program    *ir.Program
}

// CompileFull compiles .arb source and extracts top-level segments.
//
// Deprecated: use Compile instead. Will be removed in v2.0.0.
func CompileFull(source []byte) (*CompileResult, error) {
	parsed, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	return CompileFullParsed(parsed)
}

// CompileJSON compiles a single Arishem JSON rule.
//
// Deprecated: use Compile instead. Will be removed in v2.0.0.
func CompileJSON(condJSON, actJSON string) (*compiler.CompiledRuleset, error) {
	return compiler.CompileJSONRule("rule0", 0, condJSON, actJSON)
}

// JSONRule is the public alias for one Arishem-format JSON rule.
type JSONRule = compiler.JSONRuleInput

// CompileJSONRules compiles a batch of Arishem JSON rules.
//
// Deprecated: use Compile instead. Will be removed in v2.0.0.
func CompileJSONRules(rules []JSONRule) (*compiler.CompiledRuleset, error) {
	return compiler.CompileJSONBatch(rules)
}

// EvalContext bundles a DataContext with its StringPool so the VM can resolve
// both compile-time and runtime-interned strings.
type EvalContext struct {
	DC   vm.DataContext
	Pool *vm.StringPool
}

// EvalOption configures optional runtime evaluation behavior.
type EvalOption func(*evalOptions)

type evalOptions struct {
	tags []string
}

// WithTags restricts evaluation to rules whose tag set contains every
// requested tag.
func WithTags(tags ...string) EvalOption {
	clean := uniqueEvalTags(tags)
	return func(o *evalOptions) {
		o.tags = append(o.tags[:0], clean...)
	}
}

func applyEvalOptions(opts []EvalOption) evalOptions {
	var out evalOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&out)
		}
	}
	out.tags = uniqueEvalTags(out.tags)
	return out
}

// Eval evaluates a compiled program against a data context.
func Eval(prog *Program, dc vm.DataContext, opts ...EvalOption) ([]vm.MatchedRule, error) {
	if prog == nil {
		return nil, fmt.Errorf("nil program")
	}
	rs := prog.Ruleset
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}
	evalOpts := applyEvalOptions(opts)
	// If dc was created via DataFromMap/DataFromJSON, it shares a pool.
	// Try to extract it; otherwise create a new one.
	if ec, ok := dc.(*evalContextWrapper); ok {
		return vm.EvalWithTagFilter(rs, ec.inner, ec.pool, evalOpts.tags)
	}
	return vm.EvalWithTagFilter(rs, dc, vm.NewStringPool(rs.Constants.Strings()), evalOpts.tags)
}

// EvalDebug evaluates with full debug trace.
func EvalDebug(prog *Program, dc vm.DataContext, opts ...EvalOption) vm.DebugResult {
	if prog == nil {
		return vm.DebugResult{Error: fmt.Errorf("nil program")}
	}
	rs := prog.Ruleset
	if rs == nil {
		return vm.DebugResult{Error: fmt.Errorf("nil ruleset")}
	}
	evalOpts := applyEvalOptions(opts)
	if ec, ok := dc.(*evalContextWrapper); ok {
		return vm.EvalDebugWithTagFilter(rs, ec.inner, ec.pool, evalOpts.tags)
	}
	return vm.EvalDebugWithTagFilter(rs, dc, vm.NewStringPool(rs.Constants.Strings()), evalOpts.tags)
}

// EvalGoverned evaluates a compiled program with rule governance enabled.
func EvalGoverned(prog *Program, dc vm.DataContext, segments *govern.SegmentSet, ctx map[string]any, opts ...EvalOption) ([]vm.MatchedRule, *govern.Arbitrace, error) {
	return EvalGovernedWithOverrides(prog, dc, segments, ctx, "", nil, opts...)
}

// EvalGovernedWithOverrides evaluates a program while applying runtime overrides.
func EvalGovernedWithOverrides(prog *Program, dc vm.DataContext, segments *govern.SegmentSet, ctx map[string]any, bundleID string, view overrides.View, opts ...EvalOption) ([]vm.MatchedRule, *govern.Arbitrace, error) {
	if prog == nil {
		return nil, &govern.Arbitrace{}, fmt.Errorf("nil program")
	}
	rs := prog.Ruleset
	if rs == nil {
		return nil, &govern.Arbitrace{}, fmt.Errorf("nil ruleset")
	}
	evalOpts := applyEvalOptions(opts)
	conditionSources := ruleConditionSources(prog)
	if ec, ok := dc.(*evalContextWrapper); ok {
		return evalGovernedWithPool(rs, ec.inner, ec.pool, conditionSources, segments, ctx, bundleID, view, evalOpts.tags)
	}
	return evalGovernedWithPool(rs, dc, vm.NewStringPool(rs.Constants.Strings()), conditionSources, segments, ctx, bundleID, view, evalOpts.tags)
}

// evalContextWrapper wraps a DataContext with its StringPool.
type evalContextWrapper struct {
	inner vm.DataContext
	pool  *vm.StringPool
}

func (w *evalContextWrapper) Get(key string) vm.Value {
	return w.inner.Get(key)
}

// DataFromMap creates a DataContext from a Go map.
// The returned DataContext shares a StringPool with the evaluator.
func DataFromMap(m map[string]any, prog *Program) vm.DataContext {
	pool := prog.stringPool()
	dc := vm.DataFromMap(m, pool)
	return &evalContextWrapper{inner: dc, pool: pool}
}

// DataFromJSON creates a DataContext from JSON.
func DataFromJSON(jsonStr string, prog *Program) (vm.DataContext, error) {
	pool := prog.stringPool()
	dc, err := vm.DataFromJSON(jsonStr, pool)
	if err != nil {
		return nil, err
	}
	return &evalContextWrapper{inner: dc, pool: pool}, nil
}

func compileSegments(program *ir.Program) (*govern.SegmentSet, error) {
	segments := govern.NewSegmentSet()

	if program == nil {
		return segments, nil
	}

	for i := range program.Segments {
		segment := &program.Segments[i]
		rs, err := compileSegmentRuleset(program, segment)
		if err != nil {
			return nil, fmt.Errorf("compile segment %s: %w", segment.Name, err)
		}
		segments.Add(&govern.CompiledSegment{
			Name:    segment.Name,
			Source:  ir.RenderExpr(program, segment.Condition),
			Ruleset: rs,
		})
	}

	return segments, nil
}

func compileSegmentRuleset(program *ir.Program, segment *ir.Segment) (*compiler.CompiledRuleset, error) {
	if program == nil || segment == nil {
		return nil, fmt.Errorf("nil segment program")
	}
	synthetic := &ir.Program{
		Consts: program.Consts,
		Exprs:  program.Exprs,
		Rules: []ir.Rule{
			{
				Name:         "__seg_" + segment.Name,
				HasCondition: true,
				Condition:    segment.Condition,
				Action:       ir.Action{Name: "Match"},
			},
		},
	}
	synthetic.RebuildIndexes()
	return compiler.CompileIR(synthetic)
}

func evalGovernedWithPool(rs *compiler.CompiledRuleset, dc vm.DataContext, sp *vm.StringPool, conditionSources map[string]string, segments *govern.SegmentSet, ctx map[string]any, bundleID string, view overrides.View, tags []string) ([]vm.MatchedRule, *govern.Arbitrace, error) {
	if rs == nil {
		return nil, &govern.Arbitrace{}, fmt.Errorf("nil ruleset")
	}

	trace := &govern.Arbitrace{}
	rc := govern.NewRequestCache(segments, ctx)
	evaluator := vm.NewEvaluator(rs, sp)
	var matched []vm.MatchedRule

	for _, rule := range rs.Rules {
		if !rs.RuleMatchesTags(rule, tags) {
			continue
		}
		ruleName := evaluator.String(rule.NameIdx)
		var killSwitchOverride *bool
		var rolloutOverride *uint16
		if view != nil {
			if ov, ok := view.Rule(bundleID, ruleName); ok {
				if ov.KillSwitch != nil {
					killSwitchOverride = ov.KillSwitch
				}
				if ov.Rollout != nil {
					rolloutOverride = ov.Rollout
				}
			}
		}

		killSwitch := govern.ResolveKillSwitch(rule.KillSwitch.IsSet(), rule.KillSwitch.Enabled(), killSwitchOverride)
		if killSwitch.RecordScoped(trace, govern.ArbitraceScopeRule, ruleName, "kill_switch") {
			rc.RecordRuleResult(ruleName, false)
			continue
		}

		if !govern.RecordActiveWindow(trace, rc.EvalTime(), govern.ArbitraceScopeRule, ruleName, "", rule.HasActiveFrom, rule.ActiveFromUnixNano, rule.HasActiveUntil, rule.ActiveUntilUnixNano) {
			rc.RecordRuleResult(ruleName, false)
			continue
		}

		if !rc.CheckPrerequisitesFor(govern.ArbitraceScopeRule, ruleName, resolvePrereqs(rs, rule, evaluator), trace) {
			rc.RecordRuleResult(ruleName, false)
			continue
		}

		if !rc.CheckExclusionsFor(govern.ArbitraceScopeRule, ruleName, resolveExcludes(rs, rule, evaluator), trace) {
			rc.RecordRuleResult(ruleName, false)
			continue
		}

		if rule.HasSegment {
			segName := evaluator.String(rule.SegmentNameIdx)
			segOK, detail, segErr := rc.EvalSegment(segName)
			trace.AppendScoped(govern.ArbitracePhaseMatch, govern.ArbitraceScopeRule, ruleName, govern.ArbitraceKindSegment, segName, "", segOK, detail)
			if segErr != nil {
				return nil, trace, fmt.Errorf("rule %s segment %s: %w", ruleName, segName, segErr)
			}
			if !segOK {
				rc.RecordRuleResult(ruleName, false)
				continue
			}
		}

		// Evaluate the rule condition. Empty condition (ConditionLen==0) means
		// unconditional — EvalRuleCondition returns true for segment-only rules.
		condOK, condErr := evaluator.EvalRuleCondition(rule, dc)
		if condErr != nil {
			return nil, trace, fmt.Errorf("rule %s: %w", ruleName, condErr)
		}
		conditionDetail := conditionSources[ruleName]
		if conditionDetail == "" {
			if rule.HasCondition() {
				conditionDetail = "compiled rule condition"
			} else {
				conditionDetail = "no inline condition (segment-only rule)"
			}
		}
		trace.AppendScoped(govern.ArbitracePhaseMatch, govern.ArbitraceScopeRule, ruleName, govern.ArbitraceKindCondition, "", "", condOK, conditionDetail)
		if !condOK {
			rc.RecordRuleResult(ruleName, false)
			if evaluator.HasFallback(rule) {
				mr, err := evaluator.BuildFallback(rule, dc)
				if err != nil {
					return nil, trace, fmt.Errorf("rule %s fallback %s: %w", ruleName, mr.Action, err)
				}
				trace.AppendScoped(govern.ArbitracePhaseEffect, govern.ArbitraceScopeRule, ruleName, govern.ArbitraceKindFallback, "", "fallback", true, "otherwise arm selected")
				matched = append(matched, mr)
			}
			continue
		}

		if spec := effectiveRuleRollout(rule, rs, ruleName, bundleID, rolloutOverride); spec != nil {
			decision := govern.DecidePercentRollout(*spec, rc.Context())
			trace.AppendScoped(govern.ArbitracePhaseGovernance, govern.ArbitraceScopeRule, ruleName, govern.ArbitraceKindRollout, spec.Namespace, spec.CheckLabel(), decision.Allowed, decision.Detail())
			if !decision.Allowed {
				rc.RecordRuleResult(ruleName, false)
				continue
			}
		}

		rc.RecordRuleResult(ruleName, true)
		mr, err := evaluator.BuildMatch(rule, dc)
		if err != nil {
			return nil, trace, fmt.Errorf("rule %s action %s: %w", ruleName, mr.Action, err)
		}
		matched = append(matched, mr)
	}

	return matched, trace, nil
}

func effectiveRuleRollout(rule compiler.RuleHeader, rs *compiler.CompiledRuleset, ruleName, bundleID string, override *uint16) *govern.PercentRollout {
	hasRollout := rule.HasRollout
	rolloutBps := rule.RolloutBps
	if override != nil {
		hasRollout = true
		rolloutBps = *override
	}
	if !hasRollout {
		return nil
	}
	subject := govern.DefaultRolloutSubject
	if rule.HasRolloutSubject {
		subject = rs.Constants.GetString(rule.RolloutSubjectIdx)
	}
	namespace := ""
	if rule.HasRolloutNamespace {
		namespace = rs.Constants.GetString(rule.RolloutNamespaceIdx)
	}
	if namespace == "" {
		namespace = govern.AutoRolloutNamespace(bundleID, "rule:"+ruleName)
	}
	return &govern.PercentRollout{
		PercentBps: rolloutBps,
		SubjectKey: subject,
		Namespace:  namespace,
	}
}

func resolvePrereqs(rs *compiler.CompiledRuleset, rule compiler.RuleHeader, evaluator *vm.Evaluator) []string {
	if rule.PrereqLen == 0 {
		return nil
	}

	start := int(rule.PrereqOff)
	end := start + int(rule.PrereqLen)
	if start < 0 || start >= len(rs.Prereqs) {
		return nil
	}
	if end > len(rs.Prereqs) {
		end = len(rs.Prereqs)
	}

	names := make([]string, 0, end-start)
	for _, idx := range rs.Prereqs[start:end] {
		names = append(names, evaluator.String(idx))
	}
	return names
}

func resolveExcludes(rs *compiler.CompiledRuleset, rule compiler.RuleHeader, evaluator *vm.Evaluator) []string {
	if rule.ExcludeLen == 0 {
		return nil
	}
	start := int(rule.ExcludeOff)
	end := start + int(rule.ExcludeLen)
	if start < 0 || start >= len(rs.Excludes) {
		return nil
	}
	if end > len(rs.Excludes) {
		end = len(rs.Excludes)
	}
	names := make([]string, 0, end-start)
	for _, idx := range rs.Excludes[start:end] {
		names = append(names, evaluator.String(idx))
	}
	return names
}

func nodeText(n *gotreesitter.Node, source []byte) string {
	return string(source[n.StartByte():n.EndByte()])
}

func uniqueEvalTags(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func ruleConditionSources(prog *Program) map[string]string {
	if prog == nil || prog.IR == nil || len(prog.IR.Rules) == 0 {
		return nil
	}
	out := make(map[string]string, len(prog.IR.Rules))
	for _, rule := range prog.IR.Rules {
		if !rule.HasCondition {
			continue
		}
		out[rule.Name] = ir.RenderExpr(prog.IR, rule.Condition)
	}
	return out
}
