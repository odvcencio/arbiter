package ir_test

import (
	"strings"
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/ir"
)

// TestLowerRejectsExcessiveExprNesting guards lowerExpr's recursion-depth
// guard: a source file with deeply nested parens must produce a bounded
// diagnostic instead of recursing until the goroutine stack overflows.
func TestLowerRejectsExcessiveExprNesting(t *testing.T) {
	const depth = 2100 // above ir.maxExprDepth (2000), still fast to parse
	src := "rule R { when { user.score > 0 } then D { v: " +
		strings.Repeat("(", depth) + "1" + strings.Repeat(")", depth) +
		" } }\n"

	parsed, err := arbiter.ParseSource([]byte(src))
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	_, err = ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	if err == nil {
		t.Fatal("Lower: expected an error for excessive expression nesting, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum depth") {
		t.Fatalf("Lower: expected a %q diagnostic, got: %v", "exceeds maximum depth", err)
	}
}

// TestLowerAcceptsModerateExprNesting guards against the depth guard
// rejecting ordinary, moderately nested expressions.
func TestLowerAcceptsModerateExprNesting(t *testing.T) {
	const depth = 50
	src := "rule R { when { user.score > 0 } then D { v: " +
		strings.Repeat("(", depth) + "1" + strings.Repeat(")", depth) +
		" } }\n"

	parsed, err := arbiter.ParseSource([]byte(src))
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	if _, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang); err != nil {
		t.Fatalf("Lower: unexpected error for moderate nesting: %v", err)
	}
}
