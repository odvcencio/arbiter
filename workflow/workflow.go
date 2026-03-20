package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/expert"
)

// OutcomeFactMapper maps an emitted outcome into a chained fact for a target arbiter.
type OutcomeFactMapper func(source arbiter.ArbiterDeclaration, target arbiter.ArbiterDeclaration, outcome expert.Outcome) (expert.Fact, bool, error)

// Options configures workflow compilation and per-arbiter expert sessions.
type Options struct {
	Envelope          func(arbiter.ArbiterDeclaration) map[string]any
	SessionOptions    func(arbiter.ArbiterDeclaration) expert.Options
	OutcomeFactMapper OutcomeFactMapper
}

// Result is one workflow execution pass across all arbiters.
type Result struct {
	Order    []string
	Arbiters map[string]ArbiterRun
}

// ArbiterRun is one arbiter's incremental execution result in a workflow pass.
type ArbiterRun struct {
	Sync  expert.SyncSummary
	Delta expert.Result
}

// Workflow executes a set of chained arbiter declarations over persistent expert sessions.
type Workflow struct {
	order      []string
	arbiters   map[string]*runtimeArbiter
	external   map[string][]expert.Fact
	mapOutcome OutcomeFactMapper
}

type runtimeArbiter struct {
	decl         arbiter.ArbiterDeclaration
	session      *expert.Session
	checkpoint   expert.Checkpoint
	chainSources map[string]struct{}
	handlers     []compiledChainHandler
}

type compiledChainHandler struct {
	target string
	filter *compiledOutcomeFilter
	spec   arbiter.ArbiterHandler
}

type compiledOutcomeFilter struct {
	ruleset *compiler.CompiledRuleset
}

// Compile parses source once, compiles arbiters plus expert rules, and prepares
// a persistent chained workflow runtime.
func Compile(source []byte, opts Options) (*Workflow, error) {
	parsed, err := arbiter.ParseSource(source)
	if err != nil {
		return nil, err
	}
	full, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, err
	}
	program, err := expert.CompileParsed(parsed, full)
	if err != nil {
		return nil, err
	}
	return buildWorkflow(full, program, opts)
}

// CompileFile resolves includes, compiles the workflow once, and remaps diagnostics.
func CompileFile(path string, opts Options) (*Workflow, error) {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return nil, err
	}
	full, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	program, err := expert.CompileParsed(parsed, full)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	w, err := buildWorkflow(full, program, opts)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	return w, nil
}

// ExternalSources returns the declared non-chain source targets used by the workflow.
func (w *Workflow) ExternalSources() []string {
	if w == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, name := range w.order {
		for _, source := range w.arbiters[name].decl.Sources {
			if strings.HasPrefix(source.Target, "chain://") {
				continue
			}
			if _, ok := seen[source.Target]; ok {
				continue
			}
			seen[source.Target] = struct{}{}
			out = append(out, source.Target)
		}
	}
	slices.Sort(out)
	return out
}

// SetSourceFacts updates one external source snapshot. Chain sources are runtime-owned.
func (w *Workflow) SetSourceFacts(target string, facts []expert.Fact) error {
	if w == nil {
		return fmt.Errorf("nil workflow")
	}
	if strings.HasPrefix(target, "chain://") {
		return fmt.Errorf("workflow source %q is runtime-owned", target)
	}
	w.external[target] = cloneExpertFacts(facts)
	return nil
}

// Run executes one topologically ordered workflow pass and forwards chained outcome deltas.
func (w *Workflow) Run(ctx context.Context) (Result, error) {
	if w == nil {
		return Result{}, fmt.Errorf("nil workflow")
	}

	result := Result{
		Order:    append([]string(nil), w.order...),
		Arbiters: make(map[string]ArbiterRun, len(w.order)),
	}
	chainInputs := make(map[string]map[string][]expert.Fact)

	for _, name := range w.order {
		arb := w.arbiters[name]
		input, err := w.buildInputFacts(arb, chainInputs[name])
		if err != nil {
			return Result{}, err
		}
		syncSummary, err := arb.session.SyncFacts(input)
		if err != nil {
			return Result{}, fmt.Errorf("workflow arbiter %s sync: %w", name, err)
		}
		mark := arb.checkpoint
		if _, err := arb.session.Run(ctx); err != nil {
			return Result{}, fmt.Errorf("workflow arbiter %s run: %w", name, err)
		}
		delta := arb.session.DeltaSince(mark)
		arb.checkpoint = arb.session.Checkpoint()
		result.Arbiters[name] = ArbiterRun{
			Sync:  syncSummary,
			Delta: delta,
		}

		for _, handler := range arb.handlers {
			targetFacts, err := buildChainedFacts(arb.decl, w.arbiters[handler.target].decl, handler, delta.Outcomes, w.mapOutcome)
			if err != nil {
				return Result{}, err
			}
			if len(targetFacts) == 0 {
				continue
			}
			if chainInputs[handler.target] == nil {
				chainInputs[handler.target] = make(map[string][]expert.Fact)
			}
			chainInputs[handler.target][name] = append(chainInputs[handler.target][name], targetFacts...)
		}
	}

	return result, nil
}

func buildWorkflow(full *arbiter.CompileResult, program *expert.Program, opts Options) (*Workflow, error) {
	if full == nil {
		return nil, fmt.Errorf("nil compile result")
	}
	if program == nil {
		return nil, fmt.Errorf("nil expert program")
	}

	arbiters := instantiateArbiters(full.Arbiters, program, opts)
	if err := registerChainSources(arbiters); err != nil {
		return nil, err
	}
	graph, err := compileChainHandlers(arbiters)
	if err != nil {
		return nil, err
	}

	order, err := topoSortWorkflow(graph)
	if err != nil {
		return nil, err
	}

	mapper := opts.OutcomeFactMapper
	if mapper == nil {
		mapper = defaultOutcomeFactMapper
	}

	return &Workflow{
		order:      order,
		arbiters:   arbiters,
		external:   make(map[string][]expert.Fact),
		mapOutcome: mapper,
	}, nil
}

func instantiateArbiters(decls []arbiter.ArbiterDeclaration, program *expert.Program, opts Options) map[string]*runtimeArbiter {
	arbiters := make(map[string]*runtimeArbiter, len(decls))
	for _, decl := range decls {
		sessionOpts := expert.Options{}
		if opts.SessionOptions != nil {
			sessionOpts = opts.SessionOptions(decl)
		}
		var envelope map[string]any
		if opts.Envelope != nil {
			envelope = opts.Envelope(decl)
		}
		arbiters[decl.Name] = &runtimeArbiter{
			decl:         decl,
			session:      expert.NewSession(program, cloneMap(envelope), nil, sessionOpts),
			chainSources: make(map[string]struct{}),
		}
	}
	return arbiters
}

func registerChainSources(arbiters map[string]*runtimeArbiter) error {
	for _, arb := range arbiters {
		for _, source := range arb.decl.Sources {
			if !strings.HasPrefix(source.Target, "chain://") {
				continue
			}
			upstream := strings.TrimPrefix(source.Target, "chain://")
			if _, ok := arbiters[upstream]; !ok {
				return fmt.Errorf("workflow arbiter %s: unknown chain source %q", arb.decl.Name, source.Target)
			}
			arb.chainSources[upstream] = struct{}{}
		}
	}
	return nil
}

func compileChainHandlers(arbiters map[string]*runtimeArbiter) (map[string]map[string]struct{}, error) {
	graph := make(map[string]map[string]struct{}, len(arbiters))
	for name := range arbiters {
		graph[name] = make(map[string]struct{})
	}
	for _, arb := range arbiters {
		for _, handler := range arb.decl.Handlers {
			if handler.Kind != arbiter.ArbiterHandlerChain {
				continue
			}
			target, ok := arbiters[handler.Target]
			if !ok {
				return nil, fmt.Errorf("workflow arbiter %s: unknown chain target %q", arb.decl.Name, handler.Target)
			}
			if _, ok := target.chainSources[arb.decl.Name]; !ok {
				return nil, fmt.Errorf("workflow arbiter %s -> %s: target must declare source chain://%s", arb.decl.Name, target.decl.Name, arb.decl.Name)
			}
			filter, err := compileOutcomeFilter(handler.Where)
			if err != nil {
				return nil, fmt.Errorf("workflow arbiter %s handler %s: %w", arb.decl.Name, handler.Outcome, err)
			}
			arb.handlers = append(arb.handlers, compiledChainHandler{
				target: target.decl.Name,
				filter: filter,
				spec:   handler,
			})
			graph[arb.decl.Name][target.decl.Name] = struct{}{}
		}
	}
	return graph, nil
}

func compileOutcomeFilter(expr string) (*compiledOutcomeFilter, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	source := fmt.Sprintf("rule __workflow_filter { when { %s } then Match {} }", expr)
	ruleset, err := arbiter.Compile([]byte(source))
	if err != nil {
		return nil, err
	}
	return &compiledOutcomeFilter{ruleset: ruleset}, nil
}

func (f *compiledOutcomeFilter) Match(outcome expert.Outcome) (bool, error) {
	if f == nil {
		return true, nil
	}
	ctx := cloneMap(outcome.Params)
	if ctx == nil {
		ctx = make(map[string]any, 2)
	}
	ctx["name"] = outcome.Name
	ctx["rule"] = outcome.Rule
	dc := arbiter.DataFromMap(ctx, f.ruleset)
	matched, err := arbiter.Eval(f.ruleset, dc)
	if err != nil {
		return false, err
	}
	return len(matched) > 0, nil
}

func buildChainedFacts(source arbiter.ArbiterDeclaration, target arbiter.ArbiterDeclaration, handler compiledChainHandler, outcomes []expert.Outcome, mapper OutcomeFactMapper) ([]expert.Fact, error) {
	out := make([]expert.Fact, 0)
	for _, outcome := range outcomes {
		if handler.spec.Outcome != "*" && handler.spec.Outcome != outcome.Name {
			continue
		}
		ok, err := handler.filter.Match(outcome)
		if err != nil {
			return nil, fmt.Errorf("workflow chain %s -> %s outcome %s: %w", source.Name, target.Name, outcome.Name, err)
		}
		if !ok {
			continue
		}
		fact, include, err := mapper(source, target, outcome)
		if err != nil {
			return nil, fmt.Errorf("workflow chain %s -> %s outcome %s: %w", source.Name, target.Name, outcome.Name, err)
		}
		if include {
			out = append(out, fact)
		}
	}
	return out, nil
}

func (w *Workflow) buildInputFacts(arb *runtimeArbiter, chainFacts map[string][]expert.Fact) ([]expert.Fact, error) {
	if arb == nil {
		return nil, fmt.Errorf("nil arbiter")
	}
	out := make([]expert.Fact, 0)
	seen := make(map[string]string)
	for _, source := range arb.decl.Sources {
		var facts []expert.Fact
		switch {
		case strings.HasPrefix(source.Target, "chain://"):
			upstream := strings.TrimPrefix(source.Target, "chain://")
			facts = chainFacts[upstream]
		default:
			facts = w.external[source.Target]
		}
		for _, fact := range facts {
			id := fact.Type + "\x00" + fact.Key
			if prev, ok := seen[id]; ok {
				return nil, fmt.Errorf("workflow arbiter %s: duplicate input fact %s/%s from %s and %s", arb.decl.Name, fact.Type, fact.Key, prev, source.Target)
			}
			seen[id] = source.Target
			out = append(out, cloneExpertFact(fact))
		}
	}
	return out, nil
}

func topoSortWorkflow(graph map[string]map[string]struct{}) ([]string, error) {
	indegree := make(map[string]int, len(graph))
	for node := range graph {
		indegree[node] = 0
	}
	for _, edges := range graph {
		for target := range edges {
			indegree[target]++
		}
	}

	queue := make([]string, 0, len(graph))
	for node, deg := range indegree {
		if deg == 0 {
			queue = append(queue, node)
		}
	}
	slices.Sort(queue)

	order := make([]string, 0, len(graph))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)
		neighbors := make([]string, 0, len(graph[node]))
		for next := range graph[node] {
			neighbors = append(neighbors, next)
		}
		slices.Sort(neighbors)
		for _, next := range neighbors {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
				slices.Sort(queue)
			}
		}
	}
	if len(order) != len(graph) {
		return nil, fmt.Errorf("workflow chain graph contains a cycle")
	}
	return order, nil
}

func defaultOutcomeFactMapper(source arbiter.ArbiterDeclaration, _ arbiter.ArbiterDeclaration, outcome expert.Outcome) (expert.Fact, bool, error) {
	fields := cloneMap(outcome.Params)
	if fields == nil {
		fields = make(map[string]any, 2)
	}
	fields["source_arbiter"] = source.Name
	fields["source_rule"] = outcome.Rule
	return expert.Fact{
		Type:   outcome.Name,
		Key:    outcomeFactKey(source.Name, outcome),
		Fields: fields,
	}, true, nil
}

func outcomeFactKey(source string, outcome expert.Outcome) string {
	if key := scalarString(outcome.Params["key"]); key != "" {
		return key
	}
	if id := scalarString(outcome.Params["id"]); id != "" {
		return id
	}
	payload, _ := json.Marshal(outcome.Params)
	sum := sha256.Sum256(append(append([]byte(source+":"+outcome.Name+":"), payload...), []byte(outcome.Rule)...))
	return hex.EncodeToString(sum[:12])
}

func scalarString(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func cloneExpertFacts(facts []expert.Fact) []expert.Fact {
	if len(facts) == 0 {
		return nil
	}
	out := make([]expert.Fact, len(facts))
	for i, fact := range facts {
		out[i] = cloneExpertFact(fact)
	}
	return out
}

func cloneExpertFact(fact expert.Fact) expert.Fact {
	return expert.Fact{
		Type:          fact.Type,
		Key:           fact.Key,
		Fields:        cloneMap(fact.Fields),
		DerivedBy:     append([]string(nil), fact.DerivedBy...),
		AssertedRound: fact.AssertedRound,
		AssertedAt:    fact.AssertedAt,
	}
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
