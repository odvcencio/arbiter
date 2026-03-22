package vm

import (
	"fmt"
	"math"
	"strings"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/intern"
)

func nextInstruction(ip uint32) uint32 {
	return ip + compiler.InstrSize
}

func (vm *VM) dispatchConditionOp(instrs []byte, end, ip uint32, op compiler.OpCode, flags uint8, arg uint16, dc DataContext) (uint32, bool, bool, bool) {
	if nextIP, handled := vm.evalLoadOp(instrs, end, ip, op, flags, arg, dc); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalComparisonOp(ip, op); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalMathOp(ip, op); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalLogicOp(ip, op); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalControlOp(ip, op, arg); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalNullOp(ip, op); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalStringOp(ip, op); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalCollectionOp(ip, op); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalRangeOp(ip, op); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalIteratorOp(instrs, end, ip, op, flags, arg); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalAggregateOp(instrs, end, ip, op, flags, arg); handled {
		return nextIP, true, false, false
	}
	if nextIP, handled := vm.evalLocalOp(ip, op, arg); handled {
		return nextIP, true, false, false
	}
	if op == compiler.OpRuleMatch {
		if vm.sp > 0 {
			return 0, true, vm.peek().AsBool(), true
		}
		return 0, true, false, true
	}
	return 0, false, false, false
}

func (vm *VM) evalLoadOp(instrs []byte, end, ip uint32, op compiler.OpCode, flags uint8, arg uint16, dc DataContext) (uint32, bool) {
	switch op {
	case compiler.OpLoadStr:
		if flags != 0 {
			// Legacy list encoding kept for compatibility with older in-memory bytecode.
			vm.push(ListVal(arg, uint16(flags)))
		} else {
			vm.push(StrVal(arg))
		}
		return nextInstruction(ip), true
	case compiler.OpLoadNum:
		vm.push(NumVal(vm.pool.GetNumber(arg)))
		return nextInstruction(ip), true
	case compiler.OpLoadDec:
		vm.push(DecimalVal(vm.pool.GetDecimal(arg)))
		return nextInstruction(ip), true
	case compiler.OpLoadBool:
		vm.push(BoolVal(arg == 1))
		return nextInstruction(ip), true
	case compiler.OpLoadNull:
		if flags == intern.TypeList {
			return nextInstruction(vm.decodeListPair(instrs, ip, end, arg)), true
		}
		// Standalone list tails are treated as a no-op null push for compatibility.
		vm.push(NullVal())
		return nextInstruction(ip), true
	case compiler.OpLoadVar:
		key := vm.strPool.Get(arg)
		if raw, ok := vm.lookupLocal(key); ok {
			vm.push(anyToValue(raw, vm.strPool))
		} else {
			vm.push(dc.Get(key))
		}
		return nextInstruction(ip), true
	default:
		return 0, false
	}
}

func (vm *VM) evalComparisonOp(ip uint32, op compiler.OpCode) (uint32, bool) {
	switch op {
	case compiler.OpEq:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.valEqual(a, b)))
	case compiler.OpNeq:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(!vm.valEqual(a, b)))
	case compiler.OpGt:
		b, a := vm.pop(), vm.pop()
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(cmp > 0))
	case compiler.OpGte:
		b, a := vm.pop(), vm.pop()
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(cmp >= 0))
	case compiler.OpLt:
		b, a := vm.pop(), vm.pop()
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(cmp < 0))
	case compiler.OpLte:
		b, a := vm.pop(), vm.pop()
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(cmp <= 0))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalMathOp(ip uint32, op compiler.OpCode) (uint32, bool) {
	switch op {
	case compiler.OpAdd:
		b, a := vm.pop(), vm.pop()
		next, err := vm.addValues(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case compiler.OpSub:
		b, a := vm.pop(), vm.pop()
		next, err := vm.subValues(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case compiler.OpMul:
		b, a := vm.pop(), vm.pop()
		if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("operator * does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		vm.push(NumVal(vm.toNum(a) * vm.toNum(b)))
	case compiler.OpDiv:
		b, a := vm.pop(), vm.pop()
		if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("operator / does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		denom := vm.toNum(b)
		if denom == 0 {
			vm.push(NumVal(math.NaN()))
		} else {
			vm.push(NumVal(vm.toNum(a) / denom))
		}
	case compiler.OpMod:
		b, a := vm.pop(), vm.pop()
		if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("operator %% does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		denom := vm.toNum(b)
		if denom == 0 {
			vm.push(NumVal(math.NaN()))
		} else {
			vm.push(NumVal(math.Mod(vm.toNum(a), denom)))
		}
	case compiler.OpAbs:
		a := vm.pop()
		next, err := vm.absValue(a)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case compiler.OpMin:
		b, a := vm.pop(), vm.pop()
		next, err := vm.minValue(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case compiler.OpMax:
		b, a := vm.pop(), vm.pop()
		next, err := vm.maxValue(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case compiler.OpRound:
		a := vm.pop()
		if a.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("builtin round does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		vm.push(NumVal(math.Round(vm.toNum(a))))
	case compiler.OpFloor:
		a := vm.pop()
		if a.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("builtin floor does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		vm.push(NumVal(math.Floor(vm.toNum(a))))
	case compiler.OpCeil:
		a := vm.pop()
		if a.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("builtin ceil does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		vm.push(NumVal(math.Ceil(vm.toNum(a))))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalLogicOp(ip uint32, op compiler.OpCode) (uint32, bool) {
	switch op {
	case compiler.OpAnd:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(a.AsBool() && b.AsBool()))
	case compiler.OpOr:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(a.AsBool() || b.AsBool()))
	case compiler.OpNot:
		a := vm.pop()
		vm.push(BoolVal(!a.AsBool()))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalControlOp(ip uint32, op compiler.OpCode, arg uint16) (uint32, bool) {
	switch op {
	case compiler.OpJumpIfFalse:
		if !vm.peek().AsBool() {
			return ip + uint32(arg), true
		}
		return nextInstruction(ip), true
	case compiler.OpJumpIfTrue:
		if vm.peek().AsBool() {
			return ip + uint32(arg), true
		}
		return nextInstruction(ip), true
	default:
		return 0, false
	}
}

func (vm *VM) evalNullOp(ip uint32, op compiler.OpCode) (uint32, bool) {
	switch op {
	case compiler.OpIsNull:
		a := vm.pop()
		vm.push(BoolVal(a.IsNull()))
	case compiler.OpIsNotNull:
		a := vm.pop()
		vm.push(BoolVal(!a.IsNull()))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalStringOp(ip uint32, op compiler.OpCode) (uint32, bool) {
	switch op {
	case compiler.OpStartsWith:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(strings.HasPrefix(vm.toStr(a), vm.toStr(b))))
	case compiler.OpEndsWith:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(strings.HasSuffix(vm.toStr(a), vm.toStr(b))))
	case compiler.OpMatches:
		b, a := vm.pop(), vm.pop()
		re := vm.regex(vm.toStr(b))
		if re == nil {
			vm.push(BoolVal(false))
		} else {
			vm.push(BoolVal(re.MatchString(vm.toStr(a))))
		}
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalCollectionOp(ip uint32, op compiler.OpCode) (uint32, bool) {
	switch op {
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
	case compiler.OpRetains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listRetains(a, b)))
	case compiler.OpNotRetains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(!vm.listRetains(a, b)))
	case compiler.OpVagueContains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listVagueContains(a, b)))
	case compiler.OpSubsetOf:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listSubsetOf(a, b)))
	case compiler.OpSupersetOf:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listSubsetOf(b, a)))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalRangeOp(ip uint32, op compiler.OpCode) (uint32, bool) {
	switch op {
	case compiler.OpBetweenCC:
		hi, lo, val := vm.pop(), vm.pop(), vm.pop()
		ok, err := vm.betweenValues(val, lo, hi, true, true)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(ok))
	case compiler.OpBetweenOO:
		hi, lo, val := vm.pop(), vm.pop(), vm.pop()
		ok, err := vm.betweenValues(val, lo, hi, false, false)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(ok))
	case compiler.OpBetweenCO:
		hi, lo, val := vm.pop(), vm.pop(), vm.pop()
		ok, err := vm.betweenValues(val, lo, hi, true, false)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(ok))
	case compiler.OpBetweenOC:
		hi, lo, val := vm.pop(), vm.pop(), vm.pop()
		ok, err := vm.betweenValues(val, lo, hi, false, true)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(ok))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalIteratorOp(instrs []byte, end, ip uint32, op compiler.OpCode, flags uint8, arg uint16) (uint32, bool) {
	switch op {
	case compiler.OpIterBegin:
		list := vm.pop()
		iter := iterState{
			kind:    flags,
			varName: vm.strPool.Get(arg),
			items:   vm.listEntries(list),
			result:  flags != compiler.FlagAny,
		}
		if vm.locals == nil {
			vm.locals = make(map[string]any)
		}
		if prev, ok := vm.locals[iter.varName]; ok {
			iter.prev = prev
			iter.hadPrev = true
		}
		vm.iters = append(vm.iters, iter)

		if len(iter.items) == 0 {
			nextIP, found := vm.findMatchingLoopMid(instrs, ip, end, compiler.OpIterBegin, compiler.OpIterNext, compiler.OpIterEnd)
			if found {
				return nextInstruction(nextIP), true
			}
			return nextInstruction(ip), true
		}

		vm.locals[iter.varName] = iter.items[0]
		return nextInstruction(ip), true

	case compiler.OpIterNext:
		if len(vm.iters) == 0 {
			return nextInstruction(ip), true
		}
		bodyResult := vm.pop().AsBool()
		iter := &vm.iters[len(vm.iters)-1]

		switch iter.kind {
		case compiler.FlagAny:
			if bodyResult {
				iter.result = true
				return nextInstruction(ip), true
			}
		case compiler.FlagAll:
			if !bodyResult {
				iter.result = false
				return nextInstruction(ip), true
			}
		case compiler.FlagNone:
			if bodyResult {
				iter.result = false
				return nextInstruction(ip), true
			}
		}

		iter.index++
		if iter.index < len(iter.items) {
			vm.locals[iter.varName] = iter.items[iter.index]
			return ip - uint32(arg), true
		}
		return nextInstruction(ip), true

	case compiler.OpIterEnd:
		if len(vm.iters) == 0 {
			vm.push(BoolVal(false))
			return nextInstruction(ip), true
		}
		iter := vm.iters[len(vm.iters)-1]
		vm.iters = vm.iters[:len(vm.iters)-1]
		// If the quantifier matched, keep the last-bound value in locals
		// so action param expressions can reference the matched item.
		// Only restore/delete on non-match.
		if !iter.result {
			if iter.hadPrev {
				vm.locals[iter.varName] = iter.prev
			} else {
				delete(vm.locals, iter.varName)
			}
		}
		vm.push(BoolVal(iter.result))
		return nextInstruction(ip), true

	default:
		return 0, false
	}
}

func (vm *VM) evalAggregateOp(instrs []byte, end, ip uint32, op compiler.OpCode, flags uint8, arg uint16) (uint32, bool) {
	switch op {
	case compiler.OpAggBegin:
		list := vm.pop()
		iter := iterState{
			kind:    flags,
			varName: vm.strPool.Get(arg),
			items:   vm.listEntries(list),
		}
		if vm.locals == nil {
			vm.locals = make(map[string]any)
		}
		if prev, ok := vm.locals[iter.varName]; ok {
			iter.prev = prev
			iter.hadPrev = true
		}
		vm.iters = append(vm.iters, iter)

		if len(iter.items) == 0 {
			nextIP, found := vm.findMatchingLoopMid(instrs, ip, end, compiler.OpAggBegin, compiler.OpAggAccum, compiler.OpAggEnd)
			if found {
				return nextInstruction(nextIP), true
			}
			return nextInstruction(ip), true
		}

		vm.locals[iter.varName] = iter.items[0]
		return nextInstruction(ip), true

	case compiler.OpAggAccum:
		if len(vm.iters) == 0 {
			return nextInstruction(ip), true
		}
		val := vm.toNum(vm.pop())
		iter := &vm.iters[len(vm.iters)-1]
		iter.aggSum += val
		iter.aggCount++
		iter.index++
		if iter.index < len(iter.items) {
			vm.locals[iter.varName] = iter.items[iter.index]
			return ip - uint32(arg), true
		}
		return nextInstruction(ip), true

	case compiler.OpAggEnd:
		if len(vm.iters) == 0 {
			vm.push(NumVal(0))
			return nextInstruction(ip), true
		}
		iter := vm.iters[len(vm.iters)-1]
		vm.iters = vm.iters[:len(vm.iters)-1]
		if iter.hadPrev {
			vm.locals[iter.varName] = iter.prev
		} else {
			delete(vm.locals, iter.varName)
		}
		switch iter.kind {
		case compiler.FlagCount:
			vm.push(NumVal(float64(iter.aggCount)))
		case compiler.FlagAvg:
			if iter.aggCount == 0 {
				vm.push(NumVal(0))
			} else {
				vm.push(NumVal(iter.aggSum / float64(iter.aggCount)))
			}
		default:
			vm.push(NumVal(iter.aggSum))
		}
		return nextInstruction(ip), true

	default:
		return 0, false
	}
}

func (vm *VM) evalLocalOp(ip uint32, op compiler.OpCode, arg uint16) (uint32, bool) {
	if op != compiler.OpSetLocal {
		return 0, false
	}
	val := vm.pop()
	if vm.locals == nil {
		vm.locals = make(map[string]any)
	}
	vm.locals[vm.strPool.Get(arg)] = vm.valueToAny(val)
	return nextInstruction(ip), true
}
