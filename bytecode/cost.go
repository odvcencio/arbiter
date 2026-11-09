package bytecode

// DefaultCostLoopBound is the assumed iteration count for a quantifier or
// aggregate loop body when statically estimating worst-case evaluation cost —
// collection sizes are not known at compile time.
const DefaultCostLoopBound = 100

// intBits is the width of int: 64 on 64-bit targets, 32 on 32-bit targets such
// as TinyGo's wasm target.
const intBits = 32 << (^uint(0) >> 63)

// maxCost saturates the estimate so deeply-nested loops cannot overflow int.
// It is 1<<60 on 64-bit targets.
const maxCost = 1 << (intBits - 4)

// WorstCaseCost returns the largest worst-case instruction count among all rule
// conditions and the name of that rule. Each quantifier/aggregate loop body is
// treated as executing loopBound times (nested loops compound), giving a static
// upper bound for flagging expensive rules at authoring time — the cel-go
// EstimateCost ergonomic. A loopBound < 1 uses DefaultCostLoopBound. The
// estimate saturates at maxCost rather than overflowing.
func (rs *CompiledRuleset) WorstCaseCost(loopBound int) (int, string) {
	if rs == nil {
		return 0, ""
	}
	if loopBound < 1 {
		loopBound = DefaultCostLoopBound
	}
	var (
		worst int
		name  string
	)
	strs := rs.Constants.Strings()
	for _, rh := range rs.Rules {
		c := conditionCost(rs.Instructions, rh.ConditionOff, rh.ConditionLen, loopBound)
		if c > worst {
			worst = c
			if int(rh.NameIdx) < len(strs) {
				name = strs[rh.NameIdx]
			}
		}
	}
	return worst, name
}

// conditionCost sums the worst-case executions of each instruction in a
// condition byte range, multiplying by loopBound inside quantifier/aggregate
// loops (nested loops compound). It saturates at maxCost.
func conditionCost(instrs []byte, off, length uint32, loopBound int) int {
	end := off + length
	if end > uint32(len(instrs)) {
		end = uint32(len(instrs))
	}
	cost := 0
	mult := 1
	for ip := off; ip+InstrSize <= end; ip += InstrSize {
		var buf [InstrSize]byte
		copy(buf[:], instrs[ip:ip+InstrSize])
		op, _, _ := DecodeInstr(buf)
		switch op {
		case OpIterBegin, OpAggBegin:
			cost += mult
			if mult <= maxCost/loopBound {
				mult *= loopBound
			} else {
				mult = maxCost
			}
		case OpIterEnd, OpAggEnd:
			if mult >= loopBound {
				mult /= loopBound
			}
			cost += mult
		default:
			cost += mult
		}
		if cost > maxCost {
			cost = maxCost
		}
	}
	return cost
}
