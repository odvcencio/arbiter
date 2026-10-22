package compiler

import (
	"fmt"
	"math"

	"m31labs.dev/arbiter/intern"
)

// MaxSafeInteger is 2^53, the largest magnitude the integer numeric profile
// accepts for a literal or an arithmetic result.
const MaxSafeInteger = 1 << 53

// NumericViolation is one place where compiled bytecode breaks the integer
// numeric profile.
type NumericViolation struct {
	Rule    string // rule that owns the bytecode, or "" when no rule owns it
	Offset  uint32 // byte offset into CompiledRuleset.Instructions
	Message string
}

func (v NumericViolation) String() string {
	if v.Rule != "" {
		return fmt.Sprintf("rule %s (offset %d): %s", v.Rule, v.Offset, v.Message)
	}
	return fmt.Sprintf("offset %d: %s", v.Offset, v.Message)
}

// CheckIntegerProfile statically checks a compiled ruleset against the integer
// numeric profile (SPEC.md, "Integer numeric profile"). It returns no
// violations when the ruleset conforms. The rules, all applied to the compiled
// instruction stream:
//
//  1. Every OpLoadNum constant is an exact integer with |v| <= 2^53.
//  2. OpLoadDec never appears, and no decimal appears in a pooled list.
//  3. Every pooled list number is an exact integer with |v| <= 2^53.
//  4. The instruction that follows every OpDiv, and every OpAggEnd that
//     computes an average, is OpFloor, OpCeil or OpRound.
//
// Rule 4 is exact for postfix bytecode. A quotient is pushed onto the value
// stack by OpDiv. The next instruction either consumes it or leaves it
// underneath a newly pushed operand. Only floor, ceil and round are accepted as
// the consumer, so no other instruction can use the quotient first.
func CheckIntegerProfile(rs *CompiledRuleset) []NumericViolation {
	if rs == nil {
		return nil
	}
	var out []NumericViolation
	owners := newInstructionOwners(rs)
	add := func(off uint32, format string, args ...any) {
		out = append(out, NumericViolation{Rule: owners.at(off), Offset: off, Message: fmt.Sprintf(format, args...)})
	}

	if pool := rs.Constants; pool != nil {
		for _, item := range pool.Lists() {
			switch item.Typ {
			case intern.TypeNumber:
				if msg := integerLiteralProblem(item.Num); msg != "" {
					add(0, "list literal %v: %s", item.Num, msg)
				}
			case intern.TypeDecimal:
				add(0, "decimal value in a list literal")
			}
		}
	}

	instrs := rs.Instructions
	var buf [InstrSize]byte
	opAt := func(i int) (OpCode, uint8, uint16, bool) {
		if i < 0 || i+InstrSize > len(instrs) {
			return 0, 0, 0, false
		}
		copy(buf[:], instrs[i:i+InstrSize])
		op, flags, arg := DecodeInstr(buf)
		return op, flags, arg, true
	}
	needsRounding := func(at int, what string) {
		next, _, _, ok := opAt(at + InstrSize)
		if ok && (next == OpFloor || next == OpCeil || next == OpRound) {
			return
		}
		add(uint32(at), "%s result must pass directly through floor, ceil or round", what)
	}
	for i := 0; i+InstrSize <= len(instrs); i += InstrSize {
		op, flags, arg, _ := opAt(i)
		switch op {
		case OpLoadNum:
			if rs.Constants == nil || int(arg) >= rs.Constants.NumberCount() {
				add(uint32(i), "number constant index %d is out of range", arg)
				continue
			}
			n := rs.Constants.GetNumber(arg)
			if msg := integerLiteralProblem(n); msg != "" {
				add(uint32(i), "number literal %v: %s", n, msg)
			}
		case OpLoadDec:
			add(uint32(i), "decimal literal is not allowed")
		case OpDiv:
			needsRounding(i, "division")
		case OpAggEnd:
			if flags == FlagAvg {
				needsRounding(i, "avg")
			}
		}
	}
	return out
}

func integerLiteralProblem(n float64) string {
	switch {
	case math.IsNaN(n) || math.IsInf(n, 0):
		return "not finite"
	case n != math.Trunc(n):
		return "not an integer"
	case math.Abs(n) > MaxSafeInteger:
		return "magnitude exceeds 2^53"
	}
	return ""
}

// instructionOwners maps instruction offsets to the rule that owns them.
type instructionOwners struct {
	spans []ownerSpan
}

type ownerSpan struct {
	off, end uint32
	rule     string
}

func newInstructionOwners(rs *CompiledRuleset) instructionOwners {
	var o instructionOwners
	name := func(idx uint16) string {
		if rs.Constants == nil || int(idx) >= rs.Constants.StringCount() {
			return ""
		}
		return rs.Constants.GetString(idx)
	}
	for _, rule := range rs.Rules {
		rn := name(rule.NameIdx)
		o.spans = append(o.spans, ownerSpan{rule.ConditionOff, rule.ConditionOff + rule.ConditionLen, rn})
		actions := []uint16{rule.ActionIdx}
		if rule.FallbackIdx != 0 {
			actions = append(actions, rule.FallbackIdx)
		}
		for _, idx := range actions {
			if int(idx) >= len(rs.Actions) {
				continue
			}
			for _, p := range rs.Actions[idx].Params {
				o.spans = append(o.spans, ownerSpan{p.ValueOff, p.ValueOff + p.ValueLen, rn})
			}
		}
	}
	return o
}

func (o instructionOwners) at(off uint32) string {
	for _, s := range o.spans {
		if off >= s.off && off < s.end {
			return s.rule
		}
	}
	return ""
}
