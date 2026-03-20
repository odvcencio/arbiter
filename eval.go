package arbiter

import (
	"fmt"
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/overrides"
	"github.com/odvcencio/arbiter/vm"
)

// Compile compiles .arb source into a CompiledRuleset.
func Compile(source []byte) (*compiler.CompiledRuleset, error) {
	parsed, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	return CompileParsed(parsed)
}

// CompileResult includes a ruleset, top-level segments, and arbiter declarations.
type CompileResult struct {
	Ruleset  *compiler.CompiledRuleset
	Segments *govern.SegmentSet
	Arbiters []ArbiterDeclaration
}

// CompileFull compiles .arb source and extracts top-level segments.
func CompileFull(source []byte) (*CompileResult, error) {
	parsed, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	return CompileFullParsed(parsed)
}

// CompileJSON compiles a single Arishem JSON rule.
func CompileJSON(condJSON, actJSON string) (*compiler.CompiledRuleset, error) {
	return compiler.CompileJSONRule("rule0", 0, condJSON, actJSON)
}

// JSONRule is the public alias for one Arishem-format JSON rule.
type JSONRule = compiler.JSONRuleInput

// CompileJSONRules compiles a batch of Arishem JSON rules.
func CompileJSONRules(rules []JSONRule) (*compiler.CompiledRuleset, error) {
	return compiler.CompileJSONBatch(rules)
}

// EvalContext bundles a DataContext with its StringPool so the VM can resolve
// both compile-time and runtime-interned strings.
type EvalContext struct {
	DC   vm.DataContext
	Pool *vm.StringPool
}

// Eval evaluates a compiled ruleset against a data context.
func Eval(rs *compiler.CompiledRuleset, dc vm.DataContext) ([]vm.MatchedRule, error) {
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}
	// If dc was created via DataFromMap/DataFromJSON, it shares a pool.
	// Try to extract it; otherwise create a new one.
	if ec, ok := dc.(*evalContextWrapper); ok {
		return vm.EvalWithPool(rs, ec.inner, ec.pool)
	}
	return vm.Eval(rs, dc)
}

// EvalDebug evaluates with full debug trace.
func EvalDebug(rs *compiler.CompiledRuleset, dc vm.DataContext) vm.DebugResult {
	if rs == nil {
		return vm.DebugResult{Error: fmt.Errorf("nil ruleset")}
	}
	if ec, ok := dc.(*evalContextWrapper); ok {
		return vm.EvalDebugWithPool(rs, ec.inner, ec.pool)
	}
	return vm.EvalDebug(rs, dc)
}

// EvalGoverned evaluates a compiled ruleset with rule governance enabled.
func EvalGoverned(rs *compiler.CompiledRuleset, dc vm.DataContext, segments *govern.SegmentSet, ctx map[string]any) ([]vm.MatchedRule, *govern.Trace, error) {
	return EvalGovernedWithOverrides(rs, dc, segments, ctx, "", nil)
}

// EvalGovernedWithOverrides evaluates a ruleset while applying runtime overrides.
func EvalGovernedWithOverrides(rs *compiler.CompiledRuleset, dc vm.DataContext, segments *govern.SegmentSet, ctx map[string]any, bundleID string, view overrides.View) ([]vm.MatchedRule, *govern.Trace, error) {
	if rs == nil {
		return nil, &govern.Trace{}, fmt.Errorf("nil ruleset")
	}
	if ec, ok := dc.(*evalContextWrapper); ok {
		return evalGovernedWithPool(rs, ec.inner, ec.pool, segments, ctx, bundleID, view)
	}
	return evalGovernedWithPool(rs, dc, vm.NewStringPool(rs.Constants.Strings()), segments, ctx, bundleID, view)
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
func DataFromMap(m map[string]any, rs *compiler.CompiledRuleset) vm.DataContext {
	pool := vm.NewStringPool(rs.Constants.Strings())
	dc := vm.DataFromMap(m, pool)
	return &evalContextWrapper{inner: dc, pool: pool}
}

// DataFromJSON creates a DataContext from JSON.
func DataFromJSON(jsonStr string, rs *compiler.CompiledRuleset) (vm.DataContext, error) {
	pool := vm.NewStringPool(rs.Constants.Strings())
	dc, err := vm.DataFromJSON(jsonStr, pool)
	if err != nil {
		return nil, err
	}
	return &evalContextWrapper{inner: dc, pool: pool}, nil
}

func parseSource(source []byte) (*gotreesitter.Language, *gotreesitter.Node, error) {
	lang, root, err := parseTree(source)
	if err != nil {
		return nil, nil, err
	}
	if err := rejectIncludeDeclarations(root, source, lang); err != nil {
		return nil, nil, err
	}
	return lang, root, nil
}

func compileSegments(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (*govern.SegmentSet, error) {
	segments := govern.NewSegmentSet()

	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type(lang) != "segment_declaration" {
			continue
		}

		nameNode := child.ChildByFieldName("name", lang)
		condNode := child.ChildByFieldName("condition", lang)
		if nameNode == nil || condNode == nil {
			return nil, fmt.Errorf("segment missing name or condition")
		}

		name := nodeText(nameNode, source)
		condition := strings.TrimSpace(nodeText(condNode, source))
		rs, err := compileSegmentRuleset(name, condition)
		if err != nil {
			return nil, fmt.Errorf("compile segment %s: %w", name, err)
		}
		segments.Add(&govern.CompiledSegment{
			Name:    name,
			Source:  condition,
			Ruleset: rs,
		})
	}

	return segments, nil
}

func compileSegmentRuleset(name, condition string) (*compiler.CompiledRuleset, error) {
	synthetic := fmt.Sprintf("rule __seg_%s { when { %s } then Match {} }", name, condition)
	return Compile([]byte(synthetic))
}

func evalGovernedWithPool(rs *compiler.CompiledRuleset, dc vm.DataContext, sp *vm.StringPool, segments *govern.SegmentSet, ctx map[string]any, bundleID string, view overrides.View) ([]vm.MatchedRule, *govern.Trace, error) {
	if rs == nil {
		return nil, &govern.Trace{}, fmt.Errorf("nil ruleset")
	}

	trace := &govern.Trace{}
	rc := govern.NewRequestCache(segments, ctx)
	evaluator := vm.NewEvaluator(rs, sp)
	var matched []vm.MatchedRule

	for _, rule := range rs.Rules {
		ruleName := evaluator.String(rule.NameIdx)
		killSwitch := rule.KillSwitch
		rollout := rule.Rollout
		if view != nil {
			if ov, ok := view.Rule(bundleID, ruleName); ok {
				if ov.KillSwitch != nil {
					killSwitch = *ov.KillSwitch
				}
				if ov.Rollout != nil {
					rollout = *ov.Rollout
				}
			}
		}

		if govern.IsKillSwitched(killSwitch, trace) {
			rc.RecordRuleResult(ruleName, false)
			continue
		}

		if !rc.CheckPrerequisites(resolvePrereqs(rs, rule, evaluator), trace) {
			rc.RecordRuleResult(ruleName, false)
			continue
		}

		if !rc.CheckExclusions(resolveExcludes(rs, rule, evaluator), trace) {
			rc.RecordRuleResult(ruleName, false)
			continue
		}

		if rule.HasSegment {
			segName := evaluator.String(rule.SegmentNameIdx)
			segOK, detail := rc.EvalSegment(segName)
			trace.Append("segment "+segName, segOK, detail)
			if !segOK {
				rc.RecordRuleResult(ruleName, false)
				continue
			}
		}

		condOK, err := evaluator.EvalRuleCondition(rule, dc)
		if err != nil {
			return nil, trace, fmt.Errorf("rule %s: %w", ruleName, err)
		}
		if !condOK {
			rc.RecordRuleResult(ruleName, false)
			if evaluator.HasFallback(rule) {
				mr, err := evaluator.BuildFallback(rule, dc)
				if err != nil {
					return nil, trace, fmt.Errorf("rule %s fallback %s: %w", ruleName, mr.Action, err)
				}
				matched = append(matched, mr)
			}
			continue
		}

		if rollout > 0 {
			userID := govern.RolloutUserID(rc.Context())
			allowed := govern.RolloutAllows(rollout, rc.Context())
			trace.Append(
				fmt.Sprintf("rollout %d%%", rollout),
				allowed,
				fmt.Sprintf("bucket(%q) = %d, threshold = %d", userID, govern.Bucket(userID), rollout),
			)
			if !allowed {
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
