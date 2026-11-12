package vm

import (
	"math"
	"reflect"
	"testing"

	"m31labs.dev/arbiter/bytecode"
	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/intern"
)

// filledValue returns a Value with every field set to a non-zero value. It
// sets the fields through reflection, so a field added to Value later is
// filled too (or the test stops and asks for its kind to be handled).
func filledValue(t *testing.T) Value {
	t.Helper()
	var v Value
	rv := reflect.ValueOf(&v).Elem()
	for i := 0; i < rv.NumField(); i++ {
		field := rv.Field(i)
		switch field.Kind() {
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			field.SetUint(uint64(i + 1))
		case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
			field.SetInt(int64(i + 1))
		case reflect.Float64:
			field.SetFloat(float64(i) + 0.5)
		case reflect.Interface:
			field.Set(reflect.ValueOf("runtime"))
		default:
			t.Fatalf("Value.%s has kind %s: teach filledValue to set it", rv.Type().Field(i).Name, field.Kind())
		}
		if field.IsZero() {
			t.Fatalf("Value.%s was left at its zero value", rv.Type().Field(i).Name)
		}
	}
	return v
}

// push and pushFrom copy a Value onto the stack by naming each of its fields.
// A field added to Value and not added to those copies would be dropped
// without any error. This test fails when the field count changes, and when
// either copy loses a field.
func TestValueStackCopiesEveryField(t *testing.T) {
	if got := reflect.TypeOf(Value{}).NumField(); got != valueFields {
		t.Fatalf("Value has %d fields, but push and pushFrom copy %d: add the new field to both copies in vm.go, then update valueFields", got, valueFields)
	}
	v := filledValue(t)
	machine := &VM{}
	machine.push(v)
	machine.pushFrom(&v)
	if machine.sp != 2 {
		t.Fatalf("two pushes left the stack pointer at %d", machine.sp)
	}
	if !reflect.DeepEqual(machine.stack[0], v) {
		t.Fatalf("push dropped a field: pushed %+v, stack holds %+v", v, machine.stack[0])
	}
	if !reflect.DeepEqual(machine.stack[1], v) {
		t.Fatalf("pushFrom dropped a field: pushed %+v, stack holds %+v", v, machine.stack[1])
	}
}

// sameValue reports whether two Values hold the same fields. It compares Num
// by bits, because a NaN is not equal to itself.
func sameValue(a, b Value) bool {
	if math.Float64bits(a.Num) != math.Float64bits(b.Num) {
		return false
	}
	a.Num, b.Num = 0, 0
	return reflect.DeepEqual(a, b)
}

// The fast path answers == and != for null, bool and string values itself.
// It must give what valEqual gives for every pair of values, leave the
// operands in place when it declines, and decline exactly when a list, an
// object or a decimal is involved.
func TestFastEqualityMatchesValEqual(t *testing.T) {
	pool := intern.NewPool()
	xIdx, yIdx := pool.String("x"), pool.String("y")
	rs := makeRuleset(pool, nil)
	sp := NewStringPool(pool.Strings())
	machine := newVM(rs, sp)
	// A string interned after the VM took its snapshot of the pool.
	lateX := sp.Intern("late")
	values := map[string]Value{
		"null":                   NullVal(),
		"true":                   BoolVal(true),
		"false":                  BoolVal(false),
		"0":                      NumVal(0),
		"1":                      NumVal(1),
		"NaN":                    NumVal(math.NaN()),
		"constant x":             StrVal(xIdx),
		"constant y":             StrVal(yIdx),
		"runtime x":              {Typ: TypeString, Any: "x"},
		"runtime other":          {Typ: TypeString, Any: "other"},
		"runtime empty":          {Typ: TypeString, Any: ""},
		"interned late":          StrVal(lateX),
		"runtime late":           {Typ: TypeString, Any: "late"},
		"string index past pool": StrVal(lateX + 100),
		"constant list":          ListVal(0, 0),
		"dynamic list":           DynListVal([]any{"x"}),
		"object":                 ObjectVal(map[string]any{"x": 1.0}),
		"decimal 1":              DecimalVal(dec.MustParse("1", "")),
		"decimal 2":              DecimalVal(dec.MustParse("2", "")),
	}
	for aName, a := range values {
		for bName, b := range values {
			for _, op := range []bytecode.OpCode{bytecode.OpEq, bytecode.OpNeq} {
				want := machine.valEqual(a, b) == (op == bytecode.OpEq)
				machine.resetTransient()
				machine.push(a)
				machine.push(b)
				handled := machine.fastOp(op, 0, 0, nil)
				if scalar := a.Typ <= TypeString && b.Typ <= TypeString; handled != scalar {
					t.Fatalf("%s %v %s: fast path handled = %v, want %v", aName, op, bName, handled, scalar)
				}
				if !handled {
					if machine.sp != 2 || !sameValue(machine.stack[0], a) || !sameValue(machine.stack[1], b) {
						t.Fatalf("%s %v %s: the fast path declined but changed the operands", aName, op, bName)
					}
					continue
				}
				if machine.err != nil {
					t.Fatalf("%s %v %s: fast path set an error: %v", aName, op, bName, machine.err)
				}
				if machine.sp != 1 || !reflect.DeepEqual(machine.stack[0], BoolVal(want)) {
					t.Fatalf("%s %v %s: fast path left %+v (sp %d), want %+v", aName, op, bName, machine.stack[0], machine.sp, BoolVal(want))
				}
				if !reflect.DeepEqual(machine.stack[1], Value{}) {
					t.Fatalf("%s %v %s: fast path left the popped operand on the stack: %+v", aName, op, bName, machine.stack[1])
				}
			}
		}
	}
}

// Under tracing every comparison takes the general path, so it is recorded.
func TestFastEqualityDeclinesWhenTracing(t *testing.T) {
	pool := intern.NewPool()
	rs := makeRuleset(pool, nil)
	machine := newVM(rs, NewStringPool(pool.Strings()))
	machine.trace = &Trace{}
	machine.push(BoolVal(true))
	machine.push(BoolVal(true))
	if machine.fastOp(bytecode.OpEq, 0, 0, nil) {
		t.Fatal("the fast path handled == while tracing")
	}
	// The numeric profile does not turn the fast path off.
	machine.trace = nil
	machine.profile = NumericInteger
	if !machine.fastOp(bytecode.OpEq, 0, 0, nil) {
		t.Fatal("the fast path declined == under the integer profile")
	}
	if machine.sp != 1 || !reflect.DeepEqual(machine.stack[0], BoolVal(true)) {
		t.Fatalf("== under the integer profile left %+v (sp %d)", machine.stack[0], machine.sp)
	}
}
