package vm_test

import (
	"reflect"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bytecode"
	"m31labs.dev/arbiter/vm"
)

const evalIntoRules = `
table bands {
	lo: number | hi: number | label: string | bonus: number
	0          | 10         | "low"         | 1
	10         | 20         | "mid"         | 2
	20         | 99         | "high"        | 3
}

rule Score priority 3 {
	when { hp > 10 and armed }
	then Act { kind: "attack", score: hp * 2 + ammo, tag: role }
	otherwise Act { kind: "wait", score: 1 }
}

rule Band {
	when {
		let row = lookup bands where lo <= hp and hi > hp else { lo: 0, hi: 0, label: "none", bonus: 0 }
		row.bonus > 0
	}
	then Banded {
		let best = lookup bands where lo <= hp order by bonus desc
		label: row.label,
		bonus: row.bonus + best.bonus,
		spread: best.hi - best.lo,
	}
}

rule Fallback {
	when { hp > 1000 }
	then Never { x: 1 }
	otherwise Else {
		let r = lookup bands where lo > 1000 else { lo: -1, hi: -1, label: "else", bonus: 7 }
		label: r.label,
		bonus: r.bonus,
	}
}

rule Any {
	when { any t in threats { t > 5 } }
	then Threat { n: count(t for t in threats), top: max(hp, 0) }
}

rule Names {
	when { role == "tank" or role starts_with "sup" }
	then Role { r: role, n: hp - 3 - 2 }
}
`

func compileRules(t testing.TB, src string) *arbiter.Program {
	t.Helper()
	prog, err := arbiter.Compile([]byte(src))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

func TestEvalIntoMatchesEval(t *testing.T) {
	prog := compileRules(t, evalIntoRules)
	rs := prog.Ruleset
	pool := vm.NewStringPool(rs.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(rs, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out vm.Matches
	for _, data := range []map[string]any{
		{"hp": 5, "armed": true, "ammo": 3, "role": "tank", "threats": []any{1.0, 9.0}},
		{"hp": 15, "armed": true, "ammo": 1, "role": "support", "threats": []any{}},
		{"hp": 50, "armed": false, "ammo": 0, "role": "scout"},
		{"hp": -4, "role": "tank", "threats": []any{6.0}},
	} {
		dc := vm.DataFromMap(data, pool)
		want, err := vm.EvalWithTagFilter(rs, dc, pool, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.EvalInto(dc, &out); err != nil {
			t.Fatal(err)
		}
		got := make([]vm.MatchedRule, 0, len(out.List))
		for _, m := range out.List {
			mr := vm.MatchedRule{Name: m.Name, Priority: m.Priority, Action: m.Action, Fallback: m.Fallback}
			if len(m.Params) > 0 {
				mr.Params = map[string]any{}
				for _, p := range m.Params {
					mr.Params[p.Key] = prepared.GoValue(p.Value)
				}
			}
			if rs.Rules[m.Rule].NameIdx != rs.Rules[m.Rule].NameIdx || pool.Get(rs.Rules[m.Rule].NameIdx) != m.Name {
				t.Fatalf("match %s has rule index %d", m.Name, m.Rule)
			}
			got = append(got, mr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("data %v:\n EvalInto %+v\n Eval     %+v", data, got, want)
		}
	}
}

func TestEvalIntoKeepsParameterOrder(t *testing.T) {
	prog := compileRules(t, `rule R { when { true } then A { z: 1, a: 2, m: 3 } }`)
	pool := vm.NewStringPool(prog.Ruleset.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out vm.Matches
	if err := prepared.EvalInto(vm.DataFromMap(nil, pool), &out); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, p := range out.List[0].Params {
		keys = append(keys, p.Key)
	}
	if !reflect.DeepEqual(keys, []string{"z", "a", "m"}) {
		t.Fatalf("param order %v", keys)
	}
}

// indexed is a data context with fixed values that resolves by pool index,
// like a bound struct.
type indexed struct {
	byIdx []vm.Value
	pool  *vm.StringPool
}

func newIndexed(rs *bytecode.CompiledRuleset, pool *vm.StringPool, values map[string]vm.Value) *indexed {
	strs := rs.Constants.Strings()
	c := &indexed{byIdx: make([]vm.Value, len(strs)), pool: pool}
	for i, s := range strs {
		if v, ok := values[s]; ok {
			c.byIdx[i] = v
		}
	}
	return c
}

func (c *indexed) Get(key string) vm.Value {
	for i, v := range c.byIdx {
		if c.pool.Get(uint16(i)) == key {
			return v
		}
	}
	return vm.NullVal()
}

func (c *indexed) GetIndexed(idx uint16, _ string) vm.Value { return c.byIdx[idx] }

func TestEvalIntoDoesNotAllocate(t *testing.T) {
	prog := compileRules(t, evalIntoRules)
	rs := prog.Ruleset
	pool := vm.NewStringPool(rs.Constants.Strings())
	// Exclude the quantifier rule: its list comes from a Go slice.
	var rules []int
	for i, r := range rs.Rules {
		if pool.Get(r.NameIdx) != "Any" {
			rules = append(rules, i)
		}
	}
	prepared, err := vm.NewPreparedEvaluatorRules(rs, pool, rules)
	if err != nil {
		t.Fatal(err)
	}
	tank := vm.Value{}
	for i, s := range rs.Constants.Strings() {
		if s == "tank" {
			tank = vm.StrVal(uint16(i))
		}
	}
	ctx := newIndexed(rs, pool, map[string]vm.Value{
		"hp": vm.NumVal(15), "armed": vm.BoolVal(true), "ammo": vm.NumVal(4), "role": tank,
	})
	var out vm.Matches
	var trace vm.Trace
	for _, tr := range []*vm.Trace{nil, &trace} {
		out.Trace = tr
		run := func() {
			if err := prepared.EvalInto(ctx, &out); err != nil {
				t.Fatal(err)
			}
		}
		run()
		if len(out.List) != 4 {
			t.Fatalf("matched %d rules, want 4: %+v", len(out.List), out.List)
		}
		if allocs := testing.AllocsPerRun(500, run); allocs != 0 {
			t.Errorf("trace=%v: %.1f allocations per EvalInto, want 0", tr != nil, allocs)
		}
	}
}

func TestEvalIntoTrace(t *testing.T) {
	prog := compileRules(t, `
rule A { when { hp > 10 and armed } then X { v: 1 } }
rule B { when { hp between [0, 5] } then Y { v: 2 } }
`)
	pool := vm.NewStringPool(prog.Ruleset.Constants.Strings())
	prepared, err := vm.NewPreparedEvaluator(prog.Ruleset, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out vm.Matches
	if err := prepared.EvalInto(vm.DataFromMap(map[string]any{"hp": 20, "armed": false}, pool), &out); err != nil {
		t.Fatal(err)
	}
	if out.Trace != nil {
		t.Fatal("trace set without asking")
	}
	out.Trace = &vm.Trace{}
	if err := prepared.EvalInto(vm.DataFromMap(map[string]any{"hp": 20, "armed": false}, pool), &out); err != nil {
		t.Fatal(err)
	}
	tr := out.Trace
	if len(tr.Rules) != 2 || tr.Rules[0].Held || tr.Rules[1].Held {
		t.Fatalf("rules %+v", tr.Rules)
	}
	a := tr.Checks[tr.Rules[0].CheckStart:tr.Rules[0].CheckEnd]
	if len(a) != 2 || a[0].Op != bytecode.OpGt || !a[0].Result || a[0].A.Num != 20 || a[0].B.Num != 10 ||
		a[1].Op != bytecode.OpLoadVar || a[1].Result {
		t.Fatalf("rule A checks %+v", a)
	}
	b := tr.Checks[tr.Rules[1].CheckStart:tr.Rules[1].CheckEnd]
	if len(b) != 1 || b[0].Op != bytecode.OpBetweenCC || b[0].Result || b[0].C.Num != 5 {
		t.Fatalf("rule B checks %+v", b)
	}
}
