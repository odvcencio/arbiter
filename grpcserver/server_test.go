package grpcserver

import (
	"context"
	"net"
	"testing"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/overrides"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const testSource = `
segment enterprise {
	user.plan == "enterprise"
}

rule HighValue {
	when segment enterprise {
		user.cart_total > 100
	}
	then Approve {
		tier: "gold",
	}
}

flag checkout_v2 type boolean default false {
	when enterprise then true
}

expert rule SeedHighRisk {
	when {
		user.risk_score > 0.8
	}
	then assert RiskFlag {
		key: "high_risk",
		user_id: user.id,
		level: "high",
	}
}

expert rule RouteManualReview {
	when {
		any risk in facts.RiskFlag {
			risk.user_id == user.id
			and risk.level == "high"
		}
	}
	then emit ManualReview {
		queue: "risk",
	}
}
`

const expertMutationSource = `
expert rule LowerRisk {
	when {
		user.manual_clearance == true
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
`

func TestServerPublishEvaluateAndOverride(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}
	if pub.RuleCount != 1 || pub.FlagCount != 1 {
		t.Fatalf("unexpected publish counts: %+v", pub)
	}
	if pub.ExpertRuleCount != 2 {
		t.Fatalf("unexpected expert rule count: %+v", pub)
	}

	ctxMap, err := structpb.NewStruct(map[string]any{
		"user": map[string]any{
			"plan":       "enterprise",
			"cart_total": 150.0,
		},
	})
	if err != nil {
		t.Fatalf("NewStruct: %v", err)
	}

	eval, err := client.EvaluateRules(context.Background(), &arbiterv1.EvaluateRulesRequest{
		BundleId: pub.BundleId,
		Context:  ctxMap,
	})
	if err != nil {
		t.Fatalf("EvaluateRules: %v", err)
	}
	if len(eval.Matched) != 1 || eval.Matched[0].Action != "Approve" {
		t.Fatalf("unexpected evaluate response: %+v", eval)
	}

	flagResp, err := client.ResolveFlag(context.Background(), &arbiterv1.ResolveFlagRequest{
		BundleId: pub.BundleId,
		FlagKey:  "checkout_v2",
		Context:  ctxMap,
	})
	if err != nil {
		t.Fatalf("ResolveFlag: %v", err)
	}
	if flagResp.Variant != "true" {
		t.Fatalf("expected checkout_v2=true, got %+v", flagResp)
	}

	if _, err := client.SetRuleOverride(context.Background(), &arbiterv1.SetRuleOverrideRequest{
		BundleId:   pub.BundleId,
		RuleName:   "HighValue",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetRuleOverride: %v", err)
	}

	eval, err = client.EvaluateRules(context.Background(), &arbiterv1.EvaluateRulesRequest{
		BundleId: pub.BundleId,
		Context:  ctxMap,
	})
	if err != nil {
		t.Fatalf("EvaluateRules after override: %v", err)
	}
	if len(eval.Matched) != 0 {
		t.Fatalf("expected kill-switched rule to be blocked, got %+v", eval)
	}

	if _, err := client.SetFlagOverride(context.Background(), &arbiterv1.SetFlagOverrideRequest{
		BundleId:   pub.BundleId,
		FlagKey:    "checkout_v2",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetFlagOverride: %v", err)
	}

	flagResp, err = client.ResolveFlag(context.Background(), &arbiterv1.ResolveFlagRequest{
		BundleId: pub.BundleId,
		FlagKey:  "checkout_v2",
		Context:  ctxMap,
	})
	if err != nil {
		t.Fatalf("ResolveFlag after override: %v", err)
	}
	if flagResp.Variant != "false" || !flagResp.IsDefault {
		t.Fatalf("expected default false after kill switch, got %+v", flagResp)
	}
}

func TestServerExpertSessions(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}

	envelope, err := structpb.NewStruct(map[string]any{
		"user": map[string]any{
			"id":         "user_1",
			"risk_score": 0.95,
		},
	})
	if err != nil {
		t.Fatalf("NewStruct: %v", err)
	}

	start, err := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
		BundleId: pub.BundleId,
		Envelope: envelope,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if start.ExpertRuleCount != 2 {
		t.Fatalf("unexpected start response: %+v", start)
	}

	run, err := client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{
		SessionId: start.SessionId,
		RequestId: "req_expert_1",
	})
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if run.StopReason != "quiescent" {
		t.Fatalf("unexpected stop reason: %+v", run)
	}
	if len(run.Facts) != 1 || run.Facts[0].GetType() != "RiskFlag" {
		t.Fatalf("unexpected facts: %+v", run.Facts)
	}
	if len(run.Outcomes) != 1 || run.Outcomes[0].GetName() != "ManualReview" {
		t.Fatalf("unexpected outcomes: %+v", run.Outcomes)
	}
	if len(run.Activations) != 2 {
		t.Fatalf("unexpected activations: %+v", run.Activations)
	}

	if _, err := client.AssertFacts(context.Background(), &arbiterv1.AssertFactsRequest{
		SessionId: start.SessionId,
		Facts: []*arbiterv1.ExpertFact{{
			Type: "ExternalMarker",
			Key:  "vip",
			Fields: mustStruct(t, map[string]any{
				"kind": "vip",
			}),
		}},
	}); err != nil {
		t.Fatalf("AssertFacts: %v", err)
	}

	trace, err := client.GetSessionTrace(context.Background(), &arbiterv1.GetSessionTraceRequest{
		SessionId: start.SessionId,
	})
	if err != nil {
		t.Fatalf("GetSessionTrace: %v", err)
	}
	if len(trace.Facts) != 2 {
		t.Fatalf("expected 2 facts after external assert, got %+v", trace.Facts)
	}

	if _, err := client.RetractFacts(context.Background(), &arbiterv1.RetractFactsRequest{
		SessionId: start.SessionId,
		Facts: []*arbiterv1.FactRef{{
			Type: "ExternalMarker",
			Key:  "vip",
		}},
	}); err != nil {
		t.Fatalf("RetractFacts: %v", err)
	}

	trace, err = client.GetSessionTrace(context.Background(), &arbiterv1.GetSessionTraceRequest{
		SessionId: start.SessionId,
	})
	if err != nil {
		t.Fatalf("GetSessionTrace after retract: %v", err)
	}
	if len(trace.Facts) != 1 || trace.Facts[0].GetType() != "RiskFlag" {
		t.Fatalf("unexpected facts after retract: %+v", trace.Facts)
	}

	run, err = client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{
		SessionId: start.SessionId,
		RequestId: "req_expert_2",
	})
	if err != nil {
		t.Fatalf("RunSession second pass: %v", err)
	}
	if len(run.Outcomes) != 1 || len(run.Facts) != 1 {
		t.Fatalf("unexpected second run result: %+v", run)
	}
}

func TestServerExpertMutationRules(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "expert-mutation",
		Source: []byte(expertMutationSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}
	if pub.ExpertRuleCount != 2 {
		t.Fatalf("unexpected publish counts: %+v", pub)
	}

	envelope := mustStruct(t, map[string]any{
		"user": map[string]any{
			"manual_clearance": true,
		},
	})
	start, err := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
		BundleId: pub.BundleId,
		Envelope: envelope,
		Facts: []*arbiterv1.ExpertFact{{
			Type: "RiskFlag",
			Key:  "high_risk",
			Fields: mustStruct(t, map[string]any{
				"key":   "high_risk",
				"level": "high",
			}),
		}},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	run, err := client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{
		SessionId: start.SessionId,
	})
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if len(run.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %+v", run.Facts)
	}
	fields := run.Facts[0].GetFields().AsMap()
	if fields["level"] != "low" || fields["reviewer"] != "alice" {
		t.Fatalf("expected modified fact fields, got %+v", fields)
	}
	if len(run.Outcomes) != 1 || run.Outcomes[0].GetName() != "Approved" {
		t.Fatalf("expected approved outcome, got %+v", run.Outcomes)
	}
	if len(run.Activations) != 2 {
		t.Fatalf("expected 2 activations, got %+v", run.Activations)
	}
}

func newTestClient(t *testing.T) (arbiterv1.ArbiterServiceClient, func()) {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	arbiterv1.RegisterArbiterServiceServer(grpcSrv, NewServer(NewRegistry(), overrides.NewStore(), audit.NopSink{}))
	go func() {
		_ = grpcSrv.Serve(listener)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		grpcSrv.Stop()
		_ = listener.Close()
	}
	return arbiterv1.NewArbiterServiceClient(conn), cleanup
}

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("NewStruct: %v", err)
	}
	return s
}
