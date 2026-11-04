package arbiter_test

import (
	"errors"
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle"
	"m31labs.dev/arbiter/vm"
)

// segOnlySrc is a shared fixture: one segment-only rule (no inline condition)
// alongside two inline-condition rules.
var segOnlySrc = []byte(`
segment flagged {
	x == "bad"
}

rule InlineA {
	when { a == "yes" }
	then Allow { reason: "a" }
}

rule InlineB {
	when { b == "yes" }
	then Allow { reason: "b" }
}

rule SegOnly {
	when segment flagged
	then Deny { reason: "flagged" }
}
`)

// TestSegmentOnlyRuleCrossPaths verifies that EvalGoverned applies the segment
// even when the rule has no inline condition.
func TestSegmentOnlyRuleCrossPaths(t *testing.T) {
	prog, err := arbiter.Compile(segOnlySrc)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Context where segment fires but inline conditions do not.
	ctx := map[string]any{"x": "bad", "a": "no", "b": "no"}
	dc := arbiter.DataFromMap(ctx, prog)

	// --- EvalGoverned ---
	governed, _, err := arbiter.EvalGoverned(prog, dc, prog.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if !containsRule(governed, "SegOnly") {
		t.Errorf("EvalGoverned: want SegOnly in matches, got %v", ruleNames(governed))
	}
	if containsRule(governed, "InlineA") || containsRule(governed, "InlineB") {
		t.Errorf("EvalGoverned: unexpected inline rule match, got %v", ruleNames(governed))
	}

	basic, err := arbiter.Eval(prog, dc)
	if !errors.Is(err, arbiter.ErrGovernanceRequired) || len(basic) != 0 {
		t.Errorf("Eval: want governance error without matches, got %v, %v", basic, err)
	}

	dbg := arbiter.EvalDebug(prog, dc)
	if !errors.Is(dbg.Error, arbiter.ErrGovernanceRequired) || len(dbg.Matched) != 0 {
		t.Errorf("EvalDebug: want governance error without matches, got %v, %v", dbg.Matched, dbg.Error)
	}
}

// TestSegmentShapeMatrix guards the four layout variants for segment-only rules.
func TestSegmentShapeMatrix(t *testing.T) {
	segDef := `
segment flagged {
	x == "hit"
}
`
	hitCtx := map[string]any{"x": "hit", "a": "no", "b": "no", "c": "no"}
	missCtx := map[string]any{"x": "miss", "a": "no", "b": "no", "c": "no"}

	compile := func(t *testing.T, src []byte) *arbiter.Program {
		t.Helper()
		prog, err := arbiter.Compile(src)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return prog
	}
	eval := func(t *testing.T, prog *arbiter.Program, ctx map[string]any) []vm.MatchedRule {
		t.Helper()
		dc := arbiter.DataFromMap(ctx, prog)
		matched, _, err := arbiter.EvalGoverned(prog, dc, prog.Segments, ctx)
		if err != nil {
			t.Fatalf("EvalGoverned: %v", err)
		}
		return matched
	}

	cases := []struct {
		name string
		src  []byte
	}{
		{
			name: "2_inline_then_segment",
			src: []byte(segDef + `
rule R1 { when { a == "yes" } then Allow {} }
rule R2 { when { b == "yes" } then Allow {} }
rule SegRule { when segment flagged then Deny { reason: "flagged" } }
`),
		},
		{
			name: "3_inline_then_segment",
			src: []byte(segDef + `
rule R1 { when { a == "yes" } then Allow {} }
rule R2 { when { b == "yes" } then Allow {} }
rule R3 { when { c == "yes" } then Allow {} }
rule SegRule { when segment flagged then Deny { reason: "flagged" } }
`),
		},
		{
			name: "segment_between_inlines",
			src: []byte(segDef + `
rule R1 { when { a == "yes" } then Allow {} }
rule SegRule { when segment flagged then Deny { reason: "flagged" } }
rule R2 { when { b == "yes" } then Allow {} }
`),
		},
		{
			name: "inline_after_segment",
			src: []byte(segDef + `
rule SegRule { when segment flagged then Deny { reason: "flagged" } }
rule R1 { when { a == "yes" } then Allow {} }
rule R2 { when { b == "yes" } then Allow {} }
`),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			prog := compile(t, tc.src)

			hit := eval(t, prog, hitCtx)
			if !containsRule(hit, "SegRule") {
				t.Errorf("%s hit: want SegRule, got %v", tc.name, ruleNames(hit))
			}

			miss := eval(t, prog, missCtx)
			if containsRule(miss, "SegRule") {
				t.Errorf("%s miss: SegRule should not fire, got %v", tc.name, ruleNames(miss))
			}
		})
	}
}

// TestBundleRoundTripConditionGating pins the fail-open regression: after a
// Marshal→Unmarshal round-trip, a rule with a REAL condition must still gate
// on it under governed eval. Without this test, ConditionLen==0 on the
// restored header would be treated as unconditional and fire erroneously.
func TestBundleRoundTripConditionGating(t *testing.T) {
	src := []byte(`
rule Gated {
	when { score > 0.9 }
	then Flag { level: "high" }
}
`)
	prog, err := arbiter.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	data, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatalf("bundle.Marshal: %v", err)
	}
	restored, err := bundle.Unmarshal(data)
	if err != nil {
		t.Fatalf("bundle.Unmarshal: %v", err)
	}

	restoredProg := &arbiter.Program{Ruleset: restored}

	// score=0.5 must NOT match — condition gates it out.
	dcLow := arbiter.DataFromMap(map[string]any{"score": 0.5}, restoredProg)
	low, err := arbiter.Eval(restoredProg, dcLow)
	if err != nil {
		t.Fatalf("Eval low: %v", err)
	}
	if containsRule(low, "Gated") {
		t.Error("bundle round-trip fail-open: Gated fired with score=0.5 (condition should gate it out)")
	}

	// score=0.95 must match.
	dcHigh := arbiter.DataFromMap(map[string]any{"score": 0.95}, restoredProg)
	high, err := arbiter.Eval(restoredProg, dcHigh)
	if err != nil {
		t.Fatalf("Eval high: %v", err)
	}
	if !containsRule(high, "Gated") {
		t.Error("bundle round-trip: Gated should fire with score=0.95")
	}
}

// TestCompileJSONConditionGating asserts that a CompileJSON (Arishem loader) rule
// with a real condition still gates under governed eval.
func TestCompileJSONConditionGating(t *testing.T) {
	condJSON := `{"Operator":"==","Lhs":{"VarExpr":"status"},"Rhs":{"Const":{"StrConst":"active"}}}`
	actJSON := `{"ActionName":"Grant"}`

	rs, err := arbiter.CompileJSON(condJSON, actJSON)
	if err != nil {
		t.Fatalf("CompileJSON: %v", err)
	}
	jsonProg := &arbiter.Program{Ruleset: rs}

	// status != "active" — must not match.
	dcMiss, err := arbiter.DataFromJSON(`{"status":"inactive"}`, jsonProg)
	if err != nil {
		t.Fatalf("DataFromJSON: %v", err)
	}
	miss, err := arbiter.Eval(jsonProg, dcMiss)
	if err != nil {
		t.Fatalf("Eval miss: %v", err)
	}
	if len(miss) != 0 {
		t.Errorf("CompileJSON condition gating: want no match for status=inactive, got %v", ruleNames(miss))
	}

	// status == "active" — must match.
	dcHit, err := arbiter.DataFromJSON(`{"status":"active"}`, jsonProg)
	if err != nil {
		t.Fatalf("DataFromJSON: %v", err)
	}
	hit, err := arbiter.Eval(jsonProg, dcHit)
	if err != nil {
		t.Fatalf("Eval hit: %v", err)
	}
	if len(hit) == 0 {
		t.Error("CompileJSON condition gating: want match for status=active, got none")
	}
}

// --- helpers ---

func containsRule(matched []vm.MatchedRule, name string) bool {
	for _, m := range matched {
		if m.Name == name {
			return true
		}
	}
	return false
}

func ruleNames(matched []vm.MatchedRule) []string {
	names := make([]string, 0, len(matched))
	for _, m := range matched {
		names = append(names, m.Name)
	}
	return names
}
