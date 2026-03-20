package dataplane

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/arbiter/overrides"
)

// Full-repo race runs can push dataplane bootstrap close to 10s under CPU contention.
const agentTestTimeout = 20 * time.Second

const agentTestInitialSource = `
rule AllowCheckout {
	when {
		user.country == "US"
	}
	then Allow {}
}
`

const agentTestUpdatedSource = `
rule AllowCheckout {
	when {
		user.country == "US"
	}
	then Allow {
		tier: "gold",
	}
}
`

const agentTestPricingSource = `
rule QuotePrice {
	when {
		cart.total > 0
	}
	then Quote {}
}
`

const agentTestPricingUpdatedSource = `
rule QuotePrice {
	when {
		cart.total > 0
	}
	then Quote {
		tier: "vip",
	}
}
`

const agentTestInvalidSource = `
rule Broken {
	when {
		user.country ==
	}
	then Allow {}
}
`

func TestAgentBootstrapsAndHotSwaps(t *testing.T) {
	cp := newFakeControlPlane(t, Bundle{
		Name:   "checkout",
		Source: []byte(agentTestInitialSource),
	})
	agent := New(cp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- agent.Run(ctx, BundleLocator{Name: "checkout"}, WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	first := waitForSnapshot(t, agent.Updates(), agentTestTimeout)
	if first.Bundle.Name != "checkout" {
		t.Fatalf("unexpected bootstrap bundle: %+v", first.Bundle)
	}
	if first.Compiled == nil || first.Compiled.Ruleset == nil || len(first.Compiled.Ruleset.Rules) != 1 {
		t.Fatalf("expected compiled bootstrap snapshot, got %+v", first.Compiled)
	}

	cp.send(BundleEvent{
		Type: BundleEventActivated,
		Bundle: Bundle{
			Name:   "checkout",
			ID:     bundleIdentity("checkout", []byte(agentTestUpdatedSource)),
			Source: []byte(agentTestUpdatedSource),
			Active: true,
		},
	})

	updated := waitForSnapshot(t, agent.Updates(), agentTestTimeout)
	if updated.Compiled == nil || updated.Compiled.Ruleset == nil {
		t.Fatalf("expected compiled update snapshot, got %+v", updated.Compiled)
	}
	if updated.Bundle.Checksum == first.Bundle.Checksum {
		t.Fatalf("expected checksum change after update")
	}

	current, ok := agent.Current()
	if !ok {
		t.Fatal("expected current snapshot")
	}
	if current.Bundle.Checksum != updated.Bundle.Checksum {
		t.Fatalf("current snapshot did not hot-swap: got %s want %s", current.Bundle.Checksum, updated.Bundle.Checksum)
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAgentSkipsBadUpdateAndKeepsLastGoodSnapshot(t *testing.T) {
	cp := newFakeControlPlane(t, Bundle{
		Name:   "checkout",
		Source: []byte(agentTestInitialSource),
	})
	agent := New(cp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- agent.Run(ctx, BundleLocator{Name: "checkout"}, WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	first := waitForSnapshot(t, agent.Updates(), agentTestTimeout)
	cp.send(BundleEvent{
		Type: BundleEventPublished,
		Bundle: Bundle{
			Name:   "checkout",
			ID:     bundleIdentity("checkout", []byte(agentTestInvalidSource)),
			Source: []byte(agentTestInvalidSource),
			Active: true,
		},
	})

	select {
	case snapshot := <-agent.Updates():
		t.Fatalf("expected invalid update to be ignored, got %+v", snapshot)
	case <-time.After(250 * time.Millisecond):
	}

	current, ok := agent.Current()
	if !ok {
		t.Fatal("expected current snapshot")
	}
	if current.Bundle.Checksum != first.Bundle.Checksum {
		t.Fatalf("bad update replaced last good snapshot")
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAgentSkipsDuplicateBootstrapSnapshot(t *testing.T) {
	cp := newFakeControlPlane(t, Bundle{
		Name:   "checkout",
		Source: []byte(agentTestInitialSource),
	})
	agent := New(cp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- agent.Run(ctx, BundleLocator{Name: "checkout"}, WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	first := waitForSnapshot(t, agent.Updates(), agentTestTimeout)
	cp.send(BundleEvent{
		Type: BundleEventSnapshot,
		Bundle: Bundle{
			Name:   "checkout",
			ID:     first.Bundle.ID,
			Source: []byte(agentTestInitialSource),
			Active: true,
		},
	})

	select {
	case snapshot := <-agent.Updates():
		t.Fatalf("expected duplicate bootstrap snapshot to be ignored, got %+v", snapshot)
	case <-time.After(250 * time.Millisecond):
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAgentRunManyKeepsMultipleBundlesHot(t *testing.T) {
	cp := newFakeMultiControlPlane(t, map[string]Bundle{
		"checkout": {Name: "checkout", Source: []byte(agentTestInitialSource)},
		"pricing":  {Name: "pricing", Source: []byte(agentTestPricingSource)},
	})
	op := newFakeOverrideControlPlane(t, map[string]overrides.Snapshot{
		bundleIdentity("checkout", []byte(agentTestInitialSource)): {
			Rules: map[string]map[string]overrides.RuleOverride{
				bundleIdentity("checkout", []byte(agentTestInitialSource)): {
					"AllowCheckout": {},
				},
			},
		},
		bundleIdentity("pricing", []byte(agentTestPricingSource)): {
			Rules: map[string]map[string]overrides.RuleOverride{
				bundleIdentity("pricing", []byte(agentTestPricingSource)): {
					"QuotePrice": {},
				},
			},
		},
		bundleIdentity("checkout", []byte(agentTestUpdatedSource)): {
			Flags: map[string]map[string]overrides.FlagOverride{
				bundleIdentity("checkout", []byte(agentTestUpdatedSource)): {
					"checkout_v2": {},
				},
			},
		},
	})
	agent := New(cp, op)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- agent.RunMany(ctx, []string{"checkout", "pricing"})
	}()

	checkout := waitForCurrentBundle(t, agent, "checkout", agentTestTimeout)
	pricing := waitForCurrentBundle(t, agent, "pricing", agentTestTimeout)
	if checkout.Bundle.Name != "checkout" || pricing.Bundle.Name != "pricing" {
		t.Fatalf("unexpected current bundles: checkout=%+v pricing=%+v", checkout.Bundle, pricing.Bundle)
	}
	if _, ok := agent.Registry().GetActive("checkout"); !ok {
		t.Fatalf("expected checkout active bundle")
	}
	if _, ok := agent.Registry().GetActive("pricing"); !ok {
		t.Fatalf("expected pricing active bundle")
	}
	if len(agent.Overrides().SnapshotForBundle(checkout.Bundle.ID).Rules) != 1 {
		t.Fatalf("expected checkout overrides to load")
	}
	if len(agent.Overrides().SnapshotForBundle(pricing.Bundle.ID).Rules) != 1 {
		t.Fatalf("expected pricing overrides to load")
	}

	cp.send("checkout", BundleEvent{
		Type: BundleEventActivated,
		Bundle: Bundle{
			Name:   "checkout",
			ID:     bundleIdentity("checkout", []byte(agentTestUpdatedSource)),
			Source: []byte(agentTestUpdatedSource),
			Active: true,
		},
	})

	updatedCheckout := waitForBundleChecksumChange(t, agent, "checkout", checkout.Bundle.Checksum, agentTestTimeout)
	if len(agent.Overrides().SnapshotForBundle(updatedCheckout.Bundle.ID).Flags) != 1 {
		t.Fatalf("expected checkout overrides to rebind to updated bundle")
	}
	if len(agent.Overrides().SnapshotForBundle(pricing.Bundle.ID).Rules) != 1 {
		t.Fatalf("expected pricing overrides to remain intact")
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("RunMany: %v", err)
	}
}

func waitForSnapshot(t *testing.T, updates <-chan Snapshot, timeout time.Duration) Snapshot {
	t.Helper()
	select {
	case snapshot, ok := <-updates:
		if !ok {
			t.Fatal("updates channel closed")
		}
		return snapshot
	case <-time.After(timeout):
		t.Fatal("timed out waiting for snapshot")
		return Snapshot{}
	}
}

func waitForCurrentBundle(t *testing.T, agent *Agent, name string, timeout time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if snapshot, ok := agent.CurrentBundle(name); ok {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for current bundle %s", name)
	return Snapshot{}
}

func waitForBundleChecksumChange(t *testing.T, agent *Agent, name, checksum string, timeout time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if snapshot, ok := agent.CurrentBundle(name); ok && snapshot.Bundle.Checksum != checksum {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for checksum change on %s", name)
	return Snapshot{}
}

type fakeControlPlane struct {
	mu     sync.Mutex
	bundle Bundle
	stream *fakeStream
}

func newFakeControlPlane(t *testing.T, bundle Bundle) *fakeControlPlane {
	t.Helper()
	return &fakeControlPlane{
		bundle: bundle,
		stream: newFakeStream(),
	}
}

func (f *fakeControlPlane) GetBundle(_ context.Context, _ BundleLocator) (*Bundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bundle := cloneBundle(f.bundle)
	return &bundle, nil
}

func (f *fakeControlPlane) WatchBundles(ctx context.Context, _ WatchRequest) (BundleStream, error) {
	go func() {
		<-ctx.Done()
		_ = f.stream.Close()
	}()
	return f.stream, nil
}

func (f *fakeControlPlane) send(event BundleEvent) {
	f.mu.Lock()
	f.bundle = event.Bundle
	f.mu.Unlock()
	f.stream.send(event)
}

type fakeStream struct {
	events chan *BundleEvent
	done   chan struct{}
	once   sync.Once
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		events: make(chan *BundleEvent, 4),
		done:   make(chan struct{}),
	}
}

func (s *fakeStream) Recv() (*BundleEvent, error) {
	select {
	case ev := <-s.events:
		if ev == nil {
			return nil, io.EOF
		}
		return ev, nil
	case <-s.done:
		return nil, io.EOF
	}
}

func (s *fakeStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *fakeStream) send(event BundleEvent) {
	select {
	case s.events <- &event:
	case <-s.done:
	}
}

var _ ControlPlane = (*fakeControlPlane)(nil)
var _ BundleStream = (*fakeStream)(nil)

type fakeMultiControlPlane struct {
	mu      sync.Mutex
	bundles map[string]Bundle
	streams map[string]*fakeStream
}

func newFakeMultiControlPlane(t *testing.T, bundles map[string]Bundle) *fakeMultiControlPlane {
	t.Helper()
	streams := make(map[string]*fakeStream, len(bundles))
	for name := range bundles {
		streams[name] = newFakeStream()
	}
	return &fakeMultiControlPlane{
		bundles: bundles,
		streams: streams,
	}
}

func (f *fakeMultiControlPlane) GetBundle(_ context.Context, locator BundleLocator) (*Bundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bundle := cloneBundle(f.bundles[locator.Name])
	return &bundle, nil
}

func (f *fakeMultiControlPlane) WatchBundles(ctx context.Context, req WatchRequest) (BundleStream, error) {
	f.mu.Lock()
	stream := f.streams[req.Name]
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		if stream != nil {
			_ = stream.Close()
		}
	}()
	return stream, nil
}

func (f *fakeMultiControlPlane) send(name string, event BundleEvent) {
	f.mu.Lock()
	f.bundles[name] = event.Bundle
	stream := f.streams[name]
	f.mu.Unlock()
	if stream != nil {
		stream.send(event)
	}
}

var _ ControlPlane = (*fakeMultiControlPlane)(nil)
