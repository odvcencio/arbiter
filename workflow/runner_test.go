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

func TestRunnerFiltersWebhookDeliveriesByOutcomeFields(t *testing.T) {
	src := []byte(`
arbiter alerts {
	poll 1s
	source https://feed.internal/alerts
	on Alert where severity == "critical" webhook https://hooks.internal/critical
}

expert rule EmitAlerts priority 10 per_fact {
	when {
		any candidate in facts.InputAlert { true }
	}
	then emit Alert {
		key: candidate.key,
		severity: candidate.severity,
		channel: candidate.channel,
	}
}
`)

	w, err := Compile(src, Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var deliveries []Delivery
	runner, err := NewRunner(w, RunnerOptions{
		Loader: func(_ context.Context, target string) ([]expert.Fact, error) {
			if target != "https://feed.internal/alerts" {
				t.Fatalf("unexpected target %q", target)
			}
			return []expert.Fact{
				{
					Type: "InputAlert",
					Key:  "alert-1",
					Fields: map[string]any{
						"severity": "critical",
						"channel":  "incidents",
					},
				},
				{
					Type: "InputAlert",
					Key:  "alert-2",
					Fields: map[string]any{
						"severity": "warning",
						"channel":  "warnings",
					},
				},
			}, nil
		},
		Handlers: map[arbiter.ArbiterHandlerKind]OutcomeHandler{
			arbiter.ArbiterHandlerWebhook: OutcomeHandlerFunc(func(_ context.Context, delivery Delivery) error {
				deliveries = append(deliveries, delivery)
				return nil
			}),
		},
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	tick, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tick.Enqueued != 1 || tick.Delivered != 1 {
		t.Fatalf("tick delivery counts = %+v, want enqueued=1 delivered=1", tick)
	}
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %+v, want 1 critical delivery", deliveries)
	}
	if got := deliveries[0].Outcome.Name; got != "Alert" {
		t.Fatalf("delivery outcome = %q, want Alert", got)
	}
	if got := deliveries[0].Outcome.Params["severity"]; got != "critical" {
		t.Fatalf("delivery severity = %#v, want critical", got)
	}
	if got := deliveries[0].Outcome.Params["channel"]; got != "incidents" {
		t.Fatalf("delivery channel = %#v, want incidents", got)
	}
	sink := tick.Sinks["webhook\x00https://hooks.internal/critical"]
	if sink.Pending != 0 || !sink.Available {
		t.Fatalf("sink state = %+v, want available with no backlog", sink)
	}
}

func TestRunnerExposesSinkFailuresToRulesOnNextTick(t *testing.T) {
	src := []byte(`
arbiter sales {
	poll 1s
	source https://feed.internal/facts
	on Qualified webhook https://hooks.internal/qualified
}

expert rule EmitQualified priority 10 per_fact {
	when {
		any lead in facts.Lead { lead.score >= 90 }
	}
	then emit Qualified {
		key: lead.key,
	}
}

expert rule ObserveSinkFailure priority 20 {
	when {
		sink.hooks_internal_qualified.available == false
		and sink.hooks_internal_qualified.pending >= 1
	}
	then emit SinkUnavailable {
		pending: sink.hooks_internal_qualified.pending,
		failures: sink.hooks_internal_qualified.consecutive_failures,
	}
}
`)

	w, err := Compile(src, Options{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	attempts := 0
	now := time.Unix(3_000, 0).UTC()
	runner, err := NewRunner(w, RunnerOptions{
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
			arbiter.ArbiterHandlerWebhook: OutcomeHandlerFunc(func(_ context.Context, delivery Delivery) error {
				attempts++
				if delivery.Outcome.Name != "Qualified" {
					t.Fatalf("unexpected delivery outcome %q", delivery.Outcome.Name)
				}
				if attempts == 1 {
					return errors.New("temporary sink failure")
				}
				return nil
			}),
		},
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	first, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if first.Retried != 1 {
		t.Fatalf("first retried = %d, want 1", first.Retried)
	}
	firstSink := first.Sinks["webhook\x00https://hooks.internal/qualified"]
	if firstSink.Pending != 1 || firstSink.Available {
		t.Fatalf("first sink state = %+v, want pending failed delivery", firstSink)
	}

	now = now.Add(10 * time.Millisecond)
	second, err := runner.Tick(context.Background())
	if err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("webhook attempts = %d, want 2", attempts)
	}
	if second.Delivered != 1 {
		t.Fatalf("second delivered = %d, want 1", second.Delivered)
	}
	var sawSinkUnavailable bool
	for _, outcome := range second.Workflow.Arbiters["sales"].Delta.Outcomes {
		if outcome.Name != "SinkUnavailable" {
			continue
		}
		sawSinkUnavailable = true
		if got := outcome.Params["pending"]; got != float64(1) {
			t.Fatalf("pending = %#v, want 1", got)
		}
		if got := outcome.Params["failures"]; got != float64(1) {
			t.Fatalf("failures = %#v, want 1", got)
		}
	}
	if !sawSinkUnavailable {
		t.Fatalf("second outcomes = %+v, want SinkUnavailable", second.Workflow.Arbiters["sales"].Delta.Outcomes)
	}
	secondSink := second.Sinks["webhook\x00https://hooks.internal/qualified"]
	if secondSink.Pending != 0 || !secondSink.Available {
		t.Fatalf("second sink state = %+v, want recovered sink", secondSink)
	}
}
