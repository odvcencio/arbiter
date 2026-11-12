package vm_test

import (
	"testing"

	"m31labs.dev/arbiter/vm"
)

// countingContext counts how often each fact is read.
type countingContext struct {
	values map[string]vm.Value
	reads  map[string]int
}

func (c *countingContext) Get(key string) vm.Value {
	c.reads[key]++
	if v, ok := c.values[key]; ok {
		return v
	}
	return vm.NullVal()
}

// A prepared evaluator reads each context fact at most once per evaluation,
// however many rules test it, and reads it again on the next evaluation.
func TestPreparedEvaluatorReadsEachFactOncePerEvaluation(t *testing.T) {
	prog := compileRules(t, `
rule Low    { when { unit.hp < 10 and unit.ready == true } then Act { n: 1 } }
rule Mid    { when { unit.hp >= 10 and unit.hp < 50 and unit.ready == true } then Act { n: 2 } }
rule High   { when { unit.hp >= 50 and unit.ready == true } then Act { n: 3 } }
rule Always { when { unit.hp >= 0 } then Act { n: 4 } }
`)
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, vm.NewStringPool(prog.Ruleset.Constants.Strings()), nil)
	if err != nil {
		t.Fatal(err)
	}
	dc := &countingContext{values: map[string]vm.Value{"unit.hp": vm.NumVal(30), "unit.ready": vm.BoolVal(true)}, reads: map[string]int{}}
	names := func() string {
		t.Helper()
		matches, err := prepared.Eval(dc)
		if err != nil {
			t.Fatal(err)
		}
		out := ""
		for _, m := range matches {
			out += m.Name + ","
		}
		return out
	}
	if got := names(); got != "Mid,Always," {
		t.Fatalf("first evaluation matched %q", got)
	}
	if dc.reads["unit.hp"] != 1 || dc.reads["unit.ready"] != 1 {
		t.Fatalf("facts read more than once in one evaluation: %v", dc.reads)
	}
	// The next evaluation must see new values, not the cached ones.
	dc.values["unit.hp"] = vm.NumVal(80)
	if got := names(); got != "High,Always," {
		t.Fatalf("second evaluation used stale facts: %q", got)
	}
	if dc.reads["unit.hp"] != 2 || dc.reads["unit.ready"] != 2 {
		t.Fatalf("facts not re-read on the next evaluation: %v", dc.reads)
	}
	dc.values["unit.ready"] = vm.BoolVal(false)
	if got := names(); got != "Always," {
		t.Fatalf("third evaluation used stale facts: %q", got)
	}
}
