package vm

import (
	"fmt"

	"m31labs.dev/arbiter/bytecode"
)

// Param is one evaluated action parameter.
type Param struct {
	Key   string
	Value Value
}

// Match is one rule that PreparedEvaluator.EvalInto matched.
type Match struct {
	// Rule is the rule's index in CompiledRuleset.Rules.
	Rule     int
	Name     string
	Priority int
	Action   string
	// Fallback is true when the rule's condition did not hold and its
	// otherwise action was used.
	Fallback bool
	// Params holds the action parameters in declaration order. It points into
	// the Matches buffer and is valid until the next EvalInto with that buffer.
	Params []Param
}

// Param returns the parameter named key.
func (m *Match) Param(key string) (Value, bool) {
	for i := range m.Params {
		if m.Params[i].Key == key {
			return m.Params[i].Value, true
		}
	}
	return Value{}, false
}

// Matches is a reusable result buffer for PreparedEvaluator.EvalInto. Reusing
// one buffer across calls lets evaluation run without allocating once the
// buffer has grown to fit.
type Matches struct {
	// List holds the matched rules in declaration order.
	List []Match
	// Trace, when non-nil, receives a record of the evaluation. Leave it nil
	// in normal play: the VM then records nothing.
	Trace *Trace

	params []Param
	offs   []int
}

// Reset empties the buffer and keeps its capacity. Entries past the new
// length are overwritten by later evaluations, not cleared.
func (m *Matches) Reset() {
	m.List = m.List[:0]
	m.params = m.params[:0]
	m.offs = m.offs[:0]
	if m.Trace != nil {
		m.Trace.Reset()
	}
}

// MaxTraceChecks bounds Trace.Checks so a quantifier over a long list cannot
// grow a trace without limit. Later checks set Trace.Truncated.
const MaxTraceChecks = 4096

// Trace records which rules an evaluation tried, whether each one held, and
// the comparisons it executed, for debug overlays. Its slices are reused by
// Reset, so a trace kept for one agent does not allocate in steady state.
type Trace struct {
	Rules     []RuleTrace
	Checks    []Check
	Truncated bool
}

// RuleTrace is one rule an evaluation tried.
type RuleTrace struct {
	// Rule is the rule's index in CompiledRuleset.Rules.
	Rule int
	Name string
	// Held is true when the rule's condition evaluated to true.
	Held bool
	// Fallback is true when the condition failed and the otherwise action ran.
	Fallback bool
	// Checks are the indices [CheckStart, CheckEnd) in Trace.Checks of the
	// comparisons this rule's condition executed.
	CheckStart, CheckEnd int
}

// Check is one comparison or predicate the VM executed: a comparison, a
// membership or string test, a range test, or a null test.
type Check struct {
	// IP is the byte offset of the instruction in CompiledRuleset.Instructions.
	IP uint32
	Op bytecode.OpCode
	// A and B are the operands; C is the upper bound of a range test. Unused
	// operands are null.
	A, B, C Value
	Result  bool
}

// Reset empties the trace and keeps its capacity.
func (t *Trace) Reset() {
	clear(t.Rules)
	t.Rules = t.Rules[:0]
	clear(t.Checks)
	t.Checks = t.Checks[:0]
	t.Truncated = false
}

func (vm *VM) recordCheck(op bytecode.OpCode, a, b, c Value, result bool) {
	t := vm.trace
	if len(t.Checks) >= MaxTraceChecks {
		t.Truncated = true
		return
	}
	t.Checks = append(t.Checks, Check{IP: vm.ip, Op: op, A: a, B: b, C: c, Result: result})
}

// NewPreparedEvaluatorRules prepares an explicit rule selection: the rules at
// the given indices of rs.Rules, evaluated in the order given. It ignores tags.
// The data contexts passed to the evaluator must meet the contract documented
// on DataContext, as for NewPreparedEvaluator.
func NewPreparedEvaluatorRules(rs *bytecode.CompiledRuleset, sp *StringPool, indices []int) (*PreparedEvaluator, error) {
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}
	rules := make([]bytecode.RuleHeader, 0, len(indices))
	index := make([]int, 0, len(indices))
	for _, i := range indices {
		if i < 0 || i >= len(rs.Rules) {
			return nil, fmt.Errorf("rule index %d out of range", i)
		}
		rules = append(rules, rs.Rules[i])
		index = append(index, i)
	}
	return newPrepared(rs, sp, rules, index), nil
}

func newPrepared(rs *bytecode.CompiledRuleset, sp *StringPool, rules []bytecode.RuleHeader, index []int) *PreparedEvaluator {
	e := &PreparedEvaluator{rs: rs, rules: rules, index: index, vm: newVM(rs, sp)}
	e.vm.localMask = localNameMask(rs, e.vm.strs)
	for _, rule := range rules {
		if rule.HasActiveFrom || rule.HasActiveUntil {
			e.hasWindows = true
			break
		}
	}
	return e
}

// Rules returns the indices in CompiledRuleset.Rules of the prepared rules, in
// evaluation order. The caller must not modify the slice.
func (e *PreparedEvaluator) Rules() []int { return e.index }

// EvalInto evaluates the prepared rules and writes the matched rules, in
// declaration order, into out, replacing its previous contents. It applies
// active windows like Eval and skips other governance gates.
//
// Unlike Eval, EvalInto returns parameter values as VM values in declaration
// order instead of maps, and reads the clock only when a prepared rule has an
// active window. With a reused out and a context that does not allocate (see
// IndexedDataContext), it does not allocate for rules that use numbers,
// booleans, pooled strings, let bindings and table lookups. String
// concatenation, decimals, and conversion of typed Go slices allocate. The
// second call on an evaluator builds its fact cache, which allocates once.
//
// Values in out stay valid until the next EvalInto on this evaluator.
func (e *PreparedEvaluator) EvalInto(dc DataContext, out *Matches) error {
	if e == nil || e.rs == nil || e.vm == nil {
		return fmt.Errorf("nil prepared evaluator")
	}
	if out == nil {
		return fmt.Errorf("nil matches buffer")
	}
	out.Reset()
	vm := e.vm
	vm.resetBudget()
	vm.resetElseRows()
	e.useFactCache()
	vm.trace = out.Trace
	defer func() {
		vm.resetTransient()
		vm.resetVarCache()
		vm.trace = nil
	}()

	checkWindows := e.hasWindows
	var evalNanos int64
	if checkWindows {
		evalNanos = resolveEvalTime(dc).UnixNano()
	}
	rs := e.rs
	for i, rule := range e.rules {
		if checkWindows && !withinWindow(rule, evalNanos) {
			continue
		}
		vm.resetTransient()
		vm.trace = out.Trace
		checkStart := 0
		if out.Trace != nil {
			checkStart = len(out.Trace.Checks)
		}

		held := vm.evalCondition(rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)
		if vm.err != nil {
			return fmt.Errorf("rule %s: %w", vm.strPool.Get(rule.NameIdx), vm.err)
		}

		actionIdx, fallback := rule.ActionIdx, false
		if !held {
			if rule.FallbackIdx == 0 || int(rule.FallbackIdx) >= len(rs.Actions) {
				e.traceRule(out.Trace, i, rule, false, false, checkStart)
				continue
			}
			actionIdx, fallback = rule.FallbackIdx, true
		}
		e.traceRule(out.Trace, i, rule, held, fallback, checkStart)

		m := Match{
			Rule:     e.ruleIndex(i),
			Name:     vm.strPool.Get(rule.NameIdx),
			Priority: int(rule.Priority),
			Fallback: fallback,
		}
		out.offs = append(out.offs, len(out.params))
		if int(actionIdx) < len(rs.Actions) {
			action := &rs.Actions[actionIdx]
			m.Action = vm.strPool.Get(action.NameIdx)
			if err := vm.appendActionParams(rs.Instructions, action.Params, dc, out); err != nil {
				kind := "action"
				if fallback {
					kind = "fallback"
				}
				return fmt.Errorf("rule %s %s %s: %w", m.Name, kind, m.Action, err)
			}
		}
		out.List = append(out.List, m)
	}

	// Point each match at its parameters now that the buffer will not move.
	for i := range out.List {
		start := out.offs[i]
		end := len(out.params)
		if i+1 < len(out.offs) {
			end = out.offs[i+1]
		}
		out.List[i].Params = out.params[start:end:end]
	}
	return nil
}

func (e *PreparedEvaluator) ruleIndex(i int) int {
	if e.index != nil {
		return e.index[i]
	}
	return i
}

func (e *PreparedEvaluator) traceRule(t *Trace, i int, rule bytecode.RuleHeader, held, fallback bool, checkStart int) {
	if t == nil {
		return
	}
	t.Rules = append(t.Rules, RuleTrace{
		Rule:       e.ruleIndex(i),
		Name:       e.vm.strPool.Get(rule.NameIdx),
		Held:       held,
		Fallback:   fallback,
		CheckStart: checkStart,
		CheckEnd:   len(t.Checks),
	})
}

// appendActionParams evaluates action parameters into a Matches buffer.
func (vm *VM) appendActionParams(instrs []byte, params []bytecode.ActionParam, dc DataContext, out *Matches) error {
	if len(params) == 0 {
		return nil
	}
	return vm.eachActionParam(instrs, params, dc, func(key string, v Value) {
		out.params = append(out.params, Param{Key: key, Value: v})
	})
}

func withinWindow(rule bytecode.RuleHeader, nowUnixNano int64) bool {
	if rule.HasActiveFrom && nowUnixNano < rule.ActiveFromUnixNano {
		return false
	}
	if rule.HasActiveUntil && nowUnixNano >= rule.ActiveUntilUnixNano {
		return false
	}
	return true
}

// String resolves a string value: a pooled constant through the evaluator's
// string pool, or a runtime string. It returns "" for other types.
func (e *PreparedEvaluator) String(v Value) string {
	if v.Typ != TypeString {
		return ""
	}
	if s, ok := v.Any.(string); ok {
		return s
	}
	return e.vm.strPool.Get(v.Str)
}

// GoValue converts a value to its Go form, as Eval does for parameters: nil,
// bool, float64, string, []any, map[string]any or a decimal. Lists and table
// rows are copied, so this allocates for them.
func (e *PreparedEvaluator) GoValue(v Value) any {
	return e.vm.valueToAny(v)
}
