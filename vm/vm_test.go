// vm/vm_test.go
package vm

import (
	"testing"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/intern"
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

	matched, err := EvalWithPool(rs, dc, sp)
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
