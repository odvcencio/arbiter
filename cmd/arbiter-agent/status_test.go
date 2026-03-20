package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/arbiter/dataplane"
)

const statusTestInitialSource = `
rule AllowCheckout {
	when {
		user.country == "US"
	}
	then Allow {}
}
`

// Full-repo race runs can push dataplane readiness close to 10s under CPU contention.
const statusTestTimeout = 20 * time.Second

func TestStatusHandlerExposesHealthReadinessAndStatus(t *testing.T) {
	cp := newStatusTestControlPlane(dataplane.Bundle{
		Name:   "checkout",
		Source: []byte(statusTestInitialSource),
	})
	syncer := dataplane.New(cp)
	handler := newStatusHandler(syncer, readinessPolicy{})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before sync = %d", rr.Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- syncer.Run(ctx, dataplane.BundleLocator{Name: "checkout"}, dataplane.WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	select {
	case <-syncer.Ready():
	case <-time.After(statusTestTimeout):
		t.Fatal("timed out waiting for readiness")
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/readyz after sync = %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/status code = %d", rr.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if ready, _ := payload["ready"].(bool); !ready {
		t.Fatalf("expected ready status, got %v", payload["ready"])
	}
	bundles, _ := payload["bundles"].([]any)
	if len(bundles) == 0 {
		t.Fatal("expected bundle payload")
	}
	bundle, _ := bundles[0].(map[string]any)
	if bundle == nil {
		t.Fatal("expected bundle payload")
	}
	if _, ok := bundle["bundle_id"]; !ok {
		t.Fatal("expected bundle id")
	}
	if _, ok := bundle["checksum"]; !ok {
		t.Fatal("expected bundle checksum")
	}
	for _, key := range []string{
		"bundle_errors_total",
		"override_errors_total",
		"bundle_reconnects_total",
		"override_reconnects_total",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected top-level status field %q", key)
		}
	}
	for _, key := range []string{
		"bundle_errors_total",
		"override_errors_total",
		"bundle_reconnects",
		"override_reconnects",
	} {
		if _, ok := bundle[key]; !ok {
			t.Fatalf("expected bundle status field %q", key)
		}
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestStatusHandlerReadinessThresholdMarksStaleSyncUnready(t *testing.T) {
	cp := newStatusTestControlPlane(dataplane.Bundle{
		Name:   "checkout",
		Source: []byte(statusTestInitialSource),
	})
	syncer := dataplane.New(cp)
	handler := newStatusHandler(syncer, readinessPolicy{maxStaleness: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- syncer.Run(ctx, dataplane.BundleLocator{Name: "checkout"}, dataplane.WatchRequest{Name: "checkout", ActiveOnly: true})
	}()

	select {
	case <-syncer.Ready():
	case <-time.After(statusTestTimeout):
		t.Fatal("timed out waiting for readiness")
	}

	time.Sleep(10 * time.Millisecond)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz stale = %d", rr.Code)
	}
	if body := rr.Body.String(); body == "" || !strings.Contains(body, "stale") {
		t.Fatalf("expected stale readiness reason, got %q", body)
	}

	cancel()
	if err := <-runErr; err != context.Canceled && err != nil {
		t.Fatalf("Run: %v", err)
	}
}

type statusTestControlPlane struct {
	mu     sync.Mutex
	bundle dataplane.Bundle
	stream *statusTestStream
}

func newStatusTestControlPlane(bundle dataplane.Bundle) *statusTestControlPlane {
	return &statusTestControlPlane{
		bundle: bundle,
		stream: newStatusTestStream(),
	}
}

func (c *statusTestControlPlane) GetBundle(_ context.Context, _ dataplane.BundleLocator) (*dataplane.Bundle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bundle := c.bundle
	bundle.Source = append([]byte(nil), bundle.Source...)
	return &bundle, nil
}

func (c *statusTestControlPlane) WatchBundles(ctx context.Context, _ dataplane.WatchRequest) (dataplane.BundleStream, error) {
	go func() {
		<-ctx.Done()
		_ = c.stream.Close()
	}()
	return c.stream, nil
}

type statusTestStream struct {
	done chan struct{}
	once sync.Once
}

func newStatusTestStream() *statusTestStream {
	return &statusTestStream{done: make(chan struct{})}
}

func (s *statusTestStream) Recv() (*dataplane.BundleEvent, error) {
	<-s.done
	return nil, io.EOF
}

func (s *statusTestStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

var _ dataplane.ControlPlane = (*statusTestControlPlane)(nil)
var _ dataplane.BundleStream = (*statusTestStream)(nil)
