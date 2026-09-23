package arbiter

import (
	"strings"
	"testing"
)

// TestConstSelfCycleRejected guards against `const C = C`, which used to
// recurse in validateExpr's ExprConstRef case until the process crashed with
// an unrecoverable "fatal error: stack overflow". Compile must instead return
// a normal diagnostic error.
func TestConstSelfCycleRejected(t *testing.T) {
	src := []byte(`const C = C
rule R { when { user.score >= C } then Deny {} }
`)
	_, err := Compile(src)
	if err == nil {
		t.Fatal("Compile: expected an error for a self-referential const, got nil")
	}
	if !strings.Contains(err.Error(), "constant cycle") {
		t.Fatalf("Compile: expected a %q diagnostic, got: %v", "constant cycle", err)
	}
}

// TestConstMutualCycleRejected guards the two-step cycle `const A = B` /
// `const B = A`.
func TestConstMutualCycleRejected(t *testing.T) {
	src := []byte(`const A = B
const B = A
rule R { when { user.score >= A } then Deny {} }
`)
	_, err := Compile(src)
	if err == nil {
		t.Fatal("Compile: expected an error for a mutually-referential const, got nil")
	}
	if !strings.Contains(err.Error(), "constant cycle") {
		t.Fatalf("Compile: expected a %q diagnostic, got: %v", "constant cycle", err)
	}
}

// TestConstCycleThroughExprRejected guards a cycle reached through a binary
// expression: `const A = B + 1` / `const B = A`.
func TestConstCycleThroughExprRejected(t *testing.T) {
	src := []byte(`const A = B + 1
const B = A
rule R { when { user.score >= A } then Deny {} }
`)
	_, err := Compile(src)
	if err == nil {
		t.Fatal("Compile: expected an error for a const cycle through an expression, got nil")
	}
	if !strings.Contains(err.Error(), "constant cycle") {
		t.Fatalf("Compile: expected a %q diagnostic, got: %v", "constant cycle", err)
	}
}

// TestConstNonCyclicChainCompiles guards against the cycle guard rejecting
// legitimate, non-cyclic const chains.
func TestConstNonCyclicChainCompiles(t *testing.T) {
	src := []byte(`const A = B
const B = 5
rule R { when { user.score >= A } then Deny {} }
`)
	if _, err := Compile(src); err != nil {
		t.Fatalf("Compile: unexpected error for a non-cyclic const chain: %v", err)
	}
}
