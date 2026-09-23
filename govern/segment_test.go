package govern_test

import (
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
)

func TestCompiledSegmentEval(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`rule seg { when { user.plan == "enterprise" } then Match {} }`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	rs := prog.Ruleset

	seg := &govern.CompiledSegment{
		Name:    "enterprise",
		Source:  `user.plan == "enterprise"`,
		Ruleset: rs,
	}

	ok, err := seg.Eval(map[string]any{
		"user": map[string]any{
			"plan": "enterprise",
		},
	})
	if err != nil {
		t.Fatalf("Eval: unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected segment to match")
	}
}

// TestCompiledSegmentEvalSurfacesRuntimeError guards against a runtime error
// in the segment condition (for example, a type mismatch) being silently
// treated as "did not match".
func TestCompiledSegmentEvalSurfacesRuntimeError(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`rule seg { when { user.balance > 10.50 USD } then Match {} }`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	rs := prog.Ruleset

	seg := &govern.CompiledSegment{
		Name:    "bad_compare",
		Source:  `user.balance > 10.50 USD`,
		Ruleset: rs,
	}

	ok, err := seg.Eval(map[string]any{
		"user": map[string]any{
			"balance": "enterprise", // comparing a string to a decimal is a VM runtime error
		},
	})
	if err == nil {
		t.Fatal("Eval: expected a runtime error for a non-decimal operand, got nil")
	}
	if ok {
		t.Fatal("Eval: expected ok=false alongside the error")
	}
}
