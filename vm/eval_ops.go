package vm

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"m31labs.dev/arbiter/bytecode"
	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/intern"
)

func nextInstruction(ip uint32) uint32 {
	return ip + bytecode.InstrSize
}

func (vm *VM) dispatchConditionOp(instrs []byte, end, ip uint32, op bytecode.OpCode, flags uint8, arg uint16, dc DataContext) (uint32, bool, bool, bool) {
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
	if nextIP, handled := vm.evalTableOp(instrs, end, ip, op, flags, arg, dc); handled {
		return nextIP, true, false, false
	}
	if op == bytecode.OpRuleMatch {
		if vm.sp > 0 {
			return 0, true, vm.peek().AsBool(), true
		}
		return 0, true, false, true
	}
	return 0, false, false, false
}

func (vm *VM) evalLoadOp(instrs []byte, end, ip uint32, op bytecode.OpCode, flags uint8, arg uint16, dc DataContext) (uint32, bool) {
	switch op {
	case bytecode.OpLoadStr:
		if flags != 0 {
			// Legacy list encoding kept for compatibility with older in-memory bytecode.
			vm.push(ListVal(arg, uint16(flags)))
		} else {
			vm.push(StrVal(arg))
		}
		return nextInstruction(ip), true
	case bytecode.OpLoadNum:
		vm.push(NumVal(vm.pool.GetNumber(arg)))
		return nextInstruction(ip), true
	case bytecode.OpLoadDec:
		vm.push(DecimalVal(vm.pool.GetDecimal(arg)))
		return nextInstruction(ip), true
	case bytecode.OpLoadBool:
		vm.push(BoolVal(arg == 1))
		return nextInstruction(ip), true
	case bytecode.OpLoadNull:
		if flags == intern.TypeList {
			return nextInstruction(vm.decodeListPair(instrs, ip, end, arg)), true
		}
		// Standalone list tails are treated as a no-op null push for compatibility.
		vm.push(NullVal())
		return nextInstruction(ip), true
	case bytecode.OpLoadVar:
		key := vm.strPool.Get(arg)
		if raw, ok := vm.lookupLocal(key); ok {
			vm.push(anyToValue(raw, vm.strPool))
		} else {
			vm.push(dc.Get(key))
		}
		if vm.profile == NumericInteger {
			vm.stack[vm.sp-1] = vm.checkLoaded(vm.stack[vm.sp-1])
		}
		return nextInstruction(ip), true
	default:
		return 0, false
	}
}

func (vm *VM) evalComparisonOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	switch op {
	case bytecode.OpEq:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.valEqual(a, b)))
	case bytecode.OpNeq:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(!vm.valEqual(a, b)))
	case bytecode.OpGt:
		b, a := vm.pop(), vm.pop()
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(cmp > 0))
	case bytecode.OpGte:
		b, a := vm.pop(), vm.pop()
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(cmp >= 0))
	case bytecode.OpLt:
		b, a := vm.pop(), vm.pop()
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(cmp < 0))
	case bytecode.OpLte:
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

func (vm *VM) evalMathOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	if vm.profile == NumericInteger && op >= bytecode.OpAdd && op <= bytecode.OpCeil {
		vm.evalIntegerMathOp(op)
		return nextInstruction(ip), true
	}
	switch op {
	case bytecode.OpAdd:
		b, a := vm.pop(), vm.pop()
		next, err := vm.addValues(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case bytecode.OpSub:
		b, a := vm.pop(), vm.pop()
		next, err := vm.subValues(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case bytecode.OpMul:
		b, a := vm.pop(), vm.pop()
		if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
			ad, aok := a.Any.(dec.Value)
			bd, bok := b.Any.(dec.Value)
			if !aok && a.Typ == TypeNumber {
				ad = dec.MustParse(fmt.Sprintf("%g", a.Num), "")
				aok = true
			}
			if !bok && b.Typ == TypeNumber {
				bd = dec.MustParse(fmt.Sprintf("%g", b.Num), "")
				bok = true
			}
			if aok && bok {
				result, err := ad.Mul(bd)
				if err != nil {
					vm.setErr(err)
					vm.push(NullVal())
				} else {
					vm.push(DecimalVal(result))
				}
			} else {
				vm.setErr(fmt.Errorf("operator * requires numeric operands"))
				vm.push(NullVal())
			}
			break
		}
		// float64(...) rounds the product here and prevents the compiler from
		// fusing it into a later add (FMA on arm64, never on wasm), which would
		// make results differ by target. See SPEC.md, "Deterministic evaluation".
		vm.push(numResult(float64(vm.toNum(a) * vm.toNum(b))))
	case bytecode.OpDiv:
		b, a := vm.pop(), vm.pop()
		if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
			ad, aok := a.Any.(dec.Value)
			bd, bok := b.Any.(dec.Value)
			if !aok && a.Typ == TypeNumber {
				ad = dec.MustParse(fmt.Sprintf("%g", a.Num), "")
				aok = true
			}
			if !bok && b.Typ == TypeNumber {
				bd = dec.MustParse(fmt.Sprintf("%g", b.Num), "")
				bok = true
			}
			if aok && bok {
				result, err := ad.Div(bd, 10)
				if err != nil {
					vm.setErr(err)
					vm.push(NullVal())
				} else {
					vm.push(DecimalVal(result))
				}
			} else {
				vm.setErr(fmt.Errorf("operator / requires numeric operands"))
				vm.push(NullVal())
			}
			break
		}
		denom := vm.toNum(b)
		if denom == 0 {
			vm.push(NumVal(math.NaN()))
		} else {
			vm.push(numResult(float64(vm.toNum(a) / denom)))
		}
	case bytecode.OpMod:
		b, a := vm.pop(), vm.pop()
		if a.Typ == TypeDecimal || b.Typ == TypeDecimal {
			ad, aok := a.Any.(dec.Value)
			bd, bok := b.Any.(dec.Value)
			if aok && bok {
				result, err := ad.Mod(bd)
				if err != nil {
					vm.setErr(err)
					vm.push(NullVal())
				} else {
					vm.push(DecimalVal(result))
				}
			} else {
				vm.setErr(fmt.Errorf("operator %% requires decimal operands"))
				vm.push(NullVal())
			}
			break
		}
		denom := vm.toNum(b)
		if denom == 0 {
			vm.push(NumVal(math.NaN()))
		} else {
			// math.Mod is portable: in Go 1.25.9 (go.mod says 1.25) and 1.26.0
			// math/mod.go is pure Go (Frexp, Ldexp, subtraction) and stubs.go sets
			// haveArchMod = false on every GOARCH, amd64, arm64 and wasm included. There is no assembly path, so the
			// result is the same on all targets.
			vm.push(numResult(math.Mod(vm.toNum(a), denom)))
		}
	case bytecode.OpAbs:
		a := vm.pop()
		next, err := vm.absValue(a)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case bytecode.OpMin:
		b, a := vm.pop(), vm.pop()
		next, err := vm.minValue(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case bytecode.OpMax:
		b, a := vm.pop(), vm.pop()
		next, err := vm.maxValue(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(NullVal())
			break
		}
		vm.push(next)
	case bytecode.OpRound:
		a := vm.pop()
		if a.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("builtin round does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		vm.push(NumVal(math.Round(vm.toNum(a))))
	case bytecode.OpFloor:
		a := vm.pop()
		if a.Typ == TypeDecimal {
			vm.setErr(fmt.Errorf("builtin floor does not support decimal operands"))
			vm.push(NullVal())
			break
		}
		vm.push(NumVal(math.Floor(vm.toNum(a))))
	case bytecode.OpCeil:
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

func (vm *VM) evalLogicOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	switch op {
	case bytecode.OpAnd:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(a.AsBool() && b.AsBool()))
	case bytecode.OpOr:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(a.AsBool() || b.AsBool()))
	case bytecode.OpNot:
		a := vm.pop()
		vm.push(BoolVal(!a.AsBool()))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalControlOp(ip uint32, op bytecode.OpCode, arg uint16) (uint32, bool) {
	switch op {
	case bytecode.OpJumpIfFalse:
		if !vm.peek().AsBool() {
			return ip + uint32(arg), true
		}
		return nextInstruction(ip), true
	case bytecode.OpJumpIfTrue:
		if vm.peek().AsBool() {
			return ip + uint32(arg), true
		}
		return nextInstruction(ip), true
	default:
		return 0, false
	}
}

func (vm *VM) evalNullOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	switch op {
	case bytecode.OpIsNull:
		a := vm.pop()
		vm.push(BoolVal(a.IsNull()))
	case bytecode.OpIsNotNull:
		a := vm.pop()
		vm.push(BoolVal(!a.IsNull()))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalStringOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	switch op {
	case bytecode.OpStartsWith:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(strings.HasPrefix(vm.toStr(a), vm.toStr(b))))
	case bytecode.OpEndsWith:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(strings.HasSuffix(vm.toStr(a), vm.toStr(b))))
	case bytecode.OpMatches:
		b, a := vm.pop(), vm.pop()
		var re *regexp.Regexp
		// Fast path: use the pre-compiled regex from the ruleset when the
		// pattern was a compile-time string literal.
		if b.Typ == TypeString && vm.rs != nil && vm.rs.Regexes != nil {
			re = vm.rs.Regexes[b.Str]
		}
		if re == nil {
			re = vm.regex(vm.toStr(b))
		}
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

func (vm *VM) evalCollectionOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	switch op {
	case bytecode.OpIn:
		list, val := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listContainsVal(list, val)))
	case bytecode.OpNotIn:
		list, val := vm.pop(), vm.pop()
		vm.push(BoolVal(!vm.listContainsVal(list, val)))
	case bytecode.OpContains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listContainsVal(a, b)))
	case bytecode.OpNotContains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(!vm.listContainsVal(a, b)))
	case bytecode.OpRetains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listRetains(a, b)))
	case bytecode.OpNotRetains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(!vm.listRetains(a, b)))
	case bytecode.OpVagueContains:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listVagueContains(a, b)))
	case bytecode.OpSubsetOf:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listSubsetOf(a, b)))
	case bytecode.OpSupersetOf:
		b, a := vm.pop(), vm.pop()
		vm.push(BoolVal(vm.listSubsetOf(b, a)))
	default:
		return 0, false
	}
	return nextInstruction(ip), true
}

func (vm *VM) evalRangeOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	switch op {
	case bytecode.OpBetweenCC:
		hi, lo, val := vm.pop(), vm.pop(), vm.pop()
		ok, err := vm.betweenValues(val, lo, hi, true, true)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(ok))
	case bytecode.OpBetweenOO:
		hi, lo, val := vm.pop(), vm.pop(), vm.pop()
		ok, err := vm.betweenValues(val, lo, hi, false, false)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(ok))
	case bytecode.OpBetweenCO:
		hi, lo, val := vm.pop(), vm.pop(), vm.pop()
		ok, err := vm.betweenValues(val, lo, hi, true, false)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			break
		}
		vm.push(BoolVal(ok))
	case bytecode.OpBetweenOC:
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

func (vm *VM) evalIteratorOp(instrs []byte, end, ip uint32, op bytecode.OpCode, flags uint8, arg uint16) (uint32, bool) {
	switch op {
	case bytecode.OpIterBegin:
		list := vm.pop()
		iter := iterState{
			kind:    flags,
			varName: vm.strPool.Get(arg),
			items:   vm.listEntries(list),
			result:  flags != bytecode.FlagAny,
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
			nextIP, found := vm.findMatchingLoopMid(instrs, ip, end, bytecode.OpIterBegin, bytecode.OpIterNext, bytecode.OpIterEnd)
			if found {
				return nextInstruction(nextIP), true
			}
			return nextInstruction(ip), true
		}

		vm.locals[iter.varName] = iter.items[0]
		return nextInstruction(ip), true

	case bytecode.OpIterNext:
		if len(vm.iters) == 0 {
			return nextInstruction(ip), true
		}
		bodyResult := vm.pop().AsBool()
		iter := &vm.iters[len(vm.iters)-1]

		switch iter.kind {
		case bytecode.FlagAny:
			if bodyResult {
				iter.result = true
				return nextInstruction(ip), true
			}
		case bytecode.FlagAll:
			if !bodyResult {
				iter.result = false
				return nextInstruction(ip), true
			}
		case bytecode.FlagNone:
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

	case bytecode.OpIterEnd:
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

func (vm *VM) evalAggregateOp(instrs []byte, end, ip uint32, op bytecode.OpCode, flags uint8, arg uint16) (uint32, bool) {
	switch op {
	case bytecode.OpAggBegin:
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
			nextIP, found := vm.findMatchingLoopMid(instrs, ip, end, bytecode.OpAggBegin, bytecode.OpAggAccum, bytecode.OpAggEnd)
			if found {
				return nextInstruction(nextIP), true
			}
			return nextInstruction(ip), true
		}

		vm.locals[iter.varName] = iter.items[0]
		return nextInstruction(ip), true

	case bytecode.OpAggAccum:
		if len(vm.iters) == 0 {
			return nextInstruction(ip), true
		}
		popped := vm.pop()
		val := vm.toNum(popped)
		iter := &vm.iters[len(vm.iters)-1]
		if vm.profile == NumericInteger && flags != bytecode.FlagCount {
			// count ignores the item value, so it need not be an integer.
			sum, err := integerSum(iter.aggSum, popped)
			if err != nil {
				vm.setErr(err)
				return nextInstruction(ip), true
			}
			iter.aggSum = sum
		} else if flags != bytecode.FlagCount || vm.profile != NumericInteger {
			iter.aggSum += val
		}
		iter.aggCount++
		iter.index++
		if iter.index < len(iter.items) {
			vm.locals[iter.varName] = iter.items[iter.index]
			return ip - uint32(arg), true
		}
		return nextInstruction(ip), true

	case bytecode.OpAggEnd:
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
		case bytecode.FlagCount:
			vm.push(NumVal(float64(iter.aggCount)))
		case bytecode.FlagAvg:
			switch {
			case iter.aggCount == 0:
				vm.push(NumVal(0))
			case vm.profile == NumericInteger:
				vm.push(quotientValue(int64(iter.aggSum), int64(iter.aggCount)))
			default:
				vm.push(numResult(float64(iter.aggSum / float64(iter.aggCount))))
			}
		default:
			vm.push(numResult(iter.aggSum))
		}
		return nextInstruction(ip), true

	default:
		return 0, false
	}
}

func (vm *VM) evalLocalOp(ip uint32, op bytecode.OpCode, arg uint16) (uint32, bool) {
	if op != bytecode.OpSetLocal {
		return 0, false
	}
	val := vm.pop()
	if vm.locals == nil {
		vm.locals = make(map[string]any)
	}
	vm.locals[vm.strPool.Get(arg)] = vm.valueToAny(val)
	return nextInstruction(ip), true
}
