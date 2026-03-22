package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/expert"
)

func (r *Runner) enqueueWorkflowDeliveries(result Result) (int, error) {
	enqueued := 0
	now := r.now().UTC()
	for arbiterName, run := range result.Arbiters {
		count, err := r.enqueueArbiterDeliveries(arbiterName, run.Delta.Outcomes, now)
		if err != nil {
			return enqueued, err
		}
		enqueued += count
	}
	return enqueued, nil
}

func (r *Runner) deliverReady(ctx context.Context) (delivered int, retried int, err error) {
	now := r.now().UTC()
	for _, id := range r.pendingDeliveryIDs() {
		delivery := r.pending[id]
		if !deliveryReady(now, delivery) {
			continue
		}
		if err := r.processDeliveryAttempt(ctx, id, now); err != nil {
			return delivered, retried, err
		}
		if _, ok := r.pending[id]; ok {
			retried++
			continue
		}
		delivered++
	}
	return delivered, retried, nil
}

func (r *Runner) enqueueArbiterDeliveries(arbiterName string, outcomes []expert.Outcome, now time.Time) (int, error) {
	enqueued := 0
	for _, handler := range r.dispatchers[arbiterName] {
		count, err := r.enqueueHandlerDeliveries(arbiterName, handler, outcomes, now)
		if err != nil {
			return enqueued, err
		}
		enqueued += count
	}
	return enqueued, nil
}

func (r *Runner) enqueueHandlerDeliveries(arbiterName string, handler compiledDispatchHandler, outcomes []expert.Outcome, now time.Time) (int, error) {
	enqueued := 0
	for _, outcome := range outcomes {
		matched, err := handlerMatchesOutcome(arbiterName, handler, outcome)
		if err != nil {
			return enqueued, err
		}
		if !matched {
			continue
		}
		if err := r.queueDelivery(newDelivery(now, arbiterName, handler, outcome)); err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

func handlerMatchesOutcome(arbiterName string, handler compiledDispatchHandler, outcome expert.Outcome) (bool, error) {
	if handler.spec.Outcome != "*" && handler.spec.Outcome != outcome.Name {
		return false, nil
	}
	ok, err := handler.filter.Match(outcome)
	if err != nil {
		return false, fmt.Errorf("workflow handler %s %s: %w", arbiterName, handler.spec.Kind, err)
	}
	return ok, nil
}

func newDelivery(now time.Time, arbiterName string, handler compiledDispatchHandler, outcome expert.Outcome) Delivery {
	return Delivery{
		Arbiter:    arbiterName,
		Handler:    handler.spec,
		HandlerKey: handler.handlerKey,
		Outcome: expert.Outcome{
			Rule:   outcome.Rule,
			Name:   outcome.Name,
			Params: cloneMap(outcome.Params),
		},
		EnqueuedAt: now,
	}
}

func (r *Runner) queueDelivery(delivery Delivery) error {
	delivery.ID = r.nextDeliveryID(delivery.EnqueuedAt)
	r.pending[delivery.ID] = delivery
	return r.appendJournal("queued", delivery)
}

func (r *Runner) pendingDeliveryIDs() []string {
	ids := make([]string, 0, len(r.pending))
	for id := range r.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func deliveryReady(now time.Time, delivery Delivery) bool {
	return delivery.NextAttemptAt.IsZero() || !now.Before(delivery.NextAttemptAt)
}

func (r *Runner) processDeliveryAttempt(ctx context.Context, id string, now time.Time) error {
	delivery := r.pending[id]
	state := r.beginDeliveryAttempt(id, now)
	delivery = r.pending[id]
	if err := r.dispatch(ctx, delivery); err != nil {
		return r.recordDeliveryFailure(id, delivery, state, now, err)
	}
	return r.recordDeliverySuccess(id, delivery, state, now)
}

func (r *Runner) beginDeliveryAttempt(id string, now time.Time) *sinkState {
	delivery := r.pending[id]
	state := r.sinks[delivery.HandlerKey]
	if state != nil {
		state.LastAttemptAt = now
	}
	delivery.LastAttemptAt = now
	r.pending[id] = delivery
	return state
}

func (r *Runner) recordDeliveryFailure(id string, delivery Delivery, state *sinkState, now time.Time, err error) error {
	delivery.Attempt++
	delivery.LastError = err.Error()
	delivery.NextAttemptAt = now.Add(deliveryBackoff(delivery.Attempt, r.initialBackoff, r.maxBackoff))
	r.pending[id] = delivery
	if state != nil {
		state.Available = false
		state.LastError = delivery.LastError
		state.ConsecutiveFailures++
		state.NextRetryAt = delivery.NextAttemptAt
	}
	return r.appendJournal("failed", delivery)
}

func (r *Runner) recordDeliverySuccess(id string, delivery Delivery, state *sinkState, now time.Time) error {
	delete(r.pending, id)
	if state != nil {
		state.Available = true
		state.LastError = ""
		state.ConsecutiveFailures = 0
		state.LastSuccessAt = now
		state.NextRetryAt = time.Time{}
	}
	return r.appendJournal("delivered", delivery)
}

func (r *Runner) dispatch(ctx context.Context, delivery Delivery) error {
	switch delivery.Handler.Kind {
	case arbiter.ArbiterHandlerAudit:
		return r.deliverAudit(ctx, delivery)
	case arbiter.ArbiterHandlerStdout:
		return r.deliverStdout(ctx, delivery)
	default:
		handler := r.handlers[delivery.Handler.Kind]
		if handler == nil {
			return fmt.Errorf("no runtime handler for %s", delivery.Handler.Kind)
		}
		return handler.Deliver(ctx, delivery)
	}
}

func (r *Runner) deliverAudit(ctx context.Context, delivery Delivery) error {
	sink, err := r.auditSink(delivery.Handler.Target)
	if err != nil {
		return err
	}
	return sink.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: delivery.EnqueuedAt,
		Kind:      "arbiter_outcome",
		Context: map[string]any{
			"arbiter": delivery.Arbiter,
			"target":  delivery.Handler.Target,
			"handler": string(delivery.Handler.Kind),
		},
		Expert: &audit.ExpertDecision{
			Outcomes: []audit.ExpertOutcome{{
				Rule:   delivery.Outcome.Rule,
				Name:   delivery.Outcome.Name,
				Params: cloneMap(delivery.Outcome.Params),
			}},
		},
	})
}

func (r *Runner) deliverStdout(_ context.Context, delivery Delivery) error {
	enc := json.NewEncoder(r.stdout)
	return enc.Encode(delivery)
}

func (r *Runner) auditSink(target string) (*audit.JSONLSink, error) {
	if target == "" {
		return nil, fmt.Errorf("audit handler target is required")
	}
	if sink := r.auditSinks[target]; sink != nil {
		return sink, nil
	}
	sink, err := audit.NewJSONLSink(target)
	if err != nil {
		return nil, err
	}
	r.auditSinks[target] = sink
	return sink, nil
}

func (r *Runner) restorePending() error {
	if strings.TrimSpace(r.deliveryLog) == "" {
		return nil
	}
	file, err := os.Open(r.deliveryLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workflow delivery log open: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry deliveryJournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return fmt.Errorf("workflow delivery log decode: %w", err)
		}
		switch entry.Event {
		case "queued", "failed":
			r.pending[entry.Delivery.ID] = entry.Delivery
		case "delivered":
			delete(r.pending, entry.Delivery.ID)
		}
		if entry.Delivery.ID != "" {
			r.nextID++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("workflow delivery log read: %w", err)
	}
	return nil
}

func (r *Runner) appendJournal(event string, delivery Delivery) error {
	if strings.TrimSpace(r.deliveryLog) == "" {
		return nil
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(r.deliveryLog), 0o755); err != nil {
		return fmt.Errorf("workflow delivery log mkdir: %w", err)
	}
	file, err := os.OpenFile(r.deliveryLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("workflow delivery log open: %w", err)
	}
	defer file.Close()

	entry := deliveryJournalEntry{
		Event:    event,
		At:       r.now().UTC(),
		Delivery: delivery,
	}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		return fmt.Errorf("workflow delivery log write: %w", err)
	}
	return nil
}

func (r *Runner) nextDeliveryID(now time.Time) string {
	r.nextID++
	return fmt.Sprintf("%d-%06d", now.UTC().UnixNano(), r.nextID)
}

func (r *Runner) refreshSinkPendingCounts() {
	for _, state := range r.sinks {
		state.Pending = 0
	}
	for _, delivery := range r.pending {
		if state := r.sinks[delivery.HandlerKey]; state != nil {
			state.Pending++
		}
	}
}

func sinkHandlerKey(spec arbiter.ArbiterHandler) string {
	if spec.Kind == arbiter.ArbiterHandlerStdout {
		return string(spec.Kind)
	}
	return string(spec.Kind) + "\x00" + spec.Target
}

func deliveryBackoff(attempt int, initial, max time.Duration) time.Duration {
	if attempt <= 0 {
		return initial
	}
	backoff := initial
	for i := 1; i < attempt; i++ {
		backoff = nextBackoff(backoff, max)
	}
	return backoff
}

func nextBackoff(current, max time.Duration) time.Duration {
	if current <= 0 {
		current = time.Second
	}
	next := current * 2
	if next > max {
		return max
	}
	return next
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
