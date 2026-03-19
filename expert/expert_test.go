package expert_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/expert"
)

func TestCompileExtractsExpertRules(t *testing.T) {
	src := []byte(`
const LIMIT = 600

rule StandardDecision {
	when { applicant.score >= LIMIT }
	then Approve {}
}

expert rule SeedHighRisk priority 10 {
	when {
		applicant.score < LIMIT
	}
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	rules := program.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 expert rule, got %d", len(rules))
	}
	if rules[0].Name != "SeedHighRisk" {
		t.Fatalf("unexpected rule name %q", rules[0].Name)
	}
	if rules[0].Kind != expert.ActionAssert {
		t.Fatalf("unexpected rule kind %q", rules[0].Kind)
	}

	compiled, err := arbiter.CompileFull(src)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
	if len(compiled.Ruleset.Rules) != 1 {
		t.Fatalf("expected ordinary compiler to ignore expert rules, got %d rules", len(compiled.Ruleset.Rules))
	}
}

func TestCompileRejectsInvalidModify(t *testing.T) {
	src := []byte(`
expert rule BadModify {
	when { true }
	then modify RiskFlag {
		key: "high_risk"
	}
}
`)

	if _, err := expert.Compile(src); err == nil {
		t.Fatal("expected compile to reject modify without set block")
	}
}

func TestSessionForwardChainsAssertToEmit(t *testing.T) {
	src := []byte(`
expert rule SeedHighRisk {
	when {
		applicant.score < 600
	}
	then assert RiskFlag {
		key: "high_risk",
		applicant_id: applicant.id,
		level: "high",
	}
}

expert rule RouteManualReview {
	when {
		any risk in facts.RiskFlag {
			risk.applicant_id == applicant.id
			and risk.level == "high"
		}
	}
	then emit ManualReview {
		queue: "risk",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, map[string]any{
		"applicant": map[string]any{
			"id":    "app_123",
			"score": 540.0,
		},
	}, nil, expert.Options{})

	result, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.StopReason != expert.StopQuiescent {
		t.Fatalf("unexpected stop reason %q", result.StopReason)
	}
	if result.Rounds != 2 {
		t.Fatalf("expected 2 rounds, got %d", result.Rounds)
	}
	if result.Mutations != 1 {
		t.Fatalf("expected 1 mutation, got %d", result.Mutations)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(result.Facts))
	}
	if result.Facts[0].Type != "RiskFlag" || result.Facts[0].Key != "high_risk" {
		t.Fatalf("unexpected fact: %+v", result.Facts[0])
	}
	if len(result.Outcomes) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(result.Outcomes))
	}
	if result.Outcomes[0].Name != "ManualReview" {
		t.Fatalf("unexpected outcome: %+v", result.Outcomes[0])
	}
	if len(result.Activations) != 2 {
		t.Fatalf("expected 2 activations, got %d", len(result.Activations))
	}
	if result.Activations[0].Round != 1 || result.Activations[0].Rule != "SeedHighRisk" || !result.Activations[0].Changed {
		t.Fatalf("unexpected first activation: %+v", result.Activations[0])
	}
	if result.Activations[1].Round != 2 || result.Activations[1].Rule != "RouteManualReview" || !result.Activations[1].Changed {
		t.Fatalf("unexpected second activation: %+v", result.Activations[1])
	}
}

func TestSessionRespectsKillSwitch(t *testing.T) {
	src := []byte(`
expert rule DisabledSeed {
	kill_switch
	when { true }
	then assert RiskFlag {
		key: "blocked",
		level: "high",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	result, err := expert.NewSession(program, map[string]any{}, nil, expert.Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("expected no facts, got %+v", result.Facts)
	}
	if len(result.Outcomes) != 0 {
		t.Fatalf("expected no outcomes, got %+v", result.Outcomes)
	}
	if result.Rounds != 1 {
		t.Fatalf("expected 1 round, got %d", result.Rounds)
	}
}

func TestSessionSkipsUnchangedRulesAfterQuiescence(t *testing.T) {
	src := []byte(`
expert rule SeedHighRisk {
	when { applicant.score < 600 }
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}

expert rule RouteManualReview {
	when {
		any risk in facts.RiskFlag { risk.level == "high" }
	}
	then emit ManualReview {
		queue: "risk",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, map[string]any{
		"applicant": map[string]any{"score": 540.0},
	}, nil, expert.Options{})

	first, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	if first.Rounds != 2 || len(first.Activations) != 2 {
		t.Fatalf("unexpected first run: %+v", first)
	}

	second, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	if second.Rounds != 2 {
		t.Fatalf("expected unchanged quiescent rerun to keep rounds at 2, got %d", second.Rounds)
	}
	if len(second.Activations) != 2 {
		t.Fatalf("expected unchanged quiescent rerun to keep activations at 2, got %d", len(second.Activations))
	}

	if err := session.Assert(expert.Fact{
		Type: "Unrelated",
		Key:  "marker",
		Fields: map[string]any{
			"kind": "noop",
		},
	}); err != nil {
		t.Fatalf("Assert: %v", err)
	}

	third, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run third: %v", err)
	}
	if third.Rounds != 3 {
		t.Fatalf("expected dirty unrelated fact to advance one round, got %d", third.Rounds)
	}
	if len(third.Activations) != 2 {
		t.Fatalf("expected no additional activations for unrelated fact dirtiness, got %d", len(third.Activations))
	}
	if len(third.Facts) != 2 {
		t.Fatalf("expected unrelated fact to persist in working memory, got %+v", third.Facts)
	}
}

func TestSessionRetractsDerivedFactWhenSupportDisappears(t *testing.T) {
	src := []byte(`
expert rule SeedHighRisk {
	when {
		any marker in facts.Marker { marker.kind == "high" }
	}
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}

expert rule RouteManualReview {
	when {
		any risk in facts.RiskFlag { risk.level == "high" }
	}
	then emit ManualReview {
		queue: "risk",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, nil, []expert.Fact{{
		Type: "Marker",
		Key:  "marker_1",
		Fields: map[string]any{
			"kind": "high",
		},
	}}, expert.Options{})

	first, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	risk := requireFact(t, first.Facts, "RiskFlag", "high_risk")
	if len(risk.DerivedBy) != 1 || risk.DerivedBy[0] != "SeedHighRisk" {
		t.Fatalf("expected support provenance, got %+v", risk)
	}

	if err := session.Retract("Marker", "marker_1"); err != nil {
		t.Fatalf("Retract: %v", err)
	}

	second, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	if len(second.Facts) != 0 {
		t.Fatalf("expected derived fact to retract when support disappears, got %+v", second.Facts)
	}
}

func TestSessionUsesHighestPrioritySupportForSharedDerivedFact(t *testing.T) {
	src := []byte(`
expert rule PrimarySupport priority 20 {
	when {
		any marker in facts.MarkerA { marker.kind == "high" }
	}
	then assert RiskFlag {
		key: "shared_risk",
		level: "high",
	}
}

expert rule SecondarySupport priority 10 {
	when {
		any marker in facts.MarkerB { marker.kind == "high" }
	}
	then assert RiskFlag {
		key: "shared_risk",
		level: "review",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, nil, []expert.Fact{
		{
			Type: "MarkerA",
			Key:  "a1",
			Fields: map[string]any{
				"kind": "high",
			},
		},
		{
			Type: "MarkerB",
			Key:  "b1",
			Fields: map[string]any{
				"kind": "high",
			},
		},
	}, expert.Options{})

	first, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	shared := requireFact(t, first.Facts, "RiskFlag", "shared_risk")
	if shared.Fields["level"] != "high" {
		t.Fatalf("expected higher-priority support to win, got %+v", shared)
	}
	if len(shared.DerivedBy) != 2 || shared.DerivedBy[0] != "PrimarySupport" || shared.DerivedBy[1] != "SecondarySupport" {
		t.Fatalf("unexpected support provenance: %+v", shared)
	}

	if err := session.Retract("MarkerA", "a1"); err != nil {
		t.Fatalf("Retract: %v", err)
	}

	second, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	shared = requireFact(t, second.Facts, "RiskFlag", "shared_risk")
	if shared.Fields["level"] != "review" {
		t.Fatalf("expected remaining support to win after primary retract, got %+v", shared)
	}
	if len(shared.DerivedBy) != 1 || shared.DerivedBy[0] != "SecondarySupport" {
		t.Fatalf("unexpected support provenance after retract: %+v", shared)
	}
}

func TestSessionExpertBindingsJoinFacts(t *testing.T) {
	src := []byte(`
expert rule RouteManualReview {
	when {
		bind risk in facts.RiskFlag
		bind txn in facts.Transaction
		where {
			risk.account_id == txn.account_id
			and risk.level == "high"
		}
	}
	then emit ManualReview {
		queue: "risk",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, nil, []expert.Fact{
		{
			Type: "RiskFlag",
			Key:  "risk_1",
			Fields: map[string]any{
				"account_id": "acct_1",
				"level":      "high",
			},
		},
		{
			Type: "Transaction",
			Key:  "txn_1",
			Fields: map[string]any{
				"account_id": "acct_1",
				"amount":     250.0,
			},
		},
	}, expert.Options{})

	result, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Name != "ManualReview" {
		t.Fatalf("expected joined bindings to emit ManualReview, got %+v", result.Outcomes)
	}
}

func TestSessionStopsAtMaxMutations(t *testing.T) {
	src := []byte(`
expert rule SeedHighRisk {
	when { applicant.score < 600 }
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}

expert rule DeriveSecondaryFlag {
	when {
		any risk in facts.RiskFlag { risk.level == "high" }
	}
	then assert EscalationFlag {
		key: "escalated",
		level: "critical",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	result, err := expert.NewSession(program, map[string]any{
		"applicant": map[string]any{"score": 540.0},
	}, nil, expert.Options{MaxMutations: 1}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.StopReason != expert.StopMaxMutations {
		t.Fatalf("expected stop_max_mutations, got %+v", result)
	}
	if result.Mutations != 1 {
		t.Fatalf("expected 1 mutation, got %d", result.Mutations)
	}
	if len(result.Facts) != 1 || result.Facts[0].Type != "RiskFlag" {
		t.Fatalf("unexpected facts at mutation cutoff: %+v", result.Facts)
	}
}

func TestSessionForwardChainsModifyToEmit(t *testing.T) {
	src := []byte(`
expert rule LowerRisk {
	when {
		applicant.manual_clearance == true
		and any risk in facts.RiskFlag { risk.level == "high" }
	}
	then modify RiskFlag {
		key: "high_risk"
		set {
			level: "low",
			reviewer: "alice",
		}
	}
}

expert rule ApproveAfterLowerRisk {
	when {
		any risk in facts.RiskFlag { risk.level == "low" }
	}
	then emit Approved {
		reason: "manual_clearance",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, map[string]any{
		"applicant": map[string]any{"manual_clearance": true},
	}, []expert.Fact{{
		Type: "RiskFlag",
		Key:  "high_risk",
		Fields: map[string]any{
			"key":   "high_risk",
			"level": "high",
		},
	}}, expert.Options{})

	result, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.StopReason != expert.StopQuiescent {
		t.Fatalf("unexpected stop reason: %+v", result)
	}
	if len(result.Facts) != 1 || result.Facts[0].Fields["level"] != "low" {
		t.Fatalf("expected modified low-risk fact, got %+v", result.Facts)
	}
	if result.Facts[0].Fields["reviewer"] != "alice" {
		t.Fatalf("expected reviewer annotation, got %+v", result.Facts[0].Fields)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Name != "Approved" {
		t.Fatalf("expected approval outcome, got %+v", result.Outcomes)
	}
	if len(result.Activations) != 3 {
		t.Fatalf("expected 3 activations, got %+v", result.Activations)
	}
	if result.Activations[0].Kind != expert.ActionModify {
		t.Fatalf("expected modify activation first, got %+v", result.Activations[0])
	}
	if result.Activations[1].Kind != expert.ActionModify || result.Activations[1].Changed {
		t.Fatalf("expected steady-state modify no-op second, got %+v", result.Activations[1])
	}
	if result.Activations[2].Kind != expert.ActionEmit {
		t.Fatalf("expected emit activation third, got %+v", result.Activations[2])
	}
}

func TestSessionModifyDerivedFactRevertsWhenModifierStopsMatching(t *testing.T) {
	src := []byte(`
expert rule SeedHighRisk {
	when {
		any marker in facts.Marker { marker.kind == "high" }
	}
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}

expert rule LowerRisk {
	when {
		any clearance in facts.Clearance { clearance.active == true }
		and any risk in facts.RiskFlag { risk.level == "high" }
	}
	then modify RiskFlag {
		key: "high_risk"
		set {
			level: "low",
		}
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, nil, []expert.Fact{
		{
			Type: "Marker",
			Key:  "marker_1",
			Fields: map[string]any{
				"kind": "high",
			},
		},
		{
			Type: "Clearance",
			Key:  "clear_1",
			Fields: map[string]any{
				"active": true,
			},
		},
	}, expert.Options{})

	first, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	risk := requireFact(t, first.Facts, "RiskFlag", "high_risk")
	if risk.Fields["level"] != "low" {
		t.Fatalf("expected active modifier to lower risk, got %+v", risk)
	}

	if err := session.Retract("Clearance", "clear_1"); err != nil {
		t.Fatalf("Retract: %v", err)
	}
	second, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	risk = requireFact(t, second.Facts, "RiskFlag", "high_risk")
	if risk.Fields["level"] != "high" {
		t.Fatalf("expected derived fact to revert once modifier stops matching, got %+v", risk)
	}
}

func TestSessionForwardChainsRetractToEmit(t *testing.T) {
	src := []byte(`
expert rule ClearRisk {
	when {
		applicant.clear_risk == true
		and any risk in facts.RiskFlag { risk.level == "high" }
	}
	then retract RiskFlag {
		key: "high_risk"
	}
}

expert rule ApproveWhenNoHighRisk {
	when {
		none risk in facts.RiskFlag { risk.level == "high" }
	}
	then emit Approved {
		reason: "risk_cleared",
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, map[string]any{
		"applicant": map[string]any{"clear_risk": true},
	}, []expert.Fact{{
		Type: "RiskFlag",
		Key:  "high_risk",
		Fields: map[string]any{
			"key":   "high_risk",
			"level": "high",
		},
	}}, expert.Options{})

	result, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("expected retracted facts to be empty, got %+v", result.Facts)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Name != "Approved" {
		t.Fatalf("expected approval outcome after retract, got %+v", result.Outcomes)
	}
	if len(result.Activations) != 3 {
		t.Fatalf("expected 3 activations, got %+v", result.Activations)
	}
	if result.Activations[0].Kind != expert.ActionRetract {
		t.Fatalf("expected retract activation first, got %+v", result.Activations[0])
	}
	if result.Activations[1].Kind != expert.ActionRetract || result.Activations[1].Changed {
		t.Fatalf("expected steady-state retract no-op second, got %+v", result.Activations[1])
	}
	if result.Activations[2].Kind != expert.ActionEmit {
		t.Fatalf("expected emit activation third, got %+v", result.Activations[2])
	}
}

func TestSessionRetractDerivedFactRestoresWhenRetractorStopsMatching(t *testing.T) {
	src := []byte(`
expert rule SeedHighRisk {
	when {
		any marker in facts.Marker { marker.kind == "high" }
	}
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}

expert rule SuppressRisk {
	when {
		any suppression in facts.Suppression { suppression.active == true }
		and any risk in facts.RiskFlag { risk.level == "high" }
	}
	then retract RiskFlag {
		key: "high_risk"
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := expert.NewSession(program, nil, []expert.Fact{
		{
			Type: "Marker",
			Key:  "marker_1",
			Fields: map[string]any{
				"kind": "high",
			},
		},
		{
			Type: "Suppression",
			Key:  "suppress_1",
			Fields: map[string]any{
				"active": true,
			},
		},
	}, expert.Options{})

	first, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	if hasFact(first.Facts, "RiskFlag", "high_risk") {
		t.Fatalf("expected active retractor to hide derived fact, got %+v", first.Facts)
	}

	if err := session.Retract("Suppression", "suppress_1"); err != nil {
		t.Fatalf("Retract: %v", err)
	}
	second, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	if !hasFact(second.Facts, "RiskFlag", "high_risk") {
		t.Fatalf("expected derived fact to return when retractor stops matching, got %+v", second.Facts)
	}
}

func TestSessionNoLoopSkipsSelfReactivation(t *testing.T) {
	src := []byte(`
expert rule StampRisk {
	no_loop
	when {
		any risk in facts.RiskFlag { true }
	}
	then modify RiskFlag {
		key: "high_risk"
		set {
			reviewer: "alice",
		}
	}
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	result, err := expert.NewSession(program, map[string]any{}, []expert.Fact{{
		Type: "RiskFlag",
		Key:  "high_risk",
		Fields: map[string]any{
			"key":   "high_risk",
			"level": "high",
		},
	}}, expert.Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Rounds != 1 {
		t.Fatalf("expected no_loop to stop after one round, got %+v", result)
	}
	if len(result.Activations) != 1 || result.Activations[0].Kind != expert.ActionModify {
		t.Fatalf("unexpected no_loop activations: %+v", result.Activations)
	}
}

func TestSessionActivationGroupFiresOnlyFirstRule(t *testing.T) {
	src := []byte(`
expert rule ApproveA {
	activation_group resolution
	when { true }
	then emit OutcomeA { choice: "A" }
}

expert rule ApproveB {
	activation_group resolution
	when { true }
	then emit OutcomeB { choice: "B" }
}
`)

	program, err := expert.Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	result, err := expert.NewSession(program, map[string]any{}, nil, expert.Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Name != "OutcomeA" {
		t.Fatalf("expected only first activation-group outcome, got %+v", result.Outcomes)
	}
	if len(result.Activations) != 1 || result.Activations[0].Rule != "ApproveA" {
		t.Fatalf("expected only first activation-group activation, got %+v", result.Activations)
	}
}

func TestCompileFileResolvesIncludes(t *testing.T) {
	dir := t.TempDir()
	writeExpertTestFile(t, dir, "constants.arb", `const LIMIT = 600`)
	writeExpertTestFile(t, dir, "seed.arb", `
expert rule SeedHighRisk {
	when { applicant.score < LIMIT }
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}
`)
	writeExpertTestFile(t, dir, "route.arb", `
expert rule RouteManualReview {
	when {
		any risk in facts.RiskFlag { risk.level == "high" }
	}
	then emit ManualReview {
		queue: "risk",
	}
}
`)
	main := writeExpertTestFile(t, dir, "main.arb", `
include "constants.arb"
include "seed.arb"
include "route.arb"
`)

	program, err := expert.CompileFile(main)
	if err != nil {
		t.Fatalf("CompileFile: %v", err)
	}

	result, err := expert.NewSession(program, map[string]any{
		"applicant": map[string]any{"score": 540.0},
	}, nil, expert.Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Name != "ManualReview" {
		t.Fatalf("expected included expert rules to chain, got %+v", result)
	}
}

func TestCompileFileMapsExpertErrorsToIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	bad := writeExpertTestFile(t, dir, "bad.arb", `
expert rule BadModify {
	when { true }
	then modify RiskFlag {
		key: "high_risk",
	}
}
`)
	main := writeExpertTestFile(t, dir, "main.arb", `include "bad.arb"`)

	_, err := expert.CompileFile(main)
	if err == nil {
		t.Fatal("expected compile error")
	}
	if got := err.Error(); !strings.Contains(got, bad+":2:1:") {
		t.Fatalf("expected included expert file diagnostic, got %v", err)
	}
	if !strings.Contains(err.Error(), "expert rule BadModify modify RiskFlag: non-empty set block is required") {
		t.Fatalf("unexpected expert compile error: %v", err)
	}
}

func writeExpertTestFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func requireFact(t *testing.T, facts []expert.Fact, factType, key string) expert.Fact {
	t.Helper()
	for _, fact := range facts {
		if fact.Type == factType && fact.Key == key {
			return fact
		}
	}
	t.Fatalf("missing fact %s/%s in %+v", factType, key, facts)
	return expert.Fact{}
}

func hasFact(facts []expert.Fact, factType, key string) bool {
	for _, fact := range facts {
		if fact.Type == factType && fact.Key == key {
			return true
		}
	}
	return false
}
