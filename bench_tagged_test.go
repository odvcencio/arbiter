package arbiter

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/arbiter/vm"
)

// taggedBenchSource builds a ruleset shaped like one a host evaluates on every
// simulation tick: many rules in one file, split across a dozen tags, most of
// them testing the same few facts. A tagged evaluation selects about one rule
// in twelve.
func taggedBenchSource(rules, tags int) string {
	var b strings.Builder
	for t := 0; t < tags; t++ {
		fmt.Fprintf(&b, "tag \"t%d\"\n", t)
	}
	b.WriteString("outcome Act { n: number }\n")
	for r := 0; r < rules; r++ {
		fmt.Fprintf(&b, "rule R%d tag \"t%d\" { when { unit.kind == \"k%d\" and unit.ready == true and unit.distance < %d and run.revision >= 2 } then Act { n: %d } }\n",
			r, r%tags, r%7, 4+r%9, r)
	}
	return b.String()
}

type benchFacts struct {
	kind     vm.Value
	ready    bool
	distance float64
}

func (f *benchFacts) Get(key string) vm.Value {
	switch key {
	case "unit.kind":
		return f.kind
	case "unit.ready":
		return vm.BoolVal(f.ready)
	case "unit.distance":
		return vm.NumVal(f.distance)
	case "run.revision":
		return vm.NumVal(2)
	}
	return vm.NullVal()
}

// BenchmarkEvalTaggedSelection is the plain call a host makes every tick:
// Eval with one tag over a large ruleset and a freshly built map context.
func BenchmarkEvalTaggedSelection(b *testing.B) {
	prog, err := Compile([]byte(taggedBenchSource(128, 12)))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dc := DataFromMap(map[string]any{
			"unit": map[string]any{"kind": "k3", "ready": true, "distance": 5.0},
			"run":  map[string]any{"revision": 2},
		}, prog)
		if _, err := Eval(prog, dc, WithTags("t3")); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPreparedSharedFacts is a prepared evaluator over rules that all
// test the same facts through a struct-backed context.
func BenchmarkPreparedSharedFacts(b *testing.B) {
	prog, err := Compile([]byte(taggedBenchSource(19, 1)))
	if err != nil {
		b.Fatal(err)
	}
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, vm.NewStringPool(prog.Ruleset.Constants.Strings()), []string{"t0"})
	if err != nil {
		b.Fatal(err)
	}
	facts := &benchFacts{kind: vm.Value{Typ: vm.TypeString, Any: "k3"}, ready: true, distance: 5}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prepared.Eval(facts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPreparedSharedFactsMapContext is BenchmarkPreparedSharedFacts with
// the facts in a nested map. Reading a fact from a map context costs a path
// walk and a conversion, where the struct context above costs a switch.
func BenchmarkPreparedSharedFactsMapContext(b *testing.B) {
	prog, err := Compile([]byte(taggedBenchSource(19, 1)))
	if err != nil {
		b.Fatal(err)
	}
	pool := vm.NewStringPool(prog.Ruleset.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, pool, []string{"t0"})
	if err != nil {
		b.Fatal(err)
	}
	facts := vm.DataFromMap(map[string]any{
		"unit": map[string]any{"kind": "k3", "ready": true, "distance": 5.0},
		"run":  map[string]any{"revision": 2},
	}, pool)
	if matches, err := prepared.Eval(facts); err != nil || len(matches) == 0 {
		b.Fatalf("no rule matched: %d matches, %v", len(matches), err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prepared.Eval(facts); err != nil {
			b.Fatal(err)
		}
	}
}

// largeProgramSource builds a program of many rules that each test two facts
// of their own, plus one rule under the tag "only" that tests three other
// facts. Preparing the tag "only" selects one rule out of the whole program.
func largeProgramSource(rules int) string {
	var b strings.Builder
	b.WriteString("tag \"only\"\ntag \"rest\"\n")
	b.WriteString("outcome Act { n: number }\n")
	for r := 0; r < rules; r++ {
		fmt.Fprintf(&b, "rule R%d tag \"rest\" { when { zone%d.level >= %d and zone%d.open == true } then Act { n: %d } }\n",
			r, r, r%9, r, r)
	}
	b.WriteString("rule Only tag \"only\" { when { unit.kind == \"k3\" and unit.ready == true and unit.distance < 9 } then Act { n: 1 } }\n")
	return b.String()
}

// BenchmarkPreparedOneRuleOfLargeProgram prepares one rule out of a program
// of 1,001 rules whose other rules name 2,000 facts of their own. The cost of
// an evaluation must follow the one rule and the three facts it reads, not
// the size of the program.
func BenchmarkPreparedOneRuleOfLargeProgram(b *testing.B) {
	prog, err := Compile([]byte(largeProgramSource(1000)))
	if err != nil {
		b.Fatal(err)
	}
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, vm.NewStringPool(prog.Ruleset.Constants.Strings()), []string{"only"})
	if err != nil {
		b.Fatal(err)
	}
	facts := &benchFacts{kind: vm.Value{Typ: vm.TypeString, Any: "k3"}, ready: true, distance: 5}
	if matches, err := prepared.Eval(facts); err != nil || len(matches) != 1 {
		b.Fatalf("the prepared rule did not match: %d matches, %v", len(matches), err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prepared.Eval(facts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPreparedSingleUse builds a prepared evaluator, evaluates once and
// drops it, which is what segment evaluation does. An evaluator used once
// must not pay for state that only repeated evaluations use.
func BenchmarkPreparedSingleUse(b *testing.B) {
	prog, err := Compile([]byte(taggedBenchSource(19, 1)))
	if err != nil {
		b.Fatal(err)
	}
	pool := vm.NewStringPool(prog.Ruleset.Constants.Strings())
	facts := &benchFacts{kind: vm.Value{Typ: vm.TypeString, Any: "k3"}, ready: true, distance: 5}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, pool, nil)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := prepared.Eval(facts); err != nil {
			b.Fatal(err)
		}
	}
}
