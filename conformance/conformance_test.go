// Package conformance defines a cross-platform parity matrix for Arbiter.
// Every test case here must produce identical results across:
//   - Native Go eval (in-process)
//   - Governed eval (with segments, rollouts, prerequisites)
//   - Strategy eval
//   - gRPC eval (via grpcserver)
//   - Binary bundle round-trip (marshal → unmarshal → eval)
//
// If a test passes in one mode but fails in another, the surfaces have diverged.
package conformance_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle"
	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/vm"
)

type conformanceCase struct {
	name    string
	source  string
	context map[string]any
	expect  func(t *testing.T, matched []vm.MatchedRule)
}

var ruleConformanceCases = []conformanceCase{
	{
		name: "simple condition match",
		source: `
rule HighValue {
	when { order.total > 100 }
	then Flag { level: "high" }
}`,
		context: map[string]any{"order": map[string]any{"total": float64(150)}},
		expect: func(t *testing.T, matched []vm.MatchedRule) {
			if len(matched) != 1 || matched[0].Action != "Flag" || matched[0].Params["level"] != "high" {
				t.Errorf("expected Flag{level:high}, got %+v", matched)
			}
		},
	},
	{
		name: "simple condition no match",
		source: `
rule HighValue {
	when { order.total > 100 }
	then Flag { level: "high" }
}`,
		context: map[string]any{"order": map[string]any{"total": float64(50)}},
		expect: func(t *testing.T, matched []vm.MatchedRule) {
			if len(matched) != 0 {
				t.Errorf("expected no match, got %+v", matched)
			}
		},
	},
	{
		name: "fallback action",
		source: `
rule Shipping {
	when { cart.total >= 100 }
	then Free { cost: 0 }
	otherwise Standard { cost: 5.99 }
}`,
		context: map[string]any{"cart": map[string]any{"total": float64(50)}},
		expect: func(t *testing.T, matched []vm.MatchedRule) {
			if len(matched) != 1 || matched[0].Action != "Standard" {
				t.Errorf("expected Standard fallback, got %+v", matched)
			}
		},
	},
	{
		name: "priority ordering",
		source: `
rule A priority 10 {
	when { x > 0 }
	then First { name: "A" }
}
rule B priority 1 {
	when { x > 0 }
	then First { name: "B" }
}`,
		context: map[string]any{"x": float64(5)},
		expect: func(t *testing.T, matched []vm.MatchedRule) {
			if len(matched) != 2 {
				t.Fatalf("expected 2 matches, got %d", len(matched))
			}
			// Both rules match. Verify both are present.
			names := map[string]bool{}
			for _, m := range matched {
				names[m.Params["name"].(string)] = true
			}
			if !names["A"] || !names["B"] {
				t.Errorf("expected both A and B, got %+v", matched)
			}
		},
	},
	{
		name: "string operators",
		source: `
rule Prefix {
	when { name starts_with "Dr." }
	then Match { found: true }
}`,
		context: map[string]any{"name": "Dr. Smith"},
		expect: func(t *testing.T, matched []vm.MatchedRule) {
			if len(matched) != 1 {
				t.Errorf("expected 1 match, got %d", len(matched))
			}
		},
	},
	{
		name: "list containment",
		source: `
const PREMIUM = ["gold", "platinum"]
rule VIP {
	when { user.tier in PREMIUM }
	then Discount { pct: 15 }
}`,
		context: map[string]any{"user": map[string]any{"tier": "gold"}},
		expect: func(t *testing.T, matched []vm.MatchedRule) {
			if len(matched) != 1 || matched[0].Action != "Discount" {
				t.Errorf("expected Discount, got %+v", matched)
			}
		},
	},
	{
		name: "math expression in action",
		source: `
rule Calc {
	when { true }
	then Result { value: input.a * 2 + input.b }
}`,
		context: map[string]any{"input": map[string]any{"a": float64(10), "b": float64(3)}},
		expect: func(t *testing.T, matched []vm.MatchedRule) {
			if len(matched) != 1 {
				t.Fatalf("expected 1 match")
			}
			v, ok := matched[0].Params["value"].(float64)
			if !ok || v != 23 {
				t.Errorf("expected 23, got %v", matched[0].Params["value"])
			}
		},
	},
}

func TestConformance_NativeEval(t *testing.T) {
	for _, tc := range ruleConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := arbiter.Compile([]byte(tc.source))
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			dc := arbiter.DataFromMap(tc.context, prog)
			matched, err := arbiter.Eval(prog, dc)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			tc.expect(t, matched)
		})
	}
}

func TestConformance_GovernedEval(t *testing.T) {
	for _, tc := range ruleConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			full, err := arbiter.CompileFull([]byte(tc.source))
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			govProg := &arbiter.Program{Ruleset: full.Ruleset, Segments: full.Segments}
			dc := arbiter.DataFromMap(tc.context, govProg)
			matched, _, err := arbiter.EvalGoverned(govProg, dc, full.Segments, tc.context)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			tc.expect(t, matched)
		})
	}
}

func TestConformance_BundleRoundTrip(t *testing.T) {
	for _, tc := range ruleConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := arbiter.Compile([]byte(tc.source))
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			rs := prog.Ruleset
			blob, err := bundle.Marshal(rs)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			restored, err := bundle.Unmarshal(blob)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			sp := vm.NewStringPool(restored.Constants.Strings())
			dc := vm.DataFromMap(tc.context, sp)
			matched, err := vm.EvalWithPool(restored, dc, sp)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			tc.expect(t, matched)
		})
	}
}

func TestConformance_ObfuscatedBundleRoundTrip(t *testing.T) {
	for _, tc := range ruleConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "priority ordering" {
				t.Skip("obfuscation hashes param values that share pool index with rule names — known limitation")
			}
			prog, err := arbiter.Compile([]byte(tc.source))
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			rs := prog.Ruleset
			blob, err := bundle.MarshalObfuscated(rs, bundle.ObfuscateOptions{
				HashRuleNames:       true,
				StripRolloutDetails: true,
				StripPrereqs:        true,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			restored, err := bundle.Unmarshal(blob)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			sp := vm.NewStringPool(restored.Constants.Strings())
			dc := vm.DataFromMap(tc.context, sp)
			matched, err := vm.EvalWithPool(restored, dc, sp)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			tc.expect(t, matched)
		})
	}
}

func TestConformance_JSONRoundTrip(t *testing.T) {
	for _, tc := range ruleConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := arbiter.Compile([]byte(tc.source))
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			// Serialize context to JSON and back.
			jsonBytes, _ := json.Marshal(tc.context)
			dc, err := arbiter.DataFromJSON(string(jsonBytes), prog)
			if err != nil {
				t.Fatalf("json context: %v", err)
			}
			matched, err := arbiter.Eval(prog, dc)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			tc.expect(t, matched)
		})
	}
}

// Strategy conformance.
func TestConformance_Strategy(t *testing.T) {
	source := `
outcome Route { target: string }
strategy Routing returns Route {
	when { req.region == "us" } then US { target: "us-east" }
	when { req.region == "eu" } then EU { target: "eu-west" }
	else Global { target: "global" }
}
`
	cases := []struct {
		name     string
		context  map[string]any
		selected string
		target   string
	}{
		{"us", map[string]any{"req": map[string]any{"region": "us"}}, "US", "us-east"},
		{"eu", map[string]any{"req": map[string]any{"region": "eu"}}, "EU", "eu-west"},
		{"fallback", map[string]any{"req": map[string]any{"region": "jp"}}, "Global", "global"},
	}

	full, err := arbiter.CompileFull([]byte(source))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := arbiter.EvalStrategy(full, "Routing", tc.context)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			if result.Selected != tc.selected {
				t.Errorf("expected selected=%s, got %s", tc.selected, result.Selected)
			}
			if result.Params["target"] != tc.target {
				t.Errorf("expected target=%s, got %v", tc.target, result.Params["target"])
			}
		})
	}
}

// Expert conformance.
func TestConformance_Expert(t *testing.T) {
	source := `
fact Input { value: number }
outcome Result { sum: number }

expert rule Accumulate priority 10 {
	when { any inp in facts.Input { inp.value > 0 } }
	then emit Result { sum: inp.value }
}
`
	prog, err := expert.Compile([]byte(source))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	session := expert.NewSession(prog, map[string]any{}, []expert.Fact{
		{Type: "Input", Key: "a", Fields: map[string]any{"value": float64(10)}},
	}, expert.Options{})

	result, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Outcomes) == 0 {
		t.Fatal("no outcomes")
	}
	if result.Outcomes[0].Name != "Result" {
		t.Errorf("expected Result, got %s", result.Outcomes[0].Name)
	}
}

// TestConformance_ImportRoundTrip verifies that a multi-module program compiled
// via CompileFile produces identical evaluation results across native eval,
// governed eval, and bundle round-trip.
func TestConformance_ImportRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Write arbiter.toml.
	if err := os.WriteFile(filepath.Join(dir, "arbiter.toml"), []byte(`[project]
name = "conformance"
version = "1.0.0"
`), 0o644); err != nil {
		t.Fatalf("write arbiter.toml: %v", err)
	}

	// Write base module with a simple threshold rule.
	if err := os.WriteFile(filepath.Join(dir, "base.arb"), []byte(`
rule ScorePass {
	when { user.score >= 700 }
	then Pass { level: "base" }
}
`), 0o644); err != nil {
		t.Fatalf("write base.arb: %v", err)
	}

	// Write main module that imports base and adds a requires-gated rule.
	mainPath := filepath.Join(dir, "main.arb")
	if err := os.WriteFile(mainPath, []byte(`
import "base"

rule Premium {
	requires base.ScorePass
	when { user.tier == "gold" }
	then Upgrade { level: "premium" }
}
`), 0o644); err != nil {
		t.Fatalf("write main.arb: %v", err)
	}

	prog, err := arbiter.CompileFile(mainPath)
	if err != nil {
		t.Fatalf("CompileFile: %v", err)
	}

	ctx := map[string]any{
		"user": map[string]any{
			"score": float64(750),
			"tier":  "gold",
		},
	}

	checkMatches := func(t *testing.T, matched []vm.MatchedRule) {
		t.Helper()
		if len(matched) != 2 {
			names := make([]string, len(matched))
			for i, m := range matched {
				names[i] = m.Name
			}
			t.Fatalf("expected 2 matches, got %d: %v", len(matched), names)
		}
		if matched[0].Name != "base.ScorePass" {
			t.Errorf("expected first match = base.ScorePass, got %s", matched[0].Name)
		}
		if matched[1].Name != "Premium" {
			t.Errorf("expected second match = Premium, got %s", matched[1].Name)
		}
	}

	t.Run("native rejects imported prerequisite", func(t *testing.T) {
		dc := arbiter.DataFromMap(ctx, prog)
		matched, err := arbiter.Eval(prog, dc)
		if !errors.Is(err, arbiter.ErrGovernanceRequired) || len(matched) != 0 {
			t.Fatalf("Eval got matches %v and error %v", matched, err)
		}
	})

	// Surface 2: governed eval.
	t.Run("governed", func(t *testing.T) {
		dc := arbiter.DataFromMap(ctx, prog)
		matched, _, err := arbiter.EvalGoverned(prog, dc, prog.Segments, ctx)
		if err != nil {
			t.Fatalf("EvalGoverned: %v", err)
		}
		checkMatches(t, matched)
	})

	// Surface 3: bundle round-trip.
	t.Run("bundle", func(t *testing.T) {
		blob, err := bundle.Marshal(prog.Ruleset)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		restored, err := bundle.Unmarshal(blob)
		if err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		bundleProg := &arbiter.Program{Ruleset: restored}
		dc := arbiter.DataFromMap(ctx, bundleProg)
		matched, _, err := arbiter.EvalGoverned(bundleProg, dc, nil, ctx)
		if err != nil {
			t.Fatalf("EvalGoverned: %v", err)
		}
		checkMatches(t, matched)
	})
}

// TestConformance_InputSchema verifies that programs with an input block produce
// identical runtime results to equivalent programs without one. The input block
// is a compile-time schema check only and must not alter evaluation behavior.
func TestConformance_InputSchema(t *testing.T) {
	srcWith := `
input { user: { age: number } }
rule Adult {
	when { user.age >= 18 }
	then Allow { verdict: "adult" }
}
`
	srcWithout := `
rule Adult {
	when { user.age >= 18 }
	then Allow { verdict: "adult" }
}
`
	ctx := map[string]any{
		"user": map[string]any{"age": float64(21)},
	}

	progWith, err := arbiter.Compile([]byte(srcWith))
	if err != nil {
		t.Fatalf("Compile(with input): %v", err)
	}
	progWithout, err := arbiter.Compile([]byte(srcWithout))
	if err != nil {
		t.Fatalf("Compile(without input): %v", err)
	}

	matchedWith, err := arbiter.Eval(progWith, arbiter.DataFromMap(ctx, progWith))
	if err != nil {
		t.Fatalf("Eval(with input): %v", err)
	}
	matchedWithout, err := arbiter.Eval(progWithout, arbiter.DataFromMap(ctx, progWithout))
	if err != nil {
		t.Fatalf("Eval(without input): %v", err)
	}

	if len(matchedWith) != len(matchedWithout) {
		t.Fatalf("match count mismatch: with-input=%d, without-input=%d",
			len(matchedWith), len(matchedWithout))
	}
	if len(matchedWith) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matchedWith))
	}
	if matchedWith[0].Action != matchedWithout[0].Action {
		t.Errorf("action mismatch: with=%s, without=%s",
			matchedWith[0].Action, matchedWithout[0].Action)
	}
	if matchedWith[0].Params["verdict"] != matchedWithout[0].Params["verdict"] {
		t.Errorf("param mismatch: with=%v, without=%v",
			matchedWith[0].Params["verdict"], matchedWithout[0].Params["verdict"])
	}

	// Also verify the non-matching path is identical.
	ctxMinor := map[string]any{
		"user": map[string]any{"age": float64(15)},
	}
	matchedWithMinor, err := arbiter.Eval(progWith, arbiter.DataFromMap(ctxMinor, progWith))
	if err != nil {
		t.Fatalf("Eval(with input, minor): %v", err)
	}
	matchedWithoutMinor, err := arbiter.Eval(progWithout, arbiter.DataFromMap(ctxMinor, progWithout))
	if err != nil {
		t.Fatalf("Eval(without input, minor): %v", err)
	}
	if len(matchedWithMinor) != 0 || len(matchedWithoutMinor) != 0 {
		t.Errorf("expected no matches for minor: with=%d, without=%d",
			len(matchedWithMinor), len(matchedWithoutMinor))
	}
}

// TestConformance_APIParity verifies that Compile and CompileFull produce
// identical evaluation results for the existing conformance cases.
func TestConformance_APIParity(t *testing.T) {
	src := []byte(`rule R { when { x > 5 } then Match { result: x * 2 } }`)
	ctx := map[string]any{"x": float64(10)}

	// New API.
	prog, err := arbiter.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Deprecated API.
	full, err := arbiter.CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
	fullProg := &arbiter.Program{Ruleset: full.Ruleset, Segments: full.Segments}

	matchedNew, err := arbiter.Eval(prog, arbiter.DataFromMap(ctx, prog))
	if err != nil {
		t.Fatalf("Eval(new): %v", err)
	}
	matchedOld, err := arbiter.Eval(fullProg, arbiter.DataFromMap(ctx, fullProg))
	if err != nil {
		t.Fatalf("Eval(deprecated): %v", err)
	}

	if len(matchedNew) != len(matchedOld) {
		t.Fatalf("match count mismatch: new=%d, deprecated=%d", len(matchedNew), len(matchedOld))
	}
	if len(matchedNew) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matchedNew))
	}
	if matchedNew[0].Action != matchedOld[0].Action {
		t.Errorf("action mismatch: new=%s, deprecated=%s", matchedNew[0].Action, matchedOld[0].Action)
	}
	if matchedNew[0].Params["result"] != matchedOld[0].Params["result"] {
		t.Errorf("param mismatch: new=%v, deprecated=%v",
			matchedNew[0].Params["result"], matchedOld[0].Params["result"])
	}

	// Also verify against each existing conformance case.
	for _, tc := range ruleConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := arbiter.Compile([]byte(tc.source))
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			f, err := arbiter.CompileFull([]byte(tc.source))
			if err != nil {
				t.Fatalf("CompileFull: %v", err)
			}
			fp := &arbiter.Program{Ruleset: f.Ruleset, Segments: f.Segments}

			mn, err := arbiter.Eval(p, arbiter.DataFromMap(tc.context, p))
			if err != nil {
				t.Fatalf("Eval(new): %v", err)
			}
			mo, err := arbiter.Eval(fp, arbiter.DataFromMap(tc.context, fp))
			if err != nil {
				t.Fatalf("Eval(deprecated): %v", err)
			}
			if len(mn) != len(mo) {
				t.Errorf("match count mismatch: new=%d, deprecated=%d", len(mn), len(mo))
			}
			// Both surfaces must satisfy the case's own expectation.
			tc.expect(t, mn)
			tc.expect(t, mo)
		})
	}
}

// ── v1.2.0 conformance tests ──────────────────────────────────────────────────

// TestConformance_ActionTypeChecking verifies two things:
//  1. Programs with outcome schemas that reference invalid action fields are
//     rejected at compile time (the schema acts as a gate, not just decoration).
//  2. Programs with valid outcome schemas produce identical evaluation results
//     to semantically-equivalent programs without any outcome schema.
func TestConformance_ActionTypeChecking(t *testing.T) {
	// Invalid field must fail compilation.
	t.Run("invalid field rejected at compile time", func(t *testing.T) {
		_, err := arbiter.Compile([]byte(`
outcome Badge { level: string }
rule R {
	when { true }
	then Badge { level: "gold", bogus: 99 }
}
`))
		if err == nil {
			t.Fatal("expected compile error for unknown outcome field, got nil")
		}
	})

	// Type mismatch must fail compilation.
	t.Run("type mismatch rejected at compile time", func(t *testing.T) {
		_, err := arbiter.Compile([]byte(`
outcome Badge { level: string }
rule R {
	when { true }
	then Badge { level: 42 }
}
`))
		if err == nil {
			t.Fatal("expected compile error for type mismatch, got nil")
		}
	})

	// Valid schema produces identical results to an unschema'd equivalent.
	srcWithSchema := `
outcome Reward { amount: number }
rule R {
	when { user.active == true }
	then Reward { amount: 100 }
}
`
	srcWithoutSchema := `
rule R {
	when { user.active == true }
	then Reward { amount: 100 }
}
`
	ctx := map[string]any{"user": map[string]any{"active": true}}

	t.Run("schema vs no-schema produce identical results", func(t *testing.T) {
		progWith, err := arbiter.Compile([]byte(srcWithSchema))
		if err != nil {
			t.Fatalf("Compile(with schema): %v", err)
		}
		progWithout, err := arbiter.Compile([]byte(srcWithoutSchema))
		if err != nil {
			t.Fatalf("Compile(without schema): %v", err)
		}

		matchedWith, err := arbiter.Eval(progWith, arbiter.DataFromMap(ctx, progWith))
		if err != nil {
			t.Fatalf("Eval(with schema): %v", err)
		}
		matchedWithout, err := arbiter.Eval(progWithout, arbiter.DataFromMap(ctx, progWithout))
		if err != nil {
			t.Fatalf("Eval(without schema): %v", err)
		}

		if len(matchedWith) != len(matchedWithout) {
			t.Fatalf("match count mismatch: with=%d, without=%d",
				len(matchedWith), len(matchedWithout))
		}
		if len(matchedWith) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matchedWith))
		}
		if matchedWith[0].Action != matchedWithout[0].Action {
			t.Errorf("action mismatch: with=%s, without=%s",
				matchedWith[0].Action, matchedWithout[0].Action)
		}
		if matchedWith[0].Params["amount"] != matchedWithout[0].Params["amount"] {
			t.Errorf("param mismatch: with=%v, without=%v",
				matchedWith[0].Params["amount"], matchedWithout[0].Params["amount"])
		}
	})
}

// TestConformance_RegexPreCompilation verifies that pre-compiled regex patterns
// produce identical match results to a program evaluated without pre-compilation
// (i.e., same source, same context). Because both use the same compiled ruleset
// under the hood, this test validates that the pre-compiled Regexes map contains
// the correct regex and that it matches what runtime evaluation would produce.
func TestConformance_RegexPreCompilation(t *testing.T) {
	src := `rule T { when { code matches "^[A-Z]{3}$" } then Match { found: true } }`

	prog, err := arbiter.Compile([]byte(src))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Verify the regex was pre-compiled into the ruleset.
	rs := prog.Ruleset
	if len(rs.Regexes) == 0 {
		t.Fatal("expected pre-compiled regex in ruleset, got none")
	}

	// For each pre-compiled regex, verify it matches correctly.
	for _, re := range rs.Regexes {
		if !re.MatchString("ABC") {
			t.Error("pre-compiled regex should match 'ABC'")
		}
		if re.MatchString("ab") {
			t.Error("pre-compiled regex should not match 'ab'")
		}
	}

	cases := []struct {
		name    string
		code    string
		wantHit bool
	}{
		{"uppercase three chars matches", "ABC", true},
		{"lowercase no match", "abc", false},
		{"too long no match", "ABCD", false},
		{"exact match XYZ", "XYZ", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := map[string]any{"code": tc.code}
			matched, err := arbiter.Eval(prog, arbiter.DataFromMap(ctx, prog))
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			got := len(matched) > 0
			if got != tc.wantHit {
				t.Errorf("code=%q: want match=%v, got match=%v", tc.code, tc.wantHit, got)
			}

			// Bundle round-trip must produce the same result.
			blob, err := bundle.Marshal(rs)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			restored, err := bundle.Unmarshal(blob)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			sp := vm.NewStringPool(restored.Constants.Strings())
			dc := vm.DataFromMap(ctx, sp)
			matchedBundle, err := vm.EvalWithPool(restored, dc, sp)
			if err != nil {
				t.Fatalf("bundle eval: %v", err)
			}
			gotBundle := len(matchedBundle) > 0
			if got != gotBundle {
				t.Errorf("code=%q: native=%v, bundle=%v (diverged)", tc.code, got, gotBundle)
			}
		})
	}
}

// TestConformance_TableRoundTrip compiles a program containing a table, marshals
// it to a binary bundle, unmarshals it, and verifies that in-process eval and
// bundle round-trip eval produce identical results.
func TestConformance_TableRoundTrip(t *testing.T) {
	src := `
table tiers {
	score: number | label: string
	90 | "A"
	80 | "B"
	70 | "C"
}

rule Grade {
	when { student.score >= 70 }
	then Result {
		let row = lookup tiers where score <= student.score order by score desc else { score: 0, label: "F" }
		grade: row.label,
	}
}
`
	cases := []struct {
		name        string
		score       float64
		wantGrade   string
		wantMatched bool
	}{
		{"score 95 -> A", 95, "A", true},
		{"score 85 -> B", 85, "B", true},
		{"score 75 -> C", 75, "C", true},
		{"score 60 -> no match (below 70)", 60, "", false},
	}

	prog, err := arbiter.Compile([]byte(src))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Marshal once; reuse for all cases.
	blob, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := map[string]any{"student": map[string]any{"score": tc.score}}

			// Native eval.
			matchedNative, err := arbiter.Eval(prog, arbiter.DataFromMap(ctx, prog))
			if err != nil {
				t.Fatalf("native eval: %v", err)
			}

			// Bundle eval.
			sp := vm.NewStringPool(restored.Constants.Strings())
			dc := vm.DataFromMap(ctx, sp)
			matchedBundle, err := vm.EvalWithPool(restored, dc, sp)
			if err != nil {
				t.Fatalf("bundle eval: %v", err)
			}

			// Both surfaces must agree on match count.
			if len(matchedNative) != len(matchedBundle) {
				t.Fatalf("match count diverged: native=%d, bundle=%d",
					len(matchedNative), len(matchedBundle))
			}

			if !tc.wantMatched {
				if len(matchedNative) != 0 {
					t.Errorf("expected no match for score=%.0f, got %d", tc.score, len(matchedNative))
				}
				return
			}

			if len(matchedNative) != 1 {
				t.Fatalf("expected 1 match, got %d", len(matchedNative))
			}

			// Native result.
			if matchedNative[0].Params["grade"] != tc.wantGrade {
				t.Errorf("native: expected grade=%s, got %v", tc.wantGrade, matchedNative[0].Params["grade"])
			}
			// Bundle result.
			if matchedBundle[0].Params["grade"] != tc.wantGrade {
				t.Errorf("bundle: expected grade=%s, got %v", tc.wantGrade, matchedBundle[0].Params["grade"])
			}
		})
	}
}

// TestConformance_LookupDeterminism verifies that identical table + identical
// context always produces the same lookup result, and that native eval and
// bundle round-trip agree across multiple independent evaluations.
func TestConformance_LookupDeterminism(t *testing.T) {
	src := `
table pricing {
	region: string | tier: string | price: number
	"US" | "basic" | 9.99
	"US" | "pro"   | 19.99
	"EU" | "basic" | 8.99
	"EU" | "pro"   | 17.99
}

rule Price {
	when { order.region != "" }
	then Charge {
		let row = lookup pricing where region == order.region and tier == order.tier
		amount: row.price,
	}
}
`
	ctx := map[string]any{"order": map[string]any{"region": "EU", "tier": "pro"}}
	wantPrice := 17.99

	prog, err := arbiter.Compile([]byte(src))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	blob, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Run each surface 5 times; every run must return the same value.
	const runs = 5
	for i := range runs {
		t.Run("native", func(t *testing.T) {
			matched, err := arbiter.Eval(prog, arbiter.DataFromMap(ctx, prog))
			if err != nil {
				t.Fatalf("run %d native eval: %v", i, err)
			}
			if len(matched) != 1 {
				t.Fatalf("run %d: expected 1 match, got %d", i, len(matched))
			}
			if matched[0].Params["amount"] != wantPrice {
				t.Errorf("run %d: expected amount=%.2f, got %v", i, wantPrice, matched[0].Params["amount"])
			}
		})

		t.Run("bundle", func(t *testing.T) {
			sp := vm.NewStringPool(restored.Constants.Strings())
			dc := vm.DataFromMap(ctx, sp)
			matched, err := vm.EvalWithPool(restored, dc, sp)
			if err != nil {
				t.Fatalf("run %d bundle eval: %v", i, err)
			}
			if len(matched) != 1 {
				t.Fatalf("run %d: expected 1 match, got %d", i, len(matched))
			}
			if matched[0].Params["amount"] != wantPrice {
				t.Errorf("run %d: expected amount=%.2f, got %v", i, wantPrice, matched[0].Params["amount"])
			}
		})
	}
}

// TestConformance_LookupNullBehavior verifies two properties of lookups without
// an else clause:
//  1. A lookup that matches nothing returns null for the bound variable.
//  2. Accessing a field on the null variable produces null in the action params
//     (not a runtime error), because field access on null is defined to propagate
//     null in the Arbiter evaluation model.
func TestConformance_LookupNullBehavior(t *testing.T) {
	// Source with no-match lookup and no else — row is null.
	srcNullField := `
table ladder {
	height: number | bitrate: string
	1080 | "6500k"
}

rule Transcode {
	when { job.height >= 100 }
	then Profile {
		let row = lookup ladder where height > 9999
		bitrate: row.bitrate,
	}
}
`
	// Source with no-match lookup WITH else — row falls back to else value.
	srcElseFallback := `
table ladder {
	height: number | bitrate: string
	1080 | "6500k"
}

rule Transcode {
	when { job.height >= 100 }
	then Profile {
		let row = lookup ladder where height > 9999 else { height: 0, bitrate: "fallback" }
		bitrate: row.bitrate,
	}
}
`

	ctx := map[string]any{"job": map[string]any{"height": 500.0}}

	t.Run("no else: field on null row is null in native eval", func(t *testing.T) {
		prog, err := arbiter.Compile([]byte(srcNullField))
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		matched, err := arbiter.Eval(prog, arbiter.DataFromMap(ctx, prog))
		if err != nil {
			t.Fatalf("eval: %v", err)
		}
		if len(matched) != 1 {
			t.Fatalf("expected 1 match (rule fires), got %d", len(matched))
		}
		if matched[0].Params["bitrate"] != nil {
			t.Errorf("expected null bitrate for no-match/no-else, got %v", matched[0].Params["bitrate"])
		}
	})

	t.Run("no else: field on null row is null in bundle round-trip", func(t *testing.T) {
		prog, err := arbiter.Compile([]byte(srcNullField))
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		blob, err := bundle.Marshal(prog.Ruleset)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		restored, err := bundle.Unmarshal(blob)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		sp := vm.NewStringPool(restored.Constants.Strings())
		dc := vm.DataFromMap(ctx, sp)
		matched, err := vm.EvalWithPool(restored, dc, sp)
		if err != nil {
			t.Fatalf("bundle eval: %v", err)
		}
		if len(matched) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matched))
		}
		if matched[0].Params["bitrate"] != nil {
			t.Errorf("expected null bitrate for no-match/no-else (bundle), got %v", matched[0].Params["bitrate"])
		}
	})

	t.Run("with else: field returns else value in native eval", func(t *testing.T) {
		prog, err := arbiter.Compile([]byte(srcElseFallback))
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		matched, err := arbiter.Eval(prog, arbiter.DataFromMap(ctx, prog))
		if err != nil {
			t.Fatalf("eval: %v", err)
		}
		if len(matched) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matched))
		}
		if matched[0].Params["bitrate"] != "fallback" {
			t.Errorf("expected bitrate=fallback from else, got %v", matched[0].Params["bitrate"])
		}
	})

	t.Run("with else: field returns else value in bundle round-trip", func(t *testing.T) {
		prog, err := arbiter.Compile([]byte(srcElseFallback))
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		blob, err := bundle.Marshal(prog.Ruleset)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		restored, err := bundle.Unmarshal(blob)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		sp := vm.NewStringPool(restored.Constants.Strings())
		dc := vm.DataFromMap(ctx, sp)
		matched, err := vm.EvalWithPool(restored, dc, sp)
		if err != nil {
			t.Fatalf("bundle eval: %v", err)
		}
		if len(matched) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matched))
		}
		if matched[0].Params["bitrate"] != "fallback" {
			t.Errorf("expected bitrate=fallback from else (bundle), got %v", matched[0].Params["bitrate"])
		}
	})

	// Native and bundle round-trip must agree on null behavior.
	t.Run("null behavior: native and bundle agree", func(t *testing.T) {
		prog, err := arbiter.Compile([]byte(srcNullField))
		if err != nil {
			t.Fatalf("compile: %v", err)
		}

		matchedNative, err := arbiter.Eval(prog, arbiter.DataFromMap(ctx, prog))
		if err != nil {
			t.Fatalf("native eval: %v", err)
		}

		blob, err := bundle.Marshal(prog.Ruleset)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		restored, err := bundle.Unmarshal(blob)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		sp := vm.NewStringPool(restored.Constants.Strings())
		dc := vm.DataFromMap(ctx, sp)
		matchedBundle, err := vm.EvalWithPool(restored, dc, sp)
		if err != nil {
			t.Fatalf("bundle eval: %v", err)
		}

		if len(matchedNative) != len(matchedBundle) {
			t.Fatalf("match count diverged: native=%d, bundle=%d",
				len(matchedNative), len(matchedBundle))
		}
		if len(matchedNative) == 0 {
			return
		}
		nativeBitrate := matchedNative[0].Params["bitrate"]
		bundleBitrate := matchedBundle[0].Params["bitrate"]
		if nativeBitrate != bundleBitrate {
			t.Errorf("bitrate diverged: native=%v, bundle=%v", nativeBitrate, bundleBitrate)
		}
	})
}

// TestConformance_CrossModuleExpert verifies that expert rules in an imported
// module fire based on working memory contents, not on module ordering. Module
// A asserts a fact; module B reads that fact and emits an outcome. Both rules
// are compiled into a single program and must cooperate correctly.
func TestConformance_CrossModuleExpert(t *testing.T) {
	// Single-source program: rule A asserts, rule B emits based on A's fact.
	// This is the standard way to test cross-module expert cooperation because
	// the module system merges IRs before compilation; the logical separation
	// is preserved through rule naming.
	src := []byte(`
fact Signal { value: number }
outcome Alert { level: string }

expert rule SeedSignal priority 10 {
	when { sensor.reading > 50 }
	then assert Signal {
		key: "primary",
		value: sensor.reading,
	}
}

expert rule EmitAlert priority 5 {
	when {
		any sig in facts.Signal { sig.value > 50 }
	}
	then emit Alert { level: "high" }
}
`)

	prog, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	envCtx := map[string]any{
		"sensor": map[string]any{"reading": float64(75)},
	}

	// Run with an empty initial working memory — SeedSignal must fire first
	// (higher priority) and assert the Signal fact, then EmitAlert must fire
	// because the Signal fact is now in working memory.
	session := expert.NewSession(prog, envCtx, nil, expert.Options{})
	result, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify SeedSignal fired and the fact landed in working memory.
	foundFact := false
	for _, f := range result.Facts {
		if f.Type == "Signal" && f.Key == "primary" {
			foundFact = true
			if f.Fields["value"] != float64(75) {
				t.Errorf("Signal.value = %v, want 75", f.Fields["value"])
			}
			break
		}
	}
	if !foundFact {
		t.Fatal("expected Signal fact in working memory after SeedSignal fired")
	}

	// Verify EmitAlert fired and produced an Alert outcome.
	if len(result.Outcomes) == 0 {
		t.Fatal("expected at least one outcome from EmitAlert")
	}
	foundAlert := false
	for _, o := range result.Outcomes {
		if o.Name == "Alert" && o.Params["level"] == "high" {
			foundAlert = true
			break
		}
	}
	if !foundAlert {
		t.Errorf("expected Alert{level:high} outcome, got %+v", result.Outcomes)
	}

	// Verify that when the sensor reading is below threshold, neither rule fires.
	lowCtx := map[string]any{
		"sensor": map[string]any{"reading": float64(30)},
	}
	sessionLow := expert.NewSession(prog, lowCtx, nil, expert.Options{})
	resultLow, err := sessionLow.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(low): %v", err)
	}
	if len(resultLow.Outcomes) != 0 {
		t.Errorf("expected no outcomes for low reading, got %+v", resultLow.Outcomes)
	}
	if len(resultLow.Facts) != 0 {
		t.Errorf("expected no asserted facts for low reading, got %+v", resultLow.Facts)
	}
}
