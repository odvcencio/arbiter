package vm_test

import (
	"fmt"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/vm"
)

func TestArithmeticPrecedenceAndRemainder(t *testing.T) {
	cases := []struct {
		name string
		expr string
		data map[string]any
		want float64
	}{
		{"multiply before add", "1 + 2 * 3", nil, 7},
		{"divide before subtract", "8 - 6 / 2", nil, 5},
		{"both products", "2 * 3 + 4 * 5", nil, 26},
		{"parentheses", "(1 + 2) * 3", nil, 9},
		{"left associative division", "24 / 3 * 2", nil, 16},
		{"left associative subtraction", "10 - 3 - 2", nil, 5},
		{"spaced literal remainder", "7 % 3", nil, 1},
		{"adjacent literal remainder", "7%3", nil, 1},
		{"left spaced remainder", "7 %3", nil, 1},
		{"right spaced remainder", "7% 3", nil, 1},
		{"remainder before add", "10 + 7 % 4", nil, 13},
		{"remainder and multiply associate left", "20 % 6 * 2", nil, 4},
		{"fractional remainder", "5.5 % 2", nil, 1.5},
		{"negative dividend", "-7 % 3", nil, -1},
		{"negative divisor", "7 % -3", nil, 1},
		{"runtime precedence", "a + b * c", map[string]any{"a": 1.0, "b": 2.0, "c": 3.0}, 7},
		{"runtime remainder", "a % b", map[string]any{"a": 5.5, "b": 2.0}, 1.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := fmt.Sprintf("rule R { when { true } then Out { value: %s } }", tc.expr)
			got, err := evalInteger(t, source, tc.data, vm.NumericDefault)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Params["value"] != tc.want {
				t.Fatalf("expression %q: got %+v, want value=%v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestPercentageQuantitiesUsePct(t *testing.T) {
	for _, expr := range []string{"50 pct == 50 pct", "25 pct + 25 pct == 50 pct"} {
		source := fmt.Sprintf("rule R { when { %s } then Out {} }", expr)
		program, err := arbiter.Compile([]byte(source))
		if err != nil {
			t.Fatalf("percentage comparison %q: %v", expr, err)
		}
		pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
		prepared, err := vm.NewPreparedEvaluator(program.Ruleset, pool, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := prepared.Eval(vm.DataFromMap(nil, pool))
		if err != nil || len(got) != 1 {
			t.Fatalf("percentage comparison %q: got %+v err=%v", expr, got, err)
		}
	}
	for _, expr := range []string{"50 %", "50%", "% 3", "a %", "%", "a % % b"} {
		source := fmt.Sprintf("rule R { when { true } then Out { value: %s } }", expr)
		if _, err := arbiter.Compile([]byte(source)); err == nil {
			t.Errorf("invalid expression %q must fail; use pct for percentage quantities", expr)
		}
	}
}

func TestPercentInCommentsAndStrings(t *testing.T) {
	source := []byte("// A 50 % comment is not an expression.\nrule R { when { true } then Out { value: \"50 %\" } } // 20 %\n")
	if _, err := arbiter.Compile(source); err != nil {
		t.Fatalf("percent inside a string/comment: %v", err)
	}
}
