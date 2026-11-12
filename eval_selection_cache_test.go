package arbiter

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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

func mustCompile(t *testing.T, source string) *Program {
	t.Helper()
	prog, err := Compile([]byte(source))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

func mustEval(t *testing.T, prog *Program, dc vm.DataContext, opts ...EvalOption) []vm.MatchedRule {
	t.Helper()
	matches, err := Eval(prog, dc, opts...)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	return matches
}

// noFacts is a data context with no facts. Eval builds a string pool from the
// ruleset it is given for such a context, so the context stays valid when a
// test reassigns Program.Ruleset.
type noFacts struct{}

func (noFacts) Get(string) vm.Value { return vm.NullVal() }

// cachedSelections is the number of tag sets the program's cache holds.
func cachedSelections(prog *Program) int {
	table := prog.selections.Load()
	if table == nil {
		return 0
	}
	return len(table.byKey)
}

// Eval caches which rules a tag set selects. The cached path must return what
// the per-call tag filter returns, for every tag set, on the first call and on
// every later one.
func TestEvalTagSelectionCacheMatchesTagFilter(t *testing.T) {
	prog := mustCompile(t, selectionCacheRules)
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

// taggedRule is one rule of a generated program and the tags written on it.
type taggedRule struct {
	name string
	tags []string
}

// oracleTags and oracleRules describe a program of 48 always-true rules over
// eight tags. Rule i carries the tags whose bits are set in a mask derived
// from i, so the rules cover no tags, one tag, every tag and many mixes.
func oracleTags() []string {
	return []string{"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7"}
}

func oracleRules() []taggedRule {
	tags := oracleTags()
	rules := make([]taggedRule, 0, 48)
	for i := 0; i < 48; i++ {
		mask := (i*73 + 19) & 0xFF
		switch {
		case i == 0:
			mask = 0
		case i == 1:
			mask = 0xFF
		case i <= 9:
			mask = 1 << (i - 2)
		}
		rule := taggedRule{name: fmt.Sprintf("R%d", i)}
		for bit, tag := range tags {
			if mask&(1<<bit) != 0 {
				rule.tags = append(rule.tags, tag)
			}
		}
		rules = append(rules, rule)
	}
	return rules
}

func taggedRulesSource(tags []string, rules []taggedRule) string {
	var b strings.Builder
	for _, tag := range tags {
		fmt.Fprintf(&b, "tag %q\n", tag)
	}
	for _, rule := range rules {
		fmt.Fprintf(&b, "rule %s", rule.name)
		for _, tag := range rule.tags {
			fmt.Fprintf(&b, " tag %q", tag)
		}
		b.WriteString(" { when { true } then A {} }\n")
	}
	return b.String()
}

// oracleSelect states tag selection without using the engine: a rule is
// selected when every requested tag is one of the tags written on it, and
// selected rules keep their declaration order. It reads the tags from the
// table the source was generated from, not from the compiled ruleset.
func oracleSelect(rules []taggedRule, requested []string) string {
	out := ""
	for _, rule := range rules {
		selected := true
		for _, want := range requested {
			carried := false
			for _, tag := range rule.tags {
				if tag == want {
					carried = true
				}
			}
			if !carried {
				selected = false
			}
		}
		if selected {
			out += rule.name + ","
		}
	}
	return out
}

// tagsOfMask returns the oracle tags whose bits are set in mask, in ascending
// or descending order.
func tagsOfMask(mask int, descending bool) []string {
	var out []string
	for bit, tag := range oracleTags() {
		if mask&(1<<bit) != 0 {
			out = append(out, tag)
		}
	}
	if descending {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

// The cached selection must equal an independent statement of tag selection,
// not only the engine's own tag filter (which shares RuleMatchesTags with the
// cache). The 256 subsets of eight tags are four times the cache bound, so
// the comparison covers stored selections, selections computed past the
// bound, both tag orders and sets with a tag no rule carries.
func TestEvalTagSelectionCacheMatchesIndependentOracle(t *testing.T) {
	rules := oracleRules()
	prog := mustCompile(t, taggedRulesSource(oracleTags(), rules))
	dc := noFacts{}
	check := func(pass string, tags []string) {
		t.Helper()
		got := matchNames(mustEval(t, prog, dc, WithTags(tags...)))
		if want := oracleSelect(rules, tags); got != want {
			t.Fatalf("%s, tags %q: selected %q, want %q", pass, tags, got, want)
		}
	}
	for pass := 0; pass < 3; pass++ {
		for mask := 0; mask < 256; mask++ {
			check(fmt.Sprintf("pass %d ascending", pass), tagsOfMask(mask, false))
			check(fmt.Sprintf("pass %d descending", pass), tagsOfMask(mask, true))
		}
	}
	for mask := 0; mask < 256; mask++ {
		tags := append(tagsOfMask(mask, false), "carried-by-no-rule")
		check("unknown tag last", tags)
		check("unknown tag first", append([]string{"carried-by-no-rule"}, tagsOfMask(mask, false)...))
	}
	if got := cachedSelections(prog); got != maxCachedSelections {
		t.Fatalf("cache holds %d tag sets after 255 distinct ones, want the bound %d", got, maxCachedSelections)
	}
}

// Tag sets can come from callers, so the cache stops growing at its bound and
// still answers correctly beyond it.
func TestEvalTagSelectionCacheIsBounded(t *testing.T) {
	rules := oracleRules()
	prog := mustCompile(t, taggedRulesSource(oracleTags(), rules))
	dc := noFacts{}
	for mask := 1; mask < 256; mask++ {
		mustEval(t, prog, dc, WithTags(tagsOfMask(mask, false)...))
		if got := cachedSelections(prog); got > maxCachedSelections {
			t.Fatalf("selection cache grew to %d tag sets, past its bound %d", got, maxCachedSelections)
		}
	}
	if got := cachedSelections(prog); got != maxCachedSelections {
		t.Fatalf("cache holds %d tag sets, want the bound %d", got, maxCachedSelections)
	}
	// Mask 255 came last, so it has no slot; it is still answered correctly.
	tags := tagsOfMask(255, false)
	if _, stored := prog.selections.Load().byKey[string(appendSelectionKey(nil, tags))]; stored {
		t.Fatalf("tag set %q took a slot past the bound", tags)
	}
	if got, want := matchNames(mustEval(t, prog, dc, WithTags(tags...))), oracleSelect(rules, tags); got != want {
		t.Fatalf("selection beyond the bound: %q, want %q", got, want)
	}
	// The no-tag selection does not compete for the bounded slots.
	if got, want := matchNames(mustEval(t, prog, dc)), oracleSelect(rules, nil); got != want {
		t.Fatalf("no-tag selection with a full cache: %q, want %q", got, want)
	}
	if prog.selections.Load().all == nil {
		t.Fatal("the no-tag selection was not stored once the cache was full")
	}
}

// A tag set that names a tag no rule carries always selects nothing. Such
// sets must not take cache slots: callers can make up any number of them, and
// a full table would stop caching the real tag sets for the life of the
// Program.
func TestEvalTagSelectionCacheSkipsUnknownTags(t *testing.T) {
	prog := mustCompile(t, selectionCacheRules)
	dc := DataFromMap(map[string]any{"order": map[string]any{"total": 12}}, prog)
	for i := 0; i < 200; i++ {
		tags := []string{fmt.Sprintf("caller-supplied-%d", i)}
		switch i % 3 {
		case 1:
			tags = append(tags, "fraud") // a real tag after the unknown one
		case 2:
			tags = append([]string{"fraud", "batch"}, tags...) // real tags before it
		}
		if got := mustEval(t, prog, dc, WithTags(tags...)); len(got) != 0 {
			t.Fatalf("tag set %q selected %d rules", tags, len(got))
		}
		if sel := prog.selection(prog.Ruleset, tags); sel != noRulesSelected {
			t.Fatalf("tag set %q did not get the shared empty selection", tags)
		}
	}
	if got := cachedSelections(prog); got != 0 {
		t.Fatalf("200 unknown tag sets took %d cache slots, want 0", got)
	}
	// A real tag set is still cached: the second lookup returns the stored
	// selection, not a new one.
	if got := matchNames(mustEval(t, prog, dc, WithTags("fraud"))); got != "FraudRealtime,FraudBatch," {
		t.Fatalf("real tag set after the unknown ones: %q", got)
	}
	if got := cachedSelections(prog); got != 1 {
		t.Fatalf("cache holds %d tag sets after one real set, want 1", got)
	}
	first := prog.selection(prog.Ruleset, []string{"fraud"})
	if again := prog.selection(prog.Ruleset, []string{"fraud"}); again != first {
		t.Fatal("the real tag set was computed again instead of served from the cache")
	}
}

// nulTagRules has a governed rule under the tags "beta" and "checkout", and
// optionally an open rule under the single tag "beta\x00checkout". A cache key
// that joins tags with a NUL byte gives both tag sets the same key.
func nulTagRules(declareJoinedTag bool) string {
	source := `
tag "beta"
tag "checkout"

rule Gated tag "beta" tag "checkout" {
	rollout 50
	when { true }
	then A {}
}
`
	if declareJoinedTag {
		source += "\ntag \"beta\x00checkout\"\n\nrule Joined tag \"beta\x00checkout\" {\n\twhen { true }\n\tthen B {}\n}\n"
	}
	return source
}

// WithTags("beta\x00checkout") and WithTags("beta", "checkout") are different
// tag sets. Neither may be served the other's selection or the other's
// governance refusal, whichever is evaluated first.
func TestEvalTagSelectionCacheSeparatesTagsThatContainNUL(t *testing.T) {
	for _, declared := range []bool{true, false} {
		wantJoined := ""
		if declared {
			wantJoined = "Joined,"
		}
		evalJoined := func(t *testing.T, prog *Program) {
			t.Helper()
			got, err := Eval(prog, noFacts{}, WithTags("beta\x00checkout"))
			if err != nil {
				t.Fatalf("one tag containing NUL was refused: %v", err)
			}
			if matchNames(got) != wantJoined {
				t.Fatalf("one tag containing NUL selected %q, want %q", matchNames(got), wantJoined)
			}
		}
		evalPair := func(t *testing.T, prog *Program) {
			t.Helper()
			for _, tags := range [][]string{{"beta", "checkout"}, {"checkout", "beta"}} {
				if got, err := Eval(prog, noFacts{}, WithTags(tags...)); !errors.Is(err, ErrGovernanceRequired) {
					t.Fatalf("tags %q: governed rule evaluated plainly: matches %q, error %v", tags, matchNames(got), err)
				}
			}
		}
		name := "joined tag not declared"
		if declared {
			name = "joined tag declared"
		}
		t.Run(name+"/one tag first", func(t *testing.T) {
			prog := mustCompile(t, nulTagRules(declared))
			for call := 0; call < 2; call++ {
				evalJoined(t, prog)
				evalPair(t, prog)
			}
		})
		t.Run(name+"/two tags first", func(t *testing.T) {
			prog := mustCompile(t, nulTagRules(declared))
			for call := 0; call < 2; call++ {
				evalPair(t, prog)
				evalJoined(t, prog)
			}
		})
	}
}

// The key of a tag set is injective and ignores the order of the tags.
func TestSelectionKeyIsInjectiveAndIgnoresOrder(t *testing.T) {
	sets := [][]string{
		{"a"}, {"b"}, {"a", "b"}, {"ab"}, {"a\x00b"}, {"a", "\x00b"}, {"a\x00", "b"},
		{"1:a"}, {"a", "1:a"}, {"1:a1:b"}, {"a1:b"}, {"a", "b", "c"}, {"ab", "c"}, {"a", "bc"},
		{"abc"}, {"1"}, {"1", ":"}, {"1:"}, {"11:a"}, {"aaaaaaaaaaa"}, {"a", "aaaaaaaaaa"},
	}
	seen := map[string][]string{}
	for _, tags := range sets {
		key := string(appendSelectionKey(nil, tags))
		if other, ok := seen[key]; ok {
			t.Fatalf("tag sets %q and %q share the key %q", other, tags, key)
		}
		seen[key] = tags
		reversed := make([]string, len(tags))
		for i, tag := range tags {
			reversed[len(tags)-1-i] = tag
		}
		given := slices.Clone(reversed)
		if got := string(appendSelectionKey(nil, reversed)); got != key {
			t.Fatalf("tag set %q: key %q, but %q in the other order", tags, key, got)
		}
		if !slices.Equal(reversed, given) {
			t.Fatalf("building the key reordered the caller's tags: %q became %q", given, reversed)
		}
	}
	// A set larger than the on-stack sort buffer.
	long := []string{"j", "i", "h", "g", "f", "e", "d", "c", "b", "a"}
	sorted := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	if a, b := string(appendSelectionKey(nil, long)), string(appendSelectionKey(nil, sorted)); a != b {
		t.Fatalf("ten tags: key %q, but %q in sorted order", a, b)
	}
	if long[0] != "j" || long[9] != "a" {
		t.Fatalf("building the key reordered the caller's tags: %q", long)
	}
}

// Tag matching treats the requested tags as a set, so the same tags in another
// order are one tag set and take one cache slot.
func TestEvalTagSelectionCacheUsesOneSlotForAnyTagOrder(t *testing.T) {
	prog := mustCompile(t, selectionCacheRules)
	dc := DataFromMap(map[string]any{"order": map[string]any{"total": 12}}, prog)
	for call := 0; call < 2; call++ {
		for _, tags := range [][]string{{"fraud", "batch"}, {"batch", "fraud"}} {
			if got := matchNames(mustEval(t, prog, dc, WithTags(tags...))); got != "FraudBatch," {
				t.Fatalf("tags %q selected %q", tags, got)
			}
		}
	}
	if got := cachedSelections(prog); got != 1 {
		t.Fatalf("the same two tags in both orders took %d cache slots, want 1", got)
	}
	if a, b := prog.selection(prog.Ruleset, []string{"fraud", "batch"}), prog.selection(prog.Ruleset, []string{"batch", "fraud"}); a != b {
		t.Fatal("the two orders were served different selections")
	}
}

func TestEvalTagSelectionCacheIsSafeForConcurrentUse(t *testing.T) {
	prog := mustCompile(t, selectionCacheRules)
	cases := []struct {
		tags  []string
		names string
	}{
		{nil, "FraudRealtime,FraudBatch,Untagged,"},
		{[]string{"fraud"}, "FraudRealtime,FraudBatch,"},
		{[]string{"realtime"}, "FraudRealtime,"},
		{[]string{"batch"}, "FraudBatch,"},
		{[]string{"fraud", "batch"}, "FraudBatch,"},
		{[]string{"batch", "fraud"}, "FraudBatch,"},
		{[]string{"realtime", "batch"}, ""},
		{[]string{"missing"}, ""},
		{[]string{"fraud", "missing"}, ""},
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			dc := DataFromMap(map[string]any{"order": map[string]any{"total": 12}}, prog)
			for i := 0; i < 200; i++ {
				// Each worker starts at another case, so first uses collide.
				for n := range cases {
					c := cases[(n+worker)%len(cases)]
					got, err := Eval(prog, dc, WithTags(c.tags...))
					if err != nil || matchNames(got) != c.names {
						errs <- fmt.Errorf("tags %q: %q, %v", c.tags, matchNames(got), err)
						return
					}
				}
			}
		}(worker)
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
		t.Fatalf("compile: %v", err)
	}
	dc := DataFromMap(map[string]any{}, prog)
	for call := 0; call < 2; call++ {
		if _, err := Eval(prog, dc, WithTags("gated")); !errors.Is(err, ErrGovernanceRequired) {
			t.Fatalf("call %d: governed rule evaluated plainly: %v", call, err)
		}
		if got := matchNames(mustEval(t, prog, dc, WithTags("open"))); got != "Open," {
			t.Fatalf("call %d: open selection %q", call, got)
		}
		// With no tags every rule is selected, the governed one included.
		if _, err := Eval(prog, dc); !errors.Is(err, ErrGovernanceRequired) {
			t.Fatalf("call %d: governed rule evaluated plainly with no tags: %v", call, err)
		}
	}
}

// Program.Ruleset is an exported field. After a caller assigns another
// ruleset, Eval must select from that ruleset: a selection computed for the
// old one holds the old ruleset's rule headers.
func TestEvalTagSelectionCacheFollowsReassignedRuleset(t *testing.T) {
	first := mustCompile(t, `
tag "live"

rule OldLive tag "live" { when { true } then A {} }
rule OldOther { when { true } then B {} }
`)
	second := mustCompile(t, `
tag "live"
tag "extra"

rule NewOther { when { true } then C {} }
rule NewExtra tag "extra" { when { true } then D {} }
rule NewLive tag "live" tag "extra" { when { true } then E {} }
`)
	prog := first
	firstRuleset := first.Ruleset
	describe := func(opts ...EvalOption) string {
		t.Helper()
		out := ""
		for _, m := range mustEval(t, prog, noFacts{}, opts...) {
			out += m.Name + ":" + m.Action + ","
		}
		return out
	}
	for call := 0; call < 2; call++ {
		if got := describe(WithTags("live")); got != "OldLive:A," {
			t.Fatalf("first ruleset, tag live: %q", got)
		}
		if got := describe(); got != "OldLive:A,OldOther:B," {
			t.Fatalf("first ruleset, no tags: %q", got)
		}
		// "extra" is on no rule of the first ruleset.
		if got := describe(WithTags("extra")); got != "" {
			t.Fatalf("first ruleset, tag extra: %q", got)
		}
	}

	prog.Ruleset = second.Ruleset
	for call := 0; call < 2; call++ {
		if got := describe(WithTags("live")); got != "NewLive:E," {
			t.Fatalf("second ruleset, tag live: %q (a selection of the old ruleset was served)", got)
		}
		if got := describe(); got != "NewOther:C,NewExtra:D,NewLive:E," {
			t.Fatalf("second ruleset, no tags: %q (a selection of the old ruleset was served)", got)
		}
		if got := describe(WithTags("extra")); got != "NewExtra:D,NewLive:E," {
			t.Fatalf("second ruleset, tag extra: %q (the old ruleset's tags were used)", got)
		}
	}

	prog.Ruleset = firstRuleset
	if got := describe(WithTags("live")); got != "OldLive:A," {
		t.Fatalf("first ruleset again, tag live: %q", got)
	}
	if got := describe(WithTags("extra")); got != "" {
		t.Fatalf("first ruleset again, tag extra: %q", got)
	}
}
