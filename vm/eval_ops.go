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
	// One switch over the opcode groups (each group is a contiguous range, see
	// package bytecode) instead of asking every group in turn.
	var nextIP uint32
	var handled bool
	switch {
	case op <= bytecode.OpLoadVar:
		nextIP, handled = vm.evalLoadOp(instrs, end, ip, op, flags, arg, dc)
	case op <= bytecode.OpLte:
		nextIP, handled = vm.evalComparisonOp(ip, op)
	case op <= bytecode.OpSupersetOf:
		nextIP, handled = vm.evalCollectionOp(ip, op)
	case op <= bytecode.OpMatches:
		nextIP, handled = vm.evalStringOp(ip, op)
	case op <= bytecode.OpBetweenOC:
		nextIP, handled = vm.evalRangeOp(ip, op)
	case op <= bytecode.OpIsNotNull:
		nextIP, handled = vm.evalNullOp(ip, op)
	case op <= bytecode.OpCeil:
		nextIP, handled = vm.evalMathOp(ip, op)
	case op <= bytecode.OpNot:
		nextIP, handled = vm.evalLogicOp(ip, op)
	case op <= bytecode.OpJumpIfTrue:
		nextIP, handled = vm.evalControlOp(ip, op, arg)
	case op <= bytecode.OpIterEnd:
		nextIP, handled = vm.evalIteratorOp(instrs, end, ip, op, flags, arg)
	case op == bytecode.OpRuleMatch:
		if vm.sp > 0 {
			return 0, true, vm.peek().AsBool(), true
		}
		return 0, true, false, true
	case op <= bytecode.OpAggEnd:
		nextIP, handled = vm.evalAggregateOp(instrs, end, ip, op, flags, arg)
	case op == bytecode.OpSetLocal:
		nextIP, handled = vm.evalLocalOp(ip, op, arg)
	default:
		nextIP, handled = vm.evalTableOp(instrs, end, ip, op, flags, arg, dc)
	}
	return nextIP, handled, false, false
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
		vm.loadVar(arg, dc)
		return nextInstruction(ip), true
	default:
		return 0, false
	}
}

// loadVar pushes the variable whose name is string constant arg: a local if
// one is bound under that name, otherwise the data context's value.
func (vm *VM) loadVar(arg uint16, dc DataContext) {
	var key string
	if int(arg) < len(vm.strs) {
		key = vm.strs[arg]
	} else {
		key = vm.strPool.Get(arg)
	}
	// Each branch keeps the loaded Value in its own local and pushes it through
	// a pointer. Passing the call result straight to push, or assigning it to
	// a variable shared by the branches, makes the compiler copy the Value as
	// a block, which stalls on store forwarding (see push).
	if vm.localMask != nil && (int(arg) >= len(vm.localMask) || !vm.localMask[arg]) {
		// Prepared evaluators know which names can be locals. Any other name
		// is a fact of the data context. With the fact cache on, the fact is
		// read once per evaluation and later loads push the stored Value: a
		// rule set tests the same few facts in most of its rules.
		var at uint16
		cacheable := int(arg) < len(vm.varSlot)
		if cacheable {
			at = vm.varSlot[arg]
		}
		switch {
		case at != 0:
			vm.pushFrom(&vm.varVals[at-1])
		case !cacheable || len(vm.varVals) >= maxCachedVars:
			// The cache is off or full, or the name was interned after the
			// cache was built: read the fact on every use.
			if vm.idc != nil {
				v := vm.idc.GetIndexed(arg, key)
				vm.pushFrom(&v)
			} else {
				v := dc.Get(key)
				vm.pushFrom(&v)
			}
		case vm.idc != nil:
			// The context is called before the cache is touched, so a context
			// that panics leaves no half-filled entry behind.
			v := vm.idc.GetIndexed(arg, key)
			c := vm.cacheVar(arg)
			c.Typ, c.Bool, c.Str, c.ListIdx, c.ListLen, c.Num, c.Any = v.Typ, v.Bool, v.Str, v.ListIdx, v.ListLen, v.Num, v.Any
			vm.pushFrom(c)
		default:
			v := dc.Get(key)
			c := vm.cacheVar(arg)
			c.Typ, c.Bool, c.Str, c.ListIdx, c.ListLen, c.Num, c.Any = v.Typ, v.Bool, v.Str, v.ListIdx, v.ListLen, v.Num, v.Any
			vm.pushFrom(c)
		}
	} else if local, ok := vm.lookupLocal(key); ok {
		vm.pushFrom(&local)
	} else if vm.idc != nil {
		v := vm.idc.GetIndexed(arg, key)
		vm.pushFrom(&v)
	} else {
		v := dc.Get(key)
		vm.pushFrom(&v)
	}
	if vm.sp == 0 {
		return
	}
	if vm.profile == NumericInteger {
		vm.stack[vm.sp-1] = vm.checkLoaded(vm.stack[vm.sp-1])
	}
	if vm.trace != nil && vm.stack[vm.sp-1].Typ == TypeBool {
		// A boolean read straight from the input, such as self.has_orb,
		// is a condition of its own; record it like a comparison.
		v := vm.stack[vm.sp-1]
		vm.recordCheck(bytecode.OpLoadVar, v, Value{}, Value{}, v.Bool)
	}
}

func (vm *VM) evalComparisonOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	if op < bytecode.OpEq || op > bytecode.OpLte {
		return 0, false
	}
	b, a := vm.pop(), vm.pop()
	var result bool
	switch op {
	case bytecode.OpEq:
		result = vm.valEqual(a, b)
	case bytecode.OpNeq:
		result = !vm.valEqual(a, b)
	default:
		cmp, err := vm.orderedCompare(a, b)
		if err != nil {
			vm.setErr(err)
			vm.push(BoolVal(false))
			return nextInstruction(ip), true
		}
		switch op {
		case bytecode.OpGt:
			result = cmp > 0
		case bytecode.OpGte:
			result = cmp >= 0
		case bytecode.OpLt:
			result = cmp < 0
		default:
			result = cmp <= 0
		}
	}
	if vm.trace != nil {
		vm.recordCheck(op, a, b, Value{}, result)
	}
	vm.push(BoolVal(result))
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
	if op != bytecode.OpIsNull && op != bytecode.OpIsNotNull {
		return 0, false
	}
	a := vm.pop()
	result := a.IsNull() == (op == bytecode.OpIsNull)
	if vm.trace != nil {
		vm.recordCheck(op, a, Value{}, Value{}, result)
	}
	vm.push(BoolVal(result))
	return nextInstruction(ip), true
}

func (vm *VM) evalStringOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	var result bool
	switch op {
	case bytecode.OpStartsWith, bytecode.OpEndsWith, bytecode.OpMatches:
	default:
		return 0, false
	}
	b, a := vm.pop(), vm.pop()
	switch op {
	case bytecode.OpStartsWith:
		result = strings.HasPrefix(vm.toStr(a), vm.toStr(b))
	case bytecode.OpEndsWith:
		result = strings.HasSuffix(vm.toStr(a), vm.toStr(b))
	default:
		var re *regexp.Regexp
		// Fast path: use the pre-compiled regex from the ruleset when the
		// pattern was a compile-time string literal.
		if b.Typ == TypeString && vm.rs != nil && vm.rs.Regexes != nil {
			re = vm.rs.Regexes[b.Str]
		}
		if re == nil {
			re = vm.regex(vm.toStr(b))
		}
		result = re != nil && re.MatchString(vm.toStr(a))
	}
	if vm.trace != nil {
		vm.recordCheck(op, a, b, Value{}, result)
	}
	vm.push(BoolVal(result))
	return nextInstruction(ip), true
}

func (vm *VM) evalCollectionOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	if op < bytecode.OpIn || op > bytecode.OpSupersetOf {
		return 0, false
	}
	b, a := vm.pop(), vm.pop()
	var result bool
	switch op {
	case bytecode.OpIn:
		result = vm.listContainsVal(b, a)
	case bytecode.OpNotIn:
		result = !vm.listContainsVal(b, a)
	case bytecode.OpContains:
		result = vm.listContainsVal(a, b)
	case bytecode.OpNotContains:
		result = !vm.listContainsVal(a, b)
	case bytecode.OpRetains:
		result = vm.listRetains(a, b)
	case bytecode.OpNotRetains:
		result = !vm.listRetains(a, b)
	case bytecode.OpVagueContains:
		result = vm.listVagueContains(a, b)
	case bytecode.OpSubsetOf:
		result = vm.listSubsetOf(a, b)
	default:
		result = vm.listSubsetOf(b, a)
	}
	if vm.trace != nil {
		vm.recordCheck(op, a, b, Value{}, result)
	}
	vm.push(BoolVal(result))
	return nextInstruction(ip), true
}

func (vm *VM) evalRangeOp(ip uint32, op bytecode.OpCode) (uint32, bool) {
	if op < bytecode.OpBetweenCC || op > bytecode.OpBetweenOC {
		return 0, false
	}
	hi, lo, val := vm.pop(), vm.pop(), vm.pop()
	includeLow := op == bytecode.OpBetweenCC || op == bytecode.OpBetweenCO
	includeHigh := op == bytecode.OpBetweenCC || op == bytecode.OpBetweenOC
	result, err := vm.betweenValues(val, lo, hi, includeLow, includeHigh)
	if err != nil {
		vm.setErr(err)
		vm.push(BoolVal(false))
		return nextInstruction(ip), true
	}
	if vm.trace != nil {
		vm.recordCheck(op, val, lo, hi, result)
	}
	vm.push(BoolVal(result))
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
		if i := vm.findLocal(iter.varName); i >= 0 {
			iter.prev = vm.locals[i].v
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

		vm.bindLocal(iter.varName, anyToValue(iter.items[0], vm.strPool))
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
			vm.bindLocal(iter.varName, anyToValue(iter.items[iter.index], vm.strPool))
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
				vm.bindLocal(iter.varName, iter.prev)
			} else {
				vm.unbindLocal(iter.varName)
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
		if i := vm.findLocal(iter.varName); i >= 0 {
			iter.prev = vm.locals[i].v
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

		vm.bindLocal(iter.varName, anyToValue(iter.items[0], vm.strPool))
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
			vm.bindLocal(iter.varName, anyToValue(iter.items[iter.index], vm.strPool))
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
			vm.bindLocal(iter.varName, iter.prev)
		} else {
			vm.unbindLocal(iter.varName)
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
	vm.bindLocal(vm.strPool.Get(arg), localValue(vm.pop()))
	return nextInstruction(ip), true
}
