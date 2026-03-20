package dataplane

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/arbiter/overrides"
)

func TestAgentBootstrapsAndRebindsOverrides(t *testing.T) {
	bundleA := Bundle{
		Name:   "checkout",
		Source: []byte(agentTestInitialSource),
	}
	ovA := overrides.Snapshot{
		Rules: map[string]map[string]overrides.RuleOverride{
			bundleIdentity("checkout", []byte(agentTestInitialSource)): {
				"AllowCheckout": {},
			},
		},
	}
	ovB := overrides.Snapshot{
		Flags: map[string]map[string]overrides.FlagOverride{
			bundleIdentity("checkout", []byte(agentTestUpdatedSource)): {
				"checkout_v2": {},
			},
		},
	}

	cp := newFakeControlPlane(t, bundleA)
	op := newFakeOverrideControlPlane(t, map[string]overrides.Snapshot{
		bundleIdentity("checkout", []byte(agentTestInitialSource)): ovA,
		bundleIdentity("checkout", []byte(agentTestUpdatedSource)): ovB,
	})
	agent := New(cp, op)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- agent.Run(ctx, BundleLocator{Name: "checkout"}, WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	_ = waitForSnapshot(t, agent.Updates(), agentTestTimeout)

	snapA := agent.Overrides().SnapshotForBundle(bundleIdentity("checkout", []byte(agentTestInitialSource)))
	if len(snapA.Rules) != 1 {
		t.Fatalf("expected initial override snapshot, got %+v", snapA)
	}
	waitForOverrideWatch(t, op, bundleIdentity("checkout", []byte(agentTestInitialSource)), agentTestTimeout)

	cp.send(BundleEvent{
		Type: BundleEventActivated,
		Bundle: Bundle{
			Name:   "checkout",
			ID:     bundleIdentity("checkout", []byte(agentTestUpdatedSource)),
			Source: []byte(agentTestUpdatedSource),
			Active: true,
		},
	})

	_ = waitForSnapshot(t, agent.Updates(), agentTestTimeout)

	snapB := agent.Overrides().SnapshotForBundle(bundleIdentity("checkout", []byte(agentTestUpdatedSource)))
	if len(snapB.Flags) != 1 {
		t.Fatalf("expected reloaded override snapshot, got %+v", snapB)
	}
	snapA = agent.Overrides().SnapshotForBundle(bundleIdentity("checkout", []byte(agentTestInitialSource)))
	if len(snapA.Rules) != 0 || len(snapA.Flags) != 0 || len(snapA.FlagRules) != 0 {
		t.Fatalf("expected old bundle overrides to be cleared, got %+v", snapA)
	}
	waitForOverrideWatch(t, op, bundleIdentity("checkout", []byte(agentTestUpdatedSource)), agentTestTimeout)

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAgentKeepsLastGoodBundleWhenOverrideBootstrapFails(t *testing.T) {
	bundleA := Bundle{
		Name:   "checkout",
		Source: []byte(agentTestInitialSource),
	}
	updatedID := bundleIdentity("checkout", []byte(agentTestUpdatedSource))

	cp := newFakeControlPlane(t, bundleA)
	op := newFakeOverrideControlPlaneWithErrors(t,
		map[string]overrides.Snapshot{
			bundleIdentity("checkout", []byte(agentTestInitialSource)): {
				Rules: map[string]map[string]overrides.RuleOverride{
					bundleIdentity("checkout", []byte(agentTestInitialSource)): {
						"AllowCheckout": {},
					},
				},
			},
		},
		map[string]error{
			updatedID: io.EOF,
		},
	)
	agent := New(cp, op)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- agent.Run(ctx, BundleLocator{Name: "checkout"}, WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	first := waitForSnapshot(t, agent.Updates(), agentTestTimeout)
	cp.send(BundleEvent{
		Type: BundleEventActivated,
		Bundle: Bundle{
			Name:   "checkout",
			ID:     updatedID,
			Source: []byte(agentTestUpdatedSource),
			Active: true,
		},
	})

	select {
	case snapshot := <-agent.Updates():
		t.Fatalf("expected failed override bootstrap to suppress update, got %+v", snapshot)
	case <-time.After(250 * time.Millisecond):
	}

	current, ok := agent.Current()
	if !ok {
		t.Fatal("expected current snapshot")
	}
	if current.Bundle.Checksum != first.Bundle.Checksum {
		t.Fatalf("expected last good snapshot to remain active, got %s want %s", current.Bundle.Checksum, first.Bundle.Checksum)
	}
	active, ok := agent.Registry().GetActive("checkout")
	if !ok || active.Checksum != first.Bundle.Checksum {
		t.Fatalf("expected registry active bundle to remain old snapshot, got %+v ok=%v", active, ok)
	}
	if _, ok := agent.Registry().Get(updatedID); ok {
		t.Fatalf("expected failed bundle update to stay out of local registry")
	}
	snapA := agent.Overrides().SnapshotForBundle(first.Bundle.ID)
	if len(snapA.Rules) != 1 {
		t.Fatalf("expected original overrides to remain intact, got %+v", snapA)
	}
	snapB := agent.Overrides().SnapshotForBundle(updatedID)
	if len(snapB.Rules) != 0 || len(snapB.Flags) != 0 || len(snapB.FlagRules) != 0 {
		t.Fatalf("expected failed bundle overrides to be cleared, got %+v", snapB)
	}

	status := waitForAgentStatus(t, agent, func(status AgentStatus) bool {
		return len(status.Bundles) == 1 && status.Bundles[0].OverrideErrorsTotal == 1
	}, agentTestTimeout)
	if status.Bundles[0].BundleErrorsTotal != 0 {
		t.Fatalf("expected override bootstrap failure to avoid bundle error double-counting, got %+v", status.Bundles[0])
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

type fakeOverrideControlPlane struct {
	mu      sync.Mutex
	store   map[string]overrides.Snapshot
	streams map[string]*fakeOverrideStream
	getErr  map[string]error
}

func newFakeOverrideControlPlane(t *testing.T, store map[string]overrides.Snapshot) *fakeOverrideControlPlane {
	t.Helper()
	return newFakeOverrideControlPlaneWithErrors(t, store, nil)
}

func newFakeOverrideControlPlaneWithErrors(t *testing.T, store map[string]overrides.Snapshot, getErr map[string]error) *fakeOverrideControlPlane {
	t.Helper()
	streams := make(map[string]*fakeOverrideStream, len(store))
	return &fakeOverrideControlPlane{
		store:   store,
		streams: streams,
		getErr:  getErr,
	}
}

func (f *fakeOverrideControlPlane) GetOverrides(_ context.Context, locator OverrideLocator) (*overrides.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.getErr[locator.BundleID]; ok {
		return nil, err
	}
	snapshot := f.store[locator.BundleID]
	return &snapshot, nil
}

func (f *fakeOverrideControlPlane) WatchOverrides(ctx context.Context, locator OverrideLocator) (OverrideStream, error) {
	f.mu.Lock()
	stream := newFakeOverrideStream()
	f.streams[locator.BundleID] = stream
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		_ = stream.Close()
	}()
	return stream, nil
}

func (f *fakeOverrideControlPlane) watchCount(bundleID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stream, ok := f.streams[bundleID]; ok {
		if stream != nil {
			return 1
		}
	}
	return 0
}

type fakeOverrideStream struct {
	events chan *OverrideEvent
	done   chan struct{}
	once   sync.Once
}

func newFakeOverrideStream() *fakeOverrideStream {
	return &fakeOverrideStream{
		events: make(chan *OverrideEvent, 4),
		done:   make(chan struct{}),
	}
}

func (s *fakeOverrideStream) Recv() (*OverrideEvent, error) {
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

func (s *fakeOverrideStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

var _ OverrideControlPlane = (*fakeOverrideControlPlane)(nil)
var _ OverrideStream = (*fakeOverrideStream)(nil)

func waitForOverrideWatch(t *testing.T, op *fakeOverrideControlPlane, bundleID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if op.watchCount(bundleID) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for override watch on %s", bundleID)
}
