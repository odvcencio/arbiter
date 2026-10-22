package vm_test

import (
	"fmt"
	"math"
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
	big := float64(1 << 53) // 2^53, one above the largest accepted magnitude
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
		{name: "max safe integer sum is allowed", expr: "a + b", data: map[string]any{"a": big - 2, "b": 1.0}, want: big - 1},

		{name: "fractional input", expr: "a + b", data: map[string]any{"a": 1.5, "b": 1.0}, wantErr: "not an integer"},
		{name: "operand above 2^53", expr: "a + b", data: map[string]any{"a": big * 2, "b": 0.0}, wantErr: "exceeds 2^53"},
		{name: "add overflows 2^53", expr: "a + b", data: map[string]any{"a": big - 1, "b": 1.0}, wantErr: "exceeds 2^53"},
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

func TestIntegerProfileInputBoundary(t *testing.T) {
	src := "rule R { when { true } then Out { v: a } }"
	for name, a := range map[string]any{
		"fractional passed through": 0.5,
		"int64 above 2^53-1":        int64(9007199254740993),
		"uint64 above 2^53-1":       uint64(9007199254740993),
		"int 2^53":                  int(1 << 53),
		"float 2^53":                float64(1 << 53),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := evalInteger(t, src, map[string]any{"a": a}, vm.NumericInteger); err == nil || !strings.Contains(err.Error(), "integer profile") {
				t.Fatalf("expected an integer profile error, got %v", err)
			}
		})
	}
	got, err := evalInteger(t, src, map[string]any{"a": int64(9007199254740991)}, vm.NumericInteger)
	if err != nil || len(got) != 1 || got[0].Params["v"] != float64(9007199254740991) {
		t.Fatalf("2^53-1 must pass: %+v %v", got, err)
	}
	if _, err := evalInteger(t, src, map[string]any{"a": 0.5}, vm.NumericDefault); err != nil {
		t.Fatalf("default profile must pass fractional input: %v", err)
	}
}

func TestIntegerProfileCountIgnoresItemValue(t *testing.T) {
	got, err := evalInteger(t, "rule R { when { true } then Out { n: count(i.n for i in items) } }",
		map[string]any{"items": []any{map[string]any{"n": 1}, map[string]any{"n": 3}}}, vm.NumericInteger)
	if err != nil || len(got) != 1 || got[0].Params["n"] != 2.0 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestIntegerProfileRoundIsExactForLargeQuotients(t *testing.T) {
	// float64(6755399441055745 / 4503599627370497) rounds to 1.5, so a float
	// round gives 2. The exact quotient is 1.49999999999999977..., so round is 1.
	data := map[string]any{"a": 6755399441055745.0, "b": 4503599627370497.0}
	got, err := evalInteger(t, "rule R { when { true } then Out { r: round(a / b), f: floor(a / b), c: ceil(a / b), n: round((0 - a) / b) } }", data, vm.NumericInteger)
	if err != nil || len(got) != 1 {
		t.Fatalf("err=%v got=%+v", err, got)
	}
	p := got[0].Params
	if p["r"] != 1.0 || p["f"] != 1.0 || p["c"] != 2.0 || p["n"] != -1.0 {
		t.Fatalf("exact rounding wrong: %+v", p)
	}
	// Half cases round away from zero, as math.Round does.
	for _, tc := range []struct{ a, b, want float64 }{{5, 2, 3}, {-5, 2, -3}, {7, 2, 4}, {-7, 2, -4}, {1, 3, 0}, {2, 3, 1}, {-1, 3, 0}, {-2, 3, -1}, {5, -2, -3}} {
		got, err := evalInteger(t, "rule R { when { true } then Out { r: round(a / b) } }", map[string]any{"a": tc.a, "b": tc.b}, vm.NumericInteger)
		if err != nil || got[0].Params["r"] != tc.want {
			t.Fatalf("round(%v/%v): got %+v err=%v want %v", tc.a, tc.b, got, err, tc.want)
		}
	}
}

func TestDefaultProfileCanonicalizesNaN(t *testing.T) {
	inf := 1e308
	got, err := evalInteger(t, "rule R { when { true } then Out { v: (a * a) - (a * a), w: (a * a) * 0, d: x / y } }", map[string]any{"a": inf, "x": 1.0, "y": 0.0}, vm.NumericDefault)
	if err != nil || len(got) != 1 {
		t.Fatalf("err=%v got=%+v", err, got)
	}
	for k, v := range got[0].Params {
		f, ok := v.(float64)
		if !ok || f == f {
			continue // not NaN
		}
		if bits := math.Float64bits(f); bits != math.Float64bits(math.NaN()) {
			t.Fatalf("%s: NaN bits %#x are not canonical", k, bits)
		}
	}
}

func TestBudgetResetOnEvaluator(t *testing.T) {
	program, err := arbiter.Compile([]byte("rule R { when { sum(i.n for i in items) > 0 } then Out {} }"))
	if err != nil {
		t.Fatal(err)
	}
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	ev := vm.NewEvaluator(program.Ruleset, pool)
	items := make([]any, 1000)
	for i := range items {
		items[i] = map[string]any{"n": 1.0}
	}
	dc := vm.DataFromMap(map[string]any{"items": items}, pool)
	for i := 0; i < 600; i++ {
		ev.ResetBudget()
		if _, err := ev.EvalRuleCondition(program.Ruleset.Rules[0], dc); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}

// Constant folding must give what the VM computes at run time. The old fold
// converted to int64, which is implementation-defined for large values, dropped
// fractions, and panicked on (7) % (0.5).
func TestConstantModFoldMatchesRuntime(t *testing.T) {
	cases := [][2]float64{{10000000000000000000, 7}, {7, 0.5}, {7.5, 2}, {-7.5, 2}, {1e300, 3}}
	for _, c := range cases {
		folded := fmt.Sprintf("rule R { when { true } then Out { v: (%v) %% (%v) } }", c[0], c[1])
		runtime := "rule R { when { true } then Out { v: (a) % (b) } }"
		got, err := evalInteger(t, folded, nil, vm.NumericDefault)
		if err != nil || len(got) != 1 {
			t.Fatalf("%v: folded eval err=%v got=%+v", c, err, got)
		}
		want, err := evalInteger(t, runtime, map[string]any{"a": c[0], "b": c[1]}, vm.NumericDefault)
		if err != nil || got[0].Params["v"] != want[0].Params["v"] || got[0].Params["v"] != math.Mod(c[0], c[1]) {
			t.Fatalf("%v: folded=%v runtime=%v math.Mod=%v", c, got[0].Params["v"], want[0].Params["v"], math.Mod(c[0], c[1]))
		}
	}
}
