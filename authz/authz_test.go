package authz_test

import (
	"testing"

	"github.com/odvcencio/arbiter/authz"
)

func TestEvaluateSourceAllowsMatchingRequest(t *testing.T) {
	src := []byte(`
segment same_org {
	actor.org_id == resource.org_id
}

rule AdminRead {
	when segment same_org {
		actor.role == "admin"
		and action == "read"
	}
	then Allow {
		reason: "admin_read",
	}
}
`)

	decision, err := authz.EvaluateSource(src, authz.Request{
		Actor: map[string]any{
			"role":   "admin",
			"org_id": "org_1",
		},
		Action: "read",
		Resource: map[string]any{
			"org_id": "org_1",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateSource: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("expected request to be allowed, got %+v", decision)
	}
	if len(decision.Matched) != 1 || decision.Matched[0].Action != "Allow" {
		t.Fatalf("unexpected matches: %+v", decision.Matched)
	}
}

func TestEvaluateSourceDeniesNonMatchingRequest(t *testing.T) {
	src := []byte(`
rule AdminRead {
	when {
		actor.role == "admin"
		and action == "read"
	}
	then Allow {
		reason: "admin_read",
	}
}
`)

	decision, err := authz.EvaluateSource(src, authz.Request{
		Actor: map[string]any{
			"role": "viewer",
		},
		Action: "read",
		Resource: map[string]any{
			"id": "doc_1",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateSource: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("expected request to be denied, got %+v", decision)
	}
	if len(decision.Matched) != 0 {
		t.Fatalf("expected no allow matches, got %+v", decision.Matched)
	}
}

func TestBuildContextPreservesExtraContext(t *testing.T) {
	ctx := authz.BuildContext(authz.Request{
		Actor:  map[string]any{"id": "u_1"},
		Action: "read",
		Resource: map[string]any{
			"id": "doc_1",
		},
		Context: map[string]any{
			"tenant": "t_1",
		},
	})
	if ctx["tenant"] != "t_1" {
		t.Fatalf("expected tenant passthrough, got %+v", ctx)
	}
	if ctx["action"] != "read" {
		t.Fatalf("expected action binding, got %+v", ctx)
	}
}
