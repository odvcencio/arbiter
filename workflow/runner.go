package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

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
