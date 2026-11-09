// compiler/opcode.go
package compiler

import "m31labs.dev/arbiter/bytecode"

// The instruction set lives in package bytecode so that the VM and the bundle
// codec can use it without importing the compiler, the IR, or the parser.
// These aliases keep the compiler names working.

// OpCode represents a single bytecode operation.
type OpCode = bytecode.OpCode

const (
	OpLoadStr       = bytecode.OpLoadStr
	OpLoadNum       = bytecode.OpLoadNum
	OpLoadDec       = bytecode.OpLoadDec
	OpLoadBool      = bytecode.OpLoadBool
	OpLoadNull      = bytecode.OpLoadNull
	OpLoadVar       = bytecode.OpLoadVar
	OpEq            = bytecode.OpEq
	OpNeq           = bytecode.OpNeq
	OpGt            = bytecode.OpGt
	OpGte           = bytecode.OpGte
	OpLt            = bytecode.OpLt
	OpLte           = bytecode.OpLte
	OpIn            = bytecode.OpIn
	OpNotIn         = bytecode.OpNotIn
	OpContains      = bytecode.OpContains
	OpNotContains   = bytecode.OpNotContains
	OpRetains       = bytecode.OpRetains
	OpNotRetains    = bytecode.OpNotRetains
	OpVagueContains = bytecode.OpVagueContains
	OpSubsetOf      = bytecode.OpSubsetOf
	OpSupersetOf    = bytecode.OpSupersetOf
	OpStartsWith    = bytecode.OpStartsWith
	OpEndsWith      = bytecode.OpEndsWith
	OpMatches       = bytecode.OpMatches
	OpBetweenCC     = bytecode.OpBetweenCC
	OpBetweenOO     = bytecode.OpBetweenOO
	OpBetweenCO     = bytecode.OpBetweenCO
	OpBetweenOC     = bytecode.OpBetweenOC
	OpIsNull        = bytecode.OpIsNull
	OpIsNotNull     = bytecode.OpIsNotNull
	OpAdd           = bytecode.OpAdd
	OpSub           = bytecode.OpSub
	OpMul           = bytecode.OpMul
	OpDiv           = bytecode.OpDiv
	OpMod           = bytecode.OpMod
	OpAbs           = bytecode.OpAbs
	OpMin           = bytecode.OpMin
	OpMax           = bytecode.OpMax
	OpRound         = bytecode.OpRound
	OpFloor         = bytecode.OpFloor
	OpCeil          = bytecode.OpCeil
	OpAnd           = bytecode.OpAnd
	OpOr            = bytecode.OpOr
	OpNot           = bytecode.OpNot
	OpJumpIfFalse   = bytecode.OpJumpIfFalse
	OpJumpIfTrue    = bytecode.OpJumpIfTrue
	OpIterBegin     = bytecode.OpIterBegin
	OpIterNext      = bytecode.OpIterNext
	OpIterEnd       = bytecode.OpIterEnd
	OpRuleMatch     = bytecode.OpRuleMatch
	OpAggBegin      = bytecode.OpAggBegin
	OpAggAccum      = bytecode.OpAggAccum
	OpAggEnd        = bytecode.OpAggEnd
	OpSetLocal      = bytecode.OpSetLocal
	OpLookup        = bytecode.OpLookup
	OpTableField    = bytecode.OpTableField
)

// Iterator flags for OpIterBegin.
const (
	FlagAny  = bytecode.FlagAny
	FlagAll  = bytecode.FlagAll
	FlagNone = bytecode.FlagNone
)

// Aggregate flags for OpAggBegin/OpAggAccum/OpAggEnd.
const (
	FlagSum   = bytecode.FlagSum
	FlagCount = bytecode.FlagCount
	FlagAvg   = bytecode.FlagAvg
)

// InstrSize is the fixed width of every instruction in bytes.
const InstrSize = bytecode.InstrSize

// EncodeInstr encodes an instruction into 4 bytes: [opcode, flags, arg_lo, arg_hi].
func EncodeInstr(op OpCode, flags uint8, arg uint16) [InstrSize]byte {
	return bytecode.EncodeInstr(op, flags, arg)
}

// DecodeInstr decodes 4 bytes into opcode, flags, and arg.
func DecodeInstr(buf [InstrSize]byte) (OpCode, uint8, uint16) {
	return bytecode.DecodeInstr(buf)
}

// Emit appends an encoded instruction to a byte slice and returns the new slice.
func Emit(dst []byte, op OpCode, flags uint8, arg uint16) []byte {
	return bytecode.Emit(dst, op, flags, arg)
}
