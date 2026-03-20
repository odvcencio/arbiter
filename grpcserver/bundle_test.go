package grpcserver

import (
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
