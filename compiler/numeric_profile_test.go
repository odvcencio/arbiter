package compiler_test

import (
	"strings"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/compiler"
)

func violations(t *testing.T, src string) []compiler.NumericViolation {
	t.Helper()
	program, err := arbiter.Compile([]byte(src))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return compiler.CheckIntegerProfile(program.Ruleset)
}

func TestCheckIntegerProfile(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // substring of the first violation; "" means conforming
	}{
		{name: "integers and arithmetic", src: `rule R { when { a * 3 + b % 4 - 1 > 9007199254740992 } then Out { v: max(a, b) } }`},
		{name: "floor of division", src: `rule R { when { floor(a / b) > 2 } then Out { v: ceil(a / 2) + round(b / 3) } }`},
		{name: "avg through floor", src: `rule R { when { true } then Out { v: floor(avg(i.n for i in xs)) } }`},
		{name: "negative integer", src: `rule R { when { a > -5 } then Out {} }`},
		{name: "integer list", src: `rule R { when { a in [1, 2, 3] } then Out {} }`},

		{name: "non-integer literal", src: `rule R { when { a > 1.5 } then Out {} }`, want: "not an integer"},
		{name: "literal above 2^53", src: `rule R { when { a > 9007199254740994 } then Out {} }`, want: "exceeds 2^53"},
		{name: "fractional literal in action", src: `rule R { when { true } then Out { v: 0.25 } }`, want: "not an integer"},
		{name: "fractional list literal", src: `rule R { when { a in [1, 2.5] } then Out {} }`, want: "not an integer"},
		{name: "decimal literal", src: `rule R { when { amount > 10 USD } then Out {} }`, want: "decimal"},
		{name: "unrounded division in comparison", src: `rule R { when { a / b > 2 } then Out {} }`, want: "must pass directly through floor, ceil or round"},
		{name: "unrounded division in param", src: `rule R { when { true } then Out { v: a / b } }`, want: "must pass directly through floor, ceil or round"},
		{name: "division rounded after another operation", src: `rule R { when { floor(a / b + 1) > 2 } then Out {} }`, want: "must pass directly through floor, ceil or round"},
		{name: "unrounded avg", src: `rule R { when { avg(i.n for i in xs) > 2 } then Out {} }`, want: "avg result"},
		{name: "constant fractional quotient", src: `rule R { when { floor(7 / 2) > 2 } then Out {} }`, want: "not an integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := violations(t, tc.src)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("expected no violations, got %v", got)
				}
				return
			}
			if len(got) == 0 || !strings.Contains(got[0].String(), tc.want) {
				t.Fatalf("expected a violation containing %q, got %v", tc.want, got)
			}
			if got[0].Rule != "R" {
				t.Fatalf("violation not attributed to rule R: %+v", got[0])
			}
		})
	}
}
