package vm_test

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/vm"
)

func evalInteger(t *testing.T, source string, data map[string]any, profile vm.NumericProfile) ([]vm.MatchedRule, error) {
	t.Helper()
	program, err := arbiter.Compile([]byte(source))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(program.Ruleset, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared.SetNumericProfile(profile)
	return prepared.Eval(vm.DataFromMap(data, pool))
}

func TestIntegerProfileRuntimeGuard(t *testing.T) {
	big := float64(1 << 53)
	cases := []struct {
		name    string
		expr    string
		data    map[string]any
		want    any    // expected value of param v
		wantErr string // substring of the deterministic error
	}{
		{name: "add", expr: "a + b", data: map[string]any{"a": 7.0, "b": 5.0}, want: 12.0},
		{name: "sub", expr: "a - b", data: map[string]any{"a": 7.0, "b": 12.0}, want: -5.0},
		{name: "mul", expr: "a * b", data: map[string]any{"a": -7.0, "b": 6.0}, want: -42.0},
		{name: "mod", expr: "a % b", data: map[string]any{"a": -7.0, "b": 3.0}, want: -1.0},
		{name: "floor div", expr: "floor(a / b)", data: map[string]any{"a": 7.0, "b": 2.0}, want: 3.0},
		{name: "ceil div", expr: "ceil(a / b)", data: map[string]any{"a": 7.0, "b": 2.0}, want: 4.0},
		{name: "round div", expr: "round(a / b)", data: map[string]any{"a": 7.0, "b": 2.0}, want: 4.0},
		{name: "floor of negative div", expr: "floor(a / b)", data: map[string]any{"a": -7.0, "b": 2.0}, want: -4.0},
		{name: "abs min max", expr: "max(abs(a), min(b, 3))", data: map[string]any{"a": -9.0, "b": 5.0}, want: 9.0},
		{name: "sum", expr: "sum(i.n for i in items)", data: map[string]any{"items": []any{map[string]any{"n": 1.0}, map[string]any{"n": 2.0}}}, want: 3.0},
		{name: "avg rounded", expr: "floor(avg(i.n for i in items))", data: map[string]any{"items": []any{map[string]any{"n": 1.0}, map[string]any{"n": 2.0}}}, want: 1.0},
		{name: "max safe integer sum is allowed", expr: "a + b", data: map[string]any{"a": big - 1, "b": 1.0}, want: big},

		{name: "fractional input", expr: "a + b", data: map[string]any{"a": 1.5, "b": 1.0}, wantErr: "not an integer"},
		{name: "operand above 2^53", expr: "a + b", data: map[string]any{"a": big * 2, "b": 0.0}, wantErr: "exceeds 2^53"},
		{name: "add overflows 2^53", expr: "a + b", data: map[string]any{"a": big, "b": 1.0}, wantErr: "exceeds 2^53"},
		{name: "mul overflows 2^53", expr: "a * b", data: map[string]any{"a": 94906267.0, "b": 94906267.0}, wantErr: "exceeds 2^53"},
		{name: "mul at 2^53+1 does not round down", expr: "a * b", data: map[string]any{"a": 3002399751580331.0, "b": 3.0}, wantErr: "exceeds 2^53"},
		{name: "division by zero", expr: "floor(a / b)", data: map[string]any{"a": 7.0, "b": 0.0}, wantErr: "division by zero"},
		{name: "modulo by zero", expr: "a % b", data: map[string]any{"a": 7.0, "b": 0.0}, wantErr: "modulo by zero"},
		{name: "unrounded division used in add", expr: "a / b + 1", data: map[string]any{"a": 7.0, "b": 2.0}, wantErr: "not an integer"},
		{name: "missing operand", expr: "a + b", data: map[string]any{"a": 1.0}, wantErr: "non-number"},
		{name: "fractional aggregate item", expr: "sum(i.n for i in items)", data: map[string]any{"items": []any{map[string]any{"n": 0.5}}}, wantErr: "not an integer"},
		{name: "aggregate overflow", expr: "sum(i.n for i in items)", data: map[string]any{"items": []any{map[string]any{"n": big}, map[string]any{"n": 1.0}}}, wantErr: "exceeds 2^53"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf("rule R { when { true } then Out { v: %s } }", tc.expr)
			got, err := evalInteger(t, src, tc.data, vm.NumericInteger)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got result=%v err=%v", tc.wantErr, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 || got[0].Params["v"] != tc.want {
				t.Fatalf("got %+v, want v=%v", got, tc.want)
			}
		})
	}
}

// The default profile keeps IEEE behavior: the same inputs that the integer
// profile rejects evaluate normally.
func TestDefaultProfileKeepsFloat64Arithmetic(t *testing.T) {
	got, err := evalInteger(t, "rule R { when { true } then Out { v: a / b + 1 } }", map[string]any{"a": 7.0, "b": 2.0}, vm.NumericDefault)
	if err != nil || len(got) != 1 || got[0].Params["v"] != 4.5 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestIntegerProfileOnEvaluator(t *testing.T) {
	program, err := arbiter.Compile([]byte("rule R { when { a / 2 + 1 > 1 } then Out {} }"))
	if err != nil {
		t.Fatal(err)
	}
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	ev := vm.NewEvaluator(program.Ruleset, pool)
	ev.SetNumericProfile(vm.NumericInteger)
	_, err = ev.EvalRuleCondition(program.Ruleset.Rules[0], vm.DataFromMap(map[string]any{"a": 5.0}, pool))
	if err == nil {
		t.Fatal("expected an error: a / 2 is fractional and feeds an add")
	}
}

// Floor of a correctly rounded quotient equals integer floor division for
// operands up to 2^53. This spot-checks that claim, which SPEC.md relies on.
func TestIntegerDivisionFloorMatchesIntegerArithmetic(t *testing.T) {
	src := "rule R { when { true } then Out { v: floor(a / b) } }"
	program, err := arbiter.Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	prepared, _ := vm.NewPreparedEvaluator(program.Ruleset, pool, nil)
	prepared.SetNumericProfile(vm.NumericInteger)
	seed := uint64(88172645463325252)
	next := func() uint64 { seed ^= seed << 13; seed ^= seed >> 7; seed ^= seed << 17; return seed }
	for i := 0; i < 20000; i++ {
		a := int64(next()%(1<<53)) - (1 << 52)
		b := int64(next()%(1<<(next()%53+1))) + 1
		if next()%2 == 0 {
			b = -b
		}
		want := a / b
		if (a%b != 0) && ((a < 0) != (b < 0)) {
			want--
		}
		got, err := prepared.Eval(vm.DataFromMap(map[string]any{"a": float64(a), "b": float64(b)}, pool))
		if err != nil || len(got) != 1 || got[0].Params["v"] != float64(want) {
			t.Fatalf("floor(%d / %d): got %+v err=%v, want %d", a, b, got, err, want)
		}
	}
}

func benchmarkMath(b *testing.B, profile vm.NumericProfile) {
	program, err := arbiter.Compile([]byte(`rule R { when { (a * 3 + b) % 1000 > floor(a / 7) - abs(b) } then Out { v: max(a, b) + min(a, b) - round(b / 4) } }`))
	if err != nil {
		b.Fatal(err)
	}
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(program.Ruleset, pool, nil)
	if err != nil {
		b.Fatal(err)
	}
	prepared.SetNumericProfile(profile)
	dc := vm.DataFromMap(map[string]any{"a": 12345.0, "b": 678.0}, pool)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prepared.Eval(dc); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPreparedMathDefault(b *testing.B) { benchmarkMath(b, vm.NumericDefault) }
func BenchmarkPreparedMathInteger(b *testing.B) { benchmarkMath(b, vm.NumericInteger) }
