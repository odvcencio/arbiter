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
		{"adjacent precedence", "1+2*3", nil, 7},
		{"adjacent left associative division", "24/3*2", nil, 16},
		{"divide before subtract", "8 - 6 / 2", nil, 5},
		{"both products", "2 * 3 + 4 * 5", nil, 26},
		{"runtime product chains", "a * b * c - b * c", map[string]any{"a": 2.0, "b": 3.0, "c": 4.0}, 12},
		{"parentheses", "(1 + 2) * 3", nil, 9},
		{"left associative division", "24 / 3 * 2", nil, 16},
		{"left associative subtraction", "10 - 3 - 2", nil, 5},
		{"adjacent subtraction", "10-3-2", nil, 5},
		{"left spaced subtraction", "10 -3 -2", nil, 5},
		{"right spaced subtraction", "10- 3- 2", nil, 5},
		{"subtract negative", "10--3", nil, 13},
		{"subtract negative chain", "-10--3-2", nil, -9},
		{"subtract before product", "10-3*2", nil, 4},
		{"subtract exponent", "1e-2-3e-3", nil, 0.007},
		{"negative exponent", "-1e-2", nil, -0.01},
		{"subtract variable", "10-a", map[string]any{"a": 3.0}, 7},
		{"variable minus literal", "a-3", map[string]any{"a": 10.0}, 7},
		{"multiply negative", "3*-2", nil, -6},
		// v1.12.0 grouped many chains of five or more terms from the right.
		{"five-term subtraction chain", "1000 - 2 - 3 - 4 - 5", nil, 986},
		{"six-term subtraction chain", "1000 - 2 - 3 - 4 - 5 - 6", nil, 980},
		{"nine-term subtraction chain", "1000 - 2 - 3 - 4 - 5 - 6 - 7 - 8 - 9", nil, 956},
		{"five-term division chain", "1000 / 2 / 5 / 4 / 5", nil, 5},
		{"eight-term division chain", "40320 / 2 / 3 / 4 / 5 / 6 / 7 / 8", nil, 1},
		{"long mixed sum and difference", "w + i + d + g - l - h - 14600", map[string]any{"w": 100000.0, "i": 1000.0, "d": 500.0, "g": 2000.0, "l": 2500.0, "h": 3000.0}, 83400},
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

func TestSubtractionWithUnits(t *testing.T) {
	for _, expr := range []string{
		"10 kg-3 kg == 7 kg", "10 kg--3 kg == 13 kg", "-10 kg-3 kg == -13 kg",
		"10 USD-3 USD == 7 USD", "10 USD--3 USD == 13 USD", "-10 USD-3 USD == -13 USD",
	} {
		source := fmt.Sprintf("rule R { when { %s } then Out {} }", expr)
		got, err := evalInteger(t, source, nil, vm.NumericDefault)
		if err != nil || len(got) != 1 {
			t.Errorf("expression %q: got %+v err=%v", expr, got, err)
		}
	}
}

func TestIncompleteArithmeticIsRejected(t *testing.T) {
	for _, expr := range []string{"10-", "-", "a-", "-a", "-(1+2)", "10+", "10*", "10/", "10%", "1--", "10 - - 3", "1e400", "-1e400"} {
		source := fmt.Sprintf("rule R { when { true } then Out { value: %s } }", expr)
		if _, err := arbiter.Compile([]byte(source)); err == nil {
			t.Errorf("invalid expression %q silently compiled", expr)
		}
	}
}
