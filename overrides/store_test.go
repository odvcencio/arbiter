package overrides

import "testing"

func TestStoreSnapshotRoundTripViaFile(t *testing.T) {
	store := NewStore()
	kill := true
	rollout := uint8(25)
	flagRollout := uint8(60)

	if err := store.SetRule("bundle_a", "Approve", RuleOverride{
		KillSwitch: &kill,
		Rollout:    &rollout,
	}); err != nil {
		t.Fatalf("SetRule: %v", err)
	}
	if err := store.SetFlag("bundle_a", "checkout_v2", FlagOverride{
		KillSwitch: &kill,
	}); err != nil {
		t.Fatalf("SetFlag: %v", err)
	}
	if err := store.SetFlagRule("bundle_a", "checkout_v2", 1, FlagRuleOverride{
		Rollout: &flagRollout,
	}); err != nil {
		t.Fatalf("SetFlagRule: %v", err)
	}

	path := t.TempDir() + "/overrides.json"
	if err := store.SaveFile(path); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	loaded := NewStore()
	if err := loaded.LoadFile(path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if got, ok := loaded.Rule("bundle_a", "Approve"); !ok || got.KillSwitch == nil || !*got.KillSwitch || got.Rollout == nil || *got.Rollout != 25 {
		t.Fatalf("unexpected rule override: %+v ok=%v", got, ok)
	}
	if got, ok := loaded.Flag("bundle_a", "checkout_v2"); !ok || got.KillSwitch == nil || !*got.KillSwitch {
		t.Fatalf("unexpected flag override: %+v ok=%v", got, ok)
	}
	if got, ok := loaded.FlagRule("bundle_a", "checkout_v2", 1); !ok || got.Rollout == nil || *got.Rollout != 60 {
		t.Fatalf("unexpected flag rule override: %+v ok=%v", got, ok)
	}
}

func TestStoreSnapshotReturnsDeepCopy(t *testing.T) {
	store := NewStore()
	kill := true
	if err := store.SetRule("bundle_a", "Approve", RuleOverride{KillSwitch: &kill}); err != nil {
		t.Fatalf("SetRule: %v", err)
	}

	snapshot := store.Snapshot()
	if snapshot.Rules["bundle_a"]["Approve"].KillSwitch == nil {
		t.Fatal("expected kill switch copy")
	}
	*snapshot.Rules["bundle_a"]["Approve"].KillSwitch = false

	got, ok := store.Rule("bundle_a", "Approve")
	if !ok || got.KillSwitch == nil || !*got.KillSwitch {
		t.Fatalf("expected original store to remain unchanged, got %+v ok=%v", got, ok)
	}
}

func TestFileStorePersistsOnMutation(t *testing.T) {
	path := t.TempDir() + "/overrides/store.json"
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	kill := true
	if err := store.SetFlag("bundle_a", "checkout_v2", FlagOverride{KillSwitch: &kill}); err != nil {
		t.Fatalf("SetFlag: %v", err)
	}

	loaded := NewStore()
	if err := loaded.LoadFile(path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	got, ok := loaded.Flag("bundle_a", "checkout_v2")
	if !ok || got.KillSwitch == nil || !*got.KillSwitch {
		t.Fatalf("expected persisted flag override, got %+v ok=%v", got, ok)
	}
}
