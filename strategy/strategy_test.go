package strategy_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/overrides"
)

func TestEvalStrategySelectsCandidateAndFallsBack(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
	reason: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		let domestic = user.country == "US"
		domestic
	} then Domestic {
		target: "domestic",
		reason: "local",
	}

	else Global {
		target: "global",
		reason: "fallback",
	}
}
`)

	us, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{"country": "US"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy US: %v", err)
	}
	if us.Selected != "Domestic" {
		t.Fatalf("Selected = %q, want Domestic", us.Selected)
	}
	if us.Params["target"] != "domestic" {
		t.Fatalf("unexpected params: %+v", us.Params)
	}

	row, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{"country": "DE"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy ROW: %v", err)
	}
	if row.Selected != "Global" {
		t.Fatalf("Selected = %q, want Global", row.Selected)
	}
	if len(row.Arbitrace.Steps) == 0 {
		t.Fatalf("expected trace steps, got %+v", row)
	}
}

func TestEvalStrategyFallsThroughOnRolloutMiss(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	rollout 20
	when {
		user.country == "US"
	} then Canary {
		target: "canary",
	}

	else Stable {
		target: "stable",
	}
}
`)

	var lowBucket, highBucket string
	namespace := govern.AutoRolloutNamespace("", "strategy:CheckoutRouting:candidate:Canary")
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("user_%d", i)
		bucket := govern.RolloutBucket(namespace, id)
		if bucket < 2000 && lowBucket == "" {
			lowBucket = id
		}
		if bucket >= 2000 && highBucket == "" {
			highBucket = id
		}
		if lowBucket != "" && highBucket != "" {
			break
		}
	}

	low, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{
			"country": "US",
			"id":      lowBucket,
		},
	})
	if err != nil {
		t.Fatalf("EvalStrategy low bucket: %v", err)
	}
	if low.Selected != "Canary" {
		t.Fatalf("Selected = %q, want Canary", low.Selected)
	}

	high, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{
			"country": "US",
			"id":      highBucket,
		},
	})
	if err != nil {
		t.Fatalf("EvalStrategy high bucket: %v", err)
	}
	if high.Selected != "Stable" {
		t.Fatalf("Selected = %q, want Stable", high.Selected)
	}
}

func TestEvalStrategyKillSwitchSkipsCandidate(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	kill_switch on
	when {
		user.country == "US"
	} then Disabled {
		target: "disabled",
	}

	else Stable {
		target: "stable",
	}
}
`)

	result, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{"country": "US"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Selected != "Stable" {
		t.Fatalf("Selected = %q, want Stable", result.Selected)
	}
	want := []govern.ArbitraceStep{
		{
			Check:       "strategy:CheckoutRouting/Disabled:kill_switch",
			Result:      true,
			Detail:      "kill_switch declared on",
			Phase:       govern.ArbitracePhaseGovernance,
			Scope:       govern.ArbitraceScopeStrategyCandidate,
			Subject:     "CheckoutRouting/Disabled",
			Kind:        govern.ArbitraceKindKillSwitch,
			Disposition: govern.ArbitraceDispositionPassed,
		},
		{
			Check:       "strategy:CheckoutRouting/Stable:fallback",
			Result:      true,
			Detail:      "else arm selected",
			Phase:       govern.ArbitracePhaseMatch,
			Scope:       govern.ArbitraceScopeStrategyCandidate,
			Subject:     "CheckoutRouting/Stable",
			Kind:        govern.ArbitraceKindFallback,
			Disposition: govern.ArbitraceDispositionPassed,
		},
	}
	if !reflect.DeepEqual(result.Arbitrace.Steps, want) {
		t.Fatalf("trace steps = %#v, want %#v", result.Arbitrace.Steps, want)
	}
}

func TestEvalStrategyKillSwitchOffKeepsCandidateEligible(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	kill_switch off
	when {
		user.country == "US"
	} then Enabled {
		target: "enabled",
	}

	else Stable {
		target: "stable",
	}
}
`)

	result, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{"country": "US"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Selected != "Enabled" {
		t.Fatalf("Selected = %q, want Enabled", result.Selected)
	}
	want := []govern.ArbitraceStep{
		{
			Check:       "strategy:CheckoutRouting/Enabled:kill_switch",
			Result:      false,
			Detail:      "kill_switch declared off",
			Phase:       govern.ArbitracePhaseGovernance,
			Scope:       govern.ArbitraceScopeStrategyCandidate,
			Subject:     "CheckoutRouting/Enabled",
			Kind:        govern.ArbitraceKindKillSwitch,
			Disposition: govern.ArbitraceDispositionBlocked,
		},
		{
			Check:       "strategy:CheckoutRouting/Enabled:condition",
			Result:      true,
			Detail:      "user.country == \"US\"",
			Phase:       govern.ArbitracePhaseMatch,
			Scope:       govern.ArbitraceScopeStrategyCandidate,
			Subject:     "CheckoutRouting/Enabled",
			Kind:        govern.ArbitraceKindCondition,
			Disposition: govern.ArbitraceDispositionPassed,
		},
	}
	if !reflect.DeepEqual(result.Arbitrace.Steps, want) {
		t.Fatalf("trace steps = %#v, want %#v", result.Arbitrace.Steps, want)
	}
}

func TestEvalStrategyActiveWindowSkipsCandidate(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	active_from 2026-01-10T00:00:00Z
	when {
		user.country == "US"
	} then Canary {
		target: "canary",
	}

	else Stable {
		target: "stable",
	}
}
`)

	result, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"__now": "2026-01-09T23:59:59Z",
		"user":  map[string]any{"country": "US"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Selected != "Stable" {
		t.Fatalf("Selected = %q, want Stable", result.Selected)
	}
	if len(result.Arbitrace.Steps) < 2 {
		t.Fatalf("expected active window + fallback steps, got %#v", result.Arbitrace.Steps)
	}
	first := result.Arbitrace.Steps[0]
	if first.Check != "strategy:CheckoutRouting/Canary:active_from 2026-01-10T00:00:00Z" {
		t.Fatalf("unexpected first arbitrace check: %#v", first)
	}
	if first.Kind != govern.ArbitraceKindActiveFrom || first.Result {
		t.Fatalf("unexpected active_from arbitrace step: %#v", first)
	}
}

func TestEvalStrategyElseMatchesWhenAllWhenArmsFail(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		user.country == "US"
	} then Domestic {
		target: "domestic",
	}

	when {
		user.country == "CA"
	} then Canada {
		target: "canada",
	}

	else Global {
		target: "global",
	}
}
`)

	result, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{"country": "DE"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Selected != "Global" {
		t.Fatalf("Selected = %q, want Global", result.Selected)
	}
	if got := len(result.Arbitrace.Steps); got != 3 {
		t.Fatalf("trace step count = %d, want 3", got)
	}
}

func TestEvalStrategyTraceCapturesRecognitionPath(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		user.country == "US"
	} then Domestic {
		target: "domestic",
	}

	else Global {
		target: "global",
	}
}
`)

	result, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{"country": "DE"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	want := []govern.ArbitraceStep{
		{
			Check:       "strategy:CheckoutRouting/Domestic:condition",
			Result:      false,
			Detail:      `user.country == "US"`,
			Phase:       govern.ArbitracePhaseMatch,
			Scope:       govern.ArbitraceScopeStrategyCandidate,
			Subject:     "CheckoutRouting/Domestic",
			Kind:        govern.ArbitraceKindCondition,
			Disposition: govern.ArbitraceDispositionBlocked,
		},
		{
			Check:       "strategy:CheckoutRouting/Global:fallback",
			Result:      true,
			Detail:      "else arm selected",
			Phase:       govern.ArbitracePhaseMatch,
			Scope:       govern.ArbitraceScopeStrategyCandidate,
			Subject:     "CheckoutRouting/Global",
			Kind:        govern.ArbitraceKindFallback,
			Disposition: govern.ArbitraceDispositionPassed,
		},
	}
	if !reflect.DeepEqual(result.Arbitrace.Steps, want) {
		t.Fatalf("trace steps = %#v, want %#v", result.Arbitrace.Steps, want)
	}
}

func TestEvalStrategyWithOverridesAddsCandidateRollout(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		user.country == "US"
	} then Canary {
		target: "canary",
	}

	else Stable {
		target: "stable",
	}
}
`)

	store := overrides.NewStore()
	rollout := uint16(2000)
	if err := store.SetStrategy("bundle_test", "CheckoutRouting", "Canary", overrides.StrategyOverride{
		Rollout: &rollout,
	}); err != nil {
		t.Fatalf("SetStrategy: %v", err)
	}

	namespace := govern.AutoRolloutNamespace("bundle_test", "strategy:CheckoutRouting:candidate:Canary")
	var lowBucket, highBucket string
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("user_%d", i)
		bucket := govern.RolloutBucket(namespace, id)
		if bucket < 2000 && lowBucket == "" {
			lowBucket = id
		}
		if bucket >= 2000 && highBucket == "" {
			highBucket = id
		}
		if lowBucket != "" && highBucket != "" {
			break
		}
	}

	low, err := arbiter.EvalStrategyWithOverrides(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{
			"country": "US",
			"id":      lowBucket,
		},
	}, "bundle_test", store)
	if err != nil {
		t.Fatalf("EvalStrategyWithOverrides low bucket: %v", err)
	}
	if low.Selected != "Canary" {
		t.Fatalf("Selected = %q, want Canary", low.Selected)
	}

	high, err := arbiter.EvalStrategyWithOverrides(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{
			"country": "US",
			"id":      highBucket,
		},
	}, "bundle_test", store)
	if err != nil {
		t.Fatalf("EvalStrategyWithOverrides high bucket: %v", err)
	}
	if high.Selected != "Stable" {
		t.Fatalf("Selected = %q, want Stable", high.Selected)
	}
}

func TestCompileFullRejectsDuplicateStrategyLabels(t *testing.T) {
	_, err := arbiter.CompileFull([]byte(`
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		user.country == "US"
	} then Duplicate {
		target: "domestic",
	}

	else Duplicate {
		target: "global",
	}
}
`))
	if err == nil {
		t.Fatal("expected duplicate label error")
	}
	if !strings.Contains(err.Error(), `duplicate candidate label "Duplicate"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEvalStrategySurfacesSegmentRuntimeError guards against a candidate
// segment's runtime error (a type mismatch in its condition) being silently
// treated as "candidate did not match". It must be reported the same way a
// candidate condition error already is: as a returned error.
func TestEvalStrategySurfacesSegmentRuntimeError(t *testing.T) {
	full := compileStrategyBundle(t, `
segment bad_compare { user.balance > 10.50 USD }

outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when segment bad_compare {
		true
	} then Domestic {
		target: "domestic",
	}

	else Global {
		target: "global",
	}
}
`)

	_, err := arbiter.EvalStrategy(full, "CheckoutRouting", map[string]any{
		"user": map[string]any{"balance": "enterprise"},
	})
	if err == nil {
		t.Fatal("EvalStrategy: expected a segment runtime error, got nil")
	}
}

func compileStrategyBundle(t *testing.T, source string) *arbiter.CompileResult {
	t.Helper()
	full, err := arbiter.CompileFull([]byte(source))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
	if full.Strategies == nil {
		t.Fatal("CompileFull returned nil strategies")
	}
	if full.Strategies.Count() == 0 {
		t.Fatal("CompileFull returned no strategies")
	}
	return full
}
