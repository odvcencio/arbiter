package grpcserver

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const bundleSourceV1 = `
rule Decide {
	when { user.score >= 600 }
	then Approved { tier: "gold" }
}
`

const bundleSourceV2 = `
rule Decide {
	when { user.score >= 600 }
	then Approved { tier: "platinum" }
}
`

func TestRegistryPersistsHistoryAndRollback(t *testing.T) {
	path := t.TempDir() + "/bundles/registry.json"
	registry, err := NewFileRegistry(path)
	if err != nil {
		t.Fatalf("NewFileRegistry: %v", err)
	}

	first, err := registry.Publish("checkout", []byte(bundleSourceV1))
	if err != nil {
		t.Fatalf("Publish first: %v", err)
	}
	second, err := registry.Publish("checkout", []byte(bundleSourceV2))
	if err != nil {
		t.Fatalf("Publish second: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("expected distinct bundle ids, got %q", first.ID)
	}

	reloaded, err := NewFileRegistry(path)
	if err != nil {
		t.Fatalf("Reload registry: %v", err)
	}
	active, ok := reloaded.GetActive("checkout")
	if !ok || active.ID != second.ID {
		t.Fatalf("expected second bundle active after reload, got %+v ok=%v", active, ok)
	}

	rolledBack, previous, err := reloaded.Rollback("checkout")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolledBack.ID != first.ID || previous.ID != second.ID {
		t.Fatalf("unexpected rollback result: current=%+v previous=%+v", rolledBack, previous)
	}

	finalReload, err := NewFileRegistry(path)
	if err != nil {
		t.Fatalf("Reload after rollback: %v", err)
	}
	active, ok = finalReload.GetActive("checkout")
	if !ok || active.ID != first.ID {
		t.Fatalf("expected first bundle active after rollback reload, got %+v ok=%v", active, ok)
	}
}

func TestRegistryInstallStoresCompiledBundleWithoutPrematureActivation(t *testing.T) {
	registry := NewRegistry()

	bundle, err := BuildBundle("checkout", []byte(bundleSourceV1), time.Time{})
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}

	stored, err := registry.Install(bundle, false)
	if err != nil {
		t.Fatalf("Install inactive: %v", err)
	}
	if stored.ID != bundle.ID {
		t.Fatalf("unexpected stored bundle id: got %s want %s", stored.ID, bundle.ID)
	}
	if _, ok := registry.GetActive("checkout"); ok {
		t.Fatalf("expected install(false) to leave checkout inactive")
	}
	if got, ok := registry.Get(bundle.ID); !ok || got.ID != bundle.ID {
		t.Fatalf("expected compiled bundle to be present after install, got %+v ok=%v", got, ok)
	}

	activated, err := registry.Install(bundle, true)
	if err != nil {
		t.Fatalf("Install active: %v", err)
	}
	if activated.ID != bundle.ID {
		t.Fatalf("unexpected activated bundle id: got %s want %s", activated.ID, bundle.ID)
	}
	active, ok := registry.GetActive("checkout")
	if !ok || active.ID != bundle.ID {
		t.Fatalf("expected install(true) to activate checkout bundle, got %+v ok=%v", active, ok)
	}
}

func TestRegistryPersistenceStatusTracksHealth(t *testing.T) {
	path := t.TempDir() + "/bundles/registry.json"
	registry, err := NewFileRegistry(path)
	if err != nil {
		t.Fatalf("NewFileRegistry: %v", err)
	}
	if status := registry.PersistenceStatus(); !status.Configured || !status.Durable || status.File != path || !status.Healthy || status.WritesTotal == 0 {
		t.Fatalf("unexpected initial persistence status: %+v", status)
	}

	if _, err := registry.Publish("checkout", []byte(bundleSourceV1)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if status := registry.PersistenceStatus(); !status.Healthy || status.WritesTotal < 2 {
		t.Fatalf("expected successful persisted publish, got %+v", status)
	}

	badPath := t.TempDir()
	registry.mu.Lock()
	registry.path = badPath
	registry.mu.Unlock()
	registry.persistence.Configure(true, true, badPath)

	if _, err := registry.Publish("checkout_failure", []byte(bundleSourceV1)); err == nil {
		t.Fatal("expected publish against directory path to fail")
	}
	status := registry.PersistenceStatus()
	if status.Healthy || status.ErrorsTotal != 1 || status.LastError == "" || status.LastErrorAt.IsZero() {
		t.Fatalf("expected failed persistence status, got %+v", status)
	}
}

func TestRegistryFailedWritesLeaveActiveRevisionAndWatchersUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := NewFileRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.Publish("checkout", []byte(bundleSourceV1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Publish("checkout", []byte(bundleSourceV2))
	if err != nil {
		t.Fatal(err)
	}
	nextSource := []byte(`rule Decide { when { true } then Review {} }`)
	next, err := BuildBundle("checkout", nextSource, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	events, cancel := registry.Subscribe()
	defer cancel()
	registry.path = t.TempDir() // A regular snapshot cannot replace this directory.

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"publish", func() error { _, err := registry.Publish("checkout", nextSource); return err }},
		{"install", func() error { _, err := registry.Install(next, false); return err }},
		{"activate", func() error { _, err := registry.Activate("checkout", first.ID); return err }},
		{"rollback", func() error { _, _, err := registry.Rollback("checkout"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil {
				t.Fatal("expected snapshot write to fail")
			}
			active, ok := registry.GetActive("checkout")
			if !ok || active.ID != second.ID {
				t.Fatalf("failed write changed active revision to %+v", active)
			}
			if _, ok := registry.Get(next.ID); ok {
				t.Fatal("failed write published a candidate")
			}
			select {
			case event := <-events:
				t.Fatalf("failed write notified watchers: %+v", event)
			default:
			}
		})
	}

	registry.path = path
	published, err := registry.Publish("checkout", nextSource)
	if err != nil || published.ID != next.ID {
		t.Fatalf("publish retry failed: %+v, %v", published, err)
	}
	select {
	case event := <-events:
		if event.Type != bundleEventPublished || event.Bundle.ID != next.ID {
			t.Fatalf("wrong event after retry: %+v", event)
		}
	default:
		t.Fatal("successful retry did not notify watchers")
	}
	reloaded, err := NewFileRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if active, ok := reloaded.GetActive("checkout"); !ok || active.ID != next.ID {
		t.Fatalf("retry did not persist the active revision: %+v", active)
	}
}

func TestRegistryConcurrentPublishesPersistSameState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := NewFileRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			source := fmt.Appendf(nil, "rule Decide { when { true } then Review { revision: %d } }", i)
			if _, err := registry.Publish("checkout", source); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	reloaded, err := NewFileRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := registry.GetActive("checkout")
	persisted, ok := reloaded.GetActive("checkout")
	if current == nil || !ok || persisted.ID != current.ID {
		t.Fatalf("live and persisted active revisions differ: %+v / %+v", current, persisted)
	}
	for i := range 8 {
		source := fmt.Appendf(nil, "rule Decide { when { true } then Review { revision: %d } }", i)
		id := bundleIdentity("checkout", source)
		if _, ok := reloaded.Get(id); !ok {
			t.Errorf("persisted state lost concurrent revision %d", i)
		}
	}
}
