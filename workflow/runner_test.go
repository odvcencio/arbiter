package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/expert"
)

func TestRunnerKeepsLastKnownGoodFactsAndExposesSourceStaleness(t *testing.T) {
	src := []byte(`
arbiter sales {
	poll 1s
	source https://feed.internal/facts
}

expert rule QualifyLead priority 10 per_fact {
	when {
		any lead in facts.Lead { lead.score >= 90 }
	}
	then emit Qualified {
		key: lead.key,
	}
}

expert rule AlertOnStaleSource priority 5 {
	when {
		source.feed_internal_facts.available == false
		and source.feed_internal_facts.__source_age_seconds >= 60
	}
	then emit SourceStale {
		age_seconds: source.feed_internal_facts.__source_age_seconds,
	}
}
`)

	w, err := Compile(src, Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	attempts := 0
	now := time.Unix(1_000, 0).UTC()
	runner, err := NewRunner(w, RunnerOptions{
		Now: func() time.Time { return now },
		Loader: func(_ context.Context, target string) ([]expert.Fact, error) {
			attempts++
			if target != "https://feed.internal/facts" {
				t.Fatalf("unexpected target %q", target)
			}
			if attempts == 1 {
				return []expert.Fact{{
					Type: "Lead",
					Key:  "lead-1",
					Fields: map[string]any{
						"score": float64(95),
					},
				}}, nil
			}
			return nil, errors.New("source unavailable")
		},
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		SourceAttempts: 2,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	first, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if got := first.Workflow.Arbiters["sales"].Delta.Outcomes; len(got) != 1 || got[0].Name != "Qualified" {
		t.Fatalf("first outcomes = %+v", got)
	}

	now = now.Add(70 * time.Second)
	second, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if got := second.Workflow.Arbiters["sales"].Delta.Outcomes; len(got) != 1 || got[0].Name != "SourceStale" {
		t.Fatalf("second outcomes = %+v", got)
	}
	if got := second.Workflow.Arbiters["sales"].Delta.Outcomes[0].Params["age_seconds"]; got != float64(70) {
		t.Fatalf("age_seconds = %#v, want 70", got)
	}
	state := second.Sources["https://feed.internal/facts"]
	if state.Available {
		t.Fatalf("source state = %+v, want unavailable", state)
	}
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("source failures = %d, want 1", state.ConsecutiveFailures)
	}
	if state.FactCount != 1 {
		t.Fatalf("source fact count = %d, want 1", state.FactCount)
	}

	third, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("third Tick: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts after backoff skip = %d, want 3", attempts)
	}
	if got := third.Workflow.Arbiters["sales"].Delta.Outcomes; len(got) != 0 {
		t.Fatalf("third outcomes = %+v, want none during backoff window", got)
	}
}

func TestRunnerRestoresPendingSinkDeliveriesFromJournal(t *testing.T) {
	src := []byte(`
arbiter sales {
	poll 1s
	source https://feed.internal/facts
	on Qualified webhook https://hooks.internal/qualified
}

expert rule QualifyLead priority 10 per_fact {
	when {
		any lead in facts.Lead { lead.score >= 90 }
	}
	then emit Qualified {
		key: lead.key,
	}
}
`)

	makeWorkflow := func(t *testing.T) *Workflow {
		t.Helper()
		w, err := Compile(src, Options{})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return w
	}

	logPath := filepath.Join(t.TempDir(), "deliveries.jsonl")
	now := time.Unix(2_000, 0).UTC()
	webhookAttempts := 0
	handler := OutcomeHandlerFunc(func(_ context.Context, delivery Delivery) error {
		webhookAttempts++
		if delivery.Handler.Kind != "webhook" {
			t.Fatalf("unexpected handler kind %q", delivery.Handler.Kind)
		}
		if webhookAttempts == 1 {
			return errors.New("sink unavailable")
		}
		return nil
	})

	runner, err := NewRunner(makeWorkflow(t), RunnerOptions{
		Now: func() time.Time { return now },
		Loader: func(_ context.Context, _ string) ([]expert.Fact, error) {
			return []expert.Fact{{
				Type: "Lead",
				Key:  "lead-1",
				Fields: map[string]any{
					"score": float64(95),
				},
			}}, nil
		},
		Handlers: map[arbiter.ArbiterHandlerKind]OutcomeHandler{
			arbiter.ArbiterHandlerWebhook: handler,
		},
		DeliveryLog:    logPath,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner first: %v", err)
	}
	first, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if first.Sinks["webhook\x00https://hooks.internal/qualified"].Pending != 1 {
		t.Fatalf("first sink state = %+v", first.Sinks)
	}
	if err := runner.Close(); err != nil {
		t.Fatalf("Close first runner: %v", err)
	}

	now = now.Add(10 * time.Second)
	runner, err = NewRunner(makeWorkflow(t), RunnerOptions{
		Now: func() time.Time { return now },
		Loader: func(_ context.Context, _ string) ([]expert.Fact, error) {
			return nil, nil
		},
		Handlers: map[arbiter.ArbiterHandlerKind]OutcomeHandler{
			arbiter.ArbiterHandlerWebhook: handler,
		},
		DeliveryLog:    logPath,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner second: %v", err)
	}
	second, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if webhookAttempts != 2 {
		t.Fatalf("webhookAttempts = %d, want 2", webhookAttempts)
	}
	if second.Sinks["webhook\x00https://hooks.internal/qualified"].Pending != 0 {
		t.Fatalf("second sink state = %+v", second.Sinks)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	logText := string(data)
	if !strings.Contains(logText, `"event":"queued"`) || !strings.Contains(logText, `"event":"failed"`) || !strings.Contains(logText, `"event":"delivered"`) {
		t.Fatalf("delivery log = %s", logText)
	}
}
