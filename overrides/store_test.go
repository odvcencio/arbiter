package overrides

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/arbiter/ir"
)

func TestStoreSnapshotRoundTripViaFile(t *testing.T) {
	store := NewStore()
	kill := true
	rollout := uint16(2500)
	flagRollout := uint16(6000)
	strategyRollout := uint16(7500)

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
	if err := store.SetStrategy("bundle_a", "CheckoutRouting", "Canary", StrategyOverride{
		KillSwitch: &kill,
		Rollout:    &strategyRollout,
	}); err != nil {
		t.Fatalf("SetStrategy: %v", err)
	}

	path := t.TempDir() + "/overrides.json"
	if err := store.SaveFile(path); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	loaded := NewStore()
	if err := loaded.LoadFile(path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if got, ok := loaded.Rule("bundle_a", "Approve"); !ok || got.KillSwitch == nil || !*got.KillSwitch || got.Rollout == nil || *got.Rollout != 2500 {
		t.Fatalf("unexpected rule override: %+v ok=%v", got, ok)
	}
	if got, ok := loaded.Flag("bundle_a", "checkout_v2"); !ok || got.KillSwitch == nil || !*got.KillSwitch {
		t.Fatalf("unexpected flag override: %+v ok=%v", got, ok)
	}
	if got, ok := loaded.FlagRule("bundle_a", "checkout_v2", 1); !ok || got.Rollout == nil || *got.Rollout != 6000 {
		t.Fatalf("unexpected flag rule override: %+v ok=%v", got, ok)
	}
	if got, ok := loaded.Strategy("bundle_a", "CheckoutRouting", "Canary"); !ok || got.KillSwitch == nil || !*got.KillSwitch || got.Rollout == nil || *got.Rollout != 7500 {
		t.Fatalf("unexpected strategy override: %+v ok=%v", got, ok)
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

func TestStorePersistenceStatusTracksHealth(t *testing.T) {
	path := t.TempDir() + "/overrides/store.json"
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if status := store.PersistenceStatus(); !status.Configured || !status.Durable || status.File != path || !status.Healthy || status.WritesTotal == 0 {
		t.Fatalf("unexpected initial persistence status: %+v", status)
	}

	kill := true
	if err := store.SetFlag("bundle_a", "checkout_v2", FlagOverride{KillSwitch: &kill}); err != nil {
		t.Fatalf("SetFlag: %v", err)
	}
	if status := store.PersistenceStatus(); !status.Healthy || status.WritesTotal < 2 {
		t.Fatalf("expected successful persisted mutation, got %+v", status)
	}

	badPath := t.TempDir()
	store.mu.Lock()
	store.path = badPath
	store.mu.Unlock()
	store.persistence.Configure(true, true, badPath)
	if err := store.SetFlag("bundle_a", "checkout_v3", FlagOverride{KillSwitch: &kill}); err == nil {
		t.Fatal("expected SetFlag against directory path to fail")
	}
	status := store.PersistenceStatus()
	if status.Healthy || status.ErrorsTotal != 1 || status.LastError == "" || status.LastErrorAt.IsZero() {
		t.Fatalf("expected failed persistence status, got %+v", status)
	}
}

func TestStoreNotifiesSubscriberAfterPersistenceRetry(t *testing.T) {
	store := NewStore()
	path := filepath.Join(t.TempDir(), "overrides.json")
	if err := store.UseFile(path); err != nil {
		t.Fatal(err)
	}
	_, events, cancel := store.Subscribe("bundle")
	defer cancel()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	setRule := func() error {
		kill := true
		return store.SetRule("bundle", "Review", RuleOverride{KillSwitch: &kill})
	}
	if err := setRule(); err == nil {
		t.Fatal("write to directory unexpectedly succeeded")
	}
	select {
	case event := <-events:
		t.Fatalf("failed write notified subscriber: %+v", event)
	default:
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := setRule(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Type != OverrideEventRule || event.RuleName != "Review" || event.Rule.KillSwitch == nil || !*event.Rule.KillSwitch {
			t.Fatalf("unexpected retry event: %+v", event)
		}
	default:
		t.Fatal("successful retry did not notify subscriber")
	}
}

func TestStoreSnapshotForBundleReturnsDeepCopy(t *testing.T) {
	store := NewStore()
	kill := true
	rollout := uint16(4200)
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
	initialRollout := uint16(1100)
	if err := store.SetRule("bundle_a", "Approve", RuleOverride{Rollout: &initialRollout}); err != nil {
		t.Fatalf("SetRule initial: %v", err)
	}

	snapshot, events, cancel := store.Subscribe("bundle_a")
	defer cancel()

	if snapshot.BundleID != "bundle_a" {
		t.Fatalf("unexpected snapshot bundle id: %+v", snapshot)
	}
	if got := snapshot.Rules["Approve"]; got.Rollout == nil || *got.Rollout != 1100 {
		t.Fatalf("unexpected initial snapshot: %+v", got)
	}

	newRollout := uint16(4400)
	if err := store.SetRule("bundle_a", "Approve", RuleOverride{Rollout: &newRollout}); err != nil {
		t.Fatalf("SetRule update: %v", err)
	}

	event := mustRecvOverrideEvent(t, events)
	if event.Type != OverrideEventRule || event.BundleID != "bundle_a" || event.RuleName != "Approve" {
		t.Fatalf("unexpected rule event: %+v", event)
	}
	if event.Rule.Rollout == nil || *event.Rule.Rollout != 4400 {
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

	flagRollout := uint16(6000)
	if err := store.SetFlagRule("bundle_a", "checkout_v2", 1, FlagRuleOverride{Rollout: &flagRollout}); err != nil {
		t.Fatalf("SetFlagRule: %v", err)
	}
	event = mustRecvOverrideEvent(t, events)
	if event.Type != OverrideEventFlagRule || event.RuleIndex != 1 || event.FlagKey != "checkout_v2" {
		t.Fatalf("unexpected flag rule event: %+v", event)
	}
	if event.FlagRule.Rollout == nil || *event.FlagRule.Rollout != 6000 {
		t.Fatalf("unexpected flag rule payload: %+v", event.FlagRule)
	}

	strategyKill := true
	strategyRollout := uint16(7200)
	if err := store.SetStrategy("bundle_a", "CheckoutRouting", "Canary", StrategyOverride{
		KillSwitch: &strategyKill,
		Rollout:    &strategyRollout,
	}); err != nil {
		t.Fatalf("SetStrategy: %v", err)
	}
	event = mustRecvOverrideEvent(t, events)
	if event.Type != OverrideEventStrategy || event.StrategyName != "CheckoutRouting" || event.CandidateLabel != "Canary" {
		t.Fatalf("unexpected strategy event: %+v", event)
	}
	if event.Strategy.KillSwitch == nil || !*event.Strategy.KillSwitch || event.Strategy.Rollout == nil || *event.Strategy.Rollout != 7200 {
		t.Fatalf("unexpected strategy payload: %+v", event.Strategy)
	}
}

func TestStoreRestoreBundleReplacesOnlySelectedBundle(t *testing.T) {
	store := NewStore()
	kill := true
	rollout := uint16(3300)
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
	if got, ok := store.Rule("bundle_a", "Deny"); !ok || got.Rollout == nil || *got.Rollout != 3300 {
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

func TestOverrideKillSwitchState(t *testing.T) {
	enabled := true
	disabled := false

	tests := []struct {
		name string
		got  ir.KillSwitchState
		want ir.KillSwitchState
	}{
		{name: "rule absent", got: RuleOverride{}.KillSwitchState(), want: ir.KillSwitchUnset},
		{name: "rule on", got: RuleOverride{KillSwitch: &enabled}.KillSwitchState(), want: ir.KillSwitchOn},
		{name: "rule off", got: RuleOverride{KillSwitch: &disabled}.KillSwitchState(), want: ir.KillSwitchOff},
		{name: "flag absent", got: FlagOverride{}.KillSwitchState(), want: ir.KillSwitchUnset},
		{name: "flag on", got: FlagOverride{KillSwitch: &enabled}.KillSwitchState(), want: ir.KillSwitchOn},
		{name: "flag off", got: FlagOverride{KillSwitch: &disabled}.KillSwitchState(), want: ir.KillSwitchOff},
		{name: "strategy absent", got: StrategyOverride{}.KillSwitchState(), want: ir.KillSwitchUnset},
		{name: "strategy on", got: StrategyOverride{KillSwitch: &enabled}.KillSwitchState(), want: ir.KillSwitchOn},
		{name: "strategy off", got: StrategyOverride{KillSwitch: &disabled}.KillSwitchState(), want: ir.KillSwitchOff},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Fatalf("%s: state = %q, want %q", tt.name, tt.got, tt.want)
		}
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
