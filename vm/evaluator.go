package vm

import (
	"fmt"

	"m31labs.dev/arbiter/bytecode"
)

// Evaluator reuses a VM across multiple rule evaluations.
type Evaluator struct {
	rs *bytecode.CompiledRuleset
	vm *VM
}

// PreparedEvaluator evaluates bytecode for one immutable ruleset and tag
// selection. It applies active windows and skips other governance gates.
// It reuses VM scratch state and is not safe for concurrent use.
//
// A prepared evaluator that is used more than once keeps a fact cache: during
// one Eval or EvalInto call it reads a fact from the data context the first
// time a rule needs it and reuses that Value for every later rule and action
// parameter in the same call. The cache is emptied when the call returns,
// also when it returns an error, so the next call reads every fact again.
// Names that a let binding, a quantifier or aggregate variable, or a table
// column can bind are never cached. The cache is built when the evaluator is
// used a second time, so an evaluator built for one evaluation does not pay
// for it; that first evaluation reads a fact each time a rule tests it.
//
// The data context must therefore behave as a snapshot for the length of one
// call. See DataContext for the two rules a context has to meet.
type PreparedEvaluator struct {
	rs         *bytecode.CompiledRuleset
	rules      []bytecode.RuleHeader
	index      []int // index of each prepared rule in rs.Rules
	hasWindows bool
	evaluated  bool // one evaluation has started; the next one builds the fact cache
	vm         *VM
}

// factCacheStart is the number of facts the fact cache has room for when it
// is built.
const factCacheStart = 16

// useFactCache runs at the start of every evaluation. It builds the fact
// cache at the start of the second one: an evaluator built for a single
// evaluation never allocates it.
func (e *PreparedEvaluator) useFactCache() {
	if e.vm.varSlot != nil {
		return
	}
	if !e.evaluated {
		e.evaluated = true
		return
	}
	e.vm.varSlot = make([]uint16, len(e.vm.localMask))
	// Room for the facts of a typical evaluation; append grows it if needed.
	e.vm.varVals = make([]Value, 0, factCacheStart)
	e.vm.varLoaded = make([]uint16, 0, factCacheStart)
}

// NewPreparedEvaluator selects matching rules once. The caller must not mutate
// the ruleset or string pool while the evaluator is in use.
//
// The evaluator may read each fact once per Eval or EvalInto call and reuse
// it, so the data contexts passed to it must meet the contract documented on
// DataContext: equal values for the same key during one call, and returned
// Values that do not change until the call returns.
func NewPreparedEvaluator(rs *bytecode.CompiledRuleset, sp *StringPool, tags []string) (*PreparedEvaluator, error) {
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}
	rules := make([]bytecode.RuleHeader, 0, len(rs.Rules))
	index := make([]int, 0, len(rs.Rules))
	for i, rule := range rs.Rules {
		if rs.RuleMatchesTags(rule, tags) {
			rules = append(rules, rule)
			index = append(index, i)
		}
	}
	return newPrepared(rs, sp, rules, index), nil
}

// Eval evaluates the prepared rule selection. Active windows are checked
// against the current data context on every call.
func (e *PreparedEvaluator) Eval(dc DataContext) ([]MatchedRule, error) {
	if e == nil || e.rs == nil || e.vm == nil {
		return nil, fmt.Errorf("nil prepared evaluator")
	}
	// PreparedEvaluator reuses its VM across repeated calls, so each call is
	// its own logical request and needs its own instruction budget.
	e.vm.resetBudget()
	e.vm.resetElseRows()
	e.useFactCache()
	// Deferred, so the facts are forgotten when the evaluation fails too.
	defer e.vm.resetVarCache()
	return evalRuleSelection(e.rs, dc, e.vm, e.rules, nil)
}

// NewEvaluator creates a reusable evaluator for a compiled ruleset.
func NewEvaluator(rs *bytecode.CompiledRuleset, sp *StringPool) *Evaluator {
	return &Evaluator{
		rs: rs,
		vm: newVM(rs, sp),
	}
}

// ResetBudget starts a new per-request instruction budget. A host that reuses
// one Evaluator across requests (an expert session does so across Run calls)
// must call it once per request, or the budget accumulates for the life of the
// evaluator and evaluation eventually fails with "instruction limit exceeded".
func (e *Evaluator) ResetBudget() {
	if e != nil && e.vm != nil {
		e.vm.resetBudget()
	}
}

// String resolves a compile-time string constant through the evaluator's pool.
func (e *Evaluator) String(idx uint16) string {
	return e.vm.strPool.Get(idx)
}

// EvalRuleCondition evaluates one rule's condition.
func (e *Evaluator) EvalRuleCondition(rule bytecode.RuleHeader, dc DataContext) (bool, error) {
	e.vm.sp = 0
	e.vm.clearLocals()
	e.vm.iters = e.vm.iters[:0]
	e.vm.err = nil
	e.vm.resetElseRows()

	ok := e.vm.evalCondition(e.rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)
	if e.vm.err != nil {
		return false, e.vm.err
	}
	return ok, nil
}

// HasFallback reports whether a rule has a valid fallback action.
func (e *Evaluator) HasFallback(rule bytecode.RuleHeader) bool {
	return rule.FallbackIdx != 0 && int(rule.FallbackIdx) < len(e.rs.Actions)
}

// BuildMatch builds the normal action result for a matched rule.
func (e *Evaluator) BuildMatch(rule bytecode.RuleHeader, dc DataContext) (MatchedRule, error) {
	return e.buildAction(rule, rule.ActionIdx, false, dc)
}

// BuildFallback builds the fallback action result for a rule.
func (e *Evaluator) BuildFallback(rule bytecode.RuleHeader, dc DataContext) (MatchedRule, error) {
	return e.buildAction(rule, rule.FallbackIdx, true, dc)
}

func (e *Evaluator) buildAction(rule bytecode.RuleHeader, actionIdx uint16, fallback bool, dc DataContext) (MatchedRule, error) {
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
