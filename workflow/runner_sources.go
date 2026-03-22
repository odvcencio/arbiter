package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/expert/factsource"
)

func (r *Runner) syncSources(ctx context.Context) error {
	targets := r.workflow.ExternalSources()
	slices.Sort(targets)
	for _, target := range targets {
		if err := r.syncSourceTarget(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) syncSourceTarget(ctx context.Context, target string) error {
	state := r.sources[target]
	if state == nil {
		return nil
	}

	now := r.now().UTC()
	if sourceRetryPending(state, now) {
		return r.restoreSourceFacts(target, state)
	}

	facts, err := r.loadSourceWithRetry(ctx, state)
	if err != nil {
		return r.restoreSourceFacts(target, state)
	}

	r.markSourceLoadSuccess(state, facts, now)
	return r.workflow.SetSourceFacts(target, facts)
}

func sourceRetryPending(state *sourceState, now time.Time) bool {
	return !state.NextRetryAt.IsZero() && now.Before(state.NextRetryAt)
}

func (r *Runner) restoreSourceFacts(target string, state *sourceState) error {
	if len(state.lastFacts) == 0 {
		return r.workflow.SetSourceFacts(target, nil)
	}
	return r.workflow.SetSourceFacts(target, state.lastFacts)
}

func (r *Runner) markSourceLoadSuccess(state *sourceState, facts []expert.Fact, now time.Time) {
	state.Available = true
	state.LastError = ""
	state.ConsecutiveFailures = 0
	state.NextRetryAt = time.Time{}
	state.LastSuccessAt = now
	state.FactCount = len(facts)
	state.lastFacts = cloneExpertFacts(facts)
}

func (r *Runner) loadSourceWithRetry(ctx context.Context, state *sourceState) ([]expert.Fact, error) {
	if state == nil {
		return nil, fmt.Errorf("nil source state")
	}

	backoff := r.initialBackoff
	var lastErr error
	for attempt := 1; attempt <= r.sourceAttempts; attempt++ {
		now := r.now().UTC()
		state.LastAttemptAt = now
		facts, err := r.loader(ctx, state.Target)
		if err == nil {
			return facts, nil
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if attempt == r.sourceAttempts {
			break
		}
		if err := sleepContext(ctx, backoff); err != nil {
			return nil, err
		}
		backoff = nextBackoff(backoff, r.maxBackoff)
	}

	state.Available = false
	state.LastError = lastErr.Error()
	state.ConsecutiveFailures++
	state.NextRetryAt = r.now().UTC().Add(backoff)
	state.FactCount = len(state.lastFacts)
	return nil, lastErr
}

func (r *Runner) syncArbiterEnvelopes() {
	for _, name := range r.workflow.order {
		arb := r.workflow.arbiters[name]
		if arb == nil || arb.session == nil {
			continue
		}
		envelope := cloneMap(arb.baseEnvelope)
		if envelope == nil {
			envelope = make(map[string]any)
		}
		if src := r.sourceEnvelope(arb); len(src) > 0 {
			envelope["source"] = src
		}
		if sink := r.sinkEnvelope(arb); len(sink) > 0 {
			envelope["sink"] = sink
		}
		arb.session.SetEnvelope(envelope)
	}
}

func (r *Runner) sourceEnvelope(arb *runtimeArbiter) map[string]any {
	if arb == nil {
		return nil
	}
	now := r.now().UTC()
	out := make(map[string]any)
	for _, source := range arb.decl.Sources {
		if strings.HasPrefix(source.Target, "chain://") {
			continue
		}
		state := r.sources[source.Target]
		if state == nil {
			continue
		}
		out[state.Alias] = sourceEnvelopeMeta(state, now)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *Runner) sinkEnvelope(arb *runtimeArbiter) map[string]any {
	if arb == nil {
		return nil
	}
	out := make(map[string]any)
	for _, handler := range arb.decl.Handlers {
		if handler.Kind == arbiter.ArbiterHandlerChain {
			continue
		}
		state := r.sinks[sinkHandlerKey(handler)]
		if state == nil {
			continue
		}
		out[state.Alias] = sinkEnvelopeMeta(state)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sourceEnvelopeMeta(state *sourceState, now time.Time) map[string]any {
	meta := map[string]any{
		"target":               state.Target,
		"alias":                state.Alias,
		"available":            state.Available,
		"fact_count":           float64(state.FactCount),
		"consecutive_failures": float64(state.ConsecutiveFailures),
	}
	if state.LastError != "" {
		meta["last_error"] = state.LastError
	}
	if !state.LastAttemptAt.IsZero() {
		meta["last_attempt_at"] = float64(state.LastAttemptAt.Unix())
	}
	if !state.LastSuccessAt.IsZero() {
		meta["last_success_at"] = float64(state.LastSuccessAt.Unix())
	}
	if !state.NextRetryAt.IsZero() {
		meta["next_retry_at"] = float64(state.NextRetryAt.Unix())
	}
	meta["__source_age_seconds"] = sourceAgeSeconds(state, now)
	return meta
}

func sourceAgeSeconds(state *sourceState, now time.Time) float64 {
	if state.LastSuccessAt.IsZero() || now.Before(state.LastSuccessAt) {
		return 0
	}
	return float64(int64(now.Sub(state.LastSuccessAt).Seconds()))
}

func sinkEnvelopeMeta(state *sinkState) map[string]any {
	meta := map[string]any{
		"kind":                 state.Kind,
		"target":               state.Target,
		"available":            state.Available,
		"pending":              float64(state.Pending),
		"consecutive_failures": float64(state.ConsecutiveFailures),
	}
	if state.LastError != "" {
		meta["last_error"] = state.LastError
	}
	if !state.LastAttemptAt.IsZero() {
		meta["last_attempt_at"] = float64(state.LastAttemptAt.Unix())
	}
	if !state.LastSuccessAt.IsZero() {
		meta["last_success_at"] = float64(state.LastSuccessAt.Unix())
	}
	if !state.NextRetryAt.IsZero() {
		meta["next_retry_at"] = float64(state.NextRetryAt.Unix())
	}
	return meta
}

func toExpertFacts(facts []factsource.Fact) []expert.Fact {
	out := make([]expert.Fact, 0, len(facts))
	for _, fact := range facts {
		out = append(out, expert.Fact{
			Type:   fact.Type,
			Key:    fact.Key,
			Fields: cloneMap(fact.Fields),
		})
	}
	return out
}

func runtimeAlias(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "runtime"
	}
	if parsed := strings.TrimPrefix(raw, "chain://"); parsed != raw {
		raw = parsed
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	alias := strings.Trim(b.String(), "_")
	if alias == "" {
		return "runtime"
	}
	if alias[0] >= '0' && alias[0] <= '9' {
		return "runtime_" + alias
	}
	return alias
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
