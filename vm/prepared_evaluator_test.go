package vm_test

import (
	"reflect"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/vm"
)

func TestPreparedEvaluatorMatchesTagFilteredEvaluation(t *testing.T) {
	program, err := arbiter.Compile([]byte(`
tag "alpha"
tag "beta"

rule First priority 20 tag "alpha" tag "beta" {
	active_from 2026-01-10T00:00:00Z
	active_until 2026-01-20T00:00:00Z
	when { code matches "^[A-Z]{3}$" }
	then Route { choice: code }
	otherwise Route { choice: "fallback" }
}

rule Second priority 10 tag "alpha" {
	when { score >= 5 }
	then Score { value: score }
}

rule Untagged { when { true } then Open {} }
`))
	if err != nil {
		t.Fatal(err)
	}

	tagSets := [][]string{nil, []string{"alpha"}, []string{"alpha", "beta"}, []string{"alpha", "alpha"}, []string{"", ""}, []string{"missing"}}
	times := []string{"2026-01-09T23:59:59Z", "2026-01-15T00:00:00Z", "2026-01-20T00:00:00Z"}
	for _, tags := range tagSets {
		pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
		prepared, err := vm.NewPreparedEvaluator(program.Ruleset, pool, tags)
		if err != nil {
			t.Fatalf("prepare %q: %v", tags, err)
		}
		for _, now := range times {
			dc := vm.DataFromMap(map[string]any{"code": "ABC", "score": 7, "__now": now}, pool)
			want, wantErr := vm.EvalWithTagFilter(program.Ruleset, dc, pool, tags)
			got, gotErr := prepared.Eval(dc)
			if (gotErr == nil) != (wantErr == nil) || !reflect.DeepEqual(got, want) {
				t.Fatalf("tags=%q now=%s: prepared=(%+v,%v) legacy=(%+v,%v)", tags, now, got, gotErr, want, wantErr)
			}
		}
	}
}

func TestPreparedEvaluatorResultsSurviveLaterCalls(t *testing.T) {
	program, err := arbiter.Compile([]byte(`
tag "chosen"
rule Choice tag "chosen" {
	when { true }
	then Pick { value: value }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(program.Ruleset, pool, []string{"chosen"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := prepared.Eval(vm.DataFromMap(map[string]any{"value": "first"}, pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.Eval(vm.DataFromMap(map[string]any{"value": "second"}, pool)); err != nil {
		t.Fatal(err)
	}
	if got := first[0].Params["value"]; got != "first" {
		t.Fatalf("first result changed after evaluator reuse: %v", got)
	}
}

func TestPreparedEvaluatorFallbackAndPreparedTagOwnership(t *testing.T) {
	program, err := arbiter.Compile([]byte(`
tag "alpha"
tag "beta"
rule Choice tag "alpha" tag "beta" {
	when { code matches "^[A-Z]{3}$" }
	then Pick { value: code }
	otherwise Pick { value: "fallback" }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	tags := []string{"alpha", "beta"}
	prepared, err := vm.NewPreparedEvaluator(program.Ruleset, pool, tags)
	if err != nil {
		t.Fatal(err)
	}
	tags[0] = "missing"
	got, err := prepared.Eval(vm.DataFromMap(map[string]any{"code": "lower"}, pool))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Fallback || got[0].Params["value"] != "fallback" {
		t.Fatalf("prepared fallback changed after tag mutation: %+v", got)
	}
}

func TestPreparedEvaluatorRejectsNilRuleset(t *testing.T) {
	if _, err := vm.NewPreparedEvaluator(nil, nil, nil); err == nil || err.Error() != "nil ruleset" {
		t.Fatalf("unexpected error: %v", err)
	}
}
