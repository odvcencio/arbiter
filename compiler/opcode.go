// compiler/opcode.go
package compiler

import "encoding/binary"

// OpCode represents a single bytecode operation.
type OpCode uint8

const (
	// Stack operations
	OpLoadStr  OpCode = iota // push Constants.strings[arg]
	OpLoadNum                // push Constants.numbers[arg]
	OpLoadBool               // push true (arg=1) or false (arg=0)
	OpLoadNull               // push null
	OpLoadVar                // push DataContext.Get(Constants.strings[arg])

	// Comparison (pop 2, push bool)
	OpEq
	OpNeq
	OpGt
	OpGte
	OpLt
	OpLte

	// Collection (pop 2, push bool)
	OpIn
	OpNotIn
	OpContains
	OpNotContains
	OpRetains
	OpNotRetains
	OpVagueContains
	OpSubsetOf
	OpSupersetOf

	// String (pop 2, push bool)
	OpStartsWith
	OpEndsWith
	OpMatches

	// Range (pop 3: value, low, high — push bool)
	OpBetweenCC // [a,b]
	OpBetweenOO // (a,b)
	OpBetweenCO // [a,b)
	OpBetweenOC // (a,b]

	// Null (pop 1, push bool)
	OpIsNull
	OpIsNotNull

	// Math (pop 2, push number)
	OpAdd
	OpSub
	OpMul
	OpDiv
	OpMod

	// Logic
	OpAnd // pop 2, push bool
	OpOr  // pop 2, push bool
	OpNot // pop 1, push !bool

	// Control flow
	OpJumpIfFalse // skip arg bytes forward if top is false
	OpJumpIfTrue  // skip arg bytes forward if top is true

	// Quantifiers
	OpIterBegin // pop collection, begin iteration
	OpIterNext  // advance iterator or jump if exhausted
	OpIterEnd   // push final result, cleanup

	// Rule boundary
	OpRuleMatch // pop bool — if true, record rule as matched

	// Future-facing statement/value helpers.
	OpAggBegin // pop collection, begin aggregation
	OpAggAccum // pop value, accumulate
	OpAggEnd   // push aggregate result, cleanup
	OpSetLocal // pop value, store in locals
)

// Iterator flags for OpIterBegin.
const (
	FlagAny  uint8 = 0
	FlagAll  uint8 = 1
	FlagNone uint8 = 2
)

// Aggregate flags for OpAggBegin/OpAggAccum/OpAggEnd.
const (
	FlagSum   uint8 = 0
	FlagCount uint8 = 1
	FlagAvg   uint8 = 2
)

// InstrSize is the fixed width of every instruction in bytes.
const InstrSize = 4

// EncodeInstr encodes an instruction into 4 bytes: [opcode, flags, arg_lo, arg_hi].
func EncodeInstr(op OpCode, flags uint8, arg uint16) [InstrSize]byte {
	var buf [InstrSize]byte
	buf[0] = uint8(op)
	buf[1] = flags
	binary.LittleEndian.PutUint16(buf[2:], arg)
	return buf
}

// DecodeInstr decodes 4 bytes into opcode, flags, and arg.
func DecodeInstr(buf [InstrSize]byte) (OpCode, uint8, uint16) {
	return OpCode(buf[0]), buf[1], binary.LittleEndian.Uint16(buf[2:])
}

// Emit appends an encoded instruction to a byte slice and returns the new slice.
func Emit(dst []byte, op OpCode, flags uint8, arg uint16) []byte {
	instr := EncodeInstr(op, flags, arg)
	return append(dst, instr[:]...)
}
