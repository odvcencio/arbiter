package vm

import (
	"strings"
	"testing"

	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/intern"
)

func TestPreparedEvaluatorClearsPoppedIteratorBackingAndRecoversAfterError(t *testing.T) {
	pool := intern.NewPool()
	aIdx, bIdx := pool.String("a"), pool.String("b")
	zeroIdx := pool.Number(0)
	var code []byte
	code = compiler.Emit(code, compiler.OpLoadVar, 0, aIdx)
	code = compiler.Emit(code, compiler.OpLoadVar, 0, bIdx)
	code = compiler.Emit(code, compiler.OpMul, 0, 0)
	code = compiler.Emit(code, compiler.OpLoadNum, 0, zeroIdx)
	code = compiler.Emit(code, compiler.OpGt, 0, 0)
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)
	rs := makeRuleset(pool, code)
	sp := NewStringPool(pool.Strings())
	prepared, err := NewPreparedEvaluator(rs, sp, nil)
	if err != nil {
		t.Fatal(err)
	}

	retained := make([]any, 1024)
	prepared.vm.iters = make([]iterState, 1, 4)
	prepared.vm.iters[0] = iterState{items: retained, prev: retained, hadPrev: true}
	prepared.vm.iters = prepared.vm.iters[:0]

	_, err = prepared.Eval(DataFromMap(map[string]any{"a": Value{Typ: TypeDecimal, Any: "bad"}, "b": 2}, sp))
	if err == nil || !strings.Contains(err.Error(), "rule test-rule: operator * requires numeric operands") {
		t.Fatalf("unexpected prepared error: %v", err)
	}
	for i, state := range prepared.vm.iters[:cap(prepared.vm.iters)] {
		if state.items != nil || state.prev != nil {
			t.Fatalf("iterator backing slot %d retained transient data: %+v", i, state)
		}
	}

	matched, err := prepared.Eval(DataFromMap(map[string]any{"a": 2, "b": 3}, sp))
	if err != nil || len(matched) != 1 {
		t.Fatalf("successful reuse after error = (%+v, %v)", matched, err)
	}
}

// TestPreparedEvaluatorGivesEachCallAFreshInstructionBudget guards against
// PreparedEvaluator's reused VM leaking instruction budget across calls: it
// is documented to serve "repeated decisions," so each Eval call is its own
// logical request and must not inherit budget consumption from prior calls.
func TestPreparedEvaluatorGivesEachCallAFreshInstructionBudget(t *testing.T) {
	pool := intern.NewPool()
	code := straightLineBoolCondition((maxInstructionsPerEval * 3) / 4)
	rs := makeRuleset(pool, code)
	sp := NewStringPool(pool.Strings())
	prepared, err := NewPreparedEvaluator(rs, sp, nil)
	if err != nil {
		t.Fatal(err)
	}

	dc := DataFromMap(map[string]any{}, sp)
	for i := 0; i < 3; i++ {
		if _, err := prepared.Eval(dc); err != nil {
			t.Fatalf("call %d: unexpected error (budget leaked across calls?): %v", i, err)
		}
	}
}
