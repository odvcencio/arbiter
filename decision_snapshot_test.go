package arbiter_test

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/flags"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/overrides"
)

const snapshotSource = `
segment enterprise { user.plan == "enterprise" }

rule Review {
    active_from 2026-01-10T00:00:00Z
    when segment enterprise { user.plan == "enterprise" }
    then Approve { plan: user.plan, clock: __now }
}

outcome Route { target: string }
strategy Checkout returns Route {
    when segment enterprise { true } then Priority { target: user.plan }
    else Standard { target: "default" }
}

flag ready type boolean default false {
    when enterprise then true
}
flag checkout type boolean default false {
    requires ready
    when enterprise then true
}
`

func TestDecisionSnapshotKeepsInputsAcrossModalities(t *testing.T) {
	prog, err := arbiter.Compile([]byte(snapshotSource))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := flags.Load([]byte(snapshotSource))
	if err != nil {
		t.Fatal(err)
	}
	store := overrides.NewStore()
	setOverrides := func(kill *bool) {
		t.Helper()
		for _, err := range []error{
			store.SetRule("bundle", "Review", overrides.RuleOverride{KillSwitch: kill}),
			store.SetStrategy("bundle", "Checkout", "Priority", overrides.StrategyOverride{KillSwitch: kill}),
			store.SetFlag("bundle", "ready", overrides.FlagOverride{KillSwitch: kill}),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	kill := false
	setOverrides(&kill)

	// Neither the setter input nor a returned override owns the store's values.
	kill = true
	live, ok := store.Rule("bundle", "Review")
	if !ok || *live.KillSwitch {
		t.Fatalf("setter leaked pointer into store: %+v", live)
	}
	*live.KillSwitch = true

	at := time.Date(2026, 1, 10, 2, 0, 0, 123, time.FixedZone("test", 7200))
	roles := []any{map[string]any{"name": "buyer"}}
	user := map[string]any{"plan": "basic", "roles": roles}
	facts := map[string]any{"user": user, "user.plan": "enterprise", "__now": "1900-01-01T00:00:00Z"}
	snapshot, err := govern.NewDecisionSnapshot(facts, at, "bundle", store)
	if err != nil {
		t.Fatal(err)
	}
	if user["plan"] != "basic" {
		t.Fatalf("snapshot changed caller's nested facts: %v", user)
	}
	if got := snapshot.EvalTime(); got.Location() != time.UTC || !got.Equal(at) {
		t.Fatalf("evaluation time: %v", got)
	}

	user["plan"], facts["user.plan"] = "guest", "guest"
	roles[0].(map[string]any)["name"] = "changed"
	setOverrides(&kill)

	viewFacts := snapshot.Facts()
	viewUser := viewFacts["user"].(map[string]any)
	if role := viewUser["roles"].([]any)[0].(map[string]any)["name"]; role != "buyer" {
		t.Fatalf("nested snapshot value: %v", role)
	}
	viewUser["plan"] = "changed"
	bundleID, view := snapshot.Overrides()
	captured, ok := view.Rule(bundleID, "Review")
	if !ok || *captured.KillSwitch {
		t.Fatalf("snapshot did not retain prior override: %+v", captured)
	}
	*captured.KillSwitch = true

	matched, _, err := arbiter.EvalDecision(prog, snapshot)
	if err != nil || len(matched) != 1 || matched[0].Action != "Approve" ||
		matched[0].Params["plan"] != "enterprise" || matched[0].Params["clock"] != at.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("rule used inconsistent inputs: matches=%+v, error=%v", matched, err)
	}
	route, err := prog.Strategies.EvaluateDecision("Checkout", snapshot)
	if err != nil || route.Selected != "Priority" || route.Params["target"] != "enterprise" {
		t.Fatalf("strategy used inconsistent inputs: %+v, %v", route, err)
	}
	if flag := flags.ExplainDecision("checkout", snapshot); flag.Variant.Name != "true" {
		t.Fatalf("flag prerequisite used live overrides: %+v", flag)
	}

	// A later decision sees the updates, including the prerequisite override.
	facts["user.plan"] = "enterprise"
	next, err := govern.NewDecisionSnapshot(facts, at.Add(time.Second), "bundle", store)
	if err != nil {
		t.Fatal(err)
	}
	matched, _, err = arbiter.EvalDecision(prog, next)
	if err != nil || len(matched) != 0 {
		t.Fatalf("later decision missed the rule override: %+v, %v", matched, err)
	}
	route, err = prog.Strategies.EvaluateDecision("Checkout", next)
	if err != nil || route.Selected != "Standard" {
		t.Fatalf("later decision missed the strategy override: %+v, %v", route, err)
	}
	if flag := flags.ExplainDecision("checkout", next); flag.Variant.Name != "false" {
		t.Fatalf("later decision missed the prerequisite override: %+v", flag)
	}
}

func TestDecisionSnapshotRejectsUnboundedFactTrees(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	for _, facts := range []map[string]any{
		cyclic,
		{strings.Repeat("a.", 256) + "b": true},
		{"nested": []any{new(int)}},
	} {
		if _, err := govern.NewDecisionSnapshot(facts, time.Now(), "", nil); err == nil {
			t.Fatalf("invalid decision facts accepted")
		}
	}
}
