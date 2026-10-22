package compiler

import (
	"fmt"
	"math"

	"m31labs.dev/arbiter/intern"
)

// MaxSafeInteger is 2^53 - 1, the largest magnitude the integer numeric profile
// accepts for a literal or an arithmetic result.
const MaxSafeInteger = 1<<53 - 1

// NumericViolation is one place where compiled bytecode breaks the integer
// numeric profile.
type NumericViolation struct {
	Rule    string // rule that owns the bytecode, or "" when no rule owns it
	Offset  uint32 // byte offset into CompiledRuleset.Instructions
	Message string
	// Warning marks a finding that does not fail the check but will fail at
	// run time (for example string concatenation).
	Warning bool
}

func (v NumericViolation) String() string {
	if v.Rule != "" {
		return fmt.Sprintf("rule %s (offset %d): %s", v.Rule, v.Offset, v.Message)
	}
	return fmt.Sprintf("offset %d: %s", v.Offset, v.Message)
}

// Errors returns the violations that are not warnings.
func Errors(vs []NumericViolation) []NumericViolation {
	var out []NumericViolation
	for _, v := range vs {
		if !v.Warning {
			out = append(out, v)
		}
	}
	return out
}

// CheckIntegerProfile statically checks a compiled ruleset against the integer
// numeric profile (SPEC.md, "Integer numeric profile"). It returns no
// violations when the ruleset conforms. The rules, all applied to the compiled
// instruction stream:
//
//  1. Every OpLoadNum constant is an exact integer with |v| <= 2^53-1.
//  2. OpLoadDec never appears, and no decimal appears in a pooled list.
//  3. Every pooled list number is an exact integer with |v| <= 2^53-1.
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

	for _, table := range rs.Tables {
		for r, row := range table.Rows {
			for c, cell := range row {
				col := ""
				if c < len(table.Columns) {
					col = table.Columns[c]
				}
				if msg := poolValueProblem(rs.Constants, cell); msg != "" {
					out = append(out, NumericViolation{Rule: "table " + table.Name, Message: fmt.Sprintf("row %d column %s: %s", r, col, msg)})
				}
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
		case OpLoadNull:
			if flags == intern.TypeList {
				if next, nflags, length, ok := opAt(i + InstrSize); ok && next == OpLoadNull && nflags == 0xFF {
					if msg := listProblem(rs.Constants, arg, length, 0); msg != "" {
						add(uint32(i), "list literal: %s", msg)
					}
				}
			}
		case OpAdd, OpMin, OpMax:
			// Heuristic: a string literal directly feeding the operator.
			p1, f1, _, ok1 := opAt(i - InstrSize)
			p2, f2, _, ok2 := opAt(i - 2*InstrSize)
			if (ok1 && p1 == OpLoadStr && f1 == 0) || (ok2 && p2 == OpLoadStr && f2 == 0) {
				out = append(out, NumericViolation{Rule: owners.at(uint32(i)), Offset: uint32(i), Warning: true,
					Message: "string operand: concatenation and string min/max are errors at run time under the integer profile"})
			}
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

// poolValueProblem reports why one pooled value breaks the profile, or "".
func poolValueProblem(pool *intern.Pool, v intern.PoolValue) string {
	switch v.Typ {
	case intern.TypeNumber:
		if msg := integerLiteralProblem(v.Num); msg != "" {
			return fmt.Sprintf("number %v: %s", v.Num, msg)
		}
	case intern.TypeDecimal:
		return "decimal value"
	case intern.TypeList:
		return listProblem(pool, v.ListIdx, v.ListLen, 1)
	}
	return ""
}

func listProblem(pool *intern.Pool, idx, length uint16, depth int) string {
	if pool == nil || depth > 8 {
		return ""
	}
	for _, item := range pool.GetList(idx, length) {
		if msg := poolValueProblem(pool, item); msg != "" {
			return msg
		}
	}
	return ""
}

func integerLiteralProblem(n float64) string {
	switch {
	case math.IsNaN(n) || math.IsInf(n, 0):
		return "not finite"
	case n != math.Trunc(n):
		return "not an integer"
	case math.Abs(n) > MaxSafeInteger:
		return "magnitude exceeds 2^53-1"
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
