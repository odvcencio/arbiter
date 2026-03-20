package overrides

import (
	"testing"
	"time"
)

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

func TestStoreSnapshotForBundleReturnsDeepCopy(t *testing.T) {
	store := NewStore()
	kill := true
	rollout := uint8(42)
	if err := store.SetRule("bundle_a", "Approve", RuleOverride{KillSwitch: &kill, Rollout: &rollout}); err != nil {
		t.Fatalf("SetRule: %v", err)
	}

	snapshot := store.SnapshotForBundle("bundle_a")
	if snapshot.BundleID != "bundle_a" {
		t.Fatalf("unexpected bundle id: %+v", snapshot)
	}
	if snapshot.Rules["Approve"].KillSwitch == nil || !*snapshot.Rules["Approve"].KillSwitch {
		t.Fatalf("expected copied rule override: %+v", snapshot.Rules["Approve"])
	}
	*snapshot.Rules["Approve"].KillSwitch = false

	got, ok := store.Rule("bundle_a", "Approve")
	if !ok || got.KillSwitch == nil || !*got.KillSwitch {
		t.Fatalf("expected store to remain unchanged, got %+v ok=%v", got, ok)
	}
}

func TestStoreSubscribeStreamsSnapshotAndMutations(t *testing.T) {
	store := NewStore()
	initialRollout := uint8(11)
	if err := store.SetRule("bundle_a", "Approve", RuleOverride{Rollout: &initialRollout}); err != nil {
		t.Fatalf("SetRule initial: %v", err)
	}

	snapshot, events, cancel := store.Subscribe("bundle_a")
	defer cancel()

	if snapshot.BundleID != "bundle_a" {
		t.Fatalf("unexpected snapshot bundle id: %+v", snapshot)
	}
	if got := snapshot.Rules["Approve"]; got.Rollout == nil || *got.Rollout != 11 {
		t.Fatalf("unexpected initial snapshot: %+v", got)
	}

	newRollout := uint8(44)
	if err := store.SetRule("bundle_a", "Approve", RuleOverride{Rollout: &newRollout}); err != nil {
		t.Fatalf("SetRule update: %v", err)
	}

	event := mustRecvOverrideEvent(t, events)
	if event.Type != OverrideEventRule || event.BundleID != "bundle_a" || event.RuleName != "Approve" {
		t.Fatalf("unexpected rule event: %+v", event)
	}
	if event.Rule.Rollout == nil || *event.Rule.Rollout != 44 {
		t.Fatalf("unexpected rule payload: %+v", event.Rule)
	}

	kill := true
	if err := store.SetFlag("bundle_a", "checkout_v2", FlagOverride{KillSwitch: &kill}); err != nil {
		t.Fatalf("SetFlag: %v", err)
	}
	event = mustRecvOverrideEvent(t, events)
	if event.Type != OverrideEventFlag || event.FlagKey != "checkout_v2" {
		t.Fatalf("unexpected flag event: %+v", event)
	}
	if event.Flag.KillSwitch == nil || !*event.Flag.KillSwitch {
		t.Fatalf("unexpected flag payload: %+v", event.Flag)
	}

	flagRollout := uint8(60)
	if err := store.SetFlagRule("bundle_a", "checkout_v2", 1, FlagRuleOverride{Rollout: &flagRollout}); err != nil {
		t.Fatalf("SetFlagRule: %v", err)
	}
	event = mustRecvOverrideEvent(t, events)
	if event.Type != OverrideEventFlagRule || event.RuleIndex != 1 || event.FlagKey != "checkout_v2" {
		t.Fatalf("unexpected flag rule event: %+v", event)
	}
	if event.FlagRule.Rollout == nil || *event.FlagRule.Rollout != 60 {
		t.Fatalf("unexpected flag rule payload: %+v", event.FlagRule)
	}
}

func TestStoreRestoreBundleReplacesOnlySelectedBundle(t *testing.T) {
	store := NewStore()
	kill := true
	rollout := uint8(33)
	if err := store.SetRule("bundle_a", "Approve", RuleOverride{KillSwitch: &kill}); err != nil {
		t.Fatalf("SetRule bundle_a: %v", err)
	}
	if err := store.SetFlag("bundle_b", "checkout_v2", FlagOverride{KillSwitch: &kill}); err != nil {
		t.Fatalf("SetFlag bundle_b: %v", err)
	}

	store.RestoreBundle("bundle_a", Snapshot{
		Rules: map[string]map[string]RuleOverride{
			"bundle_a": {
				"Deny": {Rollout: &rollout},
			},
		},
	})

	if _, ok := store.Rule("bundle_a", "Approve"); ok {
		t.Fatalf("expected old bundle_a rule override to be cleared")
	}
	if got, ok := store.Rule("bundle_a", "Deny"); !ok || got.Rollout == nil || *got.Rollout != 33 {
		t.Fatalf("unexpected restored bundle_a rule: %+v ok=%v", got, ok)
	}
	if got, ok := store.Flag("bundle_b", "checkout_v2"); !ok || got.KillSwitch == nil || !*got.KillSwitch {
		t.Fatalf("expected bundle_b override to remain intact, got %+v ok=%v", got, ok)
	}

	store.RestoreBundle("bundle_a", Snapshot{})
	if _, ok := store.Rule("bundle_a", "Deny"); ok {
		t.Fatalf("expected bundle_a overrides to be cleared")
	}
	if _, ok := store.Flag("bundle_b", "checkout_v2"); !ok {
		t.Fatalf("expected unrelated bundle_b override to remain")
	}
}

func mustRecvOverrideEvent(t *testing.T, events <-chan OverrideEvent) OverrideEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for override event")
		return OverrideEvent{}
	}
}
