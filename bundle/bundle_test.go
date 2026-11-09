package bundle_test

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/vm"
)

func TestBundleTableRoundTrip(t *testing.T) {
	src := []byte(`
table ladder {
    height: number | bitrate: string
    1080 | "6500k"
    720  | "3800k"
}
rule R {
    when { true }
    then A {
        let row = lookup ladder where height <= 900 order by height desc else { height: 0, bitrate: "800k" }
        bitrate: row.bitrate,
    }
}`)
	prog, err := arbiter.Compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rs := prog.Ruleset

	if len(rs.Tables) == 0 {
		t.Fatal("expected compiled ruleset to have tables")
	}
	if len(rs.LookupMetas) == 0 {
		t.Fatal("expected compiled ruleset to have lookup metas")
	}

	data, err := bundle.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("bundle size: %d bytes", len(data))

	restored, err := bundle.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(restored.Tables) != len(rs.Tables) {
		t.Fatalf("tables count: want %d, got %d", len(rs.Tables), len(restored.Tables))
	}
	if len(restored.LookupMetas) != len(rs.LookupMetas) {
		t.Fatalf("lookup metas count: want %d, got %d", len(rs.LookupMetas), len(restored.LookupMetas))
	}

	// Evaluate the restored ruleset — height <= 900 matches 720 row (desc order picks it first).
	sp := vm.NewStringPool(restored.Constants.Strings())
	dc := vm.DataFromMap(map[string]any{}, sp)
	matched, err := vm.EvalWithPool(restored, dc, sp)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("no rule matched")
	}
	got := matched[0].Params["bitrate"]
	if got != "3800k" {
		t.Errorf("bitrate: want %q, got %v", "3800k", got)
	}
}

func TestRoundTripMarshalUnmarshal(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`
rule FreeShipping {
	when { order.total >= 100 }
	then ApplyShipping { cost: 0, method: "free" }
}

rule StandardShipping {
	when { order.total < 100 }
	then ApplyShipping { cost: 5.99, method: "standard" }
}
`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rs := prog.Ruleset

	blob, err := bundle.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("bundle size: %d bytes", len(blob))

	restored, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Evaluate with the restored ruleset.
	sp := vm.NewStringPool(restored.Constants.Strings())
	dc := vm.DataFromMap(map[string]any{
		"order": map[string]any{"total": float64(150)},
	}, sp)
	matched, err := vm.EvalWithPool(restored, dc, sp)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("no match")
	}
	if matched[0].Action != "ApplyShipping" {
		t.Errorf("expected ApplyShipping, got %s", matched[0].Action)
	}
	if matched[0].Params["method"] != "free" {
		t.Errorf("expected free, got %v", matched[0].Params["method"])
	}
}

func TestRoundTripPreservesKillSwitchState(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`
rule Enabled {
	kill_switch on
	when { true }
	then Allow {}
}

rule ExplicitOff {
	kill_switch off
	when { true }
	then Review {}
}

rule Unset {
	when { true }
	then Route {}
}
`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	blob, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := []ir.KillSwitchState{
		restored.Rules[0].KillSwitch,
		restored.Rules[1].KillSwitch,
		restored.Rules[2].KillSwitch,
	}
	want := []ir.KillSwitchState{
		ir.KillSwitchOn,
		ir.KillSwitchOff,
		ir.KillSwitchUnset,
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected rule count: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rule %d kill_switch = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRoundTripPreservesActiveWindow(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`
rule Windowed {
	active_from 2026-01-01T00:00:00Z
	active_until 2026-02-01T00:00:00Z
	when { true }
	then Allow {}
}
`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	blob, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	rule := restored.Rules[0]
	if !rule.HasActiveFrom || !rule.HasActiveUntil {
		t.Fatalf("expected active window fields to survive round-trip, got %+v", rule)
	}
	if got := time.Unix(0, rule.ActiveFromUnixNano).UTC().Format(time.RFC3339Nano); got != "2026-01-01T00:00:00Z" {
		t.Fatalf("active_from = %q, want 2026-01-01T00:00:00Z", got)
	}
	if got := time.Unix(0, rule.ActiveUntilUnixNano).UTC().Format(time.RFC3339Nano); got != "2026-02-01T00:00:00Z" {
		t.Fatalf("active_until = %q, want 2026-02-01T00:00:00Z", got)
	}
}

func TestObfuscatedBundle(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`
segment vip { user.tier == "gold" }

rule VIPOffer {
	rollout 50
	when segment vip { user.cart > 50 }
	then Offer { discount: 20 }
}

rule Default {
	when { true }
	then Offer { discount: 0 }
}
`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rs := prog.Ruleset

	blob, err := bundle.MarshalObfuscated(rs, bundle.ObfuscateOptions{
		HashRuleNames:       true,
		HashSegmentNames:    true,
		StripRolloutDetails: true,
		StripPrereqs:        true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	restored, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Rule names should be hashed (not readable).
	strs := restored.Constants.Strings()
	for _, s := range strs {
		if s == "VIPOffer" || s == "Default" || s == "vip" {
			t.Errorf("string pool contains unobfuscated name %q", s)
		}
	}

	// Action names and param keys should be preserved.
	found := false
	for _, s := range strs {
		if s == "Offer" {
			found = true
		}
	}
	if !found {
		t.Error("action name 'Offer' was obfuscated — should be preserved")
	}

	// Rollout should be stripped.
	for _, r := range restored.Rules {
		if r.HasRollout {
			t.Error("rollout details should have been stripped")
		}
	}

	// Prereqs should be stripped.
	if len(restored.Prereqs) > 0 || len(restored.Excludes) > 0 {
		t.Error("prereqs/excludes should have been stripped")
	}

	// Should still eval correctly.
	sp := vm.NewStringPool(restored.Constants.Strings())
	dc := vm.DataFromMap(map[string]any{
		"user": map[string]any{"tier": "gold", "cart": float64(100)},
	}, sp)
	matched, err := vm.EvalWithPool(restored, dc, sp)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("no match on obfuscated bundle")
	}
	if matched[0].Action != "Offer" {
		t.Errorf("expected Offer, got %s", matched[0].Action)
	}
}

func TestBundleTagRoundTrip(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`
tag "fraud"
tag "realtime"

rule FraudRealtime tag "fraud" tag "realtime" {
	when { true }
	then Allow {}
}

rule FraudOnly tag "fraud" {
	when { true }
	then Review {}
}
`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	blob, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(restored.Tags) != len(prog.Ruleset.Tags) {
		t.Fatalf("tag pool length = %d, want %d", len(restored.Tags), len(prog.Ruleset.Tags))
	}
	if !restored.RuleMatchesTags(restored.Rules[0], []string{"fraud", "realtime"}) {
		t.Fatal("expected restored first rule to retain fraud+realtime tags")
	}
	if restored.RuleMatchesTags(restored.Rules[1], []string{"realtime"}) {
		t.Fatal("expected restored second rule to miss realtime tag")
	}
}

func TestUnmarshalRejectsTruncatedBundle(t *testing.T) {
	blob := mustMarshalSimpleBundle(t)
	if _, err := bundle.Unmarshal(blob[:len(blob)-1]); err == nil {
		t.Fatal("expected truncated bundle to fail")
	}
}

func TestUnmarshalRejectsTrailingBytes(t *testing.T) {
	blob := append(mustMarshalSimpleBundle(t), 0xFF)
	if _, err := bundle.Unmarshal(blob); err == nil || !strings.Contains(err.Error(), "trailing bytes") {
		t.Fatalf("expected trailing bytes error, got %v", err)
	}
}

func TestUnmarshalRejectsOversizedDeclaredCount(t *testing.T) {
	blob := make([]byte, 8)
	copy(blob[:4], []byte("ARB1"))
	binary.LittleEndian.PutUint32(blob[4:], 1_000_000)
	if _, err := bundle.Unmarshal(blob); err == nil || !strings.Contains(err.Error(), "declared count") {
		t.Fatalf("expected declared count error, got %v", err)
	}
}

func mustMarshalSimpleBundle(t *testing.T) []byte {
	t.Helper()
	prog, err := arbiter.Compile([]byte(`
rule Allow {
	when { true }
	then Approved {}
}
`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	blob, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return blob
}

// Hashing rule names must not hash a string literal that shares the rule's
// name: the rule would then compare against the hash.
func TestObfuscationKeepsStringsTheRulesUse(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`
table tiers {
    name: string | rank: number
    "Gold"       | 1
}
rule Gold { when { tier == "Gold" and tier in ["Gold", "Silver"] } then Pick { v: "Gold" } }
rule Ranked {
    when { let row = lookup tiers where name == tier
           row.rank == 1 }
    then Pick { v: row.name }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := bundle.MarshalObfuscated(prog.Ruleset, bundle.ObfuscateOptions{HashRuleNames: true})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := bundle.Unmarshal(blob)
	if err != nil {
		t.Fatal(err)
	}
	pool := vm.NewStringPool(rs.Constants.Strings())
	got, err := vm.EvalWithTagFilter(rs, vm.DataFromMap(map[string]any{"tier": "Gold"}, pool), pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Params["v"] != "Gold" || got[1].Params["v"] != "Gold" {
		t.Fatalf("obfuscated bundle matched %+v, want both rules with v=Gold", got)
	}
	if got[1].Name == "Ranked" {
		t.Fatal("rule name Ranked was not hashed")
	}
}
