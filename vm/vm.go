// vm/vm.go
package vm

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/arbiter/bytecode"
	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/intern"
)

const (
	// maxStack must match bytecode.MaxValueStackDepth: the compiler's static
	// stack-depth check (compiler/stackdepth.go) rejects any expression that
	// would need to push more values than this at once, so that pathological
	// nesting fails at compile time with a clear diagnostic instead of here,
	// unpredictably, depending on runtime data.
	maxStack               = bytecode.MaxValueStackDepth
	maxInstructionsPerEval = 1 << 20
	maxRegexCacheEntries   = 256
	maxBadRegexEntries     = 256
)

// MatchedRule is the result of evaluating a rule that matched.
type MatchedRule struct {
	Name     string
	Priority int
	Action   string
	Params   map[string]any
	Fallback bool
}

// FailedRule records a rule that did not match (debug mode only).
type FailedRule struct {
	Name          string
	FailedAtInstr uint32
}

// DebugResult contains the full evaluation trace.
type DebugResult struct {
	Matched []MatchedRule
	Failed  []FailedRule
	Elapsed time.Duration
	Error   error
}

type iterState struct {
	kind     uint8
	varName  string
	items    []any
	index    int
	result   bool
	aggSum   float64
	aggCount int
	prev     Value
	hadPrev  bool
}

// VM is the bytecode evaluator.
type VM struct {
	stack     [maxStack]Value
	sp        int
	stackHigh int
	pool      *intern.Pool
	strPool   *StringPool
	rs        *bytecode.CompiledRuleset
	locals    []local // let, iterator and table-column bindings; names are unique
	frames    [][]local
	rowRefs   [][]tableRow
	elseRows  []*elseRow
	elseUsed  int
	idc       IndexedDataContext
	trace     *Trace
	strs      []string // string pool snapshot for constant indices
	localMask []bool   // per string index: can the name be a local? nil = unknown
	iters     []iterState
	regexes   map[string]*regexp.Regexp
	regexLRU  []string
	badRe     map[string]struct{}
	badReLRU  []string
	err       error
	ip        uint32
	profile   NumericProfile

	// budgetSteps accumulates instructions executed across every
	// evalCondition call made on this VM instance since the last
	// resetBudget. It is deliberately NOT cleared by resetTransient: the
	// instruction budget is per request (every rule condition evaluated
	// while answering one Eval/EvalDebug/PreparedEvaluator.Eval call), not
	// per individual condition — otherwise a ruleset with many rules, each
	// just under the cap, has no aggregate limit at all.
	budgetSteps int
}

// resetBudget starts a new per-request instruction budget. Call it once at
// the start of each logical request on a VM that may be reused across
// requests (for example PreparedEvaluator, which explicitly reuses its VM
// across repeated Eval calls). VMs created fresh per request via newVM
// already start with a zero budget and do not need this.
func (vm *VM) resetBudget() {
	vm.budgetSteps = 0
}

func newVM(rs *bytecode.CompiledRuleset, sp *StringPool) *VM {
	return &VM{
		pool:    rs.Constants,
		strPool: sp,
		rs:      rs,
		strs:    sp.snapshot(),
	}
}

// localNameMask marks, per string-pool index, the names that a let binding,
// a quantifier or aggregate variable, or a table column can bind, together
// with dotted names below them. Other names always come from the data
// context, so the VM can skip the local lookup for them.
func localNameMask(rs *bytecode.CompiledRuleset, strs []string) []bool {
	names := map[string]bool{}
	instrs := rs.Instructions
	for ip := 0; ip+bytecode.InstrSize <= len(instrs); ip += bytecode.InstrSize {
		var buf [bytecode.InstrSize]byte
		copy(buf[:], instrs[ip:ip+bytecode.InstrSize])
		op, _, arg := bytecode.DecodeInstr(buf)
		switch op {
		case bytecode.OpSetLocal, bytecode.OpIterBegin, bytecode.OpAggBegin:
			if int(arg) < len(strs) {
				names[strs[arg]] = true
			}
		}
	}
	for _, table := range rs.Tables {
		for _, col := range table.Columns {
			names[col] = true
		}
	}
	mask := make([]bool, len(strs))
	for i, s := range strs {
		base, _, _ := strings.Cut(s, ".")
		mask[i] = names[s] || names[base]
	}
	return mask
}

// push copies v onto the value stack one field at a time. A Value arrives in
// registers and is spilled with one narrow store per field; copying it as a
// block would then load 16 bytes that span those stores, which the CPU cannot
// forward from the store buffer (a stall of about ten cycles per push).
// Field-sized loads forward. The same reason is why the fast path below reads
// and writes stack slots through pointers instead of copying whole Values.
func (vm *VM) push(v Value) {
	if vm.sp >= maxStack {
		vm.stackOverflow()
		return
	}
	s := &vm.stack[vm.sp]
	s.Typ, s.Bool, s.Str, s.ListIdx, s.ListLen, s.Num, s.Any = v.Typ, v.Bool, v.Str, v.ListIdx, v.ListLen, v.Num, v.Any
	vm.sp++
	if vm.sp > vm.stackHigh {
		vm.stackHigh = vm.sp
	}
}

// pushFrom is push for a Value the caller holds in memory.
func (vm *VM) pushFrom(v *Value) {
	if vm.sp >= maxStack {
		vm.stackOverflow()
		return
	}
	s := &vm.stack[vm.sp]
	s.Typ, s.Bool, s.Str, s.ListIdx, s.ListLen, s.Num, s.Any = v.Typ, v.Bool, v.Str, v.ListIdx, v.ListLen, v.Num, v.Any
	vm.sp++
	if vm.sp > vm.stackHigh {
		vm.stackHigh = vm.sp
	}
}

//go:noinline
func (vm *VM) stackOverflow() {
	if vm.err == nil {
		vm.err = fmt.Errorf("stack overflow at instruction %d", vm.ip)
	}
}

// pushNum, pushBool and pushStr push a constant without building a Value.
func (vm *VM) pushNum(n float64) {
	if vm.sp >= maxStack {
		vm.stackOverflow()
		return
	}
	s := &vm.stack[vm.sp]
	*s = Value{}
	s.Typ, s.Num = TypeNumber, n
	vm.sp++
	if vm.sp > vm.stackHigh {
		vm.stackHigh = vm.sp
	}
}

func (vm *VM) pushBool(b bool) {
	if vm.sp >= maxStack {
		vm.stackOverflow()
		return
	}
	s := &vm.stack[vm.sp]
	*s = Value{}
	s.Typ, s.Bool = TypeBool, b
	vm.sp++
	if vm.sp > vm.stackHigh {
		vm.stackHigh = vm.sp
	}
}

func (vm *VM) pushStr(idx uint16) {
	if vm.sp >= maxStack {
		vm.stackOverflow()
		return
	}
	s := &vm.stack[vm.sp]
	*s = Value{}
	s.Typ, s.Str = TypeString, idx
	vm.sp++
	if vm.sp > vm.stackHigh {
		vm.stackHigh = vm.sp
	}
}

// topBool is peek().AsBool() without copying the Value.
func (vm *VM) topBool() bool {
	if vm.sp <= 0 {
		return false
	}
	s := &vm.stack[vm.sp-1]
	return s.Typ == TypeBool && s.Bool
}

// setBool and setNum overwrite a stack slot in place.
func setBool(s *Value, b bool) {
	*s = Value{}
	s.Typ, s.Bool = TypeBool, b
}

func setNum(s *Value, n float64) {
	if n != n {
		n = math.NaN() // canonical NaN, as numResult
	}
	*s = Value{}
	s.Typ, s.Num = TypeNumber, n
}

// strAt is toStr for a string Value read through a pointer.
func (vm *VM) strAt(v *Value) string {
	if s, ok := v.Any.(string); ok {
		return s
	}
	if int(v.Str) < len(vm.strs) {
		return vm.strs[v.Str]
	}
	return vm.strPool.Get(v.Str)
}

func (vm *VM) pop() Value {
	if vm.sp <= 0 {
		return NullVal()
	}
	vm.sp--
	return vm.stack[vm.sp]
}

func (vm *VM) peek() Value {
	if vm.sp <= 0 {
		return NullVal()
	}
	return vm.stack[vm.sp-1]
}

// Eval evaluates all rules in the compiled ruleset against the data context.
func Eval(rs *bytecode.CompiledRuleset, dc DataContext) ([]MatchedRule, error) {
	return EvalWithTagFilter(rs, dc, NewStringPool(rs.Constants.Strings()), nil)
}

// EvalWithPool evaluates using a shared StringPool (for runtime-interned strings).
//
// Deprecated: The string pool is now managed internally by Program. Use the
// root-package Eval function with a *Program instead.
func EvalWithPool(rs *bytecode.CompiledRuleset, dc DataContext, sp *StringPool) ([]MatchedRule, error) {
	return EvalWithTagFilter(rs, dc, sp, nil)
}

// evalWithPool is the internal implementation of EvalWithPool.
func evalWithPool(rs *bytecode.CompiledRuleset, dc DataContext, sp *StringPool) ([]MatchedRule, error) {
	return EvalWithTagFilter(rs, dc, sp, nil)
}

// EvalWithTagFilter evaluates bytecode using the provided tag filter and active
// windows. It skips other governance gates. Use the root package's EvalGoverned
// for policy decisions that depend on those gates.
func EvalWithTagFilter(rs *bytecode.CompiledRuleset, dc DataContext, sp *StringPool, tags []string) ([]MatchedRule, error) {
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}

	return evalRuleSelection(rs, dc, newVM(rs, sp), rs.Rules, tags)
}

// EvalRules evaluates the given rules of rs in the order given. Like
// EvalWithTagFilter it applies active windows and skips other governance
// gates; it does no tag filtering, so a caller that selects by tag does that
// once and reuses the selection.
func EvalRules(rs *bytecode.CompiledRuleset, dc DataContext, sp *StringPool, rules []bytecode.RuleHeader) ([]MatchedRule, error) {
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}
	return evalRuleSelection(rs, dc, newVM(rs, sp), rules, nil)
}

func evalRuleSelection(rs *bytecode.CompiledRuleset, dc DataContext, evaluator *VM, rules []bytecode.RuleHeader, tags []string) ([]MatchedRule, error) {
	var matched []MatchedRule
	evalNanos := resolveEvalTime(dc).UTC().UnixNano()
	evaluator.resetElseRows()
	defer evaluator.resetTransient()

	for i := range rules {
		rule := &rules[i]
		if len(tags) != 0 && !rs.RuleMatchesTags(*rule, tags) {
			continue
		}
		if (rule.HasActiveFrom || rule.HasActiveUntil) && !withinWindow(*rule, evalNanos) {
			continue
		}
		evaluator.resetTransient()

		result := evaluator.evalCondition(rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)
		if evaluator.err != nil {
			return nil, fmt.Errorf("rule %s: %w", evaluator.strPool.Get(rule.NameIdx), evaluator.err)
		}

		if result {
			mr := MatchedRule{
				Name:     evaluator.strPool.Get(rule.NameIdx),
				Priority: int(rule.Priority),
			}
			if int(rule.ActionIdx) < len(rs.Actions) {
				action := rs.Actions[rule.ActionIdx]
				mr.Action = evaluator.strPool.Get(action.NameIdx)
				params, err := evaluator.evalActionParams(rs.Instructions, action.Params, dc)
				if err != nil {
					return nil, fmt.Errorf("rule %s action %s: %w", mr.Name, mr.Action, err)
				}
				mr.Params = params
			}
			matched = append(matched, mr)
		} else if rule.FallbackIdx != 0 && int(rule.FallbackIdx) < len(rs.Actions) {
			action := rs.Actions[rule.FallbackIdx]
			mr := MatchedRule{
				Name:     evaluator.strPool.Get(rule.NameIdx),
				Priority: int(rule.Priority),
				Action:   evaluator.strPool.Get(action.NameIdx),
				Fallback: true,
			}
			params, err := evaluator.evalActionParams(rs.Instructions, action.Params, dc)
			if err != nil {
				return nil, fmt.Errorf("rule %s fallback %s: %w", mr.Name, mr.Action, err)
			}
			mr.Params = params
			matched = append(matched, mr)
		}
	}

	return matched, nil
}

func (vm *VM) resetTransient() {
	if vm.stackHigh > 0 {
		clear(vm.stack[:vm.stackHigh])
	}
	vm.sp, vm.stackHigh = 0, 0
	vm.clearLocals()
	vm.clearFrames()
	vm.idc = nil
	clear(vm.iters[:cap(vm.iters)])
	vm.iters = vm.iters[:0]
	vm.err = nil
}

// EvalDebug evaluates with full tracing.
func EvalDebug(rs *bytecode.CompiledRuleset, dc DataContext) DebugResult {
	return EvalDebugWithTagFilter(rs, dc, NewStringPool(rs.Constants.Strings()), nil)
}

// EvalDebugWithPool evaluates with full tracing using a shared StringPool.
//
// Deprecated: The string pool is now managed internally by Program. Use the
// root-package EvalDebug function with a *Program instead.
func EvalDebugWithPool(rs *bytecode.CompiledRuleset, dc DataContext, sp *StringPool) DebugResult {
	return EvalDebugWithTagFilter(rs, dc, sp, nil)
}

// evalDebugWithPool is the internal implementation of EvalDebugWithPool.
func evalDebugWithPool(rs *bytecode.CompiledRuleset, dc DataContext, sp *StringPool) DebugResult {
	return EvalDebugWithTagFilter(rs, dc, sp, nil)
}

// EvalDebugWithTagFilter evaluates with full tracing using the provided tag filter.
func EvalDebugWithTagFilter(rs *bytecode.CompiledRuleset, dc DataContext, sp *StringPool, tags []string) DebugResult {
	start := time.Now()
	vm := newVM(rs, sp)
	var result DebugResult
	evalTime := resolveEvalTime(dc)

	for _, rule := range rs.Rules {
		if !rs.RuleMatchesTags(rule, tags) {
			continue
		}
		if !ruleWithinActiveWindow(rule, evalTime) {
			result.Failed = append(result.Failed, FailedRule{
				Name: vm.strPool.Get(rule.NameIdx),
			})
			continue
		}
		vm.sp = 0
		vm.clearLocals()
		vm.iters = vm.iters[:0]
		vm.err = nil

		ok := vm.evalCondition(rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)
		if vm.err != nil {
			result.Error = fmt.Errorf("rule %s: %w", vm.strPool.Get(rule.NameIdx), vm.err)
			result.Failed = append(result.Failed, FailedRule{
				Name: vm.strPool.Get(rule.NameIdx),
			})
			break
		}

		if ok {
			mr := MatchedRule{
				Name:     vm.strPool.Get(rule.NameIdx),
				Priority: int(rule.Priority),
			}
			if int(rule.ActionIdx) < len(rs.Actions) {
				action := rs.Actions[rule.ActionIdx]
				mr.Action = vm.strPool.Get(action.NameIdx)
				params, err := vm.evalActionParams(rs.Instructions, action.Params, dc)
				if err != nil {
					result.Error = fmt.Errorf("rule %s action %s: %w", mr.Name, mr.Action, err)
					result.Failed = append(result.Failed, FailedRule{Name: mr.Name})
					result.Elapsed = time.Since(start)
					return result
				}
				mr.Params = params
			}
			result.Matched = append(result.Matched, mr)
		} else if rule.FallbackIdx != 0 && int(rule.FallbackIdx) < len(rs.Actions) {
			action := rs.Actions[rule.FallbackIdx]
			mr := MatchedRule{
				Name:     vm.strPool.Get(rule.NameIdx),
				Priority: int(rule.Priority),
				Action:   vm.strPool.Get(action.NameIdx),
				Fallback: true,
			}
			params, err := vm.evalActionParams(rs.Instructions, action.Params, dc)
			if err != nil {
				result.Error = fmt.Errorf("rule %s fallback %s: %w", mr.Name, mr.Action, err)
				result.Failed = append(result.Failed, FailedRule{Name: mr.Name})
				result.Elapsed = time.Since(start)
				return result
			}
			mr.Params = params
			result.Matched = append(result.Matched, mr)
		} else {
			result.Failed = append(result.Failed, FailedRule{
				Name: vm.strPool.Get(rule.NameIdx),
			})
		}
	}

	result.Elapsed = time.Since(start)
	return result
}

func ruleWithinActiveWindow(rule bytecode.RuleHeader, evalTime time.Time) bool {
	nowUnixNano := evalTime.UTC().UnixNano()
	if rule.HasActiveFrom && nowUnixNano < rule.ActiveFromUnixNano {
		return false
	}
	if rule.HasActiveUntil && nowUnixNano >= rule.ActiveUntilUnixNano {
		return false
	}
	return true
}

func resolveEvalTime(dc DataContext) time.Time {
	if dc != nil {
		if now, ok := valueAsEvalTime(dc.Get("__now")); ok {
			return now
		}
	}
	return time.Now().UTC()
}

func valueAsEvalTime(v Value) (time.Time, bool) {
	switch v.Typ {
	case TypeNumber:
		// Reject values float64->int64 cannot convert the same way on every target
		// (the conversion is implementation-defined outside the int64 range).
		if math.IsNaN(v.Num) || math.IsInf(v.Num, 0) || math.Abs(v.Num) >= 1<<62 {
			return time.Time{}, false
		}
		seconds, frac := math.Modf(v.Num)
		return time.Unix(int64(seconds), int64(frac*float64(time.Second))).UTC(), true
	case TypeString:
		text, _ := v.Any.(string)
		text = strings.TrimSpace(text)
		if text == "" {
			return time.Time{}, false
		}
		if ts, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return ts.UTC(), true
		}
		if n, err := strconv.ParseFloat(text, 64); err == nil {
			return valueAsEvalTime(NumVal(n))
		}
	}
	return time.Time{}, false
}

func (vm *VM) evalCondition(instrs []byte, off, length uint32, dc DataContext) bool {
	// Empty condition = unconditional (segment-only rules); segment gating happens
	// before condition eval. All eval paths share this chokepoint.
	if length == 0 {
		return true
	}

	end := off + length
	ip := off
	vm.idc, _ = dc.(IndexedDataContext)

	for ip < end {
		vm.budgetSteps++
		if vm.budgetSteps > maxInstructionsPerEval {
			vm.err = fmt.Errorf("instruction limit exceeded after %d steps", maxInstructionsPerEval)
			return false
		}
		if ip+bytecode.InstrSize > uint32(len(instrs)) {
			break
		}
		vm.ip = ip

		op := bytecode.OpCode(instrs[ip])
		flags := instrs[ip+1]
		arg := uint16(instrs[ip+2]) | uint16(instrs[ip+3])<<8

		if vm.fastOp(op, flags, arg, dc) {
			if vm.err != nil {
				return false
			}
			ip += bytecode.InstrSize
			continue
		}
		switch op {
		case bytecode.OpJumpIfFalse:
			if !vm.topBool() {
				ip += uint32(arg)
			} else {
				ip += bytecode.InstrSize
			}
			continue
		case bytecode.OpJumpIfTrue:
			if vm.topBool() {
				ip += uint32(arg)
			} else {
				ip += bytecode.InstrSize
			}
			continue
		}

		if nextIP, handled, matched, done := vm.dispatchConditionOp(instrs, end, ip, op, flags, arg, dc); handled {
			if vm.err != nil {
				return false
			}
			if done {
				return matched
			}
			ip = nextIP
			continue
		}
		if vm.err != nil {
			return false
		}

		ip += bytecode.InstrSize
	}

	// If we ran off the end without RuleMatch, check stack
	return vm.topBool()
}

// fastOp runs the commonest opcodes without the general dispatch: constant
// and variable loads, boolean logic, and comparisons and +, -, * of two
// numbers. It reports false for anything else, including any operand mix
// the general path treats specially, and when tracing (so comparisons are
// recorded) or under the integer profile (so arithmetic is checked).
func (vm *VM) fastOp(op bytecode.OpCode, flags uint8, arg uint16, dc DataContext) bool {
	switch op {
	case bytecode.OpLoadVar:
		vm.loadVar(arg, dc)
		return true
	case bytecode.OpLoadNum:
		vm.pushNum(vm.pool.GetNumber(arg))
		return true
	case bytecode.OpLoadBool:
		vm.pushBool(arg == 1)
		return true
	case bytecode.OpLoadStr:
		if flags != 0 {
			return false
		}
		vm.pushStr(arg)
		return true
	case bytecode.OpAnd, bytecode.OpOr:
		if vm.sp < 2 {
			return false
		}
		pb, pa := &vm.stack[vm.sp-1], &vm.stack[vm.sp-2]
		b, a := pb.Typ == TypeBool && pb.Bool, pa.Typ == TypeBool && pa.Bool
		vm.sp--
		if op == bytecode.OpAnd {
			setBool(pa, a && b)
		} else {
			setBool(pa, a || b)
		}
		return true
	case bytecode.OpNot:
		if vm.sp < 1 {
			return false
		}
		p := &vm.stack[vm.sp-1]
		setBool(p, !(p.Typ == TypeBool && p.Bool))
		return true
	case bytecode.OpEq, bytecode.OpNeq, bytecode.OpGt, bytecode.OpGte, bytecode.OpLt, bytecode.OpLte:
		if vm.sp < 2 || vm.trace != nil {
			return false
		}
		a, b := &vm.stack[vm.sp-2], &vm.stack[vm.sp-1]
		if a.Typ != TypeNumber || b.Typ != TypeNumber {
			if op != bytecode.OpEq && op != bytecode.OpNeq {
				return false
			}
			// Equality of the other scalar types, as valEqual: values of
			// different types are never equal, null equals null, and strings
			// compare by pool index or by content.
			var eq bool
			switch {
			case a.Typ != b.Typ:
				if a.Typ > TypeString || b.Typ > TypeString {
					return false
				}
			case a.Typ == TypeNull:
				eq = true
			case a.Typ == TypeBool:
				eq = a.Bool == b.Bool
			case a.Typ == TypeString:
				eq = (a.Any == nil && b.Any == nil && a.Str == b.Str) || vm.strAt(a) == vm.strAt(b)
			default:
				return false
			}
			*b = Value{}
			vm.sp--
			setBool(a, eq == (op == bytecode.OpEq))
			return true
		}
		cmp := 0
		switch {
		case a.Num < b.Num:
			cmp = -1
		case a.Num > b.Num:
			cmp = 1
		}
		var result bool
		switch op {
		case bytecode.OpEq:
			result = a.Num == b.Num
		case bytecode.OpNeq:
			result = a.Num != b.Num
		case bytecode.OpGt:
			result = cmp > 0
		case bytecode.OpGte:
			result = cmp >= 0
		case bytecode.OpLt:
			result = cmp < 0
		default:
			result = cmp <= 0
		}
		*b = Value{}
		vm.sp--
		setBool(a, result)
		return true
	case bytecode.OpAdd, bytecode.OpSub, bytecode.OpMul:
		if vm.sp < 2 || vm.profile != NumericDefault {
			return false
		}
		a, b := &vm.stack[vm.sp-2], &vm.stack[vm.sp-1]
		if a.Typ != TypeNumber || b.Typ != TypeNumber {
			return false
		}
		var r float64
		switch op {
		case bytecode.OpAdd:
			r = a.Num + b.Num
		case bytecode.OpSub:
			r = a.Num - b.Num
		default:
			// float64(...) rounds the product and keeps it from fusing with
			// a later add on arm64; see the general OpMul.
			r = float64(a.Num * b.Num)
		}
		*b = Value{}
		vm.sp--
		setNum(a, r)
		return true
	}
	return false
}

func (vm *VM) evalActionParams(instrs []byte, params []bytecode.ActionParam, dc DataContext) (map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	result := make(map[string]any, len(params))
	err := vm.eachActionParam(instrs, params, dc, func(key string, v Value) {
		result[key] = vm.valueToAny(v)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// eachActionParam evaluates action parameters in declaration order and calls
// emit for each one that produced a value. Every parameter starts from the
// locals that were bound when the action began, plus the action's own let
// bindings: the compiler prepends those to the first parameter's bytecode, so
// the locals after the first parameter become the base for the rest. When it
// returns, the locals are those the action began with. It saves locals only
// when there are some, so an action without bindings does not allocate.
func (vm *VM) eachActionParam(instrs []byte, params []bytecode.ActionParam, dc DataContext, emit func(key string, v Value)) error {
	begin, lets := -1, -1
	if len(vm.locals) > 0 {
		begin = vm.pushFrame()
	}
	var err error
	for i, p := range params {
		vm.sp = 0
		vm.iters = vm.iters[:0]
		vm.err = nil
		if i > 0 {
			switch {
			case lets >= 0:
				vm.restoreFrame(lets)
			case begin >= 0:
				vm.restoreFrame(begin)
			default:
				vm.clearLocals()
			}
		}
		vm.evalCondition(instrs, p.ValueOff, p.ValueLen, dc)
		if vm.err != nil {
			err = vm.err
			break
		}
		if vm.sp > 0 {
			emit(vm.strPool.Get(p.KeyIdx), vm.pop())
		}
		if i == 0 && len(params) > 1 && len(vm.locals) > 0 {
			lets = vm.pushFrame()
		}
	}
	switch {
	case begin >= 0:
		vm.popFrame(begin)
	case lets >= 0:
		vm.popFrame(lets)
		vm.clearLocals()
	default:
		vm.clearLocals()
	}
	return err
}

func (vm *VM) valueToAny(v Value) any {
	switch v.Typ {
	case TypeNull:
		return nil
	case TypeBool:
		return v.Bool
	case TypeNumber:
		return v.Num
	case TypeString:
		if s, ok := v.Any.(string); ok {
			return s
		}
		return vm.strPool.Get(v.Str)
	case TypeList:
		if items, ok := v.Any.([]any); ok {
			return items
		}
		return vm.poolListToAny(v.ListIdx, v.ListLen)
	case TypeObject:
		switch obj := v.Any.(type) {
		case *tableRow:
			return obj.toMap(vm.strPool)
		case *elseRow:
			return obj.toMap(vm)
		}
		return v.Any
	case TypeDecimal:
		if value, ok := v.Any.(dec.Value); ok {
			return value
		}
		return nil
	default:
		return nil
	}
}

func (vm *VM) setErr(err error) {
	if err != nil && vm.err == nil {
		vm.err = err
	}
}

func (vm *VM) valEqual(a, b Value) bool {
	if a.Typ != b.Typ {
		return false
	}
	switch a.Typ {
	case TypeNull:
		return true
	case TypeBool:
		return a.Bool == b.Bool
	case TypeNumber:
		return a.Num == b.Num
	case TypeString:
		// Compare by pool index first (fast), fall back to string compare
		if a.Any == nil && b.Any == nil && a.Str == b.Str {
			return true
		}
		return vm.toStr(a) == vm.toStr(b)
	case TypeDecimal:
		left, lok := decimalFromValue(a)
		right, rok := decimalFromValue(b)
		return lok && rok && left.Equal(right)
	default:
		return false
	}
}

func (vm *VM) toNum(v Value) float64 {
	if v.Typ == TypeNumber {
		return v.Num
	}
	return 0
}

func (vm *VM) toStr(v Value) string {
	if v.Typ == TypeString {
		if s, ok := v.Any.(string); ok {
			return s
		}
		return vm.strPool.Get(v.Str)
	}
	return ""
}

func (vm *VM) listContainsVal(list, val Value) bool {
	if list.Typ != TypeList {
		return false
	}

	if items, ok := list.Any.([]any); ok {
		for _, item := range items {
			if vm.valEqual(anyToValue(item, vm.strPool), val) {
				return true
			}
		}
		return false
	}

	for _, item := range vm.pool.GetList(list.ListIdx, list.ListLen) {
		if vm.valEqual(vm.poolValueToValue(item), val) {
			return true
		}
	}
	return false
}

func (vm *VM) decodeListPair(instrs []byte, ip, end uint32, listIdx uint16) uint32 {
	nextIP := ip + bytecode.InstrSize
	if nextIP+bytecode.InstrSize > uint32(len(instrs)) || nextIP >= end {
		vm.err = fmt.Errorf("malformed list encoding at instruction %d", ip)
		vm.push(NullVal())
		return ip
	}
	var buf [bytecode.InstrSize]byte
	copy(buf[:], instrs[nextIP:nextIP+bytecode.InstrSize])
	op, flags, listLen := bytecode.DecodeInstr(buf)
	if op != bytecode.OpLoadNull || flags != 0xFF {
		vm.err = fmt.Errorf("malformed list encoding at instruction %d", ip)
		vm.push(NullVal())
		return ip
	}
	vm.push(ListVal(listIdx, listLen))
	return nextIP
}

func (vm *VM) regex(pattern string) *regexp.Regexp {
	if re, ok := vm.regexes[pattern]; ok {
		return re
	}
	if _, ok := vm.badRe[pattern]; ok {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		if vm.badRe == nil {
			vm.badRe = make(map[string]struct{})
		}
		vm.trimBadRegexCache(pattern)
		vm.badRe[pattern] = struct{}{}
		return nil
	}
	if vm.regexes == nil {
		vm.regexes = make(map[string]*regexp.Regexp)
	}
	vm.trimRegexCache(pattern)
	vm.regexes[pattern] = re
	return re
}

func (vm *VM) trimRegexCache(next string) {
	if vm.regexes == nil {
		return
	}
	if _, ok := vm.regexes[next]; ok {
		return
	}
	vm.regexLRU = append(vm.regexLRU, next)
	if len(vm.regexLRU) <= maxRegexCacheEntries {
		return
	}
	evict := vm.regexLRU[0]
	vm.regexLRU = vm.regexLRU[1:]
	delete(vm.regexes, evict)
}

func (vm *VM) trimBadRegexCache(next string) {
	if vm.badRe == nil {
		return
	}
	if _, ok := vm.badRe[next]; ok {
		return
	}
	vm.badReLRU = append(vm.badReLRU, next)
	if len(vm.badReLRU) <= maxBadRegexEntries {
		return
	}
	evict := vm.badReLRU[0]
	vm.badReLRU = vm.badReLRU[1:]
	delete(vm.badRe, evict)
}

func (vm *VM) listRetains(a, b Value) bool {
	for _, item := range vm.listValues(a) {
		if vm.listContainsVal(b, item) {
			return true
		}
	}
	return false
}

func (vm *VM) listSubsetOf(a, b Value) bool {
	for _, item := range vm.listValues(a) {
		if !vm.listContainsVal(b, item) {
			return false
		}
	}
	return a.Typ == TypeList && b.Typ == TypeList
}

func (vm *VM) listVagueContains(list, needle Value) bool {
	needleStr := vm.toStr(needle)
	if needleStr == "" {
		return false
	}
	for _, item := range vm.listValues(list) {
		if item.Typ == TypeString && strings.Contains(vm.toStr(item), needleStr) {
			return true
		}
	}
	return false
}

func (vm *VM) listValues(list Value) []Value {
	if list.Typ != TypeList {
		return nil
	}
	if items, ok := list.Any.([]any); ok {
		values := make([]Value, 0, len(items))
		for _, item := range items {
			values = append(values, anyToValue(item, vm.strPool))
		}
		return values
	}
	items := vm.pool.GetList(list.ListIdx, list.ListLen)
	values := make([]Value, 0, len(items))
	for _, item := range items {
		values = append(values, vm.poolValueToValue(item))
	}
	return values
}

func (vm *VM) listEntries(list Value) []any {
	if list.Typ != TypeList {
		return nil
	}
	if items, ok := list.Any.([]any); ok {
		return items
	}
	return vm.poolListToAny(list.ListIdx, list.ListLen)
}

func (vm *VM) poolListToAny(idx, length uint16) []any {
	items := vm.pool.GetList(idx, length)
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, vm.poolValueToAny(item))
	}
	return out
}

func (vm *VM) poolValueToAny(item intern.PoolValue) any {
	switch item.Typ {
	case intern.TypeNull:
		return nil
	case intern.TypeBool:
		return item.Bool
	case intern.TypeNumber:
		return item.Num
	case intern.TypeString:
		return vm.strPool.Get(item.Str)
	case intern.TypeList:
		return vm.poolListToAny(item.ListIdx, item.ListLen)
	case intern.TypeDecimal:
		return vm.pool.GetDecimal(item.Dec)
	default:
		return nil
	}
}

func (vm *VM) poolValueToValue(item intern.PoolValue) Value {
	switch item.Typ {
	case intern.TypeNull:
		return NullVal()
	case intern.TypeBool:
		return BoolVal(item.Bool)
	case intern.TypeNumber:
		return NumVal(item.Num)
	case intern.TypeString:
		return StrVal(item.Str)
	case intern.TypeList:
		return ListVal(item.ListIdx, item.ListLen)
	case intern.TypeDecimal:
		return DecimalVal(vm.pool.GetDecimal(item.Dec))
	default:
		return NullVal()
	}
}

func (vm *VM) lookupLocal(key string) (Value, bool) {
	if len(vm.locals) == 0 {
		return Value{}, false
	}
	if i := vm.findLocal(key); i >= 0 {
		return vm.locals[i].v, true
	}
	dot := strings.IndexByte(key, '.')
	if dot <= 0 {
		return Value{}, false
	}
	i := vm.findLocal(key[:dot])
	if i < 0 {
		return Value{}, false
	}
	return vm.resolveValue(vm.locals[i].v, key[dot+1:]), true
}

// resolveValue walks a dotted path below a local value.
func (vm *VM) resolveValue(base Value, path string) Value {
	if base.Typ != TypeObject {
		return NullVal()
	}
	switch obj := base.Any.(type) {
	case *tableRow:
		part, rest, nested := strings.Cut(path, ".")
		field := obj.field(part, vm.strPool)
		if !nested {
			return field
		}
		return vm.resolveValue(field, rest)
	case *elseRow:
		part, rest, nested := strings.Cut(path, ".")
		field := obj.field(part)
		if !nested {
			return field
		}
		return vm.resolveValue(field, rest)
	default:
		return anyToValue(resolve(base.Any, path), vm.strPool)
	}
}

// local is one bound local variable. Rules bind few locals at a time, so a
// slice searched from the end is cheaper than a map, and saving or restoring
// all of them is one copy.
type local struct {
	name string
	v    Value
}

func (vm *VM) findLocal(name string) int {
	for i := len(vm.locals) - 1; i >= 0; i-- {
		if vm.locals[i].name == name {
			return i
		}
	}
	return -1
}

// bindLocal sets a local variable.
func (vm *VM) bindLocal(name string, v Value) {
	if i := vm.findLocal(name); i >= 0 {
		vm.locals[i].v = v
		return
	}
	vm.locals = append(vm.locals, local{name: name, v: v})
}

// unbindLocal removes a local variable.
func (vm *VM) unbindLocal(name string) {
	if i := vm.findLocal(name); i >= 0 {
		last := len(vm.locals) - 1
		copy(vm.locals[i:], vm.locals[i+1:])
		vm.locals[last] = local{}
		vm.locals = vm.locals[:last]
	}
}

func (vm *VM) clearLocals() {
	clear(vm.locals)
	vm.locals = vm.locals[:0]
}

// pushFrame saves a copy of the current locals and returns its depth. Frames
// are reused across evaluations, so saving and restoring does not allocate in
// steady state.
func (vm *VM) pushFrame() int {
	depth := len(vm.frames)
	if depth < cap(vm.frames) {
		vm.frames = vm.frames[:depth+1]
	} else {
		vm.frames = append(vm.frames, nil)
	}
	vm.saveFrame(depth)
	return depth
}

// saveFrame overwrites frame depth with the current locals.
func (vm *VM) saveFrame(depth int) {
	frame := vm.frames[depth]
	clear(frame)
	vm.frames[depth] = append(frame[:0], vm.locals...)
}

// restoreFrame replaces the current locals with frame depth.
func (vm *VM) restoreFrame(depth int) {
	vm.clearLocals()
	vm.locals = append(vm.locals, vm.frames[depth]...)
}

// popFrame restores frame depth and discards it and every frame above it.
func (vm *VM) popFrame(depth int) {
	vm.restoreFrame(depth)
	for i := depth; i < len(vm.frames); i++ {
		clear(vm.frames[i])
		vm.frames[i] = vm.frames[i][:0]
	}
	vm.frames = vm.frames[:depth]
}

// clearFrames drops saved frames so they do not retain caller data.
func (vm *VM) clearFrames() {
	all := vm.frames[:cap(vm.frames)]
	for i := range all {
		clear(all[i])
		all[i] = all[i][:0]
	}
	vm.frames = vm.frames[:0]
}

func (vm *VM) findMatchingLoopMid(instrs []byte, beginIP, end uint32, beginOp, midOp, endOp bytecode.OpCode) (uint32, bool) {
	depth := 0
	for pos := beginIP + bytecode.InstrSize; pos < end; pos += bytecode.InstrSize {
		if pos+bytecode.InstrSize > uint32(len(instrs)) {
			break
		}
		var buf [bytecode.InstrSize]byte
		copy(buf[:], instrs[pos:pos+bytecode.InstrSize])
		op, _, _ := bytecode.DecodeInstr(buf)
		switch op {
		case beginOp:
			depth++
		case endOp:
			if depth > 0 {
				depth--
			}
		case midOp:
			if depth == 0 {
				return pos, true
			}
		}
	}
	return 0, false
}

func (vm *VM) valueToString(v Value) string {
	switch v.Typ {
	case TypeString:
		return vm.toStr(v)
	case TypeNumber:
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	case TypeBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case TypeNull:
		return ""
	default:
		return fmt.Sprint(vm.valueToAny(v))
	}
}

func decimalFromValue(v Value) (dec.Value, bool) {
	if v.Typ != TypeDecimal {
		return dec.Value{}, false
	}
	value, ok := v.Any.(dec.Value)
	return value, ok
}

func (vm *VM) orderedCompare(a, b Value) (int, error) {
	switch {
	case a.Typ == TypeDecimal || b.Typ == TypeDecimal:
		left, lok := decimalFromValue(a)
		right, rok := decimalFromValue(b)
		if !lok || !rok {
			return 0, fmt.Errorf("ordered comparison requires matching decimal operands")
		}
		return left.Cmp(right)
	case a.Typ == TypeString && b.Typ == TypeString:
		return strings.Compare(vm.toStr(a), vm.toStr(b)), nil
	default:
		switch {
		case vm.toNum(a) < vm.toNum(b):
			return -1, nil
		case vm.toNum(a) > vm.toNum(b):
			return 1, nil
		default:
			return 0, nil
		}
	}
}

func (vm *VM) addValues(a, b Value) (Value, error) {
	if a.Typ == TypeString || b.Typ == TypeString {
		return Value{Typ: TypeString, Any: vm.valueToString(a) + vm.valueToString(b)}, nil
	}
	if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
		left, lok := decimalFromValue(a)
		right, rok := decimalFromValue(b)
		if !lok || !rok {
			return NullVal(), fmt.Errorf("operator + expects matching decimal operands")
		}
		sum, err := left.Add(right)
		if err != nil {
			return NullVal(), err
		}
		return DecimalVal(sum), nil
	}
	return numResult(vm.toNum(a) + vm.toNum(b)), nil
}

func (vm *VM) subValues(a, b Value) (Value, error) {
	if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
		left, lok := decimalFromValue(a)
		right, rok := decimalFromValue(b)
		if !lok || !rok {
			return NullVal(), fmt.Errorf("operator - expects matching decimal operands")
		}
		diff, err := left.Sub(right)
		if err != nil {
			return NullVal(), err
		}
		return DecimalVal(diff), nil
	}
	return numResult(vm.toNum(a) - vm.toNum(b)), nil
}

func (vm *VM) absValue(v Value) (Value, error) {
	if v.Typ == TypeDecimal {
		value, ok := decimalFromValue(v)
		if !ok {
			return NullVal(), fmt.Errorf("builtin abs expects a decimal operand")
		}
		return DecimalVal(value.Abs()), nil
	}
	return NumVal(math.Abs(vm.toNum(v))), nil
}

func (vm *VM) minValue(a, b Value) (Value, error) {
	cmp, err := vm.orderedCompare(a, b)
	if err != nil {
		return NullVal(), err
	}
	if cmp <= 0 {
		return a, nil
	}
	return b, nil
}

func (vm *VM) maxValue(a, b Value) (Value, error) {
	cmp, err := vm.orderedCompare(a, b)
	if err != nil {
		return NullVal(), err
	}
	if cmp >= 0 {
		return a, nil
	}
	return b, nil
}

func (vm *VM) betweenValues(val, low, high Value, includeLow, includeHigh bool) (bool, error) {
	lowCmp, err := vm.orderedCompare(val, low)
	if err != nil {
		return false, err
	}
	highCmp, err := vm.orderedCompare(val, high)
	if err != nil {
		return false, err
	}
	lowOK := lowCmp > 0 || (includeLow && lowCmp == 0)
	highOK := highCmp < 0 || (includeHigh && highCmp == 0)
	return lowOK && highOK, nil
}

// localValue normalizes a value stored by a let binding. A let used to store
// the Go form of the value and convert it back on load, which dropped the
// exact quotient that an integer-profile division carries; dropping it here
// keeps that behaviour.
func localValue(v Value) Value {
	if v.Typ == TypeNumber {
		v.Any = nil
	}
	return v
}
