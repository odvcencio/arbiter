package grpcserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	arbiterv1 "m31labs.dev/arbiter/api/arbiter/v1"
)

func TestStatelessDecisionsUseServerClock(t *testing.T) {
	ctx := context.Background()
	sink := &captureSink{}
	server := NewServer(nil, nil, sink)
	future := time.Now().UTC().Add(time.Hour)
	start := future.Format(time.RFC3339)
	published, err := server.PublishBundle(ctx, &arbiterv1.PublishBundleRequest{
		Name: "time-window",
		Source: []byte(fmt.Sprintf(`
rule Future {
    active_from %[1]s
    when { true } then Allow {}
}
flag future type boolean default false {
    active_from %[1]s
    when { true } then true
}
outcome Route { target: string }
strategy Checkout returns Route {
    active_from %[1]s
    when { true } then Future { target: "future" }
    else Standard { target: "standard" }
}
`, start)),
	})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := structpb.NewStruct(map[string]any{"__now": future.Add(time.Hour).Format(time.RFC3339), "user.id": "buyer"})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	checkAudit := func(kind string) {
		t.Helper()
		event := sink.Last()
		if event.Kind != kind || event.Timestamp.Before(before) || event.Timestamp.After(time.Now().UTC()) ||
			event.Context["__now"] != event.Timestamp.Format(time.RFC3339Nano) {
			t.Fatalf("%s audit did not record the authoritative decision time: %+v", kind, event)
		}
		user, ok := event.Context["user"].(map[string]any)
		if !ok || user["id"] != "buyer" {
			t.Fatalf("%s audit lacks the effective facts: %+v", kind, event.Context)
		}
	}

	rules, err := server.EvaluateRules(ctx, &arbiterv1.EvaluateRulesRequest{BundleId: published.BundleId, Context: facts})
	if err != nil || len(rules.GetMatched()) != 0 {
		t.Fatalf("spoofed clock opened the rule window: %+v, %v", rules, err)
	}
	checkAudit("rules")
	flag, err := server.ResolveFlag(ctx, &arbiterv1.ResolveFlagRequest{BundleId: published.BundleId, FlagKey: "future", Context: facts})
	if err != nil || flag.GetVariant() != "false" {
		t.Fatalf("spoofed clock opened the flag window: %+v, %v", flag, err)
	}
	checkAudit("flag")
	route, err := server.EvaluateStrategy(ctx, &arbiterv1.EvaluateStrategyRequest{BundleId: published.BundleId, StrategyName: "Checkout", Context: facts})
	if err != nil || route.GetSelected() != "Standard" {
		t.Fatalf("spoofed clock opened the strategy window: %+v, %v", route, err)
	}
	checkAudit("strategy")
}
