package strategy_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/govern"
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
	if len(row.Trace.Steps) == 0 {
		t.Fatalf("expected trace steps, got %+v", row)
	}
}

func TestEvalStrategyFallsThroughOnRolloutMiss(t *testing.T) {
	full := compileStrategyBundle(t, `
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		user.country == "US"
	} rollout 20 then Canary {
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
	when {
		user.country == "US"
	} kill_switch then Disabled {
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
	want := []govern.TraceStep{
		{
			Check:  "strategy:CheckoutRouting/Disabled:kill_switch",
			Result: false,
			Detail: "candidate kill_switch enabled",
		},
		{
			Check:  "strategy:CheckoutRouting/Stable:fallback",
			Result: true,
			Detail: "else arm selected",
		},
	}
	if !reflect.DeepEqual(result.Trace.Steps, want) {
		t.Fatalf("trace steps = %#v, want %#v", result.Trace.Steps, want)
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
	if got := len(result.Trace.Steps); got != 3 {
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
	want := []govern.TraceStep{
		{
			Check:  "strategy:CheckoutRouting/Domestic:condition",
			Result: false,
			Detail: `user.country == "US"`,
		},
		{
			Check:  "strategy:CheckoutRouting/Global:fallback",
			Result: true,
			Detail: "else arm selected",
		},
	}
	if !reflect.DeepEqual(result.Trace.Steps, want) {
		t.Fatalf("trace steps = %#v, want %#v", result.Trace.Steps, want)
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
