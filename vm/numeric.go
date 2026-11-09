package vm

import (
	"fmt"
	"math"
	"math/bits"

	"m31labs.dev/arbiter/bytecode"
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

// MaxSafeInteger is 2^53 - 1, the largest magnitude the integer profile
// accepts. Every integer up to it is exact in float64, and rounding any larger
// integer to float64 gives a value above it, so an inexact input is always
// caught by the range check.
const MaxSafeInteger = bytecode.MaxSafeInteger

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
		return 0, integerError("%s operand %v exceeds 2^53-1", op, n)
	}
	return int64(n), nil
}

func integerResult(op string, n int64) (Value, error) {
	if n > MaxSafeInteger || n < -MaxSafeInteger {
		return NullVal(), integerError("%s result %d exceeds 2^53-1", op, n)
	}
	return NumVal(float64(n)), nil
}

// integerSum adds one aggregate item to a running sum, exactly.
func integerSum(sum float64, item Value) (float64, error) {
	x, err := asInteger("sum", item)
	if err != nil {
		return 0, err
	}
	// sum is always an exact integer of magnitude at most 2^53-1 here.
	total := int64(sum) + x
	if total > MaxSafeInteger || total < -MaxSafeInteger {
		return 0, integerError("sum result %d exceeds 2^53-1", total)
	}
	return float64(total), nil
}

// evalIntegerMathOp runs one math opcode under the integer profile. It pushes
// the result, or records an error and pushes null.
func (vm *VM) evalIntegerMathOp(op bytecode.OpCode) {
	v, err := vm.integerMath(op)
	if err != nil {
		vm.setErr(err)
		v = NullVal()
	}
	vm.push(v)
}

func (vm *VM) integerMath(op bytecode.OpCode) (Value, error) {
	switch op {
	case bytecode.OpAbs:
		a := vm.pop()
		x, err := asInteger("abs", a)
		if err != nil {
			return NullVal(), err
		}
		if x < 0 {
			x = -x
		}
		return integerResult("abs", x)
	case bytecode.OpRound, bytecode.OpFloor, bytecode.OpCeil:
		a := vm.pop()
		name := "round"
		if op == bytecode.OpFloor {
			name = "floor"
		} else if op == bytecode.OpCeil {
			name = "ceil"
		}
		if a.Typ != TypeNumber || math.IsNaN(a.Num) || math.IsInf(a.Num, 0) || math.Abs(a.Num) > MaxSafeInteger {
			return NullVal(), integerError("%s requires a finite number of magnitude at most 2^53-1", name)
		}
		if q, ok := a.Any.(quotient); ok {
			// Round the exact rational n/d with integer arithmetic. Rounding
			// the float64 quotient is wrong for large operands, for example
			// round(6755399441055745 / 4503599627370497).
			return NumVal(float64(q.round(op))), nil
		}
		var r float64
		switch op {
		case bytecode.OpFloor:
			r = math.Floor(a.Num)
		case bytecode.OpCeil:
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
	case bytecode.OpAdd:
		return integerResult(name, x+y)
	case bytecode.OpSub:
		return integerResult(name, x-y)
	case bytecode.OpMul:
		ax, ay := absUint(x), absUint(y)
		hi, lo := bits.Mul64(ax, ay)
		if hi != 0 || lo > MaxSafeInteger {
			return NullVal(), integerError("* result exceeds 2^53-1")
		}
		r := int64(lo)
		if (x < 0) != (y < 0) {
			r = -r
		}
		return NumVal(float64(r)), nil
	case bytecode.OpDiv:
		if y == 0 {
			return NullVal(), integerError("division by zero")
		}
		// IEEE-754 division is correctly rounded on every target. For operands
		// of magnitude at most 2^53-1, floor of the quotient equals integer floor
		// division, so floor(a / b) is exact.
		return quotientValue(x, y), nil
	case bytecode.OpMod:
		if y == 0 {
			return NullVal(), integerError("modulo by zero")
		}
		return integerResult(name, x%y)
	case bytecode.OpMin:
		if x <= y {
			return NumVal(float64(x)), nil
		}
		return NumVal(float64(y)), nil
	case bytecode.OpMax:
		if x >= y {
			return NumVal(float64(x)), nil
		}
		return NumVal(float64(y)), nil
	}
	return NullVal(), integerError("unsupported math opcode")
}

// quotient is the exact rational n/d carried by the result of a division under
// the integer profile. The Value's Num holds the correctly rounded float64.
type quotient struct{ n, d int64 }

func quotientValue(n, d int64) Value {
	return Value{Typ: TypeNumber, Num: float64(float64(n) / float64(d)), Any: quotient{n, d}}
}

// round applies floor, ceil or round (half away from zero, as math.Round) to
// n/d exactly. |n| <= 2^53-1 and |d| <= 2^53-1, so 2*rem cannot overflow.
func (q quotient) round(op bytecode.OpCode) int64 {
	n, d := q.n, q.d
	if d < 0 {
		n, d = -n, -d
	}
	fl := n / d
	rem := n % d
	if rem < 0 {
		fl--
		rem += d
	}
	// n/d = fl + rem/d with 0 <= rem < d.
	switch op {
	case bytecode.OpFloor:
		return fl
	case bytecode.OpCeil:
		if rem != 0 {
			return fl + 1
		}
		return fl
	}
	// Round half away from zero.
	switch {
	case 2*rem > d:
		return fl + 1
	case 2*rem < d:
		return fl
	case n >= 0:
		return fl + 1
	default:
		return fl
	}
}

// checkLoaded applies the integer profile to a number read from input, a local
// variable, an iterator item or a table cell. Passing an input through to an
// output needs no arithmetic, so the arithmetic guard alone would miss it.
func (vm *VM) checkLoaded(v Value) Value {
	if v.Typ != TypeNumber {
		return v
	}
	if _, err := asInteger("input", v); err != nil {
		vm.setErr(err)
		return NullVal()
	}
	return v
}

func absUint(x int64) uint64 {
	if x < 0 {
		return uint64(-x)
	}
	return uint64(x)
}

func integerOpName(op bytecode.OpCode) string {
	switch op {
	case bytecode.OpAdd:
		return "+"
	case bytecode.OpSub:
		return "-"
	case bytecode.OpMul:
		return "*"
	case bytecode.OpDiv:
		return "/"
	case bytecode.OpMod:
		return "%"
	case bytecode.OpMin:
		return "min"
	case bytecode.OpMax:
		return "max"
	}
	return "arithmetic"
}

// numResult builds a number result and replaces every NaN with math.NaN().
// Hardware NaN bits differ by architecture (amd64 produces 0xfff8..., arm64
// 0x7ff8...), so a raw NaN would make digests differ.
func numResult(f float64) Value {
	if f != f {
		return NumVal(math.NaN())
	}
	return NumVal(f)
}
