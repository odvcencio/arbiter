package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/expert/factsource"
)

// SourceLoader loads one external source snapshot.
type SourceLoader func(context.Context, string) ([]expert.Fact, error)

// OutcomeHandler delivers one pending sink delivery.
type OutcomeHandler interface {
	Deliver(context.Context, Delivery) error
}

// OutcomeHandlerFunc adapts a function to OutcomeHandler.
type OutcomeHandlerFunc func(context.Context, Delivery) error

// Deliver implements OutcomeHandler.
func (f OutcomeHandlerFunc) Deliver(ctx context.Context, delivery Delivery) error {
	return f(ctx, delivery)
}

// RunnerOptions configure one reliable workflow runtime.
type RunnerOptions struct {
	Loader         SourceLoader
	Handlers       map[arbiter.ArbiterHandlerKind]OutcomeHandler
	Now            func() time.Time
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	SourceAttempts int
	DeliveryLog    string
	Stdout         io.Writer
}

// SourceSnapshot describes one source's current runtime health.
type SourceSnapshot struct {
	Target              string    `json:"target"`
	Alias               string    `json:"alias"`
	Available           bool      `json:"available"`
	FactCount           int       `json:"fact_count,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	LastAttemptAt       time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	NextRetryAt         time.Time `json:"next_retry_at,omitempty"`
}

// SinkSnapshot describes one handler target's current delivery health.
type SinkSnapshot struct {
	Key                 string    `json:"key"`
	Alias               string    `json:"alias"`
	Kind                string    `json:"kind"`
	Target              string    `json:"target,omitempty"`
	Available           bool      `json:"available"`
	Pending             int       `json:"pending,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	LastAttemptAt       time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	NextRetryAt         time.Time `json:"next_retry_at,omitempty"`
}

// Delivery is one durable sink attempt.
type Delivery struct {
	ID            string                 `json:"id"`
	Arbiter       string                 `json:"arbiter"`
	Handler       arbiter.ArbiterHandler `json:"handler"`
	HandlerKey    string                 `json:"handler_key"`
	Outcome       expert.Outcome         `json:"outcome"`
	Attempt       int                    `json:"attempt"`
	EnqueuedAt    time.Time              `json:"enqueued_at"`
	LastAttemptAt time.Time              `json:"last_attempt_at,omitempty"`
	NextAttemptAt time.Time              `json:"next_attempt_at,omitempty"`
	LastError     string                 `json:"last_error,omitempty"`
}

// TickResult is one reliable workflow cycle.
type TickResult struct {
	Workflow  Result                    `json:"workflow"`
	Sources   map[string]SourceSnapshot `json:"sources,omitempty"`
	Sinks     map[string]SinkSnapshot   `json:"sinks,omitempty"`
	Delivered int                       `json:"delivered,omitempty"`
	Retried   int                       `json:"retried,omitempty"`
	Enqueued  int                       `json:"enqueued,omitempty"`
}

type Runner struct {
	workflow       *Workflow
	now            func() time.Time
	loader         SourceLoader
	handlers       map[arbiter.ArbiterHandlerKind]OutcomeHandler
	dispatchers    map[string][]compiledDispatchHandler
	sources        map[string]*sourceState
	sinks          map[string]*sinkState
	pending        map[string]Delivery
	initialBackoff time.Duration
	maxBackoff     time.Duration
	sourceAttempts int
	nextID         uint64
	stdout         io.Writer
	deliveryLog    string
	logMu          sync.Mutex
	auditSinks     map[string]*audit.JSONLSink
}

type compiledDispatchHandler struct {
	spec       arbiter.ArbiterHandler
	filter     *compiledOutcomeFilter
	handlerKey string
}

type sourceState struct {
	SourceSnapshot
	lastFacts []expert.Fact
}

type sinkState struct {
	SinkSnapshot
}

type deliveryJournalEntry struct {
	Event    string    `json:"event"`
	At       time.Time `json:"at"`
	Delivery Delivery  `json:"delivery"`
}

// NewRunner wraps a compiled workflow with reliable source polling and sink delivery.
func NewRunner(w *Workflow, opts RunnerOptions) (*Runner, error) {
	if w == nil {
		return nil, fmt.Errorf("nil workflow")
	}
	if opts.Loader == nil {
		opts.Loader = func(_ context.Context, target string) ([]expert.Fact, error) {
			facts, err := factsource.Load(target)
			if err != nil {
				return nil, err
			}
			return toExpertFacts(facts), nil
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.SourceAttempts <= 0 {
		opts.SourceAttempts = 3
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}

	r := &Runner{
		workflow:       w,
		now:            opts.Now,
		loader:         opts.Loader,
		handlers:       opts.Handlers,
		dispatchers:    make(map[string][]compiledDispatchHandler, len(w.order)),
		sources:        make(map[string]*sourceState),
		sinks:          make(map[string]*sinkState),
		pending:        make(map[string]Delivery),
		initialBackoff: opts.InitialBackoff,
		maxBackoff:     opts.MaxBackoff,
		sourceAttempts: opts.SourceAttempts,
		stdout:         opts.Stdout,
		deliveryLog:    opts.DeliveryLog,
		auditSinks:     make(map[string]*audit.JSONLSink),
	}

	for _, target := range w.ExternalSources() {
		r.sources[target] = &sourceState{
			SourceSnapshot: SourceSnapshot{
				Target: target,
				Alias:  runtimeAlias(target),
			},
		}
	}
	if err := r.compileDispatchers(); err != nil {
		return nil, err
	}
	if err := r.restorePending(); err != nil {
		return nil, err
	}
	r.refreshSinkPendingCounts()
	r.syncArbiterEnvelopes()
	return r, nil
}

// Close releases any cached audit sinks.
func (r *Runner) Close() error {
	if r == nil {
		return nil
	}
	var firstErr error
	for target, sink := range r.auditSinks {
		if err := sink.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close audit sink %s: %w", target, err)
		}
	}
	return firstErr
}

// Tick loads external sources with retry/backoff, keeps last-known-good facts on failure,
// runs the workflow once, and drains any ready sink deliveries.
func (r *Runner) Tick(ctx context.Context) (TickResult, error) {
	if r == nil || r.workflow == nil {
		return TickResult{}, fmt.Errorf("nil runner")
	}
	if err := r.syncSources(ctx); err != nil {
		return TickResult{}, err
	}
	r.syncArbiterEnvelopes()

	workflowResult, err := r.workflow.Run(ctx)
	if err != nil {
		return TickResult{}, err
	}

	enqueued, err := r.enqueueWorkflowDeliveries(workflowResult)
	if err != nil {
		return TickResult{}, err
	}
	delivered, retried, err := r.deliverReady(ctx)
	if err != nil {
		return TickResult{}, err
	}
	r.refreshSinkPendingCounts()

	return TickResult{
		Workflow:  workflowResult,
		Sources:   r.SourceStates(),
		Sinks:     r.SinkStates(),
		Delivered: delivered,
		Retried:   retried,
		Enqueued:  enqueued,
	}, nil
}

// SourceStates returns a snapshot of runtime source health.
func (r *Runner) SourceStates() map[string]SourceSnapshot {
	if r == nil {
		return nil
	}
	out := make(map[string]SourceSnapshot, len(r.sources))
	for target, state := range r.sources {
		out[target] = state.SourceSnapshot
	}
	return out
}

// SinkStates returns a snapshot of runtime sink health.
func (r *Runner) SinkStates() map[string]SinkSnapshot {
	if r == nil {
		return nil
	}
	out := make(map[string]SinkSnapshot, len(r.sinks))
	for key, state := range r.sinks {
		out[key] = state.SinkSnapshot
	}
	return out
}

func (r *Runner) compileDispatchers() error {
	for _, name := range r.workflow.order {
		arb := r.workflow.arbiters[name]
		handlers := make([]compiledDispatchHandler, 0, len(arb.decl.Handlers))
		for _, spec := range arb.decl.Handlers {
			if spec.Kind == arbiter.ArbiterHandlerChain {
				continue
			}
			filter, err := compileOutcomeFilter(spec.Where)
			if err != nil {
				return fmt.Errorf("workflow arbiter %s handler %s: %w", name, spec.Outcome, err)
			}
			if !r.supportsHandler(spec.Kind) {
				return fmt.Errorf("workflow arbiter %s: no runtime handler registered for %s", name, spec.Kind)
			}
			handlerKey := sinkHandlerKey(spec)
			handlers = append(handlers, compiledDispatchHandler{
				spec:       spec,
				filter:     filter,
				handlerKey: handlerKey,
			})
			if _, ok := r.sinks[handlerKey]; !ok {
				r.sinks[handlerKey] = &sinkState{
					SinkSnapshot: SinkSnapshot{
						Key:       handlerKey,
						Alias:     runtimeAlias(firstNonEmpty(spec.Target, string(spec.Kind))),
						Kind:      string(spec.Kind),
						Target:    spec.Target,
						Available: true,
					},
				}
			}
		}
		r.dispatchers[name] = handlers
	}
	return nil
}

func (r *Runner) supportsHandler(kind arbiter.ArbiterHandlerKind) bool {
	switch kind {
	case arbiter.ArbiterHandlerAudit, arbiter.ArbiterHandlerStdout:
		return true
	default:
		_, ok := r.handlers[kind]
		return ok
	}
}

func (r *Runner) syncSources(ctx context.Context) error {
	targets := r.workflow.ExternalSources()
	slices.Sort(targets)
	for _, target := range targets {
		state := r.sources[target]
		if state == nil {
			continue
		}
		now := r.now().UTC()
		if !state.NextRetryAt.IsZero() && now.Before(state.NextRetryAt) {
			if len(state.lastFacts) > 0 {
				if err := r.workflow.SetSourceFacts(target, state.lastFacts); err != nil {
					return err
				}
			}
			continue
		}
		facts, err := r.loadSourceWithRetry(ctx, state)
		if err != nil {
			if len(state.lastFacts) > 0 {
				if setErr := r.workflow.SetSourceFacts(target, state.lastFacts); setErr != nil {
					return setErr
				}
			} else {
				if setErr := r.workflow.SetSourceFacts(target, nil); setErr != nil {
					return setErr
				}
			}
			continue
		}
		state.Available = true
		state.LastError = ""
		state.ConsecutiveFailures = 0
		state.NextRetryAt = time.Time{}
		state.LastSuccessAt = r.now().UTC()
		state.FactCount = len(facts)
		state.lastFacts = cloneExpertFacts(facts)
		if err := r.workflow.SetSourceFacts(target, facts); err != nil {
			return err
		}
	}
	return nil
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
		if !state.LastSuccessAt.IsZero() && !now.Before(state.LastSuccessAt) {
			meta["__source_age_seconds"] = float64(int64(now.Sub(state.LastSuccessAt).Seconds()))
		} else {
			meta["__source_age_seconds"] = float64(0)
		}
		out[state.Alias] = meta
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
		out[state.Alias] = meta
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *Runner) enqueueWorkflowDeliveries(result Result) (int, error) {
	enqueued := 0
	now := r.now().UTC()
	for arbiterName, run := range result.Arbiters {
		for _, handler := range r.dispatchers[arbiterName] {
			for _, outcome := range run.Delta.Outcomes {
				if handler.spec.Outcome != "*" && handler.spec.Outcome != outcome.Name {
					continue
				}
				ok, err := handler.filter.Match(outcome)
				if err != nil {
					return enqueued, fmt.Errorf("workflow handler %s %s: %w", arbiterName, handler.spec.Kind, err)
				}
				if !ok {
					continue
				}
				delivery := Delivery{
					ID:         r.nextDeliveryID(now),
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
				r.pending[delivery.ID] = delivery
				if err := r.appendJournal("queued", delivery); err != nil {
					return enqueued, err
				}
				enqueued++
			}
		}
	}
	return enqueued, nil
}

func (r *Runner) deliverReady(ctx context.Context) (delivered int, retried int, err error) {
	ids := make([]string, 0, len(r.pending))
	now := r.now().UTC()
	for id := range r.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		delivery := r.pending[id]
		if !delivery.NextAttemptAt.IsZero() && now.Before(delivery.NextAttemptAt) {
			continue
		}
		state := r.sinks[delivery.HandlerKey]
		if state != nil {
			state.LastAttemptAt = now
		}
		delivery.LastAttemptAt = now
		if err := r.dispatch(ctx, delivery); err != nil {
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
			if journalErr := r.appendJournal("failed", delivery); journalErr != nil {
				return delivered, retried, journalErr
			}
			retried++
			continue
		}
		delete(r.pending, id)
		if state != nil {
			state.Available = true
			state.LastError = ""
			state.ConsecutiveFailures = 0
			state.LastSuccessAt = now
			state.NextRetryAt = time.Time{}
		}
		if journalErr := r.appendJournal("delivered", delivery); journalErr != nil {
			return delivered, retried, journalErr
		}
		delivered++
	}
	return delivered, retried, nil
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

func sinkHandlerKey(spec arbiter.ArbiterHandler) string {
	if spec.Kind == arbiter.ArbiterHandlerStdout {
		return string(spec.Kind)
	}
	return string(spec.Kind) + "\x00" + spec.Target
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
