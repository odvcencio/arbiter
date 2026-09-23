package arbiter

import (
	"strings"
	"testing"
)

// TestTemplateInlinesWithArgs verifies a parameterized template is inlined at
// its call sites with the arguments substituted for the parameters.
func TestTemplateInlinesWithArgs(t *testing.T) {
	src := []byte(`
template HighRisk(score, cap) = score >= cap

input { risk: { score: number } }
outcome Deny { reason: string }
rule Block {
	when { HighRisk(risk.score, 80) }
	then Deny { reason: "risk" }
}
`)
	prog, err := Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// score 90 >= 80 → match
	hi, err := Eval(prog, DataFromMap(map[string]any{"risk": map[string]any{"score": 90.0}}, prog))
	if err != nil {
		t.Fatalf("Eval hi: %v", err)
	}
	if len(hi) != 1 {
		t.Fatalf("score 90 >= cap 80 should match; got %d", len(hi))
	}

	// score 50 >= 80 → no match (proves the argument, not a constant, was substituted)
	lo, err := Eval(prog, DataFromMap(map[string]any{"risk": map[string]any{"score": 50.0}}, prog))
	if err != nil {
		t.Fatalf("Eval lo: %v", err)
	}
	if len(lo) != 0 {
		t.Fatalf("score 50 >= cap 80 should not match; got %d", len(lo))
	}
}

// TestTemplateReusedWithDifferentArgs verifies one template inlines independently
// at multiple call sites with different arguments.
func TestTemplateReusedWithDifferentArgs(t *testing.T) {
	src := []byte(`
template AtLeast(value, floor) = value >= floor

input { a: number b: number }
outcome Hit { which: string }
rule A { when { AtLeast(a, 10) } then Hit { which: "a" } }
rule B { when { AtLeast(b, 100) } then Hit { which: "b" } }
`)
	prog, err := Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// a=20 (>=10) matches A; b=50 (<100) does not match B.
	matched, err := Eval(prog, DataFromMap(map[string]any{"a": 20.0, "b": 50.0}, prog))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) != 1 || matched[0].Name != "A" {
		t.Fatalf("expected only rule A to match; got %d matches", len(matched))
	}
}

// TestTemplateNested verifies a template body may call another template, with
// each call's parameter bindings kept independent.
func TestTemplateNested(t *testing.T) {
	src := []byte(`
template Inc(x) = x + 1
template TwiceInc(y) = Inc(y) + Inc(y)

input { n: number }
outcome Hit { v: string }
rule R { when { TwiceInc(n) >= 10 } then Hit { v: "y" } }
`)
	prog, err := Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// TwiceInc(4) = (4+1)+(4+1) = 10 >= 10 → match
	m, err := Eval(prog, DataFromMap(map[string]any{"n": 4.0}, prog))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("TwiceInc(4)=10 >= 10 should match; got %d", len(m))
	}
	// TwiceInc(3) = 8 >= 10 → no match
	m2, err := Eval(prog, DataFromMap(map[string]any{"n": 3.0}, prog))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(m2) != 0 {
		t.Fatalf("TwiceInc(3)=8 >= 10 should not match; got %d", len(m2))
	}
}

func TestTemplateArgCountMismatch(t *testing.T) {
	src := []byte(`
template Need2(a, b) = a >= b
rule R { when { Need2(1) } then X {} }
`)
	_, err := Compile(src)
	if err == nil {
		t.Fatal("expected an argument-count error for Need2(1)")
	}
	if !strings.Contains(err.Error(), "expects 2") {
		t.Fatalf("expected arg-count error mentioning 'expects 2', got: %v", err)
	}
}

func TestTemplateRecursionRejected(t *testing.T) {
	src := []byte(`
template Loop(x) = Loop(x)
rule R { when { Loop(1) } then X {} }
`)
	_, err := Compile(src)
	if err == nil {
		t.Fatal("expected a recursion error for self-referential template")
	}
	if !strings.Contains(err.Error(), "recursive") {
		t.Fatalf("expected recursion error, got: %v", err)
	}
}
