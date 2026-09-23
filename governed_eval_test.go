package arbiter

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/arbiter/govern"
)

func TestCompileFullExtractsSegments(t *testing.T) {
	src := []byte(`
segment high_risk {
	model.risk_score > 0.8
}

rule EnhancedRiskCheck {
	when segment high_risk {
		tx.amount > 5000
	}
	then Hold {}
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
	if len(result.Ruleset.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(result.Ruleset.Rules))
	}
	if _, ok := result.Segments.Get("high_risk"); !ok {
		t.Fatal("expected high_risk segment to be extracted")
	}
	if !result.Ruleset.Rules[0].HasSegment {
		t.Fatal("expected compiled rule to reference a segment")
	}
	if result.Program == nil {
		t.Fatal("expected CompileFull to retain lowered program")
	}
	if len(result.Program.Segments) != 1 {
		t.Fatalf("expected lowered program to contain 1 segment, got %d", len(result.Program.Segments))
	}
}

func TestEvalGovernedUsesPrereqsAndSegments(t *testing.T) {
	src := []byte(`
segment high_risk {
	model.risk_score > 0.8
}

rule BasicRiskCheck priority 0 {
	when { model.risk_score > 0.5 }
	then Flag { level: "review" }
}

rule EnhancedRiskCheck priority 1 {
	requires BasicRiskCheck
	when segment high_risk {
		tx.amount > 5000
	}
	then Flag { level: "hold" }
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	ctx := map[string]any{
		"model": map[string]any{
			"risk_score": 0.9,
		},
		"tx": map[string]any{
			"amount": 6000.0,
		},
	}
	dc := DataFromMap(ctx, &Program{Ruleset: result.Ruleset, Segments: result.Segments})

	matched, arbitrace, err := EvalGoverned(&Program{Ruleset: result.Ruleset, Segments: result.Segments}, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matched))
	}
	if matched[0].Name != "BasicRiskCheck" || matched[1].Name != "EnhancedRiskCheck" {
		t.Fatalf("unexpected match order: %+v", matched)
	}

	var sawPrereq bool
	var sawSegment bool
	for _, step := range arbitrace.Steps {
		if step.Check == "requires BasicRiskCheck" && step.Result {
			sawPrereq = true
		}
		if step.Check == "segment high_risk" && step.Result {
			sawSegment = true
		}
	}
	if !sawPrereq {
		t.Fatal("expected successful prerequisite arbitrace step")
	}
	if !sawSegment {
		t.Fatal("expected successful segment arbitrace step")
	}
}

func TestEvalGovernedKillSwitchSkipsFallback(t *testing.T) {
	src := []byte(`
rule Disabled {
	kill_switch on
	when { true }
	then Allow {}
	otherwise Deny { reason: "off" }
}

rule WithFallback {
	when { false }
	then Approve {}
	otherwise Reject { reason: "low" }
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	ctx := map[string]any{}
	dc := DataFromMap(ctx, &Program{Ruleset: result.Ruleset, Segments: result.Segments})
	matched, arbitrace, err := EvalGoverned(&Program{Ruleset: result.Ruleset, Segments: result.Segments}, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 fallback match, got %d", len(matched))
	}
	if matched[0].Name != "WithFallback" || !matched[0].Fallback {
		t.Fatalf("unexpected matched rule: %+v", matched[0])
	}

	var sawKillSwitch bool
	for _, step := range arbitrace.Steps {
		if step.Check == "kill_switch" && step.Result {
			sawKillSwitch = true
		}
	}
	if !sawKillSwitch {
		t.Fatal("expected kill_switch arbitrace step")
	}
}

func TestEvalGovernedKillSwitchOffTracesDeclaration(t *testing.T) {
	src := []byte(`
rule ExplicitlyEnabled {
	kill_switch off
	when { true }
	then Allow {}
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	ctx := map[string]any{}
	dc := DataFromMap(ctx, &Program{Ruleset: result.Ruleset, Segments: result.Segments})
	matched, arbitrace, err := EvalGoverned(&Program{Ruleset: result.Ruleset, Segments: result.Segments}, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 1 || matched[0].Name != "ExplicitlyEnabled" {
		t.Fatalf("unexpected matched rules: %+v", matched)
	}

	found := false
	for _, step := range arbitrace.Steps {
		if step.Check == "kill_switch" {
			found = true
			if step.Result {
				t.Fatalf("expected kill_switch off arbitrace to be false, got %#v", step)
			}
			if step.Detail != "kill_switch declared off" {
				t.Fatalf("unexpected kill_switch detail: %#v", step)
			}
			if step.Phase != govern.ArbitracePhaseGovernance || step.Scope != govern.ArbitraceScopeRule || step.Subject != "ExplicitlyEnabled" || step.Kind != govern.ArbitraceKindKillSwitch {
				t.Fatalf("unexpected kill_switch semantics: %#v", step)
			}
		}
	}
	if !found {
		t.Fatal("expected explicit kill_switch off arbitrace step")
	}
}

func TestEvalGovernedActiveWindowTracesTemporalEligibility(t *testing.T) {
	src := []byte(`
rule Windowed {
	active_from 2026-01-10T00:00:00Z
	active_until 2026-01-20T00:00:00Z
	when { true }
	then Allow {}
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	ctx := map[string]any{"__now": "2026-01-09T23:59:59Z"}
	dc := DataFromMap(ctx, &Program{Ruleset: result.Ruleset, Segments: result.Segments})
	matched, arbitrace, err := EvalGoverned(&Program{Ruleset: result.Ruleset, Segments: result.Segments}, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected inactive rule before active_from, got %+v", matched)
	}
	if len(arbitrace.Steps) == 0 {
		t.Fatal("expected active window arbitrace step")
	}
	step := arbitrace.Steps[0]
	if step.Kind != govern.ArbitraceKindActiveFrom || step.Result {
		t.Fatalf("unexpected active_from arbitrace step: %#v", step)
	}
	if step.Target != "2026-01-10T00:00:00Z" {
		t.Fatalf("unexpected active_from target: %#v", step)
	}
	if step.Phase != govern.ArbitracePhaseGovernance || step.Scope != govern.ArbitraceScopeRule || step.Subject != "Windowed" {
		t.Fatalf("unexpected active_from semantics: %#v", step)
	}
}

func TestEvalGovernedRolloutGatesMatches(t *testing.T) {
	src := []byte(`
rule SlowRoll {
	rollout 1
	when { true }
	then Allow {}
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	blockedUser := ""
	namespace := govern.AutoRolloutNamespace("", "rule:SlowRoll")
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("user_%d", i)
		if govern.RolloutBucket(namespace, id) >= 100 {
			blockedUser = id
			break
		}
	}
	if blockedUser == "" {
		t.Fatal("failed to find blocked rollout user")
	}

	ctx := map[string]any{"user.id": blockedUser}
	dc := DataFromMap(ctx, &Program{Ruleset: result.Ruleset, Segments: result.Segments})
	matched, arbitrace, err := EvalGoverned(&Program{Ruleset: result.Ruleset, Segments: result.Segments}, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected rollout to block match, got %+v", matched)
	}
	if len(arbitrace.Steps) == 0 {
		t.Fatal("expected rollout arbitrace step")
	}
	last := arbitrace.Steps[len(arbitrace.Steps)-1]
	if !strings.Contains(last.Check, `rollout percent 1 by user.id namespace "arbiter:rule:SlowRoll"`) || last.Result {
		t.Fatalf("unexpected rollout arbitrace: %+v", last)
	}
	if !strings.Contains(last.Detail, blockedUser) || !strings.Contains(last.Detail, "threshold=100") || !strings.Contains(last.Detail, "resolution=10000") {
		t.Fatalf("expected rollout detail to mention subject and threshold, got %q", last.Detail)
	}
}

func TestEvalGovernedExcludesUsesDeferredDisposition(t *testing.T) {
	src := []byte(`
rule First {
	excludes Second
	when { true }
	then Allow {}
}

rule Second {
	when { true }
	then Review {}
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	ctx := map[string]any{}
	dc := DataFromMap(ctx, &Program{Ruleset: result.Ruleset, Segments: result.Segments})
	matched, arbitrace, err := EvalGoverned(&Program{Ruleset: result.Ruleset, Segments: result.Segments}, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 1 || matched[0].Name != "Second" {
		t.Fatalf("expected only Second to match after conservative deferral, got %+v", matched)
	}

	var deferred *govern.ArbitraceStep
	for i := range arbitrace.Steps {
		if arbitrace.Steps[i].Check == "excludes Second" {
			deferred = &arbitrace.Steps[i]
			break
		}
	}
	if deferred == nil {
		t.Fatalf("expected deferred excludes step in arbitrace, got %+v", arbitrace.Steps)
	}
	if deferred.Result {
		t.Fatalf("expected deferred step result to remain false for compatibility, got %+v", deferred)
	}
	if deferred.Disposition != govern.ArbitraceDispositionDeferred {
		t.Fatalf("expected deferred disposition, got %+v", deferred)
	}
	if deferred.Kind != govern.ArbitraceKindExcludes || deferred.Phase != govern.ArbitracePhaseGovernance {
		t.Fatalf("unexpected deferred step semantics: %+v", deferred)
	}
}

// TestEvalGovernedSurfacesSegmentRuntimeError guards against a segment's
// runtime error (a type mismatch in its condition) being silently swallowed
// as "segment did not match". It must be reported the same way a rule
// condition error is: as a returned error, with the partial arbitrace
// annotating what happened.
func TestEvalGovernedSurfacesSegmentRuntimeError(t *testing.T) {
	src := []byte(`
segment bad_compare {
	user.balance > 10.50 USD
}

rule Gated {
	when segment bad_compare {
		true
	}
	then Allow {}
}
`)

	result, err := CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	ctx := map[string]any{
		"user": map[string]any{
			"balance": "enterprise", // comparing a string to a decimal is a VM runtime error
		},
	}
	dc := DataFromMap(ctx, &Program{Ruleset: result.Ruleset, Segments: result.Segments})

	matched, arbitrace, err := EvalGoverned(&Program{Ruleset: result.Ruleset, Segments: result.Segments}, dc, result.Segments, ctx)
	if err == nil {
		t.Fatal("EvalGoverned: expected an error for a segment runtime failure, got nil")
	}
	if matched != nil {
		t.Fatalf("EvalGoverned: expected no matches alongside the error, got %+v", matched)
	}

	var sawSegmentError bool
	for _, step := range arbitrace.Steps {
		if step.Kind == govern.ArbitraceKindSegment && strings.Contains(step.Detail, "error") {
			sawSegmentError = true
		}
	}
	if !sawSegmentError {
		t.Fatalf("expected a segment arbitrace step annotated with the error, got %+v", arbitrace.Steps)
	}
}
