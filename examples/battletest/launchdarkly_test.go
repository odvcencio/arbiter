package battletest

import (
	"context"
	"fmt"
	"testing"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/govern"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func publishLD(t *testing.T, client arbiterv1.ArbiterServiceClient) string {
	t.Helper()
	return publish(t, client, "launchdarkly", "launchdarkly.arb")
}

func resolve(t *testing.T, client arbiterv1.ArbiterServiceClient, bundleID, flag string, ctx map[string]any) *arbiterv1.ResolveFlagResponse {
	t.Helper()
	return resolveFlag(t, client, bundleID, flag, ctx)
}

func rolloutUser(t *testing.T, bundleID, flag string, ruleIndex int, thresholdBps uint16, inside bool) string {
	t.Helper()
	namespace := govern.AutoRolloutNamespace(bundleID, fmt.Sprintf("flag:%s:rule:%d", flag, ruleIndex))
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("user_%03d", i)
		bucket := govern.RolloutBucket(namespace, id)
		if inside && bucket < thresholdBps {
			return id
		}
		if !inside && bucket >= thresholdBps {
			return id
		}
	}
	t.Fatalf("failed to find rollout user for %s rule %d", flag, ruleIndex)
	return ""
}

func requireVariant(t *testing.T, resp *arbiterv1.ResolveFlagResponse, want string) {
	t.Helper()
	if resp.Variant != want {
		t.Fatalf("variant = %q, want %q (reason: %s)", resp.Variant, want, resp.Reason)
	}
}

func requireDefault(t *testing.T, resp *arbiterv1.ResolveFlagResponse) {
	t.Helper()
	if !resp.IsDefault {
		t.Fatalf("expected default, got variant=%q reason=%q", resp.Variant, resp.Reason)
	}
}

func requireNotDefault(t *testing.T, resp *arbiterv1.ResolveFlagResponse) {
	t.Helper()
	if resp.IsDefault {
		t.Fatalf("expected non-default, got variant=%q", resp.Variant)
	}
}

func requirePayload(t *testing.T, resp *arbiterv1.ResolveFlagResponse, key string, want any) {
	t.Helper()
	vals := resp.Values.AsMap()
	got, ok := vals[key]
	if !ok {
		t.Fatalf("missing payload key %q (have: %v)", key, vals)
	}
	// Compare with type flexibility
	switch w := want.(type) {
	case float64:
		if g, ok := got.(float64); !ok || g != w {
			t.Errorf("payload[%s] = %v, want %v", key, got, want)
		}
	case bool:
		if g, ok := got.(bool); !ok || g != w {
			t.Errorf("payload[%s] = %v, want %v", key, got, want)
		}
	case string:
		if g, ok := got.(string); !ok || g != w {
			t.Errorf("payload[%s] = %v, want %v", key, got, want)
		}
	default:
		t.Errorf("unsupported want type for %s", key)
	}
}

// ═══════════════════════════════════════════════════════════════
//  BOOLEAN FLAGS
// ═══════════════════════════════════════════════════════════════

func TestLD_BooleanFlags(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("dark_mode on for everyone", func(t *testing.T) {
		resp := resolve(t, client, bundle, "dark_mode", map[string]any{
			"user":    map[string]any{"email": "nobody@example.com"},
			"user_id": "dark_mode_everyone",
		})
		requireVariant(t, resp, "true")
	})

	t.Run("maintenance_mode off by default (kill_switch)", func(t *testing.T) {
		resp := resolve(t, client, bundle, "maintenance_mode", map[string]any{
			"user": map[string]any{"email": "anyone@example.com"},
		})
		requireVariant(t, resp, "false")
		requireDefault(t, resp)
	})

	t.Run("payments_enabled for enterprise", func(t *testing.T) {
		resp := resolve(t, client, bundle, "payments_enabled", map[string]any{
			"user": map[string]any{"plan": "enterprise", "email": "cto@bigcorp.com"},
		})
		requireVariant(t, resp, "true")
		requireNotDefault(t, resp)
	})

	t.Run("payments_enabled for pro", func(t *testing.T) {
		resp := resolve(t, client, bundle, "payments_enabled", map[string]any{
			"user": map[string]any{"plan": "pro", "email": "dev@startup.io"},
		})
		requireVariant(t, resp, "true")
	})

	t.Run("payments_disabled for free", func(t *testing.T) {
		resp := resolve(t, client, bundle, "payments_enabled", map[string]any{
			"user": map[string]any{"plan": "free", "email": "hobby@gmail.com"},
		})
		requireVariant(t, resp, "false")
		requireDefault(t, resp)
	})
}

// ═══════════════════════════════════════════════════════════════
//  PROGRESSIVE ROLLOUT
// ═══════════════════════════════════════════════════════════════

func TestLD_ProgressiveRollout(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("internal user always gets new_dashboard", func(t *testing.T) {
		resp := resolve(t, client, bundle, "new_dashboard", map[string]any{
			"user": map[string]any{"email": "dev@m31labs.dev", "plan": "free", "cohort": "stable"},
		})
		requireVariant(t, resp, "true")
	})

	t.Run("beta cohort always gets new_dashboard", func(t *testing.T) {
		resp := resolve(t, client, bundle, "new_dashboard", map[string]any{
			"user": map[string]any{"email": "tester@example.com", "plan": "free", "cohort": "beta_2024"},
		})
		requireVariant(t, resp, "true")
	})

	t.Run("enterprise in 50pct rollout", func(t *testing.T) {
		userID := rolloutUser(t, bundle, "new_dashboard", 2, 5000, true)
		resp := resolve(t, client, bundle, "new_dashboard", map[string]any{
			"user":    map[string]any{"email": "boss@bigcorp.com", "plan": "enterprise", "cohort": "stable"},
			"user_id": userID,
		})
		requireVariant(t, resp, "true")
	})

	t.Run("free user outside 10pct rollout", func(t *testing.T) {
		userID := rolloutUser(t, bundle, "new_dashboard", 3, 1000, false)
		resp := resolve(t, client, bundle, "new_dashboard", map[string]any{
			"user":    map[string]any{"email": "casual@gmail.com", "plan": "free", "cohort": "stable"},
			"user_id": userID,
		})
		requireVariant(t, resp, "false")
		requireDefault(t, resp)
	})

	t.Run("enterprise in 50pct rollout (bucket 37)", func(t *testing.T) {
		userID := rolloutUser(t, bundle, "new_dashboard", 2, 5000, true)
		resp := resolve(t, client, bundle, "new_dashboard", map[string]any{
			"user":    map[string]any{"email": "mgr@bigcorp.com", "plan": "enterprise", "cohort": "stable"},
			"user_id": userID,
		})
		requireVariant(t, resp, "true")
	})
}

// ═══════════════════════════════════════════════════════════════
//  PREREQUISITES
// ═══════════════════════════════════════════════════════════════

func TestLD_Prerequisites(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("checkout_flow blocked when payments disabled", func(t *testing.T) {
		resp := resolve(t, client, bundle, "checkout_flow", map[string]any{
			"user": map[string]any{"email": "free@gmail.com", "plan": "free", "cohort": "stable", "country": "US", "lifetime_spend": float64(0)},
		})
		requireVariant(t, resp, "control")
		requireDefault(t, resp)
	})

	t.Run("checkout_flow works when payments enabled", func(t *testing.T) {
		resp := resolve(t, client, bundle, "checkout_flow", map[string]any{
			"user": map[string]any{"email": "dev@m31labs.dev", "plan": "enterprise", "cohort": "stable", "country": "US", "lifetime_spend": float64(0)},
		})
		// Internal user → multi_step_upsell
		requireVariant(t, resp, "multi_step_upsell")
	})

	t.Run("checkout_flow beta gets multi_step", func(t *testing.T) {
		resp := resolve(t, client, bundle, "checkout_flow", map[string]any{
			"user": map[string]any{"email": "tester@example.com", "plan": "pro", "cohort": "beta_2024", "country": "JP", "lifetime_spend": float64(0)},
		})
		requireVariant(t, resp, "multi_step")
	})

	t.Run("checkout_flow enterprise US gets single_page", func(t *testing.T) {
		resp := resolve(t, client, bundle, "checkout_flow", map[string]any{
			"user": map[string]any{"email": "cfo@bigcorp.com", "plan": "enterprise", "cohort": "stable", "country": "US", "lifetime_spend": float64(0)},
		})
		requireVariant(t, resp, "single_page")
	})
}

// ═══════════════════════════════════════════════════════════════
//  VARIANT PAYLOADS
// ═══════════════════════════════════════════════════════════════

func TestLD_VariantPayloads(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("pricing aggressive has correct payload", func(t *testing.T) {
		resp := resolve(t, client, bundle, "pricing_tier", map[string]any{
			"user": map[string]any{"plan": "pro", "lifetime_spend": float64(2000), "signup_days": float64(365)},
		})
		requireVariant(t, resp, "aggressive")
		requirePayload(t, resp, "discount_pct", float64(20))
		requirePayload(t, resp, "trial_days", float64(30))
		requirePayload(t, resp, "show_annual", true)
	})

	t.Run("pricing moderate for enterprise", func(t *testing.T) {
		resp := resolve(t, client, bundle, "pricing_tier", map[string]any{
			"user": map[string]any{"plan": "enterprise", "lifetime_spend": float64(500), "signup_days": float64(365)},
		})
		requireVariant(t, resp, "moderate")
		requirePayload(t, resp, "discount_pct", float64(10))
		requirePayload(t, resp, "trial_days", float64(14))
	})

	t.Run("pricing standard for default", func(t *testing.T) {
		resp := resolve(t, client, bundle, "pricing_tier", map[string]any{
			"user":    map[string]any{"plan": "free", "lifetime_spend": float64(0), "signup_days": float64(365)},
			"user_id": "user_002", // bucket 90, outside rollout
		})
		requireVariant(t, resp, "standard")
		requirePayload(t, resp, "discount_pct", float64(0))
		requirePayload(t, resp, "trial_days", float64(7))
		requirePayload(t, resp, "show_annual", false)
	})

	t.Run("search_v2 ai_powered for internal", func(t *testing.T) {
		resp := resolve(t, client, bundle, "search_v2", map[string]any{
			"user":   map[string]any{"email": "eng@m31labs.dev", "plan": "enterprise", "country": "US"},
			"client": map[string]any{"platform": "web"},
		})
		requireVariant(t, resp, "ai_powered")
		requirePayload(t, resp, "engine", "vector")
		requirePayload(t, resp, "rerank", true)
		requirePayload(t, resp, "max_results", float64(50))
	})

	t.Run("search_v2 hybrid for EU", func(t *testing.T) {
		resp := resolve(t, client, bundle, "search_v2", map[string]any{
			"user":   map[string]any{"email": "user@example.de", "plan": "pro", "country": "DE"},
			"client": map[string]any{"platform": "web"},
		})
		requireVariant(t, resp, "hybrid")
		requirePayload(t, resp, "engine", "hybrid")
		requirePayload(t, resp, "max_results", float64(25))
	})

	t.Run("search_v2 legacy for non-targeted", func(t *testing.T) {
		resp := resolve(t, client, bundle, "search_v2", map[string]any{
			"user":    map[string]any{"email": "user@example.jp", "plan": "free", "country": "JP"},
			"client":  map[string]any{"platform": "web"},
			"user_id": "user_002",
		})
		requireVariant(t, resp, "legacy")
		requireDefault(t, resp)
	})
}

// ═══════════════════════════════════════════════════════════════
//  SEGMENT + INLINE COMBO
// ═══════════════════════════════════════════════════════════════

func TestLD_SegmentInlineCombo(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("enterprise US gets single_page checkout", func(t *testing.T) {
		resp := resolve(t, client, bundle, "checkout_flow", map[string]any{
			"user": map[string]any{"email": "cfo@bigcorp.com", "plan": "enterprise", "cohort": "stable", "country": "US", "lifetime_spend": float64(0)},
		})
		requireVariant(t, resp, "single_page")
	})

	t.Run("enterprise non-US gets control (combo fails)", func(t *testing.T) {
		resp := resolve(t, client, bundle, "checkout_flow", map[string]any{
			"user":    map[string]any{"email": "cfo@bigcorp.co.uk", "plan": "enterprise", "cohort": "stable", "country": "GB", "lifetime_spend": float64(0)},
			"user_id": "user_002", // bucket 90, outside 30% rollout
		})
		requireVariant(t, resp, "control")
	})

	t.Run("US enterprise search gets ai_powered", func(t *testing.T) {
		resp := resolve(t, client, bundle, "search_v2", map[string]any{
			"user":   map[string]any{"email": "eng@bigcorp.com", "plan": "enterprise", "country": "US"},
			"client": map[string]any{"platform": "web"},
		})
		requireVariant(t, resp, "ai_powered")
	})

	t.Run("US free search gets legacy", func(t *testing.T) {
		resp := resolve(t, client, bundle, "search_v2", map[string]any{
			"user":    map[string]any{"email": "user@example.com", "plan": "free", "country": "US"},
			"client":  map[string]any{"platform": "web"},
			"user_id": "user_002",
		})
		requireVariant(t, resp, "legacy")
	})
}

// ═══════════════════════════════════════════════════════════════
//  RUNTIME OVERRIDES
// ═══════════════════════════════════════════════════════════════

func TestLD_RuntimeOverrides(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("kill switch override disables flag", func(t *testing.T) {
		// First verify it works
		resp := resolve(t, client, bundle, "dark_mode", map[string]any{
			"user":    map[string]any{"email": "anyone@example.com"},
			"user_id": "dark_mode_override",
		})
		requireVariant(t, resp, "true")

		// Enable kill switch via override
		_, err := client.SetFlagOverride(context.Background(), &arbiterv1.SetFlagOverrideRequest{
			BundleId:   bundle,
			FlagKey:    "dark_mode",
			KillSwitch: wrapperspb.Bool(true),
		})
		if err != nil {
			t.Fatalf("set override: %v", err)
		}

		// Now it should return default
		resp = resolve(t, client, bundle, "dark_mode", map[string]any{
			"user": map[string]any{"email": "anyone@example.com"},
		})
		requireVariant(t, resp, "false")
		requireDefault(t, resp)

		// Clear override
		_, err = client.SetFlagOverride(context.Background(), &arbiterv1.SetFlagOverrideRequest{
			BundleId: bundle,
			FlagKey:  "dark_mode",
		})
		if err != nil {
			t.Fatalf("clear override: %v", err)
		}

		// Should work again
		resp = resolve(t, client, bundle, "dark_mode", map[string]any{
			"user":    map[string]any{"email": "anyone@example.com"},
			"user_id": "dark_mode_override",
		})
		requireVariant(t, resp, "true")
	})

	t.Run("rollout override changes percentage", func(t *testing.T) {
		userID := rolloutUser(t, bundle, "new_dashboard", 3, 1000, false)
		resp := resolve(t, client, bundle, "new_dashboard", map[string]any{
			"user":    map[string]any{"email": "casual@gmail.com", "plan": "free", "cohort": "stable"},
			"user_id": userID,
		})
		requireVariant(t, resp, "false")

		// Override the last rule (index 3, the 10% catch-all) to 100%
		_, err := client.SetFlagRuleOverride(context.Background(), &arbiterv1.SetFlagRuleOverrideRequest{
			BundleId:  bundle,
			FlagKey:   "new_dashboard",
			RuleIndex: 3,
			Rollout:   wrapperspb.UInt32(100),
		})
		if err != nil {
			t.Fatalf("set rule override: %v", err)
		}

		// Now user_002 should be included
		resp = resolve(t, client, bundle, "new_dashboard", map[string]any{
			"user":    map[string]any{"email": "casual@gmail.com", "plan": "free", "cohort": "stable"},
			"user_id": userID,
		})
		requireVariant(t, resp, "true")

		// Clear override
		_, err = client.SetFlagRuleOverride(context.Background(), &arbiterv1.SetFlagRuleOverrideRequest{
			BundleId:  bundle,
			FlagKey:   "new_dashboard",
			RuleIndex: 3,
		})
		if err != nil {
			t.Fatalf("clear override: %v", err)
		}
	})
}

// ═══════════════════════════════════════════════════════════════
//  EXPLAIN / TRACE
// ═══════════════════════════════════════════════════════════════

func TestLD_Explain(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("trace shows prerequisite check", func(t *testing.T) {
		s, _ := structpb.NewStruct(map[string]any{
			"user": map[string]any{"email": "free@gmail.com", "plan": "free", "cohort": "stable", "country": "US", "lifetime_spend": float64(0)},
		})
		resp, err := client.ResolveFlag(context.Background(), &arbiterv1.ResolveFlagRequest{
			BundleId: bundle,
			FlagKey:  "checkout_flow",
			Context:  s,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}

		// Should have trace steps showing prerequisite failure
		if len(resp.Trace) == 0 {
			t.Fatal("expected trace steps")
		}

		foundPrereq := false
		for _, step := range resp.Trace {
			t.Logf("trace: %s = %v (%s)", step.Check, step.Result, step.Detail)
			if step.Check == "requires payments_enabled" {
				foundPrereq = true
				if step.Result {
					t.Error("payments_enabled should fail for free plan")
				}
			}
		}
		if !foundPrereq {
			t.Error("expected prerequisite trace step")
		}
	})

	t.Run("trace shows segment match", func(t *testing.T) {
		s, _ := structpb.NewStruct(map[string]any{
			"user": map[string]any{"email": "dev@m31labs.dev", "plan": "enterprise", "cohort": "stable", "country": "US", "lifetime_spend": float64(0)},
		})
		resp, err := client.ResolveFlag(context.Background(), &arbiterv1.ResolveFlagRequest{
			BundleId: bundle,
			FlagKey:  "checkout_flow",
			Context:  s,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}

		requireVariant(t, resp, "multi_step_upsell")

		foundSegment := false
		for _, step := range resp.Trace {
			t.Logf("trace: %s = %v (%s)", step.Check, step.Result, step.Detail)
			if step.Check == "segment internal_users" {
				foundSegment = true
				if !step.Result {
					t.Error("internal_users segment should match")
				}
			}
		}
		if !foundSegment {
			t.Error("expected segment trace step")
		}
	})
}

// ═══════════════════════════════════════════════════════════════
//  EDGE CASES
// ═══════════════════════════════════════════════════════════════

func TestLD_EdgeCases(t *testing.T) {
	client := connect(t)
	bundle := publishLD(t, client)

	t.Run("missing context fields return default", func(t *testing.T) {
		resp := resolve(t, client, bundle, "checkout_flow", map[string]any{})
		requireVariant(t, resp, "control")
		requireDefault(t, resp)
	})

	t.Run("partial context still evaluates", func(t *testing.T) {
		resp := resolve(t, client, bundle, "pricing_tier", map[string]any{
			"user": map[string]any{"plan": "enterprise"},
		})
		requireVariant(t, resp, "moderate")
	})

	t.Run("unknown flag returns error", func(t *testing.T) {
		s, _ := structpb.NewStruct(map[string]any{})
		_, err := client.ResolveFlag(context.Background(), &arbiterv1.ResolveFlagRequest{
			BundleId: bundle,
			FlagKey:  "nonexistent_flag",
			Context:  s,
		})
		if err == nil {
			t.Fatal("expected error for unknown flag")
		}
	})

	t.Run("deterministic rollout same user same result", func(t *testing.T) {
		ctx := map[string]any{
			"user":    map[string]any{"email": "test@example.com", "plan": "free", "cohort": "stable"},
			"user_id": "user_001",
		}
		results := make([]string, 10)
		for i := 0; i < 10; i++ {
			resp := resolve(t, client, bundle, "new_dashboard", ctx)
			results[i] = resp.Variant
		}
		for i := 1; i < len(results); i++ {
			if results[i] != results[0] {
				t.Fatalf("non-deterministic: run %d got %q, run 0 got %q", i, results[i], results[0])
			}
		}
		t.Logf("deterministic: all 10 runs returned %q", results[0])
	})
}
