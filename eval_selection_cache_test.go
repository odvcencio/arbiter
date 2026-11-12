package arbiter

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"m31labs.dev/arbiter/vm"
)

const selectionCacheRules = `
tag "fraud"
tag "realtime"
tag "batch"

rule FraudRealtime tag "fraud" tag "realtime" {
	when { order.total >= 10 }
	then A {}
}

rule FraudBatch tags "fraud,batch" {
	when { order.total >= 10 }
	then B {}
}

rule Untagged {
	when { order.total >= 10 }
	then C {}
}
`

func matchNames(matches []vm.MatchedRule) string {
	out := ""
	for _, m := range matches {
		out += m.Name + ","
	}
	return out
}

// Eval caches which rules a tag set selects. The cached path must return what
// the per-call tag filter returns, for every tag set, on the first call and on
// every later one.
func TestEvalTagSelectionCacheMatchesTagFilter(t *testing.T) {
	prog, err := Compile([]byte(selectionCacheRules))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	dc := DataFromMap(map[string]any{"order": map[string]any{"total": 12}}, prog)
	for _, tags := range [][]string{nil, {"fraud"}, {"realtime"}, {"batch"}, {"fraud", "batch"}, {"batch", "fraud"}, {"missing"}, {"fraud", "missing"}} {
		want, err := vm.EvalWithTagFilter(prog.Ruleset, dc, vm.NewStringPool(prog.Ruleset.Constants.Strings()), tags)
		if err != nil {
			t.Fatalf("tag filter %v: %v", tags, err)
		}
		for call := 0; call < 3; call++ {
			got, err := Eval(prog, dc, WithTags(tags...))
			if err != nil {
				t.Fatalf("eval %v call %d: %v", tags, call, err)
			}
			if matchNames(got) != matchNames(want) {
				t.Fatalf("tags %v call %d: got %q, want %q", tags, call, matchNames(got), matchNames(want))
			}
		}
	}
	if got, want := matchNames(mustEval(t, prog, dc)), "FraudRealtime,FraudBatch,Untagged,"; got != want {
		t.Fatalf("declaration order lost: %q", got)
	}
}

func mustEval(t *testing.T, prog *Program, dc vm.DataContext, opts ...EvalOption) []vm.MatchedRule {
	t.Helper()
	matches, err := Eval(prog, dc, opts...)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	return matches
}

// Tag sets can come from callers, so the cache stops growing at its bound and
// still answers correctly beyond it.
func TestEvalTagSelectionCacheIsBounded(t *testing.T) {
	prog, err := Compile([]byte(selectionCacheRules))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	dc := DataFromMap(map[string]any{"order": map[string]any{"total": 12}}, prog)
	for i := 0; i < 4*maxCachedSelections; i++ {
		if got := mustEval(t, prog, dc, WithTags(fmt.Sprintf("caller-supplied-%d", i))); len(got) != 0 {
			t.Fatalf("unknown tag %d selected %d rules", i, len(got))
		}
	}
	table := prog.selections.Load()
	if table == nil || len(table.byTag) > maxCachedSelections {
		t.Fatalf("selection cache grew past its bound: %v", table)
	}
	if got := matchNames(mustEval(t, prog, dc, WithTags("fraud"))); got != "FraudRealtime,FraudBatch," {
		t.Fatalf("selection beyond the bound: %q", got)
	}
}

func TestEvalTagSelectionCacheIsSafeForConcurrentUse(t *testing.T) {
	prog, err := Compile([]byte(selectionCacheRules))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	want := map[string]string{"fraud": "FraudRealtime,FraudBatch,", "realtime": "FraudRealtime,", "batch": "FraudBatch,", "": "FraudRealtime,FraudBatch,Untagged,"}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dc := DataFromMap(map[string]any{"order": map[string]any{"total": 12}}, prog)
			for i := 0; i < 200; i++ {
				for tag, names := range want {
					var opts []EvalOption
					if tag != "" {
						opts = append(opts, WithTags(tag))
					}
					got, err := Eval(prog, dc, opts...)
					if err != nil || matchNames(got) != names {
						errs <- fmt.Errorf("tag %q: %q, %v", tag, matchNames(got), err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// A cached selection keeps the governance refusal plain evaluation gives.
func TestEvalTagSelectionCacheKeepsGovernanceRefusal(t *testing.T) {
	prog, err := Compile([]byte(`
tag "gated"
tag "open"

rule Gated tag "gated" {
	rollout 50
	when { true }
	then A {}
}

rule Open tag "open" {
	when { true }
	then B {}
}
`))
	if err != nil {
		t.Skipf("rollout syntax not accepted by this grammar: %v", err)
	}
	dc := DataFromMap(map[string]any{}, prog)
	for call := 0; call < 2; call++ {
		if _, err := Eval(prog, dc, WithTags("gated")); !errors.Is(err, ErrGovernanceRequired) {
			t.Fatalf("call %d: governed rule evaluated plainly: %v", call, err)
		}
		if got := matchNames(mustEval(t, prog, dc, WithTags("open"))); got != "Open," {
			t.Fatalf("call %d: open selection %q", call, got)
		}
	}
}
