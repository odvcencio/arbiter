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
