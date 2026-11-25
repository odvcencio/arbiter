package vm_test

import (
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/vm"
)

// countingContext counts how often each fact is read through Get. Facts are
// looked up by their full dotted name.
type countingContext struct {
	values map[string]vm.Value
	reads  map[string]int
}

func newCountingContext(values map[string]vm.Value) *countingContext {
	return &countingContext{values: values, reads: map[string]int{}}
}

func (c *countingContext) Get(key string) vm.Value {
	c.reads[key]++
	if v, ok := c.values[key]; ok {
		return v
	}
	return vm.NullVal()
}

// forget clears the read counts, so a test can count one evaluation.
func (c *countingContext) forget() { clear(c.reads) }

// indexedCountingContext is a countingContext that also implements
// vm.IndexedDataContext. It counts the reads that arrive through GetIndexed
// and checks that the index names the key.
type indexedCountingContext struct {
	countingContext
	pool    *vm.StringPool
	indexed map[string]int
	wrong   []string
}

func (c *indexedCountingContext) GetIndexed(idx uint16, key string) vm.Value {
	c.indexed[key]++
	if c.pool.Get(idx) != key {
		c.wrong = append(c.wrong, key)
	}
	if v, ok := c.values[key]; ok {
		return v
	}
	return vm.NullVal()
}

func newPrepared(t *testing.T, prog *arbiter.Program) *vm.PreparedEvaluator {
	t.Helper()
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, vm.NewStringPool(prog.Ruleset.Constants.Strings()), nil)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func evalNames(t *testing.T, prepared *vm.PreparedEvaluator, dc vm.DataContext) string {
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

const sharedFactRules = `
rule Low    { when { unit.hp < 10 and unit.ready == true } then Act { n: 1 } }
rule Mid    { when { unit.hp >= 10 and unit.hp < 50 and unit.ready == true } then Act { n: 2 } }
rule High   { when { unit.hp >= 50 and unit.ready == true } then Act { n: 3 } }
rule Always { when { unit.hp >= 0 } then Act { n: 4 } }
`

// A prepared evaluator that is used again reads each context fact once per
// evaluation, however many rules test it, and reads it again on the next
// evaluation. Its first evaluation runs before the fact cache exists and
// reads a fact each time a rule tests it; the results are the same.
func TestPreparedEvaluatorReadsEachFactOncePerEvaluation(t *testing.T) {
	prepared := newPrepared(t, compileRules(t, sharedFactRules))
	dc := newCountingContext(map[string]vm.Value{"unit.hp": vm.NumVal(30), "unit.ready": vm.BoolVal(true)})

	if got := evalNames(t, prepared, dc); got != "Mid,Always," {
		t.Fatalf("first evaluation matched %q", got)
	}
	if dc.reads["unit.hp"] < 2 {
		t.Fatalf("the first evaluation already cached facts: %v", dc.reads)
	}

	dc.forget()
	if got := evalNames(t, prepared, dc); got != "Mid,Always," {
		t.Fatalf("second evaluation matched %q", got)
	}
	if dc.reads["unit.hp"] != 1 || dc.reads["unit.ready"] != 1 {
		t.Fatalf("facts read more than once in one evaluation: %v", dc.reads)
	}

	// The next evaluation must see new values, not the cached ones.
	dc.forget()
	dc.values["unit.hp"] = vm.NumVal(80)
	if got := evalNames(t, prepared, dc); got != "High,Always," {
		t.Fatalf("third evaluation used stale facts: %q", got)
	}
	if dc.reads["unit.hp"] != 1 || dc.reads["unit.ready"] != 1 {
		t.Fatalf("facts not read exactly once on the next evaluation: %v", dc.reads)
	}
	dc.values["unit.ready"] = vm.BoolVal(false)
	if got := evalNames(t, prepared, dc); got != "Always," {
		t.Fatalf("fourth evaluation used stale facts: %q", got)
	}
}

const evalIntoSharedFactRules = `
rule Strike {
	when { unit.hp > 10 and unit.ready == true }
	then Act { kind: "strike", power: unit.hp * 2, ready: unit.ready }
	otherwise Act { kind: "wait", power: unit.hp, ready: unit.ready }
}

rule Report {
	when { unit.hp >= 0 }
	then Note { hp: unit.hp, role: unit.role }
}

rule Role {
	when { unit.role == "tank" and unit.ready == true }
	then Note { hp: unit.hp, role: unit.role }
}
`

// describeMatches renders EvalInto results with their parameters.
func describeMatches(prepared *vm.PreparedEvaluator, out *vm.Matches) string {
	var b strings.Builder
	for _, m := range out.List {
		b.WriteString(m.Name)
		if m.Fallback {
			b.WriteString("(otherwise)")
		}
		for _, p := range m.Params {
			b.WriteString(" " + p.Key + "=")
			switch p.Value.Typ {
			case vm.TypeNumber:
				b.WriteString(strconv.FormatFloat(p.Value.Num, 'g', -1, 64))
			case vm.TypeBool:
				b.WriteString(strconv.FormatBool(p.Value.Bool))
			case vm.TypeString:
				b.WriteString(prepared.String(p.Value))
			default:
				b.WriteString("?")
			}
		}
		b.WriteString("; ")
	}
	return b.String()
}

// EvalInto reads each fact once per call too, across conditions and action
// parameters, with and without a trace, and the next call reads fresh values.
func TestPreparedEvaluatorEvalIntoReadsEachFactOncePerCall(t *testing.T) {
	for _, traced := range []bool{false, true} {
		name := "untraced"
		if traced {
			name = "traced"
		}
		t.Run(name, func(t *testing.T) {
			prepared := newPrepared(t, compileRules(t, evalIntoSharedFactRules))
			dc := newCountingContext(map[string]vm.Value{
				"unit.hp":    vm.NumVal(30),
				"unit.ready": vm.BoolVal(true),
				"unit.role":  {Typ: vm.TypeString, Any: "tank"},
			})
			var out vm.Matches
			if traced {
				out.Trace = &vm.Trace{}
			}
			evalInto := func() string {
				t.Helper()
				if err := prepared.EvalInto(dc, &out); err != nil {
					t.Fatal(err)
				}
				return describeMatches(prepared, &out)
			}
			const first = "Strike kind=strike power=60 ready=true; Report hp=30 role=tank; Role hp=30 role=tank; "
			if got := evalInto(); got != first {
				t.Fatalf("first call: %q", got)
			}
			dc.forget()
			if got := evalInto(); got != first {
				t.Fatalf("second call: %q", got)
			}
			for _, fact := range []string{"unit.hp", "unit.ready", "unit.role"} {
				if dc.reads[fact] != 1 {
					t.Fatalf("%s read %d times in one EvalInto call, want 1 (all reads: %v)", fact, dc.reads[fact], dc.reads)
				}
			}
			if traced && len(out.Trace.Rules) != 3 {
				t.Fatalf("trace recorded %d rules, want 3", len(out.Trace.Rules))
			}

			dc.forget()
			dc.values["unit.hp"] = vm.NumVal(4)
			dc.values["unit.role"] = vm.Value{Typ: vm.TypeString, Any: "scout"}
			const changed = "Strike(otherwise) kind=wait power=4 ready=true; Report hp=4 role=scout; "
			if got := evalInto(); got != changed {
				t.Fatalf("call after the facts changed: %q, want %q", got, changed)
			}
			for _, fact := range []string{"unit.hp", "unit.ready", "unit.role"} {
				if dc.reads[fact] != 1 {
					t.Fatalf("%s read %d times after the facts changed, want 1", fact, dc.reads[fact])
				}
			}
		})
	}
}

const failingRules = `
rule Before { when { unit.hp > 10 } then Act { n: unit.hp } }
rule Broken { when { unit.hp * unit.factor > 0 } then Act { n: 2 } }
rule After  { when { unit.hp > 10 and unit.ready == true } then Act { n: 3 } }
`

// An evaluation that returns an error must still forget the facts it read.
// The next evaluation reads every fact again and sees the new values.
func TestPreparedEvaluatorForgetsFactsAfterAnError(t *testing.T) {
	notANumber := vm.Value{Typ: vm.TypeDecimal, Any: "not a decimal"}
	for _, into := range []bool{false, true} {
		name := "Eval"
		if into {
			name = "EvalInto"
		}
		t.Run(name, func(t *testing.T) {
			prepared := newPrepared(t, compileRules(t, failingRules))
			dc := newCountingContext(map[string]vm.Value{
				"unit.hp":     vm.NumVal(30),
				"unit.factor": vm.NumVal(2),
				"unit.ready":  vm.BoolVal(true),
			})
			var out vm.Matches
			eval := func() (string, error) {
				if into {
					if err := prepared.EvalInto(dc, &out); err != nil {
						return "", err
					}
					names := ""
					for _, m := range out.List {
						names += m.Name + ","
					}
					return names, nil
				}
				matches, err := prepared.Eval(dc)
				if err != nil {
					return "", err
				}
				names := ""
				for _, m := range matches {
					names += m.Name + ","
				}
				return names, nil
			}
			// Two good evaluations: the second one runs with the fact cache.
			for call := 0; call < 2; call++ {
				if got, err := eval(); err != nil || got != "Before,Broken,After," {
					t.Fatalf("good evaluation %d: %q, %v", call, got, err)
				}
			}

			// The second rule fails after the first rule has read unit.hp.
			dc.forget()
			dc.values["unit.factor"] = notANumber
			if got, err := eval(); err == nil || !strings.Contains(err.Error(), "rule Broken") {
				t.Fatalf("evaluation with a bad fact: %q, %v", got, err)
			}
			if dc.reads["unit.hp"] != 1 {
				t.Fatalf("unit.hp read %d times before the error, want 1", dc.reads["unit.hp"])
			}

			// Every fact changes. Nothing of the failed evaluation may remain.
			dc.forget()
			dc.values["unit.hp"] = vm.NumVal(5)
			dc.values["unit.factor"] = vm.NumVal(3)
			dc.values["unit.ready"] = vm.BoolVal(false)
			if got, err := eval(); err != nil || got != "Broken," {
				t.Fatalf("evaluation after the error: %q, %v (want only Broken: hp is now 5)", got, err)
			}
			if dc.reads["unit.hp"] != 1 || dc.reads["unit.factor"] != 1 {
				t.Fatalf("facts not read exactly once after the error: %v", dc.reads)
			}

			// A failing evaluation that follows a good one also starts clean.
			dc.forget()
			dc.values["unit.hp"] = vm.NumVal(40)
			dc.values["unit.factor"] = notANumber
			if _, err := eval(); err == nil {
				t.Fatal("second evaluation with a bad fact did not fail")
			}
			dc.forget()
			dc.values["unit.factor"] = vm.NumVal(1)
			dc.values["unit.ready"] = vm.BoolVal(true)
			if got, err := eval(); err != nil || got != "Before,Broken,After," {
				t.Fatalf("evaluation after the second error: %q, %v", got, err)
			}
			if dc.reads["unit.hp"] != 1 || dc.reads["unit.ready"] != 1 {
				t.Fatalf("facts not read exactly once after the second error: %v", dc.reads)
			}
		})
	}
}

// With an IndexedDataContext the evaluator reads through GetIndexed, also
// once per evaluation.
func TestPreparedEvaluatorReadsIndexedFactsOncePerEvaluation(t *testing.T) {
	prog := compileRules(t, sharedFactRules)
	pool := vm.NewStringPool(prog.Ruleset.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	dc := &indexedCountingContext{
		countingContext: *newCountingContext(map[string]vm.Value{"unit.hp": vm.NumVal(30), "unit.ready": vm.BoolVal(true)}),
		pool:            pool,
		indexed:         map[string]int{},
	}
	var out vm.Matches
	names := func() string {
		t.Helper()
		if err := prepared.EvalInto(dc, &out); err != nil {
			t.Fatal(err)
		}
		got := ""
		for _, m := range out.List {
			got += m.Name + ","
		}
		return got
	}
	if got := names(); got != "Mid,Always," {
		t.Fatalf("first evaluation matched %q", got)
	}
	if dc.indexed["unit.hp"] < 2 {
		t.Fatalf("the first evaluation already cached facts: %v", dc.indexed)
	}

	clear(dc.indexed)
	dc.forget()
	if got := names(); got != "Mid,Always," {
		t.Fatalf("second evaluation matched %q", got)
	}
	if dc.indexed["unit.hp"] != 1 || dc.indexed["unit.ready"] != 1 {
		t.Fatalf("facts read through GetIndexed more than once in one evaluation: %v", dc.indexed)
	}
	if len(dc.reads) != 0 {
		t.Fatalf("an indexed context was read through Get: %v", dc.reads)
	}

	clear(dc.indexed)
	dc.values["unit.hp"] = vm.NumVal(80)
	dc.values["unit.ready"] = vm.BoolVal(false)
	if got := names(); got != "Always," {
		t.Fatalf("third evaluation used stale facts: %q", got)
	}
	if dc.indexed["unit.hp"] != 1 || dc.indexed["unit.ready"] != 1 {
		t.Fatalf("facts not read exactly once on the next evaluation: %v", dc.indexed)
	}
	if len(dc.wrong) != 0 {
		t.Fatalf("GetIndexed was given an index that does not name the key: %v", dc.wrong)
	}
}

// The quantifier variable item and the let binding total are locals. Their
// names sit next to the facts items, item_limit, total_floor and totals,
// which are cached. A local must never be answered from the cache, and a
// fact must never be answered from a local: each element of the list has its
// own cost, and each rule computes its own total.
const localBesideFactRules = `
rule Costly {
	when { any item in items { item.cost > item_limit } }
	then Act { n: 1 }
}

rule AllCheap {
	when { all item in items { item.cost <= item_limit } }
	then Act { n: 2 }
}

rule Sum {
	when {
		let total = base + bonus
		total > total_floor
	}
	then Act { n: total }
}

rule Double {
	when {
		let total = base * 2
		total > total_floor
	}
	then Act { n: total }
}

rule ItemFact {
	when { item.cost == 7 and item_limit == 5 and total_floor == 15 }
	then Act { n: 5 }
}

rule ItemFactAgain {
	when { item.cost == 7 }
	then Act { n: 6 }
}
`

func TestPreparedEvaluatorKeepsLocalsApartFromCachedFacts(t *testing.T) {
	prepared := newPrepared(t, compileRules(t, localBesideFactRules))
	items := func(costs ...float64) vm.Value {
		list := make([]any, len(costs))
		for i, cost := range costs {
			list[i] = map[string]any{"cost": cost}
		}
		return vm.DynListVal(list)
	}
	dc := newCountingContext(map[string]vm.Value{
		"items":       items(3, 9),
		"item_limit":  vm.NumVal(5),
		"base":        vm.NumVal(10),
		"bonus":       vm.NumVal(1),
		"total_floor": vm.NumVal(15),
		// Facts named like the locals. Inside the quantifier and after the
		// let, the local wins; outside, the fact is read from the context.
		"item":      vm.ObjectVal(map[string]any{"cost": 1000.0}),
		"item.cost": vm.NumVal(7),
		"total":     vm.NumVal(999),
	})
	eval := func() string {
		t.Helper()
		var out vm.Matches
		if err := prepared.EvalInto(dc, &out); err != nil {
			t.Fatal(err)
		}
		return describeMatches(prepared, &out)
	}
	// Costly: 9 > 5. AllCheap: no. Sum: 11 > 15 is false. Double: 20 > 15.
	const want = "Costly n=1; Double n=20; ItemFact n=5; ItemFactAgain n=6; "
	if got := eval(); got != want {
		t.Fatalf("first evaluation: %q, want %q", got, want)
	}
	dc.forget()
	if got := eval(); got != want {
		t.Fatalf("second evaluation: %q, want %q", got, want)
	}
	for _, fact := range []string{"items", "item_limit", "base", "bonus", "total_floor"} {
		if dc.reads[fact] != 1 {
			t.Fatalf("fact %s read %d times in one evaluation, want 1 (all reads: %v)", fact, dc.reads[fact], dc.reads)
		}
	}
	// item.cost can be a local, so it is not cached: both rules that test the
	// fact outside a quantifier read it from the context.
	if dc.reads["item.cost"] != 2 {
		t.Fatalf("the fact item.cost was read %d times, want 2 (a name that can be a local is not cached)", dc.reads["item.cost"])
	}
	if dc.reads["total"] != 0 || dc.reads["item"] != 0 {
		t.Fatalf("a local was read from the context: %v", dc.reads)
	}

	// New facts: the elements, the limit and the let inputs all change.
	dc.forget()
	dc.values["items"] = items(1, 2)
	dc.values["item_limit"] = vm.NumVal(2)
	dc.values["base"] = vm.NumVal(20)
	dc.values["item.cost"] = vm.NumVal(8)
	// Costly: no. AllCheap: yes. Sum: 21 > 15. Double: 40 > 15.
	const changed = "AllCheap n=2; Sum n=21; Double n=40; "
	if got := eval(); got != changed {
		t.Fatalf("evaluation after the facts changed: %q, want %q", got, changed)
	}
}
