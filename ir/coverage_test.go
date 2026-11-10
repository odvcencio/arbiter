package ir_test

import (
	"errors"
	"strings"
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/ir"
)

func coverage(t *testing.T, src string) error {
	t.Helper()
	parsed, err := arbiter.ParseSource([]byte(src))
	if err != nil {
		// ParseSource already runs the coverage check on the original bytes.
		return err
	}
	return ir.CheckSourceCoverage(parsed.Root, parsed.Source, parsed.Lang)
}

// Comments may sit on either side of a hidden comparison operator.
func TestCoverageAllowsCommentsAroundHiddenOperators(t *testing.T) {
	for _, expr := range []string{
		"x >= // note\n 5",
		"x /* c */ >= 5",
		"x >= /* c */ 5",
		"x # c\n != 5",
		"x /*a*/ <= /*b*/ 5",
		"x >= 5",
		"x<=5",
		"x != 5",
	} {
		src := "rule R priority 1 {\n    when { " + expr + " }\n    then ok {}\n}\n"
		if err := coverage(t, src); err != nil {
			t.Errorf("%q: %v", expr, err)
		}
		parsed, err := arbiter.ParseSource([]byte(src))
		if err != nil {
			t.Fatalf("%q: %v", expr, err)
		}
		if _, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang); err != nil {
			t.Errorf("%q: Lower: %v", expr, err)
		}
	}
}

func TestCoverageAllowsPercentInStringsAndComments(t *testing.T) {
	src := "# 85% of capacity; text @@ is fine in a comment\nrule R priority 1 {\n    when { x > 1 } # also 50% here\n    then ok { reason: \"above 85% for 1h @@\", }\n}\n/* block 100% */\n"
	if err := coverage(t, src); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageSkipsLeadingBOM(t *testing.T) {
	src := "\xEF\xBB\xBFrule R priority 1 {\n    when { x > 1 }\n    then ok {}\n}\n"
	if err := coverage(t, src); err != nil {
		t.Fatalf("BOM source: %v", err)
	}
	var cov *ir.SourceCoverageError
	err := coverage(t, "\xEF\xBB\xBFrule R { when { x > 1 @@ } then ok {} }\n")
	if !errors.As(err, &cov) {
		t.Fatalf("err = %v, want SourceCoverageError", err)
	}
	if cov.Line != 1 || cov.Col != 23 {
		t.Errorf("position = %d:%d, want 1:23 (BOM not counted)", cov.Line, cov.Col)
	}
}

func TestCoverageErrorsCarryExactPosition(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		kind      ir.CoverageKind
		line, col int
		contains  string
	}{
		{"when", "rule R priority 1 {\n    when { x > 1 @@ }\n    then ok {}\n}\n", ir.CoverageUnparsed, 2, 18, `"@@ }"`},
		{"then", "rule R priority 1 {\n    when { x > 1 }\n    then ok { a: 1 } @@\n}\n", ir.CoverageUnparsed, 3, 22, `"@@"`},
		{"duration unit", "expert rule R priority 0 {\n    when { any r in facts.F { r.x > 1 } } for 1w\n    then emit O { a: 1, }\n}\n", ir.CoverageUnparsed, 2, 47, `1w`},
		{"missing final brace", "rule A { when { x > 1 } then ok {} }\n\nrule B { when { x > 2 } then ok {\n", ir.CoverageUnparsed, 3, 1, `rule`},
		{"percent value", "rule R { when { true } then Out { value: % } }\n", ir.CoveragePercent, 1, 42, "use pct"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := coverage(t, tc.src)
			var cov *ir.SourceCoverageError
			if !errors.As(err, &cov) {
				t.Fatalf("err = %v (%T), want *ir.SourceCoverageError", err, err)
			}
			if cov.Kind != tc.kind || cov.Line != tc.line || cov.Col != tc.col {
				t.Errorf("got kind %d at %d:%d, want kind %d at %d:%d (%v)", cov.Kind, cov.Line, cov.Col, tc.kind, tc.line, tc.col, err)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error %q does not contain %q", err, tc.contains)
			}
		})
	}
}
