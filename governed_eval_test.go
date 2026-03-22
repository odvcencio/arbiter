package arbiter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/odvcencio/arbiter/govern"
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
	dc := DataFromMap(ctx, result.Ruleset)

	matched, trace, err := EvalGoverned(result.Ruleset, dc, result.Segments, ctx)
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
	for _, step := range trace.Steps {
		if step.Check == "requires BasicRiskCheck" && step.Result {
			sawPrereq = true
		}
		if step.Check == "segment high_risk" && step.Result {
			sawSegment = true
		}
	}
	if !sawPrereq {
		t.Fatal("expected successful prerequisite trace step")
	}
	if !sawSegment {
		t.Fatal("expected successful segment trace step")
	}
}

func TestEvalGovernedKillSwitchSkipsFallback(t *testing.T) {
	src := []byte(`
rule Disabled {
	kill_switch
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
	dc := DataFromMap(ctx, result.Ruleset)
	matched, trace, err := EvalGoverned(result.Ruleset, dc, result.Segments, ctx)
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
	for _, step := range trace.Steps {
		if step.Check == "kill_switch" && step.Result {
			sawKillSwitch = true
		}
	}
	if !sawKillSwitch {
		t.Fatal("expected kill_switch trace step")
	}
}

func TestEvalGovernedRolloutGatesMatches(t *testing.T) {
	src := []byte(`
rule SlowRoll {
	when { true }
	then Allow {}
	rollout 1
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
	dc := DataFromMap(ctx, result.Ruleset)
	matched, trace, err := EvalGoverned(result.Ruleset, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected rollout to block match, got %+v", matched)
	}
	if len(trace.Steps) == 0 {
		t.Fatal("expected rollout trace step")
	}
	last := trace.Steps[len(trace.Steps)-1]
	if !strings.Contains(last.Check, `rollout percent 1 by user.id namespace "arbiter:rule:SlowRoll"`) || last.Result {
		t.Fatalf("unexpected rollout trace: %+v", last)
	}
	if !strings.Contains(last.Detail, blockedUser) || !strings.Contains(last.Detail, "threshold=100") || !strings.Contains(last.Detail, "resolution=10000") {
		t.Fatalf("expected rollout detail to mention subject and threshold, got %q", last.Detail)
	}
}
