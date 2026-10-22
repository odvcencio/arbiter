package vm

import (
	"fmt"
	"math"
	"math/bits"

	"m31labs.dev/arbiter/compiler"
)

// NumericProfile selects the arithmetic rules the VM enforces at run time.
type NumericProfile uint8

const (
	// NumericDefault is IEEE float64 arithmetic (SPEC.md section 3.1).
	NumericDefault NumericProfile = iota
	// NumericInteger is the certified integer profile (SPEC.md, "Integer numeric
	// profile"). Every arithmetic operand and result must be an exact integer
	// with magnitude at most MaxSafeInteger. Division and average results may be
	// fractional, but only floor, ceil and round accept a fractional operand.
	NumericInteger
)

// MaxSafeInteger is 2^53, the largest magnitude the integer profile accepts.
const MaxSafeInteger = compiler.MaxSafeInteger

// SetNumericProfile selects the numeric profile for later evaluations.
func (e *Evaluator) SetNumericProfile(p NumericProfile) {
	if e != nil && e.vm != nil {
		e.vm.profile = p
	}
}

// SetNumericProfile selects the numeric profile for later evaluations.
func (e *PreparedEvaluator) SetNumericProfile(p NumericProfile) {
	if e != nil && e.vm != nil {
		e.vm.profile = p
	}
}

func integerError(format string, args ...any) error {
	return fmt.Errorf("integer profile: "+format, args...)
}

// asInteger returns v as an int64 when v is a number that is an exact integer
// with magnitude at most MaxSafeInteger.
func asInteger(op string, v Value) (int64, error) {
	if v.Typ != TypeNumber {
		return 0, integerError("%s requires integer operands, got a non-number", op)
	}
	n := v.Num
	if n != math.Trunc(n) || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, integerError("%s operand %v is not an integer", op, n)
	}
	if math.Abs(n) > MaxSafeInteger {
		return 0, integerError("%s operand %v exceeds 2^53", op, n)
	}
	return int64(n), nil
}

func integerResult(op string, n int64) (Value, error) {
	if n > MaxSafeInteger || n < -MaxSafeInteger {
		return NullVal(), integerError("%s result %d exceeds 2^53", op, n)
	}
	return NumVal(float64(n)), nil
}

// integerSum adds one aggregate item to a running sum, exactly.
func integerSum(sum float64, item Value) (float64, error) {
	x, err := asInteger("sum", item)
	if err != nil {
		return 0, err
	}
	// sum is always an exact integer of magnitude at most 2^53 here.
	total := int64(sum) + x
	if total > MaxSafeInteger || total < -MaxSafeInteger {
		return 0, integerError("sum result %d exceeds 2^53", total)
	}
	return float64(total), nil
}

// evalIntegerMathOp runs one math opcode under the integer profile. It pushes
// the result, or records an error and pushes null.
func (vm *VM) evalIntegerMathOp(op compiler.OpCode) {
	v, err := vm.integerMath(op)
	if err != nil {
		vm.setErr(err)
		v = NullVal()
	}
	vm.push(v)
}

func (vm *VM) integerMath(op compiler.OpCode) (Value, error) {
	switch op {
	case compiler.OpAbs:
		a := vm.pop()
		x, err := asInteger("abs", a)
		if err != nil {
			return NullVal(), err
		}
		if x < 0 {
			x = -x
		}
		return integerResult("abs", x)
	case compiler.OpRound, compiler.OpFloor, compiler.OpCeil:
		a := vm.pop()
		name := "round"
		if op == compiler.OpFloor {
			name = "floor"
		} else if op == compiler.OpCeil {
			name = "ceil"
		}
		if a.Typ != TypeNumber || math.IsNaN(a.Num) || math.IsInf(a.Num, 0) || math.Abs(a.Num) > MaxSafeInteger {
			return NullVal(), integerError("%s requires a finite number of magnitude at most 2^53", name)
		}
		var r float64
		switch op {
		case compiler.OpFloor:
			r = math.Floor(a.Num)
		case compiler.OpCeil:
			r = math.Ceil(a.Num)
		default:
			r = math.Round(a.Num)
		}
		return NumVal(r), nil
	}

	b, a := vm.pop(), vm.pop()
	name := integerOpName(op)
	x, err := asInteger(name, a)
	if err != nil {
		return NullVal(), err
	}
	y, err := asInteger(name, b)
	if err != nil {
		return NullVal(), err
	}
	switch op {
	case compiler.OpAdd:
		return integerResult(name, x+y)
	case compiler.OpSub:
		return integerResult(name, x-y)
	case compiler.OpMul:
		ax, ay := absUint(x), absUint(y)
		hi, lo := bits.Mul64(ax, ay)
		if hi != 0 || lo > MaxSafeInteger {
			return NullVal(), integerError("* result exceeds 2^53")
		}
		r := int64(lo)
		if (x < 0) != (y < 0) {
			r = -r
		}
		return NumVal(float64(r)), nil
	case compiler.OpDiv:
		if y == 0 {
			return NullVal(), integerError("division by zero")
		}
		// IEEE-754 division is correctly rounded on every target. For operands
		// of magnitude at most 2^53, floor of the quotient equals integer floor
		// division, so floor(a / b) is exact.
		return NumVal(float64(float64(x) / float64(y))), nil
	case compiler.OpMod:
		if y == 0 {
			return NullVal(), integerError("modulo by zero")
		}
		return integerResult(name, x%y)
	case compiler.OpMin:
		if x <= y {
			return NumVal(float64(x)), nil
		}
		return NumVal(float64(y)), nil
	case compiler.OpMax:
		if x >= y {
			return NumVal(float64(x)), nil
		}
		return NumVal(float64(y)), nil
	}
	return NullVal(), integerError("unsupported math opcode")
}

func absUint(x int64) uint64 {
	if x < 0 {
		return uint64(-x)
	}
	return uint64(x)
}

func integerOpName(op compiler.OpCode) string {
	switch op {
	case compiler.OpAdd:
		return "+"
	case compiler.OpSub:
		return "-"
	case compiler.OpMul:
		return "*"
	case compiler.OpDiv:
		return "/"
	case compiler.OpMod:
		return "%"
	case compiler.OpMin:
		return "min"
	case compiler.OpMax:
		return "max"
	}
	return "arithmetic"
}
