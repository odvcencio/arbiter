package compiler_test

import (
	"strings"
	"testing"

	arbiter "m31labs.dev/arbiter"
)

// nestedAddSource builds `rule R { when { true } then D { v: x + (x + (x + ...)) } }`
// with n levels of right-nested addition. Each operand is a var reference
// (not a literal), so constant folding cannot collapse the chain away before
// it reaches the compiler's stack-depth check.
func nestedAddSource(n int) []byte {
	expr := "x"
	for i := 0; i < n; i++ {
		expr = "x + (" + expr + ")"
	}
	return []byte("rule R { when { true } then D { v: " + expr + " } }\n")
}

// TestCompileRejectsExcessiveStackDepth guards against the bug where a
// deeply nested expression (300 chained `+` operators, right-associated via
// parens) compiles without error and only fails later, at eval time, with a
// VM stack-overflow error depending on runtime data. It must instead be
// rejected at compile time with a clear diagnostic.
func TestCompileRejectsExcessiveStackDepth(t *testing.T) {
	_, err := arbiter.Compile(nestedAddSource(300))
	if err == nil {
		t.Fatal("Compile: expected a stack-depth error for 300 nested additions, got nil")
	}
	if !strings.Contains(err.Error(), "stack depth") {
		t.Fatalf("Compile: expected a stack-depth diagnostic, got: %v", err)
	}
}

// TestCompileAcceptsModerateStackDepth guards against the check rejecting
// ordinary, moderately nested arithmetic.
func TestCompileAcceptsModerateStackDepth(t *testing.T) {
	if _, err := arbiter.Compile(nestedAddSource(20)); err != nil {
		t.Fatalf("Compile: unexpected error for 20 nested additions: %v", err)
	}
}

// TestEvalStackOverflowForNestedAddNowRejectedAtCompile is the eval-side
// half of the regression: before the compile-time check existed, this
// source compiled fine and only failed at Eval with a VM "stack overflow"
// error. It must now fail at Compile instead, never reaching Eval.
func TestEvalStackOverflowForNestedAddNowRejectedAtCompile(t *testing.T) {
	src := nestedAddSource(300)
	_, err := arbiter.Compile(src)
	if err == nil {
		t.Fatal("Compile: expected an error, got nil (regression: eval would fail unpredictably instead)")
	}
	if strings.Contains(err.Error(), "stack overflow at instruction") {
		t.Fatalf("Compile: got the VM's runtime stack-overflow error, want a compile-time diagnostic: %v", err)
	}
}
