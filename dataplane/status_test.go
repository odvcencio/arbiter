package dataplane

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/arbiter/overrides"
)

func TestAgentStatusReportsReadyBundleAndFreshness(t *testing.T) {
	cp := newFakeControlPlane(t, Bundle{
		Name:   "checkout",
		Source: []byte(agentTestInitialSource),
	})
	syncer := New(cp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- syncer.Run(ctx, BundleLocator{Name: "checkout"}, WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	select {
	case <-syncer.Ready():
	case <-time.After(agentTestTimeout):
		t.Fatal("timed out waiting for readiness")
	}

	status := syncer.Status()
	if !status.Ready {
		t.Fatal("expected ready status")
	}
	if status.PrimaryName != "checkout" {
		t.Fatalf("expected primary name checkout, got %q", status.PrimaryName)
	}
	if len(status.Bundles) == 0 {
		t.Fatal("expected bundle status")
	}
	if status.Bundles[0].BundleID == "" {
		t.Fatal("expected bundle id")
	}
	if status.Bundles[0].Checksum == "" {
		t.Fatal("expected bundle checksum")
	}
	if status.Bundles[0].StalenessMs < 0 {
		t.Fatalf("unexpected bundle staleness: %d", status.Bundles[0].StalenessMs)
	}
	if status.BundleErrorsTotal != 0 || status.OverrideErrorsTotal != 0 {
		t.Fatalf("unexpected healthy error counters: %+v", status)
	}
	if status.BundleReconnectsTotal != 0 || status.OverrideReconnectsTotal != 0 {
		t.Fatalf("unexpected healthy reconnect counters: %+v", status)
	}
	if status.Bundles[0].BundleErrorsTotal != 0 || status.Bundles[0].OverrideErrorsTotal != 0 {
		t.Fatalf("unexpected per-bundle healthy error counters: %+v", status.Bundles[0])
	}
	if status.Bundles[0].BundleReconnects != 0 || status.Bundles[0].OverrideReconnects != 0 {
		t.Fatalf("unexpected per-bundle healthy reconnect counters: %+v", status.Bundles[0])
	}
	if status.LastUpstreamError != "" || !status.LastUpstreamErrorAt.IsZero() {
		t.Fatalf("unexpected healthy last upstream error: %+v", status)
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAgentStatusTracksBootstrapFailuresAndReconnects(t *testing.T) {
	bundle := Bundle{
		Name:   "checkout",
		Source: []byte(agentTestInitialSource),
	}
	cp := newFlakyStatusControlPlane(bundle, 1)
	op := newFlakyStatusOverrideControlPlane(overrides.Snapshot{
		Rules: map[string]map[string]overrides.RuleOverride{
			bundleIdentity(bundle.Name, bundle.Source): {
				"AllowCheckout": {},
			},
		},
	})
	syncer := New(cp, op)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- syncer.Run(ctx, BundleLocator{Name: bundle.Name}, WatchRequest{Name: bundle.Name, ActiveOnly: true})
	}()

	status := waitForAgentStatus(t, syncer, func(status AgentStatus) bool {
		if !status.Ready || len(status.Bundles) != 1 {
			return false
		}
		bundleStatus := status.Bundles[0]
		return status.BundleErrorsTotal == 2 &&
			status.OverrideErrorsTotal == 1 &&
			status.BundleReconnectsTotal == 1 &&
			status.OverrideReconnectsTotal == 1 &&
			status.LastUpstreamError != "" &&
			!status.LastUpstreamErrorAt.IsZero() &&
			bundleStatus.BundleErrorsTotal == 2 &&
			bundleStatus.OverrideErrorsTotal == 1 &&
			bundleStatus.BundleReconnects == 1 &&
			bundleStatus.OverrideReconnects == 1
	}, agentTestTimeout)

	if got := status.Bundles[0].Name; got != "checkout" {
		t.Fatalf("unexpected bundle status name: %q", got)
	}
	if status.Bundles[0].LastBundleError == "" {
		t.Fatalf("expected last bundle error, got %+v", status.Bundles[0])
	}
	if status.Bundles[0].LastOverrideError == "" {
		t.Fatalf("expected last override error, got %+v", status.Bundles[0])
	}
	if status.Bundles[0].LastBundleErrorAt.IsZero() || status.Bundles[0].LastOverrideErrorAt.IsZero() {
		t.Fatalf("expected bundle and override failure timestamps, got %+v", status.Bundles[0])
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func waitForAgentStatus(t *testing.T, syncer *Agent, predicate func(AgentStatus) bool, timeout time.Duration) AgentStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := syncer.Status()
		if predicate(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for agent status")
	return AgentStatus{}
}

type flakyStatusControlPlane struct {
	mu              sync.Mutex
	bundle          Bundle
	getFailuresLeft int
	watchOpenCount  int
	blockingWatch   *statusBlockingBundleStream
}

func newFlakyStatusControlPlane(bundle Bundle, getFailures int) *flakyStatusControlPlane {
	return &flakyStatusControlPlane{
		bundle:          bundle,
		getFailuresLeft: getFailures,
	}
}

func (c *flakyStatusControlPlane) GetBundle(_ context.Context, _ BundleLocator) (*Bundle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getFailuresLeft > 0 {
		c.getFailuresLeft--
		return nil, errors.New("upstream bundle unavailable")
	}
	bundle := cloneBundle(c.bundle)
	return &bundle, nil
}

func (c *flakyStatusControlPlane) WatchBundles(ctx context.Context, _ WatchRequest) (BundleStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watchOpenCount++
	if c.watchOpenCount == 1 {
		return errorBundleStream{err: io.EOF}, nil
	}
	if c.blockingWatch == nil {
		c.blockingWatch = newStatusBlockingBundleStream()
		go func(stream *statusBlockingBundleStream) {
			<-ctx.Done()
			_ = stream.Close()
		}(c.blockingWatch)
	}
	return c.blockingWatch, nil
}

type flakyStatusOverrideControlPlane struct {
	mu             sync.Mutex
	snapshot       overrides.Snapshot
	watchOpenCount int
	blockingWatch  *statusBlockingOverrideStream
}

func newFlakyStatusOverrideControlPlane(snapshot overrides.Snapshot) *flakyStatusOverrideControlPlane {
	return &flakyStatusOverrideControlPlane{snapshot: snapshot}
}

func (c *flakyStatusOverrideControlPlane) GetOverrides(_ context.Context, _ OverrideLocator) (*overrides.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := c.snapshot
	return &snapshot, nil
}

func (c *flakyStatusOverrideControlPlane) WatchOverrides(ctx context.Context, _ OverrideLocator) (OverrideStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watchOpenCount++
	if c.watchOpenCount == 1 {
		return errorOverrideStream{err: io.EOF}, nil
	}
	if c.blockingWatch == nil {
		c.blockingWatch = newStatusBlockingOverrideStream()
		go func(stream *statusBlockingOverrideStream) {
			<-ctx.Done()
			_ = stream.Close()
		}(c.blockingWatch)
	}
	return c.blockingWatch, nil
}

type errorBundleStream struct {
	err error
}

func (s errorBundleStream) Recv() (*BundleEvent, error) {
	return nil, s.err
}

func (s errorBundleStream) Close() error {
	return nil
}

type errorOverrideStream struct {
	err error
}

func (s errorOverrideStream) Recv() (*OverrideEvent, error) {
	return nil, s.err
}

func (s errorOverrideStream) Close() error {
	return nil
}

type statusBlockingBundleStream struct {
	done chan struct{}
	once sync.Once
}

func newStatusBlockingBundleStream() *statusBlockingBundleStream {
	return &statusBlockingBundleStream{done: make(chan struct{})}
}

func (s *statusBlockingBundleStream) Recv() (*BundleEvent, error) {
	<-s.done
	return nil, io.EOF
}

func (s *statusBlockingBundleStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type statusBlockingOverrideStream struct {
	done chan struct{}
	once sync.Once
}

func newStatusBlockingOverrideStream() *statusBlockingOverrideStream {
	return &statusBlockingOverrideStream{done: make(chan struct{})}
}

func (s *statusBlockingOverrideStream) Recv() (*OverrideEvent, error) {
	<-s.done
	return nil, io.EOF
}

func (s *statusBlockingOverrideStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

var _ ControlPlane = (*flakyStatusControlPlane)(nil)
var _ OverrideControlPlane = (*flakyStatusOverrideControlPlane)(nil)
var _ BundleStream = errorBundleStream{}
var _ OverrideStream = errorOverrideStream{}
var _ BundleStream = (*statusBlockingBundleStream)(nil)
var _ OverrideStream = (*statusBlockingOverrideStream)(nil)
