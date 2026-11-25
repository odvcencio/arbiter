package vm

import (
	"fmt"
	"testing"

	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/intern"
)

// funcContext is a data context backed by a function.
type funcContext func(key string) Value

func (f funcContext) Get(key string) Value { return f(key) }

// indexedFuncContext also implements IndexedDataContext.
type indexedFuncContext struct {
	get     func(key string) Value
	indexed int
}

func (c *indexedFuncContext) Get(key string) Value { return c.get(key) }

func (c *indexedFuncContext) GetIndexed(_ uint16, key string) Value {
	c.indexed++
	return c.get(key)
}

// factRules builds a ruleset with one rule per condition. Each condition is a
// list of fact names; the rule holds when all of them are greater than zero.
func factRules(pool *intern.Pool, conditions ...[]string) *compiler.CompiledRuleset {
	zero := pool.Number(0)
	rs := &compiler.CompiledRuleset{
		Constants: pool,
		Actions:   []compiler.ActionEntry{{NameIdx: pool.String("Act")}},
	}
	for i, facts := range conditions {
		start := len(rs.Instructions)
		for n, fact := range facts {
			rs.Instructions = compiler.Emit(rs.Instructions, compiler.OpLoadVar, 0, pool.String(fact))
			rs.Instructions = compiler.Emit(rs.Instructions, compiler.OpLoadNum, 0, zero)
			rs.Instructions = compiler.Emit(rs.Instructions, compiler.OpGt, 0, 0)
			if n > 0 {
				rs.Instructions = compiler.Emit(rs.Instructions, compiler.OpAnd, 0, 0)
			}
		}
		rs.Instructions = compiler.Emit(rs.Instructions, compiler.OpRuleMatch, 0, 0)
		rs.Rules = append(rs.Rules, compiler.RuleHeader{
			NameIdx:      pool.String(fmt.Sprintf("rule%d", i)),
			ConditionOff: uint32(start),
			ConditionLen: uint32(len(rs.Instructions) - start),
		})
	}
	return rs
}

func mustPrepare(t *testing.T, rs *compiler.CompiledRuleset, indices ...int) *PreparedEvaluator {
	t.Helper()
	sp := NewStringPool(rs.Constants.Strings())
	var prepared *PreparedEvaluator
	var err error
	if indices == nil {
		prepared, err = NewPreparedEvaluator(rs, sp, nil)
	} else {
		prepared, err = NewPreparedEvaluatorRules(rs, sp, indices)
	}
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

// The two fact-cache fills in loadVar copy a Value by naming each field, like
// push and pushFrom. A field they miss would be dropped from every cached
// fact. This checks the fill through Get, the fill through GetIndexed, and
// the reload of a cached fact.
func TestFactCacheCopiesEveryField(t *testing.T) {
	if got := valueFields; got != 7 {
		t.Fatalf("valueFields is %d: check the two fact-cache fills in loadVar name every field, then update this test", got)
	}
	filled := filledValue(t)
	pool := intern.NewPool()
	rs := factRules(pool, []string{"a", "b"})
	aIdx, bIdx := pool.String("a"), pool.String("b")

	prepared := mustPrepare(t, rs)
	machine := prepared.vm
	machine.varSlot = make([]uint16, len(machine.localMask))

	plain := funcContext(func(string) Value { return filled })
	machine.loadVar(aIdx, plain) // fill through Get
	machine.loadVar(aIdx, plain) // reload from the cache
	indexed := &indexedFuncContext{get: func(string) Value { return filled }}
	machine.idc = indexed
	machine.loadVar(bIdx, indexed) // fill through GetIndexed
	machine.loadVar(bIdx, indexed) // reload from the cache
	if indexed.indexed != 1 {
		t.Fatalf("GetIndexed was called %d times for one fact, want 1", indexed.indexed)
	}
	if machine.sp != 4 || len(machine.varVals) != 2 {
		t.Fatalf("four loads of two facts left sp=%d and %d cached values", machine.sp, len(machine.varVals))
	}
	for i, what := range []string{"fill through Get", "reload after Get", "fill through GetIndexed", "reload after GetIndexed"} {
		if !sameValue(machine.stack[i], filled) {
			t.Fatalf("%s dropped a field: context returned %+v, stack holds %+v", what, filled, machine.stack[i])
		}
	}
	for i := range machine.varVals {
		if !sameValue(machine.varVals[i], filled) {
			t.Fatalf("cached value %d dropped a field: %+v", i, machine.varVals[i])
		}
	}
}

// An evaluator built for one evaluation must not allocate the fact cache.
// The cache is built when the evaluator is used a second time.
func TestFactCacheIsBuiltOnTheSecondEvaluation(t *testing.T) {
	positive := funcContext(func(string) Value { return NumVal(1) })
	for _, firstInto := range []bool{false, true} {
		for _, secondInto := range []bool{false, true} {
			pool := intern.NewPool()
			prepared := mustPrepare(t, factRules(pool, []string{"a", "b"}, []string{"a"}))
			var out Matches
			eval := func(into bool) {
				t.Helper()
				if into {
					if err := prepared.EvalInto(positive, &out); err != nil {
						t.Fatal(err)
					}
					return
				}
				if _, err := prepared.Eval(positive); err != nil {
					t.Fatal(err)
				}
			}
			eval(firstInto)
			if prepared.vm.varSlot != nil || prepared.vm.varVals != nil || prepared.vm.varLoaded != nil {
				t.Fatalf("first evaluation (EvalInto=%v) allocated the fact cache", firstInto)
			}
			eval(secondInto)
			if len(prepared.vm.varSlot) != len(prepared.vm.localMask) || prepared.vm.varSlot == nil {
				t.Fatalf("second evaluation (EvalInto=%v) did not build the fact cache", secondInto)
			}
			if cap(prepared.vm.varVals) != factCacheStart || cap(prepared.vm.varLoaded) != factCacheStart {
				t.Fatalf("second evaluation (EvalInto=%v) left room for %d values and %d touched entries, want %d", secondInto, cap(prepared.vm.varVals), cap(prepared.vm.varLoaded), factCacheStart)
			}
		}
	}
}

// The work of forgetting the facts must follow the facts one evaluation read,
// not the facts the whole program names. One rule out of a program of 500
// rules is prepared; the other rules name 1,000 facts of their own.
func TestFactCacheResetFollowsTheFactsRead(t *testing.T) {
	pool := intern.NewPool()
	conditions := [][]string{{"shared.a", "shared.b", "shared.a", "shared.c"}}
	for i := 0; i < 500; i++ {
		conditions = append(conditions, []string{fmt.Sprintf("zone%d.level", i), fmt.Sprintf("zone%d.open", i)})
	}
	rs := factRules(pool, conditions...)
	prepared := mustPrepare(t, rs, 0)

	mostTouched := 0
	reads := map[string]int{}
	dc := funcContext(func(key string) Value {
		reads[key]++
		if n := len(prepared.vm.varLoaded); n > mostTouched {
			mostTouched = n
		}
		return NumVal(1)
	})
	for call := 0; call < 4; call++ {
		clear(reads)
		matches, err := prepared.Eval(dc)
		if err != nil || len(matches) != 1 {
			t.Fatalf("evaluation %d: %d matches, %v", call, len(matches), err)
		}
		if call > 0 && (reads["shared.a"] != 1 || reads["shared.b"] != 1 || reads["shared.c"] != 1) {
			t.Fatalf("evaluation %d read a fact more than once: %v", call, reads)
		}
		if len(prepared.vm.varVals) != 0 || len(prepared.vm.varLoaded) != 0 {
			t.Fatalf("evaluation %d left %d cached values and %d touched entries", call, len(prepared.vm.varVals), len(prepared.vm.varLoaded))
		}
		for idx, at := range prepared.vm.varSlot {
			if at != 0 {
				t.Fatalf("evaluation %d left the slot of %q set", call, pool.Strings()[idx])
			}
		}
		for i, v := range prepared.vm.varVals[:cap(prepared.vm.varVals)] {
			if v != (Value{}) {
				t.Fatalf("evaluation %d left cached value %d uncleared: %+v", call, i, v)
			}
		}
	}
	if len(prepared.vm.varSlot) < 1000 {
		t.Fatalf("the program names %d strings; the test needs a large program", len(prepared.vm.varSlot))
	}
	// Three facts were read: when the third arrived, two were on the touched
	// list. The storage holds a few values, not one per fact of the program.
	if mostTouched != 2 {
		t.Fatalf("the touched list held %d entries when the last fact was read, want 2", mostTouched)
	}
	if c := cap(prepared.vm.varVals); c != factCacheStart {
		t.Fatalf("the cache holds room for %d values after reading 3 facts, want its starting size %d", c, factCacheStart)
	}
}

// A data context that panics must not leave facts behind either: the cache is
// emptied on the way out, and the fact that panicked has no entry.
func TestFactCacheIsEmptiedWhenTheContextPanics(t *testing.T) {
	pool := intern.NewPool()
	rs := factRules(pool, []string{"a", "b"}, []string{"a"})
	for _, into := range []bool{false, true} {
		prepared := mustPrepare(t, rs)
		var out Matches
		eval := func(dc DataContext) (n int, err error) {
			if into {
				err = prepared.EvalInto(dc, &out)
				return len(out.List), err
			}
			matches, err := prepared.Eval(dc)
			return len(matches), err
		}
		positive := funcContext(func(string) Value { return NumVal(1) })
		for call := 0; call < 2; call++ {
			if n, err := eval(positive); err != nil || n != 2 {
				t.Fatalf("EvalInto=%v: good evaluation %d: %d matches, %v", into, call, n, err)
			}
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("EvalInto=%v: the context's panic did not reach the caller", into)
				}
			}()
			eval(funcContext(func(key string) Value {
				if key == "b" {
					panic("context failure")
				}
				return NumVal(1)
			}))
		}()
		if len(prepared.vm.varVals) != 0 || len(prepared.vm.varLoaded) != 0 {
			t.Fatalf("EvalInto=%v: the panic left %d cached values", into, len(prepared.vm.varVals))
		}
		for idx, at := range prepared.vm.varSlot {
			if at != 0 {
				t.Fatalf("EvalInto=%v: the panic left the slot of %q set", into, pool.Strings()[idx])
			}
		}
		// a was 1 in the evaluation that panicked. It is -1 now.
		negative := funcContext(func(string) Value { return NumVal(-1) })
		if n, err := eval(negative); err != nil || n != 0 {
			t.Fatalf("EvalInto=%v: evaluation after the panic: %d matches, %v (a stale fact was used)", into, n, err)
		}
	}
}

// When one evaluation has cached as many facts as a slot can number, further
// facts are read on every use instead of cached.
func TestFactCacheReadsThroughWhenFull(t *testing.T) {
	pool := intern.NewPool()
	rs := factRules(pool, []string{"a"})
	aIdx := pool.String("a")
	prepared := mustPrepare(t, rs)
	machine := prepared.vm
	machine.varSlot = make([]uint16, len(machine.localMask))
	machine.varVals = make([]Value, maxCachedVars)

	reads := 0
	dc := funcContext(func(string) Value {
		reads++
		return NumVal(float64(reads))
	})
	machine.loadVar(aIdx, dc)
	machine.loadVar(aIdx, dc)
	if reads != 2 {
		t.Fatalf("a full cache read the fact %d times for two loads, want 2", reads)
	}
	if len(machine.varVals) != maxCachedVars || machine.varSlot[aIdx] != 0 || len(machine.varLoaded) != 0 {
		t.Fatalf("a full cache stored another fact: %d values, slot %d", len(machine.varVals), machine.varSlot[aIdx])
	}
	if machine.sp != 2 || machine.stack[0].Num != 1 || machine.stack[1].Num != 2 {
		t.Fatalf("loads with a full cache pushed %+v and %+v", machine.stack[0], machine.stack[1])
	}
}
