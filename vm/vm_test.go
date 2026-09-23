// vm/vm_test.go
package vm

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/intern"
)

// Helper to build a simple ruleset with one rule.
func makeRuleset(pool *intern.Pool, condition []byte) *compiler.CompiledRuleset {
	return &compiler.CompiledRuleset{
		Constants:    pool,
		Instructions: condition,
		Rules: []compiler.RuleHeader{{
			NameIdx:      pool.String("test-rule"),
			Priority:     1,
			ConditionOff: 0,
			ConditionLen: uint32(len(condition)),
			ActionIdx:    0,
			FallbackIdx:  0,
		}},
		Actions: []compiler.ActionEntry{{
			NameIdx: pool.String("TestAction"),
		}},
	}
}

func TestEvalSimpleEquality(t *testing.T) {
	// Rule: name == "alice"
	pool := intern.NewPool()
	nameIdx := pool.String("name")
	aliceIdx := pool.String("alice")

	var code []byte
	code = compiler.Emit(code, compiler.OpLoadVar, 0, nameIdx)
	code = compiler.Emit(code, compiler.OpLoadStr, 0, aliceIdx)
	code = compiler.Emit(code, compiler.OpEq, 0, 0)
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeRuleset(pool, code)
	dc := DataFromMap(map[string]any{"name": "alice"}, NewStringPool(pool.Strings()))

	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Name != "test-rule" {
		t.Errorf("matched rule name: got %q, want %q", matched[0].Name, "test-rule")
	}
}

func TestEvalNoMatch(t *testing.T) {
	// Rule: name == "alice" but data has name=bob
	pool := intern.NewPool()
	nameIdx := pool.String("name")
	aliceIdx := pool.String("alice")

	var code []byte
	code = compiler.Emit(code, compiler.OpLoadVar, 0, nameIdx)
	code = compiler.Emit(code, compiler.OpLoadStr, 0, aliceIdx)
	code = compiler.Emit(code, compiler.OpEq, 0, 0)
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeRuleset(pool, code)
	sp := NewStringPool(pool.Strings())
	dc := DataFromMap(map[string]any{"name": "bob"}, sp)

	matched, err := evalWithPool(rs, dc, sp)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matched))
	}
}

func TestEvalNumericComparison(t *testing.T) {
	// Rule: age > 18
	pool := intern.NewPool()
	ageIdx := pool.String("age")
	eighteenIdx := pool.Number(18)

	var code []byte
	code = compiler.Emit(code, compiler.OpLoadVar, 0, ageIdx)
	code = compiler.Emit(code, compiler.OpLoadNum, 0, eighteenIdx)
	code = compiler.Emit(code, compiler.OpGt, 0, 0)
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeRuleset(pool, code)
	dc := DataFromMap(map[string]any{"age": 25.0}, NewStringPool(pool.Strings()))

	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
}

func TestEvalShortCircuitAnd(t *testing.T) {
	// Rule: false && (something) -- should skip the second operand
	pool := intern.NewPool()

	var code []byte
	code = compiler.Emit(code, compiler.OpLoadBool, 0, 0) // false
	// JumpIfFalse: skip 2 instructions (8 bytes) -- the LoadBool true + And
	code = compiler.Emit(code, compiler.OpJumpIfFalse, 0, 8)
	code = compiler.Emit(code, compiler.OpLoadBool, 0, 1) // true (skipped)
	code = compiler.Emit(code, compiler.OpAnd, 0, 0)      // skipped
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeRuleset(pool, code)
	dc := DataFromMap(map[string]any{}, NewStringPool(pool.Strings()))

	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches (short-circuit false), got %d", len(matched))
	}
}

func TestEvalStackOverflowReturnsError(t *testing.T) {
	pool := intern.NewPool()
	var code []byte
	for i := 0; i < maxStack+1; i++ {
		code = compiler.Emit(code, compiler.OpLoadBool, 0, 1)
	}
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeRuleset(pool, code)
	dc := DataFromMap(map[string]any{}, NewStringPool(pool.Strings()))

	if _, err := Eval(rs, dc); err == nil {
		t.Fatal("expected stack overflow to return an error")
	}
}

func TestRegexCompilationIsCached(t *testing.T) {
	pool := intern.NewPool()
	nameIdx := pool.String("name")
	patternIdx := pool.String("^[a-z]+$")

	var code []byte
	code = compiler.Emit(code, compiler.OpLoadVar, 0, nameIdx)
	code = compiler.Emit(code, compiler.OpLoadStr, 0, patternIdx)
	code = compiler.Emit(code, compiler.OpMatches, 0, 0)
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeRuleset(pool, code)
	sp := NewStringPool(pool.Strings())
	dc := DataFromMap(map[string]any{"name": "alice"}, sp)
	vm := newVM(rs, sp)

	if !vm.evalCondition(rs.Instructions, 0, uint32(len(code)), dc) {
		t.Fatal("expected first regex evaluation to match")
	}
	if len(vm.regexes) != 1 {
		t.Fatalf("expected one cached regex after first eval, got %d", len(vm.regexes))
	}
	vm.sp = 0
	if !vm.evalCondition(rs.Instructions, 0, uint32(len(code)), dc) {
		t.Fatal("expected second regex evaluation to match")
	}
	if len(vm.regexes) != 1 {
		t.Fatalf("expected cached regex count to stay at 1, got %d", len(vm.regexes))
	}
}

func TestRegexCacheIsBounded(t *testing.T) {
	pool := intern.NewPool()
	vm := newVM(&compiler.CompiledRuleset{Constants: pool}, NewStringPool(pool.Strings()))

	for i := 0; i < maxRegexCacheEntries+32; i++ {
		pattern := fmt.Sprintf("^value-%d$", i)
		if vm.regex(pattern) == nil {
			t.Fatalf("regex(%q) returned nil", pattern)
		}
	}
	if len(vm.regexes) > maxRegexCacheEntries {
		t.Fatalf("regex cache size = %d, want <= %d", len(vm.regexes), maxRegexCacheEntries)
	}
}

func TestEvalInstructionLimitReturnsError(t *testing.T) {
	pool := intern.NewPool()
	var code []byte
	code = compiler.Emit(code, compiler.OpLoadBool, 0, 0)
	code = compiler.Emit(code, compiler.OpJumpIfFalse, 0, 0)

	rs := makeRuleset(pool, code)
	dc := DataFromMap(map[string]any{}, NewStringPool(pool.Strings()))

	if _, err := Eval(rs, dc); err == nil || !strings.Contains(err.Error(), "instruction limit exceeded") {
		t.Fatalf("Eval error = %v, want instruction limit exceeded", err)
	}
}

// straightLineBoolCondition builds a jump-free condition that pushes a bool
// and negates it n times before matching: exactly n+2 steps, deterministic,
// with a constant stack depth of 1 throughout (no infinite loop, unlike
// TestEvalInstructionLimitReturnsError's jump-back trick).
func straightLineBoolCondition(n int) []byte {
	var code []byte
	code = compiler.Emit(code, compiler.OpLoadBool, 0, 1)
	for i := 0; i < n; i++ {
		code = compiler.Emit(code, compiler.OpNot, 0, 0)
	}
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)
	return code
}

// TestEvalInstructionBudgetIsPerRequestNotPerCondition guards the fix to
// vm/vm.go's instruction budget: it used to reset on every evalCondition
// call, so N rules could each individually stay under the cap while their
// combined cost was unbounded. Two rules here each cost well under half of
// maxInstructionsPerEval alone; only their sum exceeds it.
func TestEvalInstructionBudgetIsPerRequestNotPerCondition(t *testing.T) {
	pool := intern.NewPool()
	perRuleSteps := (maxInstructionsPerEval * 2) / 3 // > half, so 2x this exceeds the cap
	cond1 := straightLineBoolCondition(perRuleSteps)
	cond2 := straightLineBoolCondition(perRuleSteps)

	code := append(append([]byte{}, cond1...), cond2...)
	rs := &compiler.CompiledRuleset{
		Constants:    pool,
		Instructions: code,
		Rules: []compiler.RuleHeader{
			{
				NameIdx:      pool.String("rule-1"),
				ConditionOff: 0,
				ConditionLen: uint32(len(cond1)),
				ActionIdx:    0,
			},
			{
				NameIdx:      pool.String("rule-2"),
				ConditionOff: uint32(len(cond1)),
				ConditionLen: uint32(len(cond2)),
				ActionIdx:    0,
			},
		},
		Actions: []compiler.ActionEntry{{NameIdx: pool.String("TestAction")}},
	}
	dc := DataFromMap(map[string]any{}, NewStringPool(pool.Strings()))

	// Each condition alone is well under the cap.
	soloVM := newVM(rs, NewStringPool(pool.Strings()))
	if !soloVM.evalCondition(rs.Instructions, 0, uint32(len(cond1)), dc) {
		t.Fatalf("a single condition of %d steps unexpectedly failed to match", perRuleSteps)
	}
	if soloVM.err != nil {
		t.Fatalf("a single condition of %d steps unexpectedly errored: %v", perRuleSteps, soloVM.err)
	}

	// But evaluating both rules in one request must trip the shared budget.
	if _, err := Eval(rs, dc); err == nil || !strings.Contains(err.Error(), "instruction limit exceeded") {
		t.Fatalf("Eval error = %v, want instruction limit exceeded (budget must accumulate across rules)", err)
	}
}

func TestDynamicStringsCompareAgainstPooledStrings(t *testing.T) {
	pool := intern.NewPool()
	helloIdx := pool.String("hello")
	sp := NewStringPool(pool.Strings())
	vm := newVM(&compiler.CompiledRuleset{Constants: pool}, sp)

	if !vm.valEqual(StrVal(helloIdx), Value{Typ: TypeString, Any: "hello"}) {
		t.Fatal("expected pooled and runtime strings to compare equal")
	}
}

func TestStringAdditionDoesNotInternRuntimeResults(t *testing.T) {
	pool := intern.NewPool()
	helloIdx := pool.String("hello")
	sp := NewStringPool(pool.Strings())
	vm := newVM(&compiler.CompiledRuleset{Constants: pool}, sp)

	result, err := vm.addValues(StrVal(helloIdx), Value{Typ: TypeString, Any: " world"})
	if err != nil {
		t.Fatalf("addValues: %v", err)
	}
	got, ok := result.Any.(string)
	if !ok || got != "hello world" {
		t.Fatalf("string addition = %#v, want %q", result.Any, "hello world")
	}
	if len(sp.strs) != 1 {
		t.Fatalf("string pool grew to %d entries, want 1", len(sp.strs))
	}
}
