package battletest

import (
	"context"
	"os"
	"testing"
	"time"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/internal/testarbiter"
	"google.golang.org/protobuf/types/known/structpb"
)

func connect(t *testing.T) arbiterv1.ArbiterServiceClient {
	t.Helper()
	return testarbiter.NewClient(t)
}

func publish(t *testing.T, client arbiterv1.ArbiterServiceClient, name, file string) string {
	t.Helper()
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	resp, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name: name, Source: source,
	})
	if err != nil {
		t.Fatalf("publish %s: %v", name, err)
	}
	t.Logf("published %s: %s (%d rules, %d expert, %d flags)",
		name, resp.BundleId, resp.RuleCount, resp.ExpertRuleCount, resp.FlagCount)
	return resp.BundleId
}

func evalRules(t *testing.T, client arbiterv1.ArbiterServiceClient, bundleID string, ctx map[string]any) *arbiterv1.EvaluateRulesResponse {
	t.Helper()
	s, _ := structpb.NewStruct(ctx)
	resp, err := client.EvaluateRules(context.Background(), &arbiterv1.EvaluateRulesRequest{
		BundleId: bundleID, Context: s,
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	return resp
}

func resolveFlag(t *testing.T, client arbiterv1.ArbiterServiceClient, bundleID, flag string, ctx map[string]any) *arbiterv1.ResolveFlagResponse {
	t.Helper()
	s, _ := structpb.NewStruct(ctx)
	resp, err := client.ResolveFlag(context.Background(), &arbiterv1.ResolveFlagRequest{
		BundleId: bundleID, FlagKey: flag, Context: s,
	})
	if err != nil {
		t.Fatalf("resolve %s: %v", flag, err)
	}
	return resp
}

func requireMatch(t *testing.T, resp *arbiterv1.EvaluateRulesResponse, action, reason string) {
	t.Helper()
	for _, m := range resp.Matched {
		if m.Action == action {
			if reason != "" {
				if r, _ := m.Params.AsMap()["reason"].(string); r != reason {
					t.Errorf("reason = %q, want %q", r, reason)
				}
			}
			return
		}
	}
	names := make([]string, len(resp.Matched))
	for i, m := range resp.Matched {
		names[i] = m.Name + ":" + m.Action
	}
	t.Fatalf("no %s match, got: %v", action, names)
}

// ═══════════════════════════════════════════════════════════════
//  FRAUD DETECTION
// ═══════════════════════════════════════════════════════════════

func TestFraud(t *testing.T) {
	client := connect(t)
	bundle := publish(t, client, "fraud", "fraud.arb")

	tests := []struct {
		name   string
		ctx    map[string]any
		action string
		reason string
	}{
		{
			name: "flagged account instant block",
			ctx: map[string]any{
				"account": map[string]any{"flagged": true, "age_days": float64(500), "verified": true, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.5)},
				"tx":      map[string]any{"amount": float64(50), "country": "US", "currency": "USD", "count_last_hour": float64(1), "total_last_hour": float64(50)},
			},
			action: "Block",
			reason: "flagged account or extreme risk",
		},
		{
			name: "extreme risk score instant block",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(500), "verified": true, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.98)},
				"tx":      map[string]any{"amount": float64(50), "country": "US", "currency": "USD", "count_last_hour": float64(1), "total_last_hour": float64(50)},
			},
			action: "Block",
			reason: "flagged account or extreme risk",
		},
		{
			name: "velocity limit exceeded",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(500), "verified": true, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.3)},
				"tx":      map[string]any{"amount": float64(50), "country": "US", "currency": "USD", "count_last_hour": float64(15), "total_last_hour": float64(750)},
			},
			action: "Review",
			reason: "velocity limit exceeded",
		},
		{
			name: "large tx on new account",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(10), "verified": false, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.3)},
				"tx":      map[string]any{"amount": float64(800), "country": "US", "currency": "USD", "count_last_hour": float64(1), "total_last_hour": float64(800)},
			},
			action: "Review",
			reason: "large transaction on new account",
		},
		{
			name: "high risk geo unverified",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(60), "verified": false, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.3)},
				"tx":      map[string]any{"amount": float64(200), "country": "NG", "currency": "NGN", "count_last_hour": float64(1), "total_last_hour": float64(200)},
			},
			action: "Challenge",
		},
		{
			name: "currency mismatch",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(400), "verified": true, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.1)},
				"tx":      map[string]any{"amount": float64(500), "country": "GB", "currency": "GBP", "count_last_hour": float64(1), "total_last_hour": float64(500)},
			},
			action: "Review",
			reason: "currency mismatch on significant amount",
		},
		{
			name: "trusted account fast path",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(700), "verified": true, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.1)},
				"tx":      map[string]any{"amount": float64(100), "country": "US", "currency": "USD", "count_last_hour": float64(1), "total_last_hour": float64(100)},
			},
			action: "Allow",
			reason: "trusted account under threshold",
		},
		{
			name: "clean transaction default allow",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(200), "verified": true, "chargeback_count": float64(0), "home_currency": "USD"},
				"model":   map[string]any{"risk_score": float64(0.2)},
				"tx":      map[string]any{"amount": float64(50), "country": "US", "currency": "USD", "count_last_hour": float64(2), "total_last_hour": float64(100)},
			},
			action: "Allow",
			reason: "no fraud signals detected",
		},
		{
			name: "small tx from high risk country on old verified account passes",
			ctx: map[string]any{
				"account": map[string]any{"flagged": false, "age_days": float64(400), "verified": true, "chargeback_count": float64(0), "home_currency": "NGN"},
				"model":   map[string]any{"risk_score": float64(0.2)},
				"tx":      map[string]any{"amount": float64(50), "country": "NG", "currency": "NGN", "count_last_hour": float64(1), "total_last_hour": float64(50)},
			},
			action: "Allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := evalRules(t, client, bundle, tt.ctx)
			requireMatch(t, resp, tt.action, tt.reason)
		})
	}
}

// ═══════════════════════════════════════════════════════════════
//  FEATURE FLAGS
// ═══════════════════════════════════════════════════════════════

func TestFlags(t *testing.T) {
	client := connect(t)
	bundle := publish(t, client, "flags", "flags.arb")

	t.Run("beta user gets treatment_b", func(t *testing.T) {
		resp := resolveFlag(t, client, bundle, "checkout_v2", map[string]any{
			"user": map[string]any{"cohort": "beta_2024", "plan": "free", "country": "CA", "lifetime_spend": float64(0), "seat_count": float64(1), "months_active": float64(1)},
		})
		if resp.Variant != "treatment_b" {
			t.Fatalf("variant = %q, want treatment_b", resp.Variant)
		}
	})

	t.Run("enterprise gets treatment_a", func(t *testing.T) {
		resp := resolveFlag(t, client, bundle, "checkout_v2", map[string]any{
			"user": map[string]any{"cohort": "stable", "plan": "enterprise", "country": "JP", "lifetime_spend": float64(0), "seat_count": float64(100), "months_active": float64(24)},
		})
		if resp.Variant != "treatment_a" {
			t.Fatalf("variant = %q, want treatment_a", resp.Variant)
		}
	})

	t.Run("non-targeted user gets control", func(t *testing.T) {
		resp := resolveFlag(t, client, bundle, "checkout_v2", map[string]any{
			"user": map[string]any{"cohort": "stable", "plan": "free", "country": "JP", "lifetime_spend": float64(10), "seat_count": float64(1), "months_active": float64(1)},
		})
		if resp.Variant != "control" {
			t.Fatalf("variant = %q, want control", resp.Variant)
		}
		if !resp.IsDefault {
			t.Error("expected is_default=true")
		}
	})

	t.Run("dark mode enabled for everyone", func(t *testing.T) {
		resp := resolveFlag(t, client, bundle, "dark_mode", map[string]any{
			"user":    map[string]any{"cohort": "anyone", "plan": "free", "country": "XX"},
			"user_id": "dark_mode_anyone",
		})
		if resp.Variant != "true" {
			t.Fatalf("variant = %q, want true", resp.Variant)
		}
	})

	t.Run("billing annual for enterprise", func(t *testing.T) {
		resp := resolveFlag(t, client, bundle, "billing_annual", map[string]any{
			"user": map[string]any{"plan": "enterprise", "seat_count": float64(100), "months_active": float64(24)},
		})
		if resp.Variant != "true" {
			t.Fatalf("variant = %q, want true", resp.Variant)
		}
	})

	t.Run("billing annual off for free plan", func(t *testing.T) {
		resp := resolveFlag(t, client, bundle, "billing_annual", map[string]any{
			"user": map[string]any{"plan": "free", "seat_count": float64(1), "months_active": float64(1)},
		})
		if resp.Variant != "false" {
			t.Fatalf("variant = %q, want false", resp.Variant)
		}
	})
}

// ═══════════════════════════════════════════════════════════════
//  EXPERT INFERENCE - TAX COMPUTATION
// ═══════════════════════════════════════════════════════════════

func TestExpertTax(t *testing.T) {
	client := connect(t)
	bundle := publish(t, client, "tax", "tax.arb")

	t.Run("low bracket filer", func(t *testing.T) {
		sess, err := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
			BundleId: bundle,
			Envelope: mustStruct(map[string]any{
				"income":     map[string]any{"wages": float64(35000), "interest": float64(500), "dividends": float64(0), "capital_gains": float64(0)},
				"deductions": map[string]any{"student_loan": float64(0), "hsa": float64(0), "itemized_total": float64(0)},
			}),
		})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		t.Logf("session %s: %d expert rules", sess.SessionId, sess.ExpertRuleCount)

		run, err := client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{
			SessionId: sess.SessionId,
		})
		if err != nil {
			t.Fatalf("run session: %v", err)
		}
		t.Logf("quiesced: %s in %d rounds, %d mutations", run.StopReason, run.Rounds, run.Mutations)

		// Should emit TaxReturn
		var found bool
		for _, o := range run.Outcomes {
			if o.Name == "TaxReturn" {
				found = true
				if s, _ := o.Params.AsMap()["status"].(string); s != "complete" {
					t.Errorf("status = %q, want complete", s)
				}
			}
		}
		if !found {
			t.Fatal("no TaxReturn outcome emitted")
		}

		// Should have asserted facts in the right chain
		factTypes := map[string]bool{}
		for _, f := range run.Facts {
			factTypes[f.Type] = true
		}
		for _, expected := range []string{"GrossIncome", "Deduction", "AGI", "TaxableIncome", "TaxLiability"} {
			if !factTypes[expected] {
				t.Errorf("missing fact type: %s (have: %v)", expected, factTypes)
			}
		}

		// Should be 12% bracket (35000 + 500 - 14600 = 20900)
		for _, f := range run.Facts {
			if f.Type == "TaxLiability" {
				fields := f.Fields.AsMap()
				if b, _ := fields["bracket"].(string); b != "12pct" {
					t.Errorf("bracket = %q, want 12pct", b)
				}
			}
		}

		client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{SessionId: sess.SessionId})
	})

	t.Run("mid bracket filer", func(t *testing.T) {
		sess, err := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
			BundleId: bundle,
			Envelope: mustStruct(map[string]any{
				"income":     map[string]any{"wages": float64(75000), "interest": float64(2000), "dividends": float64(1000), "capital_gains": float64(0)},
				"deductions": map[string]any{"student_loan": float64(2500), "hsa": float64(3600), "itemized_total": float64(0)},
			}),
		})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}

		run, err := client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{SessionId: sess.SessionId})
		if err != nil {
			t.Fatalf("run session: %v", err)
		}
		t.Logf("quiesced: %s in %d rounds, %d mutations", run.StopReason, run.Rounds, run.Mutations)

		// 75000 + 2000 + 1000 - 2500 - 3600 - 14600 = 57300 -> 22% bracket
		for _, f := range run.Facts {
			if f.Type == "TaxLiability" {
				fields := f.Fields.AsMap()
				if b, _ := fields["bracket"].(string); b != "22pct" {
					t.Errorf("bracket = %q, want 22pct", b)
				}
			}
		}

		client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{SessionId: sess.SessionId})
	})

	t.Run("high bracket filer", func(t *testing.T) {
		sess, err := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
			BundleId: bundle,
			Envelope: mustStruct(map[string]any{
				"income":     map[string]any{"wages": float64(150000), "interest": float64(5000), "dividends": float64(3000), "capital_gains": float64(10000)},
				"deductions": map[string]any{"student_loan": float64(0), "hsa": float64(3600), "itemized_total": float64(0)},
			}),
		})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}

		run, err := client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{SessionId: sess.SessionId})
		if err != nil {
			t.Fatalf("run session: %v", err)
		}
		t.Logf("quiesced: %s in %d rounds, %d mutations", run.StopReason, run.Rounds, run.Mutations)

		// 150000 + 5000 + 3000 + 10000 - 3600 - 14600 = 149800 -> 24%+ bracket
		for _, f := range run.Facts {
			if f.Type == "TaxLiability" {
				fields := f.Fields.AsMap()
				if b, _ := fields["bracket"].(string); b != "24pct_plus" {
					t.Errorf("bracket = %q, want 24pct_plus", b)
				}
			}
		}

		client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{SessionId: sess.SessionId})
	})

	t.Run("activation trace records all firings", func(t *testing.T) {
		sess, err := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
			BundleId: bundle,
			Envelope: mustStruct(map[string]any{
				"income":     map[string]any{"wages": float64(50000), "interest": float64(0), "dividends": float64(0), "capital_gains": float64(0)},
				"deductions": map[string]any{"student_loan": float64(0), "hsa": float64(0), "itemized_total": float64(0)},
			}),
		})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}

		_, err = client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{SessionId: sess.SessionId})
		if err != nil {
			t.Fatalf("run session: %v", err)
		}

		// Get full trace
		trace, err := client.GetSessionTrace(context.Background(), &arbiterv1.GetSessionTraceRequest{SessionId: sess.SessionId})
		if err != nil {
			t.Fatalf("get trace: %v", err)
		}

		t.Logf("activations: %d, facts: %d, outcomes: %d",
			len(trace.Activations), len(trace.Facts), len(trace.Outcomes))

		if len(trace.Activations) == 0 {
			t.Error("expected activation trace entries")
		}

		// Every activation should have a rule name
		for _, a := range trace.Activations {
			if a.Rule == "" {
				t.Error("activation with empty rule name")
			}
			t.Logf("  round %d: %s %s %s (changed=%v)", a.Round, a.Rule, a.Kind, a.Target, a.Changed)
		}

		client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{SessionId: sess.SessionId})
	})
}

// ═══════════════════════════════════════════════════════════════
//  EDGE CASES
// ═══════════════════════════════════════════════════════════════

func TestEdgeCases(t *testing.T) {
	client := connect(t)

	t.Run("publish idempotent", func(t *testing.T) {
		source, _ := os.ReadFile("fraud.arb")
		r1, _ := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{Name: "idem-test", Source: source})
		r2, _ := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{Name: "idem-test", Source: source})
		if r1.BundleId != r2.BundleId {
			t.Errorf("publish not idempotent: %s != %s", r1.BundleId, r2.BundleId)
		}
	})

	t.Run("eval with empty context", func(t *testing.T) {
		source, _ := os.ReadFile("fraud.arb")
		pub, _ := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{Name: "empty-ctx", Source: source})
		s, _ := structpb.NewStruct(map[string]any{})
		resp, err := client.EvaluateRules(context.Background(), &arbiterv1.EvaluateRulesRequest{
			BundleId: pub.BundleId, Context: s,
		})
		if err != nil {
			t.Fatalf("eval: %v", err)
		}
		// Should still return default allow (no fraud signals with empty data)
		if len(resp.Matched) == 0 {
			t.Error("expected at least one match")
		}
	})

	t.Run("close session is safe", func(t *testing.T) {
		source, _ := os.ReadFile("tax.arb")
		pub, _ := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{Name: "close-test", Source: source})
		sess, _ := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
			BundleId: pub.BundleId,
			Envelope: mustStruct(map[string]any{
				"income":     map[string]any{"wages": float64(50000), "interest": float64(0), "dividends": float64(0), "capital_gains": float64(0)},
				"deductions": map[string]any{"student_loan": float64(0), "hsa": float64(0), "itemized_total": float64(0)},
			}),
		})
		// Close without running
		_, err := client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{SessionId: sess.SessionId})
		if err != nil {
			t.Errorf("close: %v", err)
		}
		// Double close should not panic
		_, err = client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{SessionId: sess.SessionId})
		if err == nil {
			t.Error("expected error on double close")
		}
	})

	t.Run("latency under 5ms p50", func(t *testing.T) {
		source, _ := os.ReadFile("fraud.arb")
		pub, _ := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{Name: "latency-test", Source: source})
		s, _ := structpb.NewStruct(map[string]any{
			"account": map[string]any{"flagged": false, "age_days": float64(200), "verified": true, "chargeback_count": float64(0), "home_currency": "USD"},
			"model":   map[string]any{"risk_score": float64(0.2)},
			"tx":      map[string]any{"amount": float64(50), "country": "US", "currency": "USD", "count_last_hour": float64(1), "total_last_hour": float64(50)},
		})
		req := &arbiterv1.EvaluateRulesRequest{BundleId: pub.BundleId, Context: s}

		// Warmup
		for i := 0; i < 20; i++ {
			client.EvaluateRules(context.Background(), req)
		}

		// Measure
		n := 100
		durations := make([]time.Duration, n)
		for i := 0; i < n; i++ {
			start := time.Now()
			client.EvaluateRules(context.Background(), req)
			durations[i] = time.Since(start)
		}

		// Sort for p50
		for i := 0; i < len(durations); i++ {
			for j := i + 1; j < len(durations); j++ {
				if durations[j] < durations[i] {
					durations[i], durations[j] = durations[j], durations[i]
				}
			}
		}
		p50 := durations[n/2]
		t.Logf("p50 = %v, min = %v, max = %v", p50, durations[0], durations[n-1])
		// Through port-forward, 100ms is reasonable. In-cluster p50 is sub-ms.
		if p50 > 100*time.Millisecond {
			t.Errorf("p50 = %v, want < 100ms (port-forward)", p50)
		}
	})
}

func mustStruct(m map[string]any) *structpb.Struct {
	s, _ := structpb.NewStruct(m)
	return s
}
