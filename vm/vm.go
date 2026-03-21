// vm/vm.go
package vm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/intern"
)

const maxStack = 256

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
	prev     any
	hadPrev  bool
}

// VM is the bytecode evaluator.
type VM struct {
	stack   [maxStack]Value
	sp      int
	pool    *intern.Pool
	strPool *StringPool
	locals  map[string]any // iterator variable bindings
	iters   []iterState
	regexes map[string]*regexp.Regexp
	badRe   map[string]struct{}
	err     error
	ip      uint32
}

func newVM(rs *compiler.CompiledRuleset, sp *StringPool) *VM {
	return &VM{
		pool:    rs.Constants,
		strPool: sp,
	}
}

func (vm *VM) push(v Value) {
	if vm.sp >= maxStack {
		if vm.err == nil {
			vm.err = fmt.Errorf("stack overflow at instruction %d", vm.ip)
		}
		return
	}
	vm.stack[vm.sp] = v
	vm.sp++
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
func Eval(rs *compiler.CompiledRuleset, dc DataContext) ([]MatchedRule, error) {
	return EvalWithPool(rs, dc, NewStringPool(rs.Constants.Strings()))
}

// EvalWithPool evaluates using a shared StringPool (for runtime-interned strings).
func EvalWithPool(rs *compiler.CompiledRuleset, dc DataContext, sp *StringPool) ([]MatchedRule, error) {
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}

	vm := newVM(rs, sp)
	var matched []MatchedRule

	for _, rule := range rs.Rules {
		vm.sp = 0 // reset stack per rule
		clear(vm.locals)
		vm.iters = vm.iters[:0]
		vm.err = nil

		result := vm.evalCondition(rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)
		if vm.err != nil {
			return nil, fmt.Errorf("rule %s: %w", vm.strPool.Get(rule.NameIdx), vm.err)
		}

		if result {
			mr := MatchedRule{
				Name:     vm.strPool.Get(rule.NameIdx),
				Priority: int(rule.Priority),
			}
			if int(rule.ActionIdx) < len(rs.Actions) {
				action := rs.Actions[rule.ActionIdx]
				mr.Action = vm.strPool.Get(action.NameIdx)
				params, err := vm.evalActionParams(rs.Instructions, action.Params, dc)
				if err != nil {
					return nil, fmt.Errorf("rule %s action %s: %w", mr.Name, mr.Action, err)
				}
				mr.Params = params
			}
			matched = append(matched, mr)
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
				return nil, fmt.Errorf("rule %s fallback %s: %w", mr.Name, mr.Action, err)
			}
			mr.Params = params
			matched = append(matched, mr)
		}
	}

	return matched, nil
}

// EvalDebug evaluates with full tracing.
func EvalDebug(rs *compiler.CompiledRuleset, dc DataContext) DebugResult {
	return EvalDebugWithPool(rs, dc, NewStringPool(rs.Constants.Strings()))
}

// EvalDebugWithPool evaluates with full tracing using a shared StringPool.
func EvalDebugWithPool(rs *compiler.CompiledRuleset, dc DataContext, sp *StringPool) DebugResult {
	start := time.Now()
	vm := newVM(rs, sp)
	var result DebugResult

	for _, rule := range rs.Rules {
		vm.sp = 0
		clear(vm.locals)
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
		} else {
			result.Failed = append(result.Failed, FailedRule{
				Name: vm.strPool.Get(rule.NameIdx),
			})
		}
	}

	result.Elapsed = time.Since(start)
	return result
}

func (vm *VM) evalCondition(instrs []byte, off, length uint32, dc DataContext) bool {
	end := off + length
	ip := off

	for ip < end {
		if ip+compiler.InstrSize > uint32(len(instrs)) {
			break
		}
		vm.ip = ip

		var buf [compiler.InstrSize]byte
		copy(buf[:], instrs[ip:ip+compiler.InstrSize])
		op, flags, arg := compiler.DecodeInstr(buf)

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

		ip += compiler.InstrSize
	}

	// If we ran off the end without RuleMatch, check stack
	if vm.sp > 0 {
		return vm.peek().AsBool()
	}
	return false
}

func (vm *VM) evalActionParams(instrs []byte, params []compiler.ActionParam, dc DataContext) (map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	baseLocals := cloneLocals(vm.locals)
	result := make(map[string]any, len(params))
	for _, p := range params {
		vm.sp = 0
		vm.locals = cloneLocals(baseLocals)
		vm.iters = vm.iters[:0]
		vm.err = nil
		vm.evalCondition(instrs, p.ValueOff, p.ValueLen, dc)
		if vm.err != nil {
			return nil, vm.err
		}
		if vm.sp > 0 {
			v := vm.pop()
			key := vm.strPool.Get(p.KeyIdx)
			result[key] = vm.valueToAny(v)
		}
	}
	vm.locals = baseLocals
	return result, nil
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
		return vm.strPool.Get(v.Str)
	case TypeList:
		if items, ok := v.Any.([]any); ok {
			return items
		}
		return vm.poolListToAny(v.ListIdx, v.ListLen)
	case TypeObject:
		return v.Any
	default:
		return nil
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
		if a.Str == b.Str {
			return true
		}
		return vm.strPool.Get(a.Str) == vm.strPool.Get(b.Str)
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
	nextIP := ip + compiler.InstrSize
	if nextIP+compiler.InstrSize > uint32(len(instrs)) || nextIP >= end {
		vm.err = fmt.Errorf("malformed list encoding at instruction %d", ip)
		vm.push(NullVal())
		return ip
	}
	var buf [compiler.InstrSize]byte
	copy(buf[:], instrs[nextIP:nextIP+compiler.InstrSize])
	op, flags, listLen := compiler.DecodeInstr(buf)
	if op != compiler.OpLoadNull || flags != 0xFF {
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
		vm.badRe[pattern] = struct{}{}
		return nil
	}
	if vm.regexes == nil {
		vm.regexes = make(map[string]*regexp.Regexp)
	}
	vm.regexes[pattern] = re
	return re
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
	default:
		return NullVal()
	}
}

func (vm *VM) lookupLocal(key string) (any, bool) {
	if v, ok := vm.locals[key]; ok {
		return v, true
	}
	dot := strings.IndexByte(key, '.')
	if dot <= 0 {
		return nil, false
	}
	base, ok := vm.locals[key[:dot]]
	if !ok {
		return nil, false
	}
	return resolve(base, key[dot+1:]), true
}

func (vm *VM) findMatchingLoopMid(instrs []byte, beginIP, end uint32, beginOp, midOp, endOp compiler.OpCode) (uint32, bool) {
	depth := 0
	for pos := beginIP + compiler.InstrSize; pos < end; pos += compiler.InstrSize {
		if pos+compiler.InstrSize > uint32(len(instrs)) {
			break
		}
		var buf [compiler.InstrSize]byte
		copy(buf[:], instrs[pos:pos+compiler.InstrSize])
		op, _, _ := compiler.DecodeInstr(buf)
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

func cloneLocals(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
