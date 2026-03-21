package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/overrides"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
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

const testSourceV2 = `
segment enterprise {
	user.plan == "enterprise"
}

rule HighValue {
	when segment enterprise {
		user.cart_total > 100
	}
	then Approve {
		tier: "platinum",
	}
}

flag checkout_v2 type boolean default false {
	when enterprise then true
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
	if _, err := client.SetFlagOverride(context.Background(), &arbiterv1.SetFlagOverrideRequest{
		BundleId:   pub.BundleId,
		FlagKey:    "checkout_v2",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetFlagOverride: %v", err)
	}
	if _, err := client.SetFlagRuleOverride(context.Background(), &arbiterv1.SetFlagRuleOverrideRequest{
		BundleId:  pub.BundleId,
		FlagKey:   "checkout_v2",
		RuleIndex: 0,
		Rollout:   wrapperspb.UInt32(25),
	}); err != nil {
		t.Fatalf("SetFlagRuleOverride: %v", err)
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

func TestServerBundleHistoryAndRollbackByName(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub1, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle first: %v", err)
	}
	pub2, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSourceV2),
	})
	if err != nil {
		t.Fatalf("PublishBundle second: %v", err)
	}

	list, err := client.ListBundles(context.Background(), &arbiterv1.ListBundlesRequest{Name: "checkout"})
	if err != nil {
		t.Fatalf("ListBundles: %v", err)
	}
	if len(list.Bundles) != 2 {
		t.Fatalf("expected 2 bundles, got %+v", list.Bundles)
	}
	if !list.Bundles[0].Active || list.Bundles[0].BundleId != pub2.BundleId {
		t.Fatalf("expected second publish to be active, got %+v", list.Bundles)
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
		BundleName: "checkout",
		Context:    ctxMap,
	})
	if err != nil {
		t.Fatalf("EvaluateRules by name: %v", err)
	}
	if got := eval.Matched[0].Params.AsMap()["tier"]; got != "platinum" {
		t.Fatalf("expected active bundle tier platinum, got %+v", eval.Matched)
	}

	rollback, err := client.RollbackBundle(context.Background(), &arbiterv1.RollbackBundleRequest{Name: "checkout"})
	if err != nil {
		t.Fatalf("RollbackBundle: %v", err)
	}
	if rollback.Bundle.GetBundleId() != pub1.BundleId || rollback.PreviousBundleId != pub2.BundleId {
		t.Fatalf("unexpected rollback response: %+v", rollback)
	}

	eval, err = client.EvaluateRules(context.Background(), &arbiterv1.EvaluateRulesRequest{
		BundleName: "checkout",
		Context:    ctxMap,
	})
	if err != nil {
		t.Fatalf("EvaluateRules after rollback: %v", err)
	}
	if got := eval.Matched[0].Params.AsMap()["tier"]; got != "gold" {
		t.Fatalf("expected rolled back bundle tier gold, got %+v", eval.Matched)
	}

	activate, err := client.ActivateBundle(context.Background(), &arbiterv1.ActivateBundleRequest{
		Name:     "checkout",
		BundleId: pub2.BundleId,
	})
	if err != nil {
		t.Fatalf("ActivateBundle: %v", err)
	}
	if activate.Bundle.GetBundleId() != pub2.BundleId || !activate.Bundle.GetActive() {
		t.Fatalf("unexpected activate response: %+v", activate)
	}
}

func TestServerGetOverridesByIDAndName(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}

	if _, err := client.SetRuleOverride(context.Background(), &arbiterv1.SetRuleOverrideRequest{
		BundleId:   pub.BundleId,
		RuleName:   "HighValue",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetRuleOverride: %v", err)
	}
	if _, err := client.SetFlagOverride(context.Background(), &arbiterv1.SetFlagOverrideRequest{
		BundleId:   pub.BundleId,
		FlagKey:    "checkout_v2",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetFlagOverride: %v", err)
	}
	if _, err := client.SetFlagRuleOverride(context.Background(), &arbiterv1.SetFlagRuleOverrideRequest{
		BundleId:  pub.BundleId,
		FlagKey:   "checkout_v2",
		RuleIndex: 0,
		Rollout:   wrapperspb.UInt32(25),
	}); err != nil {
		t.Fatalf("SetFlagRuleOverride: %v", err)
	}

	byID, err := client.GetOverrides(context.Background(), &arbiterv1.GetOverridesRequest{BundleId: pub.BundleId})
	if err != nil {
		t.Fatalf("GetOverrides by id: %v", err)
	}
	assertOverrideSnapshot(t, byID.GetOverrides(), pub.BundleId)

	byName, err := client.GetOverrides(context.Background(), &arbiterv1.GetOverridesRequest{BundleName: "checkout"})
	if err != nil {
		t.Fatalf("GetOverrides by name: %v", err)
	}
	assertOverrideSnapshot(t, byName.GetOverrides(), pub.BundleId)
}

func TestServerWatchOverridesStreamsSnapshotAndMutations(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}

	if _, err := client.SetRuleOverride(context.Background(), &arbiterv1.SetRuleOverrideRequest{
		BundleId:   pub.BundleId,
		RuleName:   "HighValue",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetRuleOverride: %v", err)
	}
	if _, err := client.SetFlagOverride(context.Background(), &arbiterv1.SetFlagOverrideRequest{
		BundleId:   pub.BundleId,
		FlagKey:    "checkout_v2",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetFlagOverride seed: %v", err)
	}
	if _, err := client.SetFlagRuleOverride(context.Background(), &arbiterv1.SetFlagRuleOverrideRequest{
		BundleId:  pub.BundleId,
		FlagKey:   "checkout_v2",
		RuleIndex: 0,
		Rollout:   wrapperspb.UInt32(25),
	}); err != nil {
		t.Fatalf("SetFlagRuleOverride seed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.WatchOverrides(ctx, &arbiterv1.WatchOverridesRequest{BundleId: pub.BundleId})
	if err != nil {
		t.Fatalf("WatchOverrides: %v", err)
	}

	snapshot := mustRecvOverrideEvent(t, stream)
	if snapshot.GetType() != arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_SNAPSHOT {
		t.Fatalf("unexpected snapshot event: %+v", snapshot)
	}
	assertOverrideSnapshot(t, snapshot.GetSnapshot(), pub.BundleId)

	if _, err := client.SetFlagOverride(context.Background(), &arbiterv1.SetFlagOverrideRequest{
		BundleId:   pub.BundleId,
		FlagKey:    "checkout_v2",
		KillSwitch: wrapperspb.Bool(false),
	}); err != nil {
		t.Fatalf("SetFlagOverride: %v", err)
	}

	flagEvent := mustRecvOverrideEvent(t, stream)
	if flagEvent.GetType() != arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG || flagEvent.GetFlagKey() != "checkout_v2" {
		t.Fatalf("unexpected flag event: %+v", flagEvent)
	}
	if flagEvent.GetFlag().GetKillSwitch() != false {
		t.Fatalf("unexpected flag payload: %+v", flagEvent.GetFlag())
	}

	if _, err := client.SetFlagRuleOverride(context.Background(), &arbiterv1.SetFlagRuleOverrideRequest{
		BundleId:  pub.BundleId,
		FlagKey:   "checkout_v2",
		RuleIndex: 0,
		Rollout:   wrapperspb.UInt32(55),
	}); err != nil {
		t.Fatalf("SetFlagRuleOverride: %v", err)
	}

	flagRuleEvent := mustRecvOverrideEvent(t, stream)
	if flagRuleEvent.GetType() != arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG_RULE || flagRuleEvent.GetRuleIndex() != 0 {
		t.Fatalf("unexpected flag rule event: %+v", flagRuleEvent)
	}
	if flagRuleEvent.GetFlagRule().GetRollout() != 5500 {
		t.Fatalf("unexpected flag rule payload: %+v", flagRuleEvent.GetFlagRule())
	}
}

func TestServerGetBundleByIDAndName(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle: %v", err)
	}

	gotByID, err := client.GetBundle(context.Background(), &arbiterv1.GetBundleRequest{
		BundleId: pub.BundleId,
	})
	if err != nil {
		t.Fatalf("GetBundle by id: %v", err)
	}
	if gotByID.Bundle.GetBundleId() != pub.BundleId || string(gotByID.Source) != testSource {
		t.Fatalf("unexpected GetBundle by id response: %+v", gotByID)
	}

	gotByName, err := client.GetBundle(context.Background(), &arbiterv1.GetBundleRequest{
		BundleName: "checkout",
	})
	if err != nil {
		t.Fatalf("GetBundle by name: %v", err)
	}
	if gotByName.Bundle.GetBundleId() != pub.BundleId || !gotByName.Bundle.GetActive() {
		t.Fatalf("unexpected GetBundle by name response: %+v", gotByName)
	}
}

func TestServerWatchBundlesStreamsSnapshotsAndChanges(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub1, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle first: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.WatchBundles(ctx, &arbiterv1.WatchBundlesRequest{
		Names:      []string{"checkout"},
		ActiveOnly: true,
	})
	if err != nil {
		t.Fatalf("WatchBundles: %v", err)
	}

	snapshot := mustRecvBundleEvent(t, stream)
	if snapshot.GetType() != arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_SNAPSHOT || snapshot.GetBundle().GetBundleId() != pub1.BundleId {
		t.Fatalf("unexpected snapshot event: %+v", snapshot)
	}
	if string(snapshot.GetSource()) != testSource {
		t.Fatalf("unexpected snapshot source: %q", snapshot.GetSource())
	}

	activated, err := client.ActivateBundle(context.Background(), &arbiterv1.ActivateBundleRequest{
		Name:     "checkout",
		BundleId: pub1.BundleId,
	})
	if err != nil {
		t.Fatalf("ActivateBundle: %v", err)
	}
	if activated.Bundle.GetBundleId() != pub1.BundleId {
		t.Fatalf("unexpected activate response: %+v", activated)
	}

	activatedEvent := mustRecvBundleEvent(t, stream)
	if activatedEvent.GetType() != arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_ACTIVATED || activatedEvent.GetBundle().GetBundleId() != pub1.BundleId {
		t.Fatalf("unexpected activated event: %+v", activatedEvent)
	}

	pub2, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSourceV2),
	})
	if err != nil {
		t.Fatalf("PublishBundle second: %v", err)
	}

	published := mustRecvBundleEvent(t, stream)
	if published.GetType() != arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_PUBLISHED || published.GetBundle().GetBundleId() != pub2.BundleId {
		t.Fatalf("unexpected published event: %+v", published)
	}

	rollback, err := client.RollbackBundle(context.Background(), &arbiterv1.RollbackBundleRequest{
		Name: "checkout",
	})
	if err != nil {
		t.Fatalf("RollbackBundle: %v", err)
	}
	if rollback.Bundle.GetBundleId() != pub1.BundleId {
		t.Fatalf("unexpected rollback response: %+v", rollback)
	}

	rolledBack := mustRecvBundleEvent(t, stream)
	if rolledBack.GetType() != arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_ROLLED_BACK || rolledBack.GetBundle().GetBundleId() != pub1.BundleId || rolledBack.GetPreviousBundleId() != pub2.BundleId {
		t.Fatalf("unexpected rolled back event: %+v", rolledBack)
	}
}

func TestServerWatchBundlesCanSnapshotHistory(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	pub1, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSource),
	})
	if err != nil {
		t.Fatalf("PublishBundle first: %v", err)
	}
	pub2, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(testSourceV2),
	})
	if err != nil {
		t.Fatalf("PublishBundle second: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.WatchBundles(ctx, &arbiterv1.WatchBundlesRequest{
		Names: []string{"checkout"},
	})
	if err != nil {
		t.Fatalf("WatchBundles: %v", err)
	}

	first := mustRecvBundleEvent(t, stream)
	second := mustRecvBundleEvent(t, stream)
	if first.GetType() != arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_SNAPSHOT || second.GetType() != arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_SNAPSHOT {
		t.Fatalf("expected snapshot events, got %+v and %+v", first, second)
	}
	if first.GetBundle().GetBundleId() != pub2.BundleId || !first.GetBundle().GetActive() {
		t.Fatalf("expected newest active bundle first, got %+v", first)
	}
	if second.GetBundle().GetBundleId() != pub1.BundleId || second.GetBundle().GetActive() {
		t.Fatalf("expected previous inactive bundle second, got %+v", second)
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
	if len(run.Outcomes) != 0 || len(run.Activations) != 0 || len(run.Facts) != 1 {
		t.Fatalf("unexpected second run result: %+v", run)
	}

	if _, err := client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{
		SessionId: start.SessionId,
	}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	_, err = client.GetSessionTrace(context.Background(), &arbiterv1.GetSessionTraceRequest{
		SessionId: start.SessionId,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected closed session to be gone, got %v", err)
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
	if len(run.Activations) != 3 {
		t.Fatalf("expected 3 activations, got %+v", run.Activations)
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

func mustRecvBundleEvent(t *testing.T, stream arbiterv1.ArbiterService_WatchBundlesClient) *arbiterv1.BundleEvent {
	t.Helper()
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("WatchBundles Recv: %v", err)
	}
	return event
}

func assertOverrideSnapshot(t *testing.T, snapshot *arbiterv1.BundleOverrides, bundleID string) {
	t.Helper()
	if snapshot == nil {
		t.Fatal("expected override snapshot")
	}
	if snapshot.GetBundleId() != bundleID {
		t.Fatalf("unexpected bundle id: %+v", snapshot)
	}
	if len(snapshot.GetRules()) != 1 || len(snapshot.GetFlags()) != 1 || len(snapshot.GetFlagRules()) != 1 {
		t.Fatalf("unexpected override snapshot shape: %+v", snapshot)
	}
}

func mustRecvOverrideEvent(t *testing.T, stream arbiterv1.ArbiterService_WatchOverridesClient) *arbiterv1.OverrideEvent {
	t.Helper()
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("WatchOverrides Recv: %v", err)
	}
	return event
}

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("NewStruct: %v", err)
	}
	return s
}
