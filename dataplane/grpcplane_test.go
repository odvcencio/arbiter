package dataplane

import (
	"context"
	"net"
	"testing"
	"time"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/grpcserver"
	"github.com/odvcencio/arbiter/overrides"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const grpcPlaneInitialSource = `
rule AllowCheckout {
	when {
		user.country == "US"
	}
	then Allow {}
}
`

const grpcPlaneUpdatedSource = `
rule AllowCheckout {
	when {
		user.country == "US"
	}
	then Allow {
		tier: "gold",
	}
}
`

func TestGRPCControlPlaneSyncsFromUpstream(t *testing.T) {
	client, cleanup := newUpstreamClient(t)
	defer cleanup()

	if _, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(grpcPlaneInitialSource),
	}); err != nil {
		t.Fatalf("PublishBundle initial: %v", err)
	}

	cp := NewGRPCControlPlane(client)
	syncer := New(cp, cp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- syncer.Run(ctx, BundleLocator{Name: "checkout"}, WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	first := waitForSnapshot(t, syncer.Updates(), agentTestTimeout)
	if first.Bundle.Name != "checkout" {
		t.Fatalf("unexpected bootstrap bundle: %+v", first.Bundle)
	}
	if _, err := client.SetRuleOverride(context.Background(), &arbiterv1.SetRuleOverrideRequest{
		BundleId:   first.Bundle.ID,
		RuleName:   "AllowCheckout",
		KillSwitch: wrapperspb.Bool(true),
	}); err != nil {
		t.Fatalf("SetRuleOverride: %v", err)
	}
	waitForRuleOverride(t, syncer.Overrides(), first.Bundle.ID, "AllowCheckout", agentTestTimeout)

	if _, err := client.PublishBundle(context.Background(), &arbiterv1.PublishBundleRequest{
		Name:   "checkout",
		Source: []byte(grpcPlaneUpdatedSource),
	}); err != nil {
		t.Fatalf("PublishBundle updated: %v", err)
	}

	updated := waitForSnapshot(t, syncer.Updates(), agentTestTimeout)
	if updated.Bundle.Checksum == first.Bundle.Checksum {
		t.Fatalf("expected checksum change after upstream publish")
	}

	active, ok := syncer.Registry().GetActive("checkout")
	if !ok {
		t.Fatal("expected local active bundle")
	}
	if active.Checksum != updated.Bundle.Checksum {
		t.Fatalf("unexpected local active checksum: got %s want %s", active.Checksum, updated.Bundle.Checksum)
	}
	if _, err := client.SetRuleOverride(context.Background(), &arbiterv1.SetRuleOverrideRequest{
		BundleId:   updated.Bundle.ID,
		RuleName:   "AllowCheckout",
		KillSwitch: wrapperspb.Bool(false),
	}); err != nil {
		t.Fatalf("SetRuleOverride updated bundle: %v", err)
	}
	if ov := waitForRuleOverride(t, syncer.Overrides(), updated.Bundle.ID, "AllowCheckout", agentTestTimeout); ov.KillSwitch == nil || *ov.KillSwitch {
		t.Fatalf("expected override rebind to pick up updated bundle mutation, got %+v", ov)
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func waitForRuleOverride(t *testing.T, store *overrides.Store, bundleID, ruleName string, timeout time.Duration) overrides.RuleOverride {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ov, ok := store.Rule(bundleID, ruleName); ok {
			return ov
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for override %s on %s", ruleName, bundleID)
	return overrides.RuleOverride{}
}

func newUpstreamClient(t *testing.T) (arbiterv1.ArbiterServiceClient, func()) {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	arbiterv1.RegisterArbiterServiceServer(grpcSrv, grpcserver.NewServer(grpcserver.NewRegistry(), overrides.NewStore(), audit.NopSink{}))
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
