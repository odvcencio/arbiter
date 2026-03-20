// compiler/opcode_test.go
package compiler

import "testing"

func TestInstructionEncodeDecode(t *testing.T) {
	// Encode an instruction and decode it back
	instr := EncodeInstr(OpLoadStr, 0, 42)
	op, flags, arg := DecodeInstr(instr)
	if op != OpLoadStr {
		t.Errorf("opcode: got %d, want %d", op, OpLoadStr)
	}
	if flags != 0 {
		t.Errorf("flags: got %d, want 0", flags)
	}
	if arg != 42 {
		t.Errorf("arg: got %d, want 42", arg)
	}
}

func TestInstructionWithFlags(t *testing.T) {
	instr := EncodeInstr(OpIterBegin, FlagAny, 7)
	op, flags, arg := DecodeInstr(instr)
	if op != OpIterBegin || flags != FlagAny || arg != 7 {
		t.Errorf("got (%d, %d, %d), want (%d, %d, 7)", op, flags, arg, OpIterBegin, FlagAny)
	}
}

func TestInstructionWithAggregateFlags(t *testing.T) {
	instr := EncodeInstr(OpAggBegin, FlagAvg, 99)
	op, flags, arg := DecodeInstr(instr)
	if op != OpAggBegin || flags != FlagAvg || arg != 99 {
		t.Errorf("got (%d, %d, %d), want (%d, %d, 99)", op, flags, arg, OpAggBegin, FlagAvg)
	}
}

func TestAllOpcodesUnique(t *testing.T) {
	seen := map[OpCode]string{}
	opcodes := map[string]OpCode{
		"LoadStr": OpLoadStr, "LoadNum": OpLoadNum, "LoadBool": OpLoadBool,
		"LoadNull": OpLoadNull, "LoadVar": OpLoadVar,
		"Eq": OpEq, "Neq": OpNeq, "Gt": OpGt, "Gte": OpGte, "Lt": OpLt, "Lte": OpLte,
		"In": OpIn, "NotIn": OpNotIn, "Contains": OpContains, "NotContains": OpNotContains,
		"Retains": OpRetains, "NotRetains": OpNotRetains, "VagueContains": OpVagueContains,
		"SubsetOf": OpSubsetOf, "SupersetOf": OpSupersetOf,
		"StartsWith": OpStartsWith, "EndsWith": OpEndsWith, "Matches": OpMatches,
		"BetweenCC": OpBetweenCC, "BetweenOO": OpBetweenOO,
		"BetweenCO": OpBetweenCO, "BetweenOC": OpBetweenOC,
		"IsNull": OpIsNull, "IsNotNull": OpIsNotNull,
		"Add": OpAdd, "Sub": OpSub, "Mul": OpMul, "Div": OpDiv, "Mod": OpMod,
		"And": OpAnd, "Or": OpOr, "Not": OpNot,
		"JumpIfFalse": OpJumpIfFalse, "JumpIfTrue": OpJumpIfTrue,
		"IterBegin": OpIterBegin, "IterNext": OpIterNext, "IterEnd": OpIterEnd,
		"RuleMatch": OpRuleMatch,
		"AggBegin":  OpAggBegin, "AggAccum": OpAggAccum, "AggEnd": OpAggEnd,
		"SetLocal": OpSetLocal,
	}
	for name, op := range opcodes {
		if prev, ok := seen[op]; ok {
			t.Errorf("duplicate opcode value %d: %s and %s", op, prev, name)
		}
		seen[op] = name
	}
}
