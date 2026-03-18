package vm

import "github.com/odvcencio/arbiter/compiler"

// Evaluator reuses a VM across multiple rule evaluations.
type Evaluator struct {
	rs *compiler.CompiledRuleset
	vm *VM
}

// NewEvaluator creates a reusable evaluator for a compiled ruleset.
func NewEvaluator(rs *compiler.CompiledRuleset, sp *StringPool) *Evaluator {
	return &Evaluator{
		rs: rs,
		vm: newVM(rs, sp),
	}
}

// String resolves a compile-time string constant through the evaluator's pool.
func (e *Evaluator) String(idx uint16) string {
	return e.vm.strPool.Get(idx)
}

// EvalRuleCondition evaluates one rule's condition.
func (e *Evaluator) EvalRuleCondition(rule compiler.RuleHeader, dc DataContext) (bool, error) {
	e.vm.sp = 0
	clear(e.vm.locals)
	e.vm.iters = e.vm.iters[:0]
	e.vm.err = nil

	ok := e.vm.evalCondition(e.rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)
	if e.vm.err != nil {
		return false, e.vm.err
	}
	return ok, nil
}

// HasFallback reports whether a rule has a valid fallback action.
func (e *Evaluator) HasFallback(rule compiler.RuleHeader) bool {
	return rule.FallbackIdx != 0 && int(rule.FallbackIdx) < len(e.rs.Actions)
}

// BuildMatch builds the normal action result for a matched rule.
func (e *Evaluator) BuildMatch(rule compiler.RuleHeader, dc DataContext) (MatchedRule, error) {
	return e.buildAction(rule, rule.ActionIdx, false, dc)
}

// BuildFallback builds the fallback action result for a rule.
func (e *Evaluator) BuildFallback(rule compiler.RuleHeader, dc DataContext) (MatchedRule, error) {
	return e.buildAction(rule, rule.FallbackIdx, true, dc)
}

func (e *Evaluator) buildAction(rule compiler.RuleHeader, actionIdx uint16, fallback bool, dc DataContext) (MatchedRule, error) {
	mr := MatchedRule{
		Name:     e.String(rule.NameIdx),
		Priority: int(rule.Priority),
		Fallback: fallback,
	}
	if int(actionIdx) >= len(e.rs.Actions) {
		return mr, nil
	}

	action := e.rs.Actions[actionIdx]
	mr.Action = e.String(action.NameIdx)
	params, err := e.vm.evalActionParams(e.rs.Instructions, action.Params, dc)
	if err != nil {
		return mr, err
	}
	mr.Params = params
	return mr, nil
}
