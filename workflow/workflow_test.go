package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/odvcencio/arbiter/expert"
)

func TestWorkflowChainsOutcomeDeltasIntoDownstreamArbiters(t *testing.T) {
	src := []byte(`
arbiter fraud_detection {
	poll 5s
	source https://transactions.internal/feed
	on Block where severity == "critical" chain account_actions
}

arbiter account_actions {
	poll 5s
	source chain://fraud_detection
}

expert rule EmitBlock priority 10 per_fact {
	when {
		any txn in facts.Transaction { txn.amount > 1000 }
	}
	then emit Block {
		key: txn.key,
		account: txn.account,
		severity: "critical",
	}
}

expert rule FreezeBlocked priority 20 per_fact {
	when {
		any block in facts.Block { true }
	}
	then emit FreezeAccount {
		key: block.key,
		account: block.account,
		source: block.source_arbiter,
	}
}
`)

	w, err := Compile(src, Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := w.ExternalSources(); len(got) != 1 || got[0] != "https://transactions.internal/feed" {
		t.Fatalf("ExternalSources = %+v", got)
	}

	if err := w.SetSourceFacts("https://transactions.internal/feed", []expert.Fact{{
		Type: "Transaction",
		Key:  "txn-1",
		Fields: map[string]any{
			"amount":  1500.0,
			"account": "acct-1",
		},
	}}); err != nil {
		t.Fatalf("SetSourceFacts: %v", err)
	}

	first, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	if want, got := []string{"fraud_detection", "account_actions"}, first.Order; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("order = %+v", got)
	}
	if len(first.Arbiters["fraud_detection"].Delta.Outcomes) != 1 || first.Arbiters["fraud_detection"].Delta.Outcomes[0].Name != "Block" {
		t.Fatalf("unexpected upstream outcomes: %+v", first.Arbiters["fraud_detection"].Delta.Outcomes)
	}
	if len(first.Arbiters["account_actions"].Delta.Outcomes) != 1 || first.Arbiters["account_actions"].Delta.Outcomes[0].Name != "FreezeAccount" {
		t.Fatalf("unexpected downstream outcomes: %+v", first.Arbiters["account_actions"].Delta.Outcomes)
	}
	if got := first.Arbiters["account_actions"].Delta.Outcomes[0].Params["source"]; got != "fraud_detection" {
		t.Fatalf("downstream source = %+v", got)
	}

	second, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	if len(second.Arbiters["fraud_detection"].Delta.Outcomes) != 0 {
		t.Fatalf("expected no repeated upstream delta, got %+v", second.Arbiters["fraud_detection"].Delta.Outcomes)
	}
	if len(second.Arbiters["account_actions"].Delta.Outcomes) != 0 {
		t.Fatalf("expected no repeated downstream delta, got %+v", second.Arbiters["account_actions"].Delta.Outcomes)
	}
}

func TestWorkflowRequiresDeclaredChainSource(t *testing.T) {
	src := []byte(`
arbiter fraud_detection {
	poll 5s
	on Block chain account_actions
}

arbiter account_actions {
	poll 5s
}
`)

	_, err := Compile(src, Options{})
	if err == nil || !strings.Contains(err.Error(), "target must declare source chain://fraud_detection") {
		t.Fatalf("expected chain source validation error, got %v", err)
	}
}

func TestWorkflowRejectsChainCycles(t *testing.T) {
	src := []byte(`
arbiter a {
	poll 1s
	source chain://b
	on Ping chain b
}

arbiter b {
	poll 1s
	source chain://a
	on Pong chain a
}
`)

	_, err := Compile(src, Options{})
	if err == nil || !strings.Contains(err.Error(), "contains a cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestWorkflowRejectsRuntimeOwnedChainSourceUpdates(t *testing.T) {
	w, err := Compile([]byte(`
arbiter upstream {
	poll 1s
}
`), Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	err = w.SetSourceFacts("chain://upstream", nil)
	if err == nil || !strings.Contains(err.Error(), "runtime-owned") {
		t.Fatalf("expected runtime-owned error, got %v", err)
	}
}

func TestWorkflowRejectsUndeclaredExternalSourceUpdates(t *testing.T) {
	w, err := Compile([]byte(`
arbiter upstream {
	poll 1s
	source https://transactions.internal/feed
}
`), Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	err = w.SetSourceFacts("https://transactions.internal/typo", nil)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("expected undeclared source error, got %v", err)
	}
}

func TestWorkflowSetArbiterEnvelopeUpdatesSessionContext(t *testing.T) {
	src := []byte(`
arbiter fraud_monitor {
	stream event
}

outcome FraudAlert {
	key: string
	user: string
}

expert rule EmitAlert priority 10 {
	when { event.user == "u-123" }
	then emit FraudAlert {
		key: event.user,
		user: event.user,
	}
}
`)

	w, err := Compile(src, Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	first, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	if got := first.Arbiters["fraud_monitor"].Delta.Outcomes; len(got) != 0 {
		t.Fatalf("unexpected outcomes without envelope: %+v", got)
	}

	if err := w.SetArbiterEnvelope("fraud_monitor", map[string]any{
		"event": map[string]any{
			"user": "u-123",
		},
	}); err != nil {
		t.Fatalf("SetArbiterEnvelope: %v", err)
	}

	second, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	if got := second.Arbiters["fraud_monitor"].Delta.Outcomes; len(got) != 1 || got[0].Name != "FraudAlert" {
		t.Fatalf("unexpected outcomes after envelope update: %+v", got)
	}
}

func TestWorkflowSetSourceFactsDeepClonesNestedValues(t *testing.T) {
	src := []byte(`
arbiter upstream {
	poll 1s
	source https://resources.internal/feed
}

expert rule EmitSeen priority 10 per_fact {
	when {
		any resource in facts.Resource { resource.tags.env == "prod" }
	}
	then emit Seen {
		key: resource.key,
		env: resource.tags.env,
	}
}
`)

	w, err := Compile(src, Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	original := []expert.Fact{{
		Type: "Resource",
		Key:  "svc-1",
		Fields: map[string]any{
			"tags": map[string]any{
				"env": "prod",
			},
		},
	}}
	if err := w.SetSourceFacts("https://resources.internal/feed", original); err != nil {
		t.Fatalf("SetSourceFacts: %v", err)
	}

	original[0].Fields["tags"].(map[string]any)["env"] = "dev"

	result, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	outcomes := result.Arbiters["upstream"].Delta.Outcomes
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if got := outcomes[0].Params["env"]; got != "prod" {
		t.Fatalf("env = %#v, want prod", got)
	}
}
