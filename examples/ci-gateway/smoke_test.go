package main

import (
	"context"
	"os"
	"testing"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// addr returns the arbiter gRPC address. In-cluster tests use the k8s
// service DNS; local tests can override via ARBITER_ADDR.
func addr() string {
	if a := os.Getenv("ARBITER_ADDR"); a != "" {
		return a
	}
	return "arbiter.orchard.svc.cluster.local:8081"
}

func setup(t *testing.T) (arbiterv1.ArbiterServiceClient, string) {
	t.Helper()

	conn, err := grpc.NewClient(addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	client := arbiterv1.NewArbiterServiceClient(conn)

	source, err := os.ReadFile("../ci_governance.arb")
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}

	resp, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "ci-governance",
		Source: source,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	t.Logf("published bundle %s: %d rules, checksum %s", resp.BundleId, resp.RuleCount, resp.Checksum)
	return client, resp.BundleId
}

func eval(t *testing.T, client arbiterv1.ArbiterServiceClient, bundleID string, ctx map[string]any) *arbiterv1.EvaluateRulesResponse {
	t.Helper()
	s, err := structpb.NewStruct(ctx)
	if err != nil {
		t.Fatalf("struct: %v", err)
	}
	resp, err := client.EvaluateRules(context.Background(), &arbiterv1.EvaluateRulesRequest{
		BundleId: bundleID,
		Context:  s,
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	return resp
}

func findAction(resp *arbiterv1.EvaluateRulesResponse, action string) *arbiterv1.RuleMatch {
	for _, m := range resp.Matched {
		if m.Action == action {
			return m
		}
	}
	return nil
}

func requireAction(t *testing.T, resp *arbiterv1.EvaluateRulesResponse, action, reason string) {
	t.Helper()
	m := findAction(resp, action)
	if m == nil {
		t.Fatalf("expected %s, got matches: %v", action, resp.Matched)
	}
	if reason != "" {
		if r, _ := m.Params.AsMap()["reason"].(string); r != reason {
			t.Errorf("reason = %q, want %q", r, reason)
		}
	}
}

func requireNoDeny(t *testing.T, resp *arbiterv1.EvaluateRulesResponse) {
	t.Helper()
	if m := findAction(resp, "Deny"); m != nil {
		r, _ := m.Params.AsMap()["reason"].(string)
		t.Fatalf("unexpected Deny: %s (%s)", m.Name, r)
	}
}

// ── Test Cases ──────────────────────────────────────────────────

func TestProductionBranchAlwaysAllowed(t *testing.T) {
	client, bundle := setup(t)

	for _, branch := range []string{"main", "release"} {
		t.Run(branch, func(t *testing.T) {
			resp := eval(t, client, bundle, map[string]any{
				"workflow": map[string]any{
					"branch":               branch,
					"name":                 "ci",
					"estimated_minutes":    float64(30),
					"minutes_since_last_run": float64(0),
				},
				"billing": map[string]any{"used_minutes_pct": float64(99)},
				"context": map[string]any{"hour": float64(3)},
			})
			requireAction(t, resp, "Allow", "production branch always runs")
		})
	}
}

func TestBudgetCriticalBlocksNonProd(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "feat/new-thing",
			"name":                 "ci",
			"estimated_minutes":    float64(5),
			"minutes_since_last_run": float64(60),
		},
		"billing": map[string]any{"used_minutes_pct": float64(95)},
		"context": map[string]any{"hour": float64(14)},
	})
	requireAction(t, resp, "Deny", "budget over 90%, non-production runs blocked")
}

func TestBudgetWarningBlocksExpensiveFeatureBranch(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "feat/big-refactor",
			"name":                 "full-suite",
			"estimated_minutes":    float64(20),
			"minutes_since_last_run": float64(60),
		},
		"billing": map[string]any{"used_minutes_pct": float64(80)},
		"context": map[string]any{"hour": float64(14)},
	})
	requireAction(t, resp, "Deny", "budget over 75%, expensive feature branch runs blocked")
}

func TestBudgetWarningAllowsCheapFeatureBranch(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "feat/small-fix",
			"name":                 "lint",
			"estimated_minutes":    float64(3),
			"minutes_since_last_run": float64(60),
		},
		"billing": map[string]any{"used_minutes_pct": float64(80)},
		"context": map[string]any{"hour": float64(14)},
	})
	requireNoDeny(t, resp)
	requireAction(t, resp, "Allow", "default allow")
}

func TestRapidRerunBlocked(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "feat/wip",
			"name":                 "ci",
			"estimated_minutes":    float64(5),
			"minutes_since_last_run": float64(2),
		},
		"billing": map[string]any{"used_minutes_pct": float64(10)},
		"context": map[string]any{"hour": float64(14)},
	})
	requireAction(t, resp, "Deny", "same workflow ran less than 5 minutes ago")
}

func TestRapidRerunAllowedOnMain(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "main",
			"name":                 "ci",
			"estimated_minutes":    float64(5),
			"minutes_since_last_run": float64(1),
		},
		"billing": map[string]any{"used_minutes_pct": float64(10)},
		"context": map[string]any{"hour": float64(14)},
	})
	requireNoDeny(t, resp)
	requireAction(t, resp, "Allow", "production branch always runs")
}

func TestOffHoursBlocksExpensiveNonProd(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "feat/big-test",
			"name":                 "e2e-suite",
			"estimated_minutes":    float64(25),
			"minutes_since_last_run": float64(60),
		},
		"billing": map[string]any{"used_minutes_pct": float64(30)},
		"context": map[string]any{"hour": float64(2)},
	})
	requireAction(t, resp, "Deny", "expensive non-production workflow during off-hours")
}

func TestOffHoursAllowsCheapNonProd(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "feat/small-fix",
			"name":                 "lint",
			"estimated_minutes":    float64(3),
			"minutes_since_last_run": float64(60),
		},
		"billing": map[string]any{"used_minutes_pct": float64(30)},
		"context": map[string]any{"hour": float64(23)},
	})
	requireNoDeny(t, resp)
	requireAction(t, resp, "Allow", "default allow")
}

func TestOffHoursAllowsProductionAlways(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "release",
			"name":                 "deploy",
			"estimated_minutes":    float64(30),
			"minutes_since_last_run": float64(0),
		},
		"billing": map[string]any{"used_minutes_pct": float64(95)},
		"context": map[string]any{"hour": float64(3)},
	})
	requireAction(t, resp, "Allow", "production branch always runs")
}

func TestNormalFeatureBranchAllowed(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "feat/add-login",
			"name":                 "ci",
			"estimated_minutes":    float64(8),
			"minutes_since_last_run": float64(30),
		},
		"billing": map[string]any{"used_minutes_pct": float64(40)},
		"context": map[string]any{"hour": float64(10)},
	})
	requireNoDeny(t, resp)
	requireAction(t, resp, "Allow", "default allow")
}

func TestRandomBranchAllowedUnderBudget(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "experiment/something",
			"name":                 "ci",
			"estimated_minutes":    float64(5),
			"minutes_since_last_run": float64(60),
		},
		"billing": map[string]any{"used_minutes_pct": float64(20)},
		"context": map[string]any{"hour": float64(15)},
	})
	requireNoDeny(t, resp)
	requireAction(t, resp, "Allow", "default allow")
}

func TestBudgetCriticalStillAllowsProduction(t *testing.T) {
	client, bundle := setup(t)

	resp := eval(t, client, bundle, map[string]any{
		"workflow": map[string]any{
			"branch":               "main",
			"name":                 "deploy",
			"estimated_minutes":    float64(30),
			"minutes_since_last_run": float64(1),
		},
		"billing": map[string]any{"used_minutes_pct": float64(99)},
		"context": map[string]any{"hour": float64(3)},
	})
	requireAction(t, resp, "Allow", "production branch always runs")
}
