// vm/vm.go
package vm

import (
	"fmt"
	"math"
	"regexp"
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
}

// VM is the bytecode evaluator.
type VM struct {
	stack   [maxStack]Value
	sp      int
	pool    *intern.Pool
	strPool *StringPool
	locals  map[string]Value // iterator variable bindings
}

func newVM(rs *compiler.CompiledRuleset) *VM {
	return &VM{
		pool:    rs.Constants,
		strPool: NewStringPool(rs.Constants.Strings()),
		locals:  make(map[string]Value),
	}
}

func (vm *VM) push(v Value) {
	if vm.sp >= maxStack {
		return // stack overflow -- silently drop
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
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}

	vm := newVM(rs)
	var matched []MatchedRule

	for _, rule := range rs.Rules {
		vm.sp = 0 // reset stack per rule

		result := vm.evalCondition(rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)

		if result {
			mr := MatchedRule{
				Name:     vm.pool.GetString(rule.NameIdx),
				Priority: int(rule.Priority),
			}
			if int(rule.ActionIdx) < len(rs.Actions) {
				action := rs.Actions[rule.ActionIdx]
				mr.Action = vm.pool.GetString(action.NameIdx)
				mr.Params = vm.evalActionParams(rs.Instructions, action.Params, dc)
			}
			matched = append(matched, mr)
		} else if rule.FallbackIdx != 0 && int(rule.FallbackIdx) < len(rs.Actions) {
			action := rs.Actions[rule.FallbackIdx]
			mr := MatchedRule{
				Name:     vm.pool.GetString(rule.NameIdx),
				Priority: int(rule.Priority),
				Action:   vm.pool.GetString(action.NameIdx),
				Params:   vm.evalActionParams(rs.Instructions, action.Params, dc),
				Fallback: true,
			}
			matched = append(matched, mr)
		}
	}

	return matched, nil
}

// EvalDebug evaluates with full tracing.
func EvalDebug(rs *compiler.CompiledRuleset, dc DataContext) DebugResult {
	start := time.Now()
	vm := newVM(rs)
	var result DebugResult

	for _, rule := range rs.Rules {
		vm.sp = 0

		ok := vm.evalCondition(rs.Instructions, rule.ConditionOff, rule.ConditionLen, dc)

		if ok {
			mr := MatchedRule{
				Name:     vm.pool.GetString(rule.NameIdx),
				Priority: int(rule.Priority),
			}
			if int(rule.ActionIdx) < len(rs.Actions) {
				action := rs.Actions[rule.ActionIdx]
				mr.Action = vm.pool.GetString(action.NameIdx)
				mr.Params = vm.evalActionParams(rs.Instructions, action.Params, dc)
			}
			result.Matched = append(result.Matched, mr)
		} else {
			result.Failed = append(result.Failed, FailedRule{
				Name: vm.pool.GetString(rule.NameIdx),
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

		var buf [compiler.InstrSize]byte
		copy(buf[:], instrs[ip:ip+compiler.InstrSize])
		op, flags, arg := compiler.DecodeInstr(buf)

		switch op {
		case compiler.OpLoadStr:
			vm.push(StrVal(arg))
		case compiler.OpLoadNum:
			vm.push(NumVal(vm.pool.GetNumber(arg)))
		case compiler.OpLoadBool:
			vm.push(BoolVal(arg == 1))
		case compiler.OpLoadNull:
			if flags == intern.TypeList {
				// List load: this instruction carries listIdx in arg.
				// The next instruction (OpLoadNull, flags=0xFF) carries listLen.
				listIdx := arg
				ip += compiler.InstrSize
				if ip+compiler.InstrSize <= uint32(len(instrs)) {
					var buf2 [compiler.InstrSize]byte
					copy(buf2[:], instrs[ip:ip+compiler.InstrSize])
					_, _, listLen := compiler.DecodeInstr(buf2)
					vm.push(ListVal(listIdx, listLen))
				} else {
					vm.push(NullVal())
				}
			} else if flags == 0xFF {
				// Part of a list load pair — should not be reached standalone.
				// If reached, it's a no-op (the list case above consumed it).
				vm.push(NullVal())
			} else {
				vm.push(NullVal())
			}
		case compiler.OpLoadVar:
			key := vm.pool.GetString(arg)
			if v, ok := vm.locals[key]; ok {
				vm.push(v)
			} else {
				vm.push(dc.Get(key))
			}

		// Comparison
		case compiler.OpEq:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(vm.valEqual(a, b)))
		case compiler.OpNeq:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(!vm.valEqual(a, b)))
		case compiler.OpGt:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(vm.toNum(a) > vm.toNum(b)))
		case compiler.OpGte:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(vm.toNum(a) >= vm.toNum(b)))
		case compiler.OpLt:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(vm.toNum(a) < vm.toNum(b)))
		case compiler.OpLte:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(vm.toNum(a) <= vm.toNum(b)))

		// Math
		case compiler.OpAdd:
			b, a := vm.pop(), vm.pop()
			vm.push(NumVal(vm.toNum(a) + vm.toNum(b)))
		case compiler.OpSub:
			b, a := vm.pop(), vm.pop()
			vm.push(NumVal(vm.toNum(a) - vm.toNum(b)))
		case compiler.OpMul:
			b, a := vm.pop(), vm.pop()
			vm.push(NumVal(vm.toNum(a) * vm.toNum(b)))
		case compiler.OpDiv:
			b, a := vm.pop(), vm.pop()
			denom := vm.toNum(b)
			if denom == 0 {
				vm.push(NumVal(math.NaN()))
			} else {
				vm.push(NumVal(vm.toNum(a) / denom))
			}
		case compiler.OpMod:
			b, a := vm.pop(), vm.pop()
			denom := vm.toNum(b)
			if denom == 0 {
				vm.push(NumVal(math.NaN()))
			} else {
				vm.push(NumVal(math.Mod(vm.toNum(a), denom)))
			}

		// Logic
		case compiler.OpAnd:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(a.AsBool() && b.AsBool()))
		case compiler.OpOr:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(a.AsBool() || b.AsBool()))
		case compiler.OpNot:
			a := vm.pop()
			vm.push(BoolVal(!a.AsBool()))

		// Control flow
		case compiler.OpJumpIfFalse:
			top := vm.peek()
			if !top.AsBool() {
				ip += uint32(arg) // skip forward
				continue
			}
		case compiler.OpJumpIfTrue:
			top := vm.peek()
			if top.AsBool() {
				ip += uint32(arg)
				continue
			}

		// Null checks
		case compiler.OpIsNull:
			a := vm.pop()
			vm.push(BoolVal(a.IsNull()))
		case compiler.OpIsNotNull:
			a := vm.pop()
			vm.push(BoolVal(!a.IsNull()))

		// String operators
		case compiler.OpStartsWith:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(strings.HasPrefix(vm.toStr(a), vm.toStr(b))))
		case compiler.OpEndsWith:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(strings.HasSuffix(vm.toStr(a), vm.toStr(b))))
		case compiler.OpMatches:
			b, a := vm.pop(), vm.pop()
			re, err := regexp.Compile(vm.toStr(b))
			if err != nil {
				vm.push(BoolVal(false))
			} else {
				vm.push(BoolVal(re.MatchString(vm.toStr(a))))
			}

		// Collection -- In
		case compiler.OpIn:
			list, val := vm.pop(), vm.pop()
			vm.push(BoolVal(vm.listContainsVal(list, val)))
		case compiler.OpNotIn:
			list, val := vm.pop(), vm.pop()
			vm.push(BoolVal(!vm.listContainsVal(list, val)))
		case compiler.OpContains:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(vm.listContainsVal(a, b)))
		case compiler.OpNotContains:
			b, a := vm.pop(), vm.pop()
			vm.push(BoolVal(!vm.listContainsVal(a, b)))

		// Stubs for collection ops not yet fully wired
		case compiler.OpRetains, compiler.OpNotRetains,
			compiler.OpVagueContains, compiler.OpSubsetOf, compiler.OpSupersetOf:
			_, _ = vm.pop(), vm.pop()
			vm.push(BoolVal(false))

		// Range
		case compiler.OpBetweenCC:
			hi, lo, val := vm.pop(), vm.pop(), vm.pop()
			n, l, h := vm.toNum(val), vm.toNum(lo), vm.toNum(hi)
			vm.push(BoolVal(n >= l && n <= h))
		case compiler.OpBetweenOO:
			hi, lo, val := vm.pop(), vm.pop(), vm.pop()
			n, l, h := vm.toNum(val), vm.toNum(lo), vm.toNum(hi)
			vm.push(BoolVal(n > l && n < h))
		case compiler.OpBetweenCO:
			hi, lo, val := vm.pop(), vm.pop(), vm.pop()
			n, l, h := vm.toNum(val), vm.toNum(lo), vm.toNum(hi)
			vm.push(BoolVal(n >= l && n < h))
		case compiler.OpBetweenOC:
			hi, lo, val := vm.pop(), vm.pop(), vm.pop()
			n, l, h := vm.toNum(val), vm.toNum(lo), vm.toNum(hi)
			vm.push(BoolVal(n > l && n <= h))

		// Quantifiers -- stubs for now
		case compiler.OpIterBegin:
			_ = flags // ANY=0, ALL=1, NONE=2
			// TODO: implement in Task 7

		case compiler.OpIterNext:
			// TODO: implement in Task 7

		case compiler.OpIterEnd:
			// TODO: implement in Task 7
			vm.push(BoolVal(false))

		// Rule match
		case compiler.OpRuleMatch:
			// The condition result is whatever is on top of the stack
			if vm.sp > 0 {
				return vm.peek().AsBool()
			}
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

func (vm *VM) evalActionParams(instrs []byte, params []compiler.ActionParam, dc DataContext) map[string]any {
	if len(params) == 0 {
		return nil
	}
	result := make(map[string]any, len(params))
	for _, p := range params {
		vm.sp = 0
		vm.evalCondition(instrs, p.ValueOff, p.ValueLen, dc)
		if vm.sp > 0 {
			v := vm.pop()
			key := vm.pool.GetString(p.KeyIdx)
			result[key] = vm.valueToAny(v)
		}
	}
	return result
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
		return vm.pool.GetString(v.Str)
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
		return vm.pool.GetString(a.Str) == vm.pool.GetString(b.Str)
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
		return vm.pool.GetString(v.Str)
	}
	return ""
}

func (vm *VM) listContainsVal(list, val Value) bool {
	if list.Typ != TypeList {
		return false
	}
	items := vm.pool.GetList(list.ListIdx, list.ListLen)
	for _, item := range items {
		iv := Value{Typ: item.Typ, Num: item.Num, Str: item.Str, Bool: item.Bool}
		if vm.valEqual(iv, val) {
			return true
		}
	}
	return false
}
