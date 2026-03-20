package expert

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/overrides"
	"github.com/odvcencio/arbiter/vm"
)

// Fact is one working-memory fact.
type Fact struct {
	Type          string         `json:"type"`
	Key           string         `json:"key"`
	Fields        map[string]any `json:"fields,omitempty"`
	DerivedBy     []string       `json:"derived_by,omitempty"`
	AssertedRound int            `json:"asserted_round,omitempty"`
}

// Outcome is one emitted expert outcome.
type Outcome struct {
	Rule   string         `json:"rule"`
	Name   string         `json:"name"`
	Params map[string]any `json:"params,omitempty"`
}

// Activation records one expert-rule firing attempt.
type Activation struct {
	Round   int            `json:"round"`
	Rule    string         `json:"rule"`
	Kind    ActionKind     `json:"kind"`
	Target  string         `json:"target"`
	Params  map[string]any `json:"params,omitempty"`
	Changed bool           `json:"changed"`
	Detail  string         `json:"detail,omitempty"`
}

// StopReason describes why a session stopped.
type StopReason string

const (
	StopQuiescent        StopReason = "quiescent"
	StopMaxRounds        StopReason = "max_rounds"
	StopMaxMutations     StopReason = "max_mutations"
	StopContextCancelled StopReason = "context_cancelled"
)

// Options configures expert-session execution.
type Options struct {
	MaxRounds    int
	MaxMutations int
	BundleID     string
	Overrides    overrides.View
}

// Result is the final state of an expert session run.
type Result struct {
	Outcomes    []Outcome    `json:"outcomes,omitempty"`
	Facts       []Fact       `json:"facts,omitempty"`
	Activations []Activation `json:"activations,omitempty"`
	Rounds      int          `json:"rounds"`
	Mutations   int          `json:"mutations"`
	StopReason  StopReason   `json:"stop_reason"`
}

// Checkpoint marks a session position so callers can request a later delta.
type Checkpoint struct {
	outcomeCount    int
	activationCount int
}

type supportRecord struct {
	Instance string
	Rule     string
	Priority int
	Fact     Fact
}

type retractRecord struct {
	Instance string
	Rule     string
	Priority int
	FactType string
	FactKey  string
}

type modifyRecord struct {
	Instance  string
	Rule      string
	Priority  int
	FactType  string
	FactKey   string
	SetFields map[string]any
}

// Session runs an expert program against an envelope and working memory.
type Session struct {
	program            *Program
	envelope           map[string]any
	facts              map[string]map[string]Fact
	externalFacts      map[string]map[string]Fact
	outcomes           []Outcome
	activations        []Activation
	emitted            map[string]struct{}
	opts               Options
	mutations          int
	rounds             int
	stopReason         StopReason
	ruleResults        map[string]bool
	dirtyFacts         map[string]struct{}
	dirtySources       map[string]map[string]struct{}
	lastRoundMutations int
	stablePending      bool
	supportsByRule     map[string]map[string]supportRecord
	factSupports       map[string]map[string]map[string]supportRecord
	retractsByRule     map[string]map[string]retractRecord
	factRetracts       map[string]map[string]map[string]retractRecord
	modifiesByRule     map[string]map[string]modifyRecord
	factModifies       map[string]map[string]map[string]modifyRecord
	evalCtx            map[string]any
	factsView          map[string]any
	pool               *vm.StringPool
	dc                 vm.DataContext
	evaluator          *vm.Evaluator
}

// NewSession creates a new in-memory expert session.
func NewSession(p *Program, envelope map[string]any, facts []Fact, opts Options) *Session {
	if opts.MaxRounds <= 0 {
		opts.MaxRounds = 32
	}
	if opts.MaxMutations <= 0 {
		opts.MaxMutations = 1024
	}

	s := &Session{
		program:        p,
		envelope:       cloneMap(envelope),
		facts:          make(map[string]map[string]Fact),
		externalFacts:  make(map[string]map[string]Fact),
		emitted:        make(map[string]struct{}),
		opts:           opts,
		ruleResults:    make(map[string]bool),
		dirtyFacts:     make(map[string]struct{}),
		dirtySources:   make(map[string]map[string]struct{}),
		supportsByRule: make(map[string]map[string]supportRecord),
		factSupports:   make(map[string]map[string]map[string]supportRecord),
		retractsByRule: make(map[string]map[string]retractRecord),
		factRetracts:   make(map[string]map[string]map[string]retractRecord),
		modifiesByRule: make(map[string]map[string]modifyRecord),
		factModifies:   make(map[string]map[string]map[string]modifyRecord),
	}
	s.initEvalState()
	for _, fact := range facts {
		_, _ = s.upsertExternalFact(fact)
	}
	return s
}

// Assert inserts or updates a fact in working memory.
func (s *Session) Assert(f Fact) error {
	if s == nil {
		return fmt.Errorf("nil session")
	}
	if f.Type == "" {
		return fmt.Errorf("fact type is required")
	}
	if f.Key == "" {
		return fmt.Errorf("fact key is required")
	}
	_, err := s.upsertExternalFact(f)
	return err
}

// Retract removes a fact from working memory if present.
func (s *Session) Retract(factType, factKey string) error {
	if s == nil {
		return fmt.Errorf("nil session")
	}
	if factType == "" {
		return fmt.Errorf("fact type is required")
	}
	if factKey == "" {
		return fmt.Errorf("fact key is required")
	}
	byKey, ok := s.externalFacts[factType]
	if !ok {
		return nil
	}
	delete(byKey, factKey)
	if len(byKey) == 0 {
		delete(s.externalFacts, factType)
	}
	s.recomputeFact(factType, factKey, "")
	return nil
}

// Facts returns the current facts in deterministic order.
func (s *Session) Facts() []Fact {
	if s == nil {
		return nil
	}
	return s.sortedFacts()
}

// Trace returns the recorded expert activations.
func (s *Session) Trace() []Activation {
	if s == nil || len(s.activations) == 0 {
		return nil
	}
	out := make([]Activation, len(s.activations))
	for i, activation := range s.activations {
		out[i] = Activation{
			Round:   activation.Round,
			Rule:    activation.Rule,
			Kind:    activation.Kind,
			Target:  activation.Target,
			Params:  cloneMap(activation.Params),
			Changed: activation.Changed,
			Detail:  activation.Detail,
		}
	}
	return out
}

// Checkpoint returns a mark that can later be used with DeltaSince.
func (s *Session) Checkpoint() Checkpoint {
	if s == nil {
		return Checkpoint{}
	}
	return Checkpoint{
		outcomeCount:    len(s.outcomes),
		activationCount: len(s.activations),
	}
}

// DeltaSince returns the incremental outcomes and activations since a checkpoint,
// along with the current facts and guardrail metadata.
func (s *Session) DeltaSince(mark Checkpoint) Result {
	if s == nil {
		return Result{}
	}
	return Result{
		Outcomes:    cloneOutcomes(s.outcomes, mark.outcomeCount),
		Facts:       s.sortedFacts(),
		Activations: cloneActivations(s.activations, mark.activationCount),
		Rounds:      s.rounds,
		Mutations:   s.mutations,
		StopReason:  s.stopReason,
	}
}

// Run evaluates the expert program until it reaches a fixed point or a guardrail.
func (s *Session) Run(ctx context.Context) (Result, error) {
	if s == nil || s.program == nil {
		return Result{}, fmt.Errorf("nil expert program")
	}
	if s.program.ruleset == nil || len(s.program.rules) == 0 {
		s.stopReason = StopQuiescent
		return s.snapshot(), nil
	}
	if s.rounds > 0 && s.stopReason == StopQuiescent && len(s.dirtyFacts) == 0 {
		return s.snapshot(), nil
	}

	for round := 1; round <= s.opts.MaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			s.stopReason = StopContextCancelled
			return s.snapshot(), err
		}
		s.rounds++
		roundMutationsStart := s.mutations
		forceStableRound := s.stablePending && s.rounds > 1 && s.lastRoundMutations == 0
		firedGroups := make(map[string]struct{})

		firstPass := s.rounds == 1
		dirtyFacts := s.copyDirtyFacts()
		dirtySources := s.copyDirtySources()
		s.clearDirtyFacts()
		matched, ruleChanges, evaluated, stableDeferred, err := s.runRound(firstPass, dirtyFacts, dirtySources)
		if err != nil {
			return Result{}, err
		}
		mutated := false
		activeAssertInstances := make(map[string]struct{})
		activeRetractInstances := make(map[string]struct{})
		activeModifyInstances := make(map[string]struct{})
		for _, match := range matched {
			if err := ctx.Err(); err != nil {
				s.stopReason = StopContextCancelled
				return s.snapshot(), err
			}

			rule, ok := s.program.lookupRule(match.Name)
			if !ok {
				return Result{}, fmt.Errorf("missing expert rule metadata for %q", match.Name)
			}
			if rule.Group != "" {
				if _, blocked := firedGroups[rule.Group]; blocked {
					continue
				}
			}

			switch rule.Kind {
			case ActionAssert:
				changed, _, instance, err := s.applyAssert(round, rule, match)
				if err != nil {
					return Result{}, err
				}
				activeAssertInstances[instance] = struct{}{}
				mutated = mutated || changed
			case ActionEmit:
				_, err := s.applyEmit(round, rule, match)
				if err != nil {
					return Result{}, err
				}
			case ActionRetract:
				changed, _, instance, err := s.applyRetract(round, rule, match)
				if err != nil {
					return Result{}, err
				}
				activeRetractInstances[instance] = struct{}{}
				mutated = mutated || changed
			case ActionModify:
				changed, _, instance, err := s.applyModify(round, rule, match)
				if err != nil {
					return Result{}, err
				}
				activeModifyInstances[instance] = struct{}{}
				mutated = mutated || changed
			default:
				return Result{}, fmt.Errorf("unsupported expert action kind %q", rule.Kind)
			}
			if rule.Group != "" {
				firedGroups[rule.Group] = struct{}{}
			}

			if s.mutations >= s.opts.MaxMutations {
				s.stopReason = StopMaxMutations
				return s.snapshot(), nil
			}
		}
		for ruleName := range evaluated {
			rule, ok := s.program.lookupRule(ruleName)
			if !ok {
				continue
			}
			switch rule.Kind {
			case ActionAssert:
				if s.clearInactiveDerivedSupports(ruleName, activeAssertInstances) {
					mutated = true
				}
			case ActionRetract:
				if s.clearInactiveRetractions(ruleName, activeRetractInstances) {
					mutated = true
				}
			case ActionModify:
				if s.clearInactiveModifications(ruleName, activeModifyInstances) {
					mutated = true
				}
			}
		}

		s.lastRoundMutations = s.mutations - roundMutationsStart

		if !mutated {
			if stableDeferred {
				s.lastRoundMutations = s.mutations - roundMutationsStart
				continue
			}
			if forceStableRound {
				s.stablePending = false
			}
			s.stopReason = StopQuiescent
			return s.snapshot(), nil
		}
		if len(ruleChanges) == 0 && len(s.dirtyFacts) == 0 {
			s.stopReason = StopQuiescent
			return s.snapshot(), nil
		}
		if !s.hasPendingWork(ruleChanges) {
			s.stopReason = StopQuiescent
			return s.snapshot(), nil
		}
	}

	s.stopReason = StopMaxRounds
	return s.snapshot(), nil
}

func (s *Session) applyAssert(round int, rule Rule, match vm.MatchedRule) (bool, string, string, error) {
	keyValue, ok := match.Params["key"]
	if !ok {
		return false, "", "", fmt.Errorf("expert rule %s assert %s: missing key param", rule.Name, rule.Target)
	}
	key := factKeyString(keyValue)
	if key == "" {
		return false, "", "", fmt.Errorf("expert rule %s assert %s: empty key", rule.Name, rule.Target)
	}
	instance := mutationInstanceKey(rule.Name, key)

	fact := Fact{
		Type:   rule.Target,
		Key:    key,
		Fields: cloneMap(match.Params),
	}
	changed := s.setDerivedSupport(rule, fact)

	detail := fmt.Sprintf("assert %s/%s", rule.Target, key)
	if supporters := s.supporterNames(rule.Target, key); len(supporters) > 0 {
		detail += " supports=" + strings.Join(supporters, ",")
	}
	if changed {
		detail += " changed"
		s.mutations++
	} else {
		detail += " no-op"
	}
	s.activations = append(s.activations, Activation{
		Round:   round,
		Rule:    rule.Name,
		Kind:    rule.Kind,
		Target:  rule.Target,
		Params:  cloneMap(match.Params),
		Changed: changed,
		Detail:  detail,
	})
	return changed, detail, instance, nil
}

func (s *Session) applyEmit(round int, rule Rule, match vm.MatchedRule) (bool, error) {
	outcome := Outcome{
		Rule:   rule.Name,
		Name:   rule.Target,
		Params: cloneMap(match.Params),
	}
	key := stableKey(outcome)
	_, existed := s.emitted[key]
	if !existed {
		s.emitted[key] = struct{}{}
		s.outcomes = append(s.outcomes, outcome)
	}
	s.activations = append(s.activations, Activation{
		Round:   round,
		Rule:    rule.Name,
		Kind:    rule.Kind,
		Target:  rule.Target,
		Params:  cloneMap(match.Params),
		Changed: !existed,
		Detail:  fmt.Sprintf("emit %s", rule.Target),
	})
	return !existed, nil
}

func (s *Session) applyRetract(round int, rule Rule, match vm.MatchedRule) (bool, string, string, error) {
	key, _, err := splitMutationParams(match.Params)
	if err != nil {
		return false, "", "", fmt.Errorf("expert rule %s retract %s: %w", rule.Name, rule.Target, err)
	}
	instance := mutationInstanceKey(rule.Name, key)
	changed := s.setRetraction(rule, rule.Target, key)
	detail := fmt.Sprintf("retract %s/%s", rule.Target, key)
	if changed {
		detail += " changed"
		s.mutations++
	} else {
		detail += " no-op"
	}
	s.activations = append(s.activations, Activation{
		Round:   round,
		Rule:    rule.Name,
		Kind:    rule.Kind,
		Target:  rule.Target,
		Params:  map[string]any{"key": key},
		Changed: changed,
		Detail:  detail,
	})
	return changed, detail, instance, nil
}

func (s *Session) applyModify(round int, rule Rule, match vm.MatchedRule) (bool, string, string, error) {
	key, setFields, err := splitMutationParams(match.Params)
	if err != nil {
		return false, "", "", fmt.Errorf("expert rule %s modify %s: %w", rule.Name, rule.Target, err)
	}
	instance := mutationInstanceKey(rule.Name, key)
	changed := s.setModification(rule, rule.Target, key, setFields)
	detail := fmt.Sprintf("modify %s/%s", rule.Target, key)
	if changed {
		detail += " changed"
		s.mutations++
	} else {
		detail += " no-op"
	}
	params := map[string]any{"key": key}
	if len(setFields) > 0 {
		params["set"] = cloneMap(setFields)
	}
	s.activations = append(s.activations, Activation{
		Round:   round,
		Rule:    rule.Name,
		Kind:    rule.Kind,
		Target:  rule.Target,
		Params:  params,
		Changed: changed,
		Detail:  detail,
	})
	return changed, detail, instance, nil
}

func (s *Session) upsertExternalFact(f Fact) (bool, error) {
	if f.Type == "" {
		return false, fmt.Errorf("fact type is required")
	}
	if f.Key == "" {
		return false, fmt.Errorf("fact key is required")
	}
	if f.Fields == nil {
		f.Fields = make(map[string]any)
	}

	byKey, ok := s.externalFacts[f.Type]
	if !ok {
		byKey = make(map[string]Fact)
		s.externalFacts[f.Type] = byKey
	}

	next := Fact{
		Type:          f.Type,
		Key:           f.Key,
		Fields:        cloneMap(f.Fields),
		AssertedRound: s.rounds,
	}
	if current, ok := byKey[f.Key]; ok && stableKey(current.Fields) == stableKey(next.Fields) {
		return false, nil
	}

	byKey[f.Key] = next
	return s.recomputeFact(f.Type, f.Key, ""), nil
}

func (s *Session) setDerivedSupport(rule Rule, fact Fact) bool {
	fact.AssertedRound = s.rounds
	instance := mutationInstanceKey(rule.Name, fact.Key)
	next := supportRecord{
		Instance: instance,
		Rule:     rule.Name,
		Priority: rule.Priority,
		Fact: Fact{
			Type:          fact.Type,
			Key:           fact.Key,
			Fields:        cloneMap(fact.Fields),
			AssertedRound: fact.AssertedRound,
		},
	}

	byInstance := s.supportRecords(rule.Name)
	prev, hadPrev := byInstance[instance]
	changed := false
	sameFact := hadPrev && prev.Fact.Type == next.Fact.Type && prev.Fact.Key == next.Fact.Key
	if hadPrev {
		s.removeSupportRecord(prev)
		if !sameFact {
			changed = s.recomputeFact(prev.Fact.Type, prev.Fact.Key, rule.Name) || changed
		}
		byInstance = s.supportRecords(rule.Name)
	}

	byKey, ok := s.factSupports[next.Fact.Type]
	if !ok {
		byKey = make(map[string]map[string]supportRecord)
		s.factSupports[next.Fact.Type] = byKey
	}
	supporters, ok := byKey[next.Fact.Key]
	if !ok {
		supporters = make(map[string]supportRecord)
		byKey[next.Fact.Key] = supporters
	}
	supporters[instance] = next
	byInstance[instance] = next
	return s.recomputeFact(next.Fact.Type, next.Fact.Key, rule.Name) || changed
}

func (s *Session) clearInactiveDerivedSupports(ruleName string, active map[string]struct{}) bool {
	records := s.supportsByRule[ruleName]
	if len(records) == 0 {
		return false
	}
	changed := false
	for instance, record := range records {
		if _, ok := active[instance]; ok {
			continue
		}
		s.removeSupportRecord(record)
		changed = s.recomputeFact(record.Fact.Type, record.Fact.Key, ruleName) || changed
	}
	return changed
}

func (s *Session) removeSupportRecord(record supportRecord) {
	if byInstance, ok := s.supportsByRule[record.Rule]; ok {
		delete(byInstance, record.Instance)
		if len(byInstance) == 0 {
			delete(s.supportsByRule, record.Rule)
		}
	}
	byKey, ok := s.factSupports[record.Fact.Type]
	if !ok {
		return
	}
	supporters, ok := byKey[record.Fact.Key]
	if !ok {
		return
	}
	delete(supporters, record.Instance)
	if len(supporters) == 0 {
		delete(byKey, record.Fact.Key)
	}
	if len(byKey) == 0 {
		delete(s.factSupports, record.Fact.Type)
	}
}

func (s *Session) recomputeFact(factType, factKey, source string) bool {
	next, ok := s.desiredFact(factType, factKey)
	current, currentOK := s.currentFact(factType, factKey)
	if !ok {
		if !currentOK {
			return false
		}
		byKey := s.facts[factType]
		delete(byKey, factKey)
		if len(byKey) == 0 {
			delete(s.facts, factType)
		}
		s.markDirtySource(factType, source)
		return true
	}

	if currentOK && stableKey(current.Fields) == stableKey(next.Fields) {
		if !sameStrings(current.DerivedBy, next.DerivedBy) {
			next.AssertedRound = current.AssertedRound
			byKey := s.facts[factType]
			byKey[factKey] = next
		}
		return false
	}

	byKey, ok := s.facts[factType]
	if !ok {
		byKey = make(map[string]Fact)
		s.facts[factType] = byKey
	}
	byKey[factKey] = next
	s.markDirtySource(factType, source)
	return true
}

func (s *Session) desiredFact(factType, factKey string) (Fact, bool) {
	return s.desiredFactExcludingRule(factType, factKey, "", "")
}

func (s *Session) desiredFactExcludingRule(factType, factKey, ruleName string, kind ActionKind) (Fact, bool) {
	if s.isRetractedExcludingRule(factType, factKey, ruleName, kind) {
		return Fact{}, false
	}

	var fact Fact
	if record, ok := s.winningSupport(factType, factKey); ok {
		fact = cloneFact(record.Fact)
		fact.DerivedBy = s.supporterNames(factType, factKey)
	} else if byKey, ok := s.externalFacts[factType]; ok {
		if current, ok := byKey[factKey]; ok {
			fact = cloneFact(current)
		} else {
			return Fact{}, false
		}
	} else {
		return Fact{}, false
	}

	fact.Fields = s.applyModificationsExcludingRule(factType, factKey, fact.Fields, ruleName, kind)
	return fact, true
}

func (s *Session) currentFact(factType, factKey string) (Fact, bool) {
	byKey, ok := s.facts[factType]
	if !ok {
		return Fact{}, false
	}
	fact, ok := byKey[factKey]
	return fact, ok
}

func (s *Session) winningSupport(factType, factKey string) (supportRecord, bool) {
	byKey, ok := s.factSupports[factType]
	if !ok {
		return supportRecord{}, false
	}
	supporters, ok := byKey[factKey]
	if !ok || len(supporters) == 0 {
		return supportRecord{}, false
	}
	var winner supportRecord
	set := false
	for _, record := range supporters {
		if !set ||
			record.Priority > winner.Priority ||
			(record.Priority == winner.Priority && record.Rule < winner.Rule) ||
			(record.Priority == winner.Priority && record.Rule == winner.Rule && record.Instance < winner.Instance) {
			winner = record
			set = true
		}
	}
	return winner, set
}

func (s *Session) supporterNames(factType, factKey string) []string {
	byKey, ok := s.factSupports[factType]
	if !ok {
		return nil
	}
	supporters, ok := byKey[factKey]
	if !ok || len(supporters) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(supporters))
	out := make([]string, 0, len(supporters))
	for _, record := range supporters {
		if _, ok := seen[record.Rule]; ok {
			continue
		}
		seen[record.Rule] = struct{}{}
		out = append(out, record.Rule)
	}
	sort.Strings(out)
	return out
}

func (s *Session) applyModifications(factType, factKey string, fields map[string]any) map[string]any {
	return s.applyModificationsExcludingRule(factType, factKey, fields, "", "")
}

func (s *Session) isRetractedExcludingRule(factType, factKey, ruleName string, kind ActionKind) bool {
	byKey, ok := s.factRetracts[factType]
	if !ok {
		return false
	}
	records, ok := byKey[factKey]
	if !ok || len(records) == 0 {
		return false
	}
	for _, record := range records {
		if kind == ActionRetract && record.Rule == ruleName {
			continue
		}
		return true
	}
	return false
}

func (s *Session) applyModificationsExcludingRule(factType, factKey string, fields map[string]any, ruleName string, kind ActionKind) map[string]any {
	byKey, ok := s.factModifies[factType]
	if !ok {
		return fields
	}
	records, ok := byKey[factKey]
	if !ok || len(records) == 0 {
		return fields
	}
	out := cloneMap(fields)
	if out == nil {
		out = make(map[string]any)
	}
	for _, record := range sortedModifications(records, ruleName, kind) {
		for key, value := range record.SetFields {
			out[key] = value
		}
	}
	if _, ok := out["key"]; !ok {
		out["key"] = factKey
	}
	return out
}

func (s *Session) evalContextIgnoringOwnMutation(rule Rule) (map[string]any, vm.DataContext, bool, error) {
	targets := s.ownMutationTargets(rule)
	if len(targets) == 0 {
		return nil, nil, false, nil
	}
	factsView := make(map[string]any, len(s.factsView))
	for key, value := range s.factsView {
		factsView[key] = value
	}
	for factType, factKeys := range targets {
		replaced := make(map[string]Fact)
		for key, fact := range s.facts[factType] {
			replaced[key] = cloneFact(fact)
		}
		for factKey := range factKeys {
			tempFact, ok := s.desiredFactExcludingRule(factType, factKey, rule.Name, rule.Kind)
			if ok {
				replaced[factKey] = tempFact
			} else {
				delete(replaced, factKey)
			}
		}
		if len(replaced) == 0 {
			delete(factsView, factType)
			continue
		}
		items := make([]any, 0, len(replaced))
		for _, fact := range replaced {
			items = append(items, factEvalFields(fact))
		}
		factsView[factType] = items
	}

	ctx := cloneMap(s.evalCtx)
	ctx["facts"] = factsView
	return ctx, vm.DataFromMap(ctx, s.pool), true, nil
}

func (s *Session) ownMutationTargets(rule Rule) map[string]map[string]struct{} {
	targets := make(map[string]map[string]struct{})
	add := func(factType, factKey string) {
		if factType == "" || factKey == "" {
			return
		}
		byKey, ok := targets[factType]
		if !ok {
			byKey = make(map[string]struct{})
			targets[factType] = byKey
		}
		byKey[factKey] = struct{}{}
	}

	switch rule.Kind {
	case ActionAssert:
		for _, record := range s.supportsByRule[rule.Name] {
			add(record.Fact.Type, record.Fact.Key)
		}
	case ActionRetract:
		for _, record := range s.retractsByRule[rule.Name] {
			add(record.FactType, record.FactKey)
		}
	case ActionModify:
		for _, record := range s.modifiesByRule[rule.Name] {
			add(record.FactType, record.FactKey)
		}
	}

	if len(targets) == 0 {
		return nil
	}
	return targets
}

func (s *Session) supportRecords(ruleName string) map[string]supportRecord {
	if s.supportsByRule[ruleName] == nil {
		s.supportsByRule[ruleName] = make(map[string]supportRecord)
	}
	return s.supportsByRule[ruleName]
}

func (s *Session) retractionRecords(ruleName string) map[string]retractRecord {
	if s.retractsByRule[ruleName] == nil {
		s.retractsByRule[ruleName] = make(map[string]retractRecord)
	}
	return s.retractsByRule[ruleName]
}

func (s *Session) modificationRecords(ruleName string) map[string]modifyRecord {
	if s.modifiesByRule[ruleName] == nil {
		s.modifiesByRule[ruleName] = make(map[string]modifyRecord)
	}
	return s.modifiesByRule[ruleName]
}

func (s *Session) setRetraction(rule Rule, factType, factKey string) bool {
	instance := mutationInstanceKey(rule.Name, factKey)
	next := retractRecord{
		Instance: instance,
		Rule:     rule.Name,
		Priority: rule.Priority,
		FactType: factType,
		FactKey:  factKey,
	}
	byInstance := s.retractionRecords(rule.Name)
	prev, hadPrev := byInstance[instance]
	changed := false
	sameFact := hadPrev && prev.FactType == factType && prev.FactKey == factKey
	if hadPrev {
		s.removeRetraction(prev)
		if !sameFact {
			changed = s.recomputeFact(prev.FactType, prev.FactKey, rule.Name) || changed
		}
		byInstance = s.retractionRecords(rule.Name)
	}
	byKey, ok := s.factRetracts[factType]
	if !ok {
		byKey = make(map[string]map[string]retractRecord)
		s.factRetracts[factType] = byKey
	}
	records, ok := byKey[factKey]
	if !ok {
		records = make(map[string]retractRecord)
		byKey[factKey] = records
	}
	records[instance] = next
	byInstance[instance] = next
	return s.recomputeFact(factType, factKey, rule.Name) || changed
}

func (s *Session) clearInactiveRetractions(ruleName string, active map[string]struct{}) bool {
	records := s.retractsByRule[ruleName]
	if len(records) == 0 {
		return false
	}
	changed := false
	for instance, record := range records {
		if _, ok := active[instance]; ok {
			continue
		}
		s.removeRetraction(record)
		changed = s.recomputeFact(record.FactType, record.FactKey, ruleName) || changed
	}
	return changed
}

func (s *Session) removeRetraction(record retractRecord) {
	if byInstance, ok := s.retractsByRule[record.Rule]; ok {
		delete(byInstance, record.Instance)
		if len(byInstance) == 0 {
			delete(s.retractsByRule, record.Rule)
		}
	}
	byKey, ok := s.factRetracts[record.FactType]
	if !ok {
		return
	}
	records, ok := byKey[record.FactKey]
	if !ok {
		return
	}
	delete(records, record.Instance)
	if len(records) == 0 {
		delete(byKey, record.FactKey)
	}
	if len(byKey) == 0 {
		delete(s.factRetracts, record.FactType)
	}
}

func (s *Session) setModification(rule Rule, factType, factKey string, setFields map[string]any) bool {
	instance := mutationInstanceKey(rule.Name, factKey)
	next := modifyRecord{
		Instance:  instance,
		Rule:      rule.Name,
		Priority:  rule.Priority,
		FactType:  factType,
		FactKey:   factKey,
		SetFields: cloneMap(setFields),
	}
	byInstance := s.modificationRecords(rule.Name)
	prev, hadPrev := byInstance[instance]
	changed := false
	sameFact := hadPrev && prev.FactType == factType && prev.FactKey == factKey
	if hadPrev {
		s.removeModification(prev)
		if !sameFact {
			changed = s.recomputeFact(prev.FactType, prev.FactKey, rule.Name) || changed
		}
		byInstance = s.modificationRecords(rule.Name)
	}
	byKey, ok := s.factModifies[factType]
	if !ok {
		byKey = make(map[string]map[string]modifyRecord)
		s.factModifies[factType] = byKey
	}
	records, ok := byKey[factKey]
	if !ok {
		records = make(map[string]modifyRecord)
		byKey[factKey] = records
	}
	records[instance] = next
	byInstance[instance] = next
	return s.recomputeFact(factType, factKey, rule.Name) || changed
}

func (s *Session) clearInactiveModifications(ruleName string, active map[string]struct{}) bool {
	records := s.modifiesByRule[ruleName]
	if len(records) == 0 {
		return false
	}
	changed := false
	for instance, record := range records {
		if _, ok := active[instance]; ok {
			continue
		}
		s.removeModification(record)
		changed = s.recomputeFact(record.FactType, record.FactKey, ruleName) || changed
	}
	return changed
}

func (s *Session) removeModification(record modifyRecord) {
	if byInstance, ok := s.modifiesByRule[record.Rule]; ok {
		delete(byInstance, record.Instance)
		if len(byInstance) == 0 {
			delete(s.modifiesByRule, record.Rule)
		}
	}
	byKey, ok := s.factModifies[record.FactType]
	if !ok {
		return
	}
	records, ok := byKey[record.FactKey]
	if !ok {
		return
	}
	delete(records, record.Instance)
	if len(records) == 0 {
		delete(byKey, record.FactKey)
	}
	if len(byKey) == 0 {
		delete(s.factModifies, record.FactType)
	}
}

func (s *Session) runRound(firstPass bool, dirtyFacts map[string]struct{}, dirtySources map[string]map[string]struct{}) ([]vm.MatchedRule, map[string]struct{}, map[string]struct{}, bool, error) {
	s.refreshContextView(firstPass, dirtyFacts)
	rc := govern.NewRequestCache(s.program.segments, s.evalCtx)
	for name, matched := range s.ruleResults {
		rc.RecordRuleResult(name, matched)
	}

	current := make(map[string]bool, len(s.ruleResults)+len(s.program.rules))
	for name, matched := range s.ruleResults {
		current[name] = matched
	}
	ruleChanges := make(map[string]struct{})
	matched := make([]vm.MatchedRule, 0)
	evaluated := make(map[string]struct{})
	stableDeferred := s.stablePending && s.lastRoundMutations > 0
	forceStableRound := s.stablePending && s.rounds > 1 && s.lastRoundMutations == 0

	for i, header := range s.program.ruleset.Rules {
		rule := s.program.rules[i]
		shouldEval := firstPass || s.shouldEvaluate(rule, dirtyFacts, dirtySources, ruleChanges)
		if rule.Stable && forceStableRound {
			shouldEval = true
		}
		if !shouldEval {
			continue
		}
		if rule.Stable && (s.rounds == 1 || s.lastRoundMutations > 0) {
			s.stablePending = true
			stableDeferred = true
			continue
		}
		evaluated[rule.Name] = struct{}{}

		dc := s.dc
		ruleRC := rc
		tempCtx, tempDC, ok, err := s.evalContextIgnoringOwnMutation(rule)
		if err != nil {
			return nil, nil, nil, false, err
		}
		if ok {
			dc = tempDC
			ruleRC = govern.NewRequestCache(s.program.segments, tempCtx)
			for name, matched := range current {
				ruleRC.RecordRuleResult(name, matched)
			}
		}

		result, mr, err := s.evalRule(rule, header, s.evaluator, dc, ruleRC)
		if err != nil {
			return nil, nil, nil, false, err
		}
		prev := current[rule.Name]
		current[rule.Name] = result
		if prev != result || firstPass {
			ruleChanges[rule.Name] = struct{}{}
		}
		rc.RecordRuleResult(rule.Name, result)
		if result && rule.PerFact {
			// per_fact: collect all matching facts and emit one match per fact.
			matches := s.evalPerFact(rule, header, s.evaluator, dc, ruleRC)
			matched = append(matched, matches...)
		} else if result {
			matched = append(matched, mr)
		}
	}

	s.ruleResults = current
	return matched, ruleChanges, evaluated, stableDeferred, nil
}

// evalPerFact iterates all facts in the rule's first fact dependency and
// evaluates the rule condition with each fact individually. Returns one
// MatchedRule per matching fact, with the quantifier variable bound to that
// fact so action params can reference its fields.
func (s *Session) evalPerFact(rule Rule, header compiler.RuleHeader, evaluator *vm.Evaluator, dc vm.DataContext, rc *govern.RequestCache) []vm.MatchedRule {
	if len(rule.FactDeps) == 0 {
		return nil
	}
	factType := rule.FactDeps[0]
	factsOfType, ok := s.facts[factType]
	if !ok {
		// Also check external facts
		factsOfType, ok = s.externalFacts[factType]
		if !ok {
			return nil
		}
	}

	var results []vm.MatchedRule
	for _, fact := range factsOfType {
		// Build a temporary context with this single fact as the only fact of its type
		tempCtx := s.buildSingleFactContext(factType, fact)
		tempDC := vm.DataFromMap(tempCtx, s.pool)
		ok, mr, err := s.evalRule(rule, header, evaluator, tempDC, rc)
		if err != nil || !ok {
			continue
		}
		results = append(results, mr)
	}
	return results
}

// buildSingleFactContext creates an evaluation context where only one fact of
// the given type exists, allowing the quantifier to bind to exactly that fact.
func (s *Session) buildSingleFactContext(factType string, fact Fact) map[string]any {
	ctx := make(map[string]any, len(s.evalCtx))
	for k, v := range s.evalCtx {
		ctx[k] = v
	}
	// Override the facts for this type with just this one fact
	factsMap, ok := ctx["facts"].(map[string]any)
	if !ok {
		factsMap = make(map[string]any)
	}
	// Clone the facts map so we don't mutate the shared one
	newFacts := make(map[string]any, len(factsMap))
	for k, v := range factsMap {
		newFacts[k] = v
	}
	// Replace this fact type with a single-element list
	fields := make(map[string]any, len(fact.Fields)+2)
	fields["key"] = fact.Key
	fields["type"] = fact.Type
	for k, v := range fact.Fields {
		fields[k] = v
	}
	newFacts[factType] = []any{fields}
	ctx["facts"] = newFacts
	return ctx
}

func (s *Session) evalRule(rule Rule, header compiler.RuleHeader, evaluator *vm.Evaluator, dc vm.DataContext, rc *govern.RequestCache) (bool, vm.MatchedRule, error) {
	if govern.IsKillSwitched(effectiveRuleKillSwitch(header, rule, s.opts.BundleID, s.opts.Overrides), nil) {
		return false, vm.MatchedRule{}, nil
	}
	if !rc.CheckPrerequisites(rule.Prereqs, nil) {
		return false, vm.MatchedRule{}, nil
	}
	if !rc.CheckExclusions(rule.Excludes, nil) {
		return false, vm.MatchedRule{}, nil
	}
	if header.HasSegment {
		segName := evaluator.String(header.SegmentNameIdx)
		segOK, _ := rc.EvalSegment(segName)
		if !segOK {
			return false, vm.MatchedRule{}, nil
		}
	}
	condOK, err := evaluator.EvalRuleCondition(header, dc)
	if err != nil {
		return false, vm.MatchedRule{}, fmt.Errorf("expert rule %s: %w", rule.Name, err)
	}
	if !condOK {
		return false, vm.MatchedRule{}, nil
	}
	rollout := effectiveRuleRollout(header, rule, s.opts.BundleID, s.opts.Overrides)
	if rollout > 0 && !govern.RolloutAllows(rollout, rc.Context()) {
		return false, vm.MatchedRule{}, nil
	}
	match, err := evaluator.BuildMatch(header, dc)
	if err != nil {
		return false, vm.MatchedRule{}, fmt.Errorf("expert rule %s action %s: %w", rule.Name, match.Action, err)
	}
	return true, match, nil
}

func (s *Session) shouldEvaluate(rule Rule, dirtyFacts map[string]struct{}, dirtySources map[string]map[string]struct{}, dirtyRules map[string]struct{}) bool {
	if len(rule.Prereqs) == 0 && len(rule.FactDeps) == 0 {
		return false
	}
	prereqDirty := false
	for _, prereq := range rule.Prereqs {
		if _, ok := dirtyRules[prereq]; ok {
			prereqDirty = true
			break
		}
	}
	factDirty := false
	selfOnly := true
	for _, factType := range rule.FactDeps {
		if _, ok := dirtyFacts[factType]; ok {
			factDirty = true
			if !changedOnlyByRule(dirtySources[factType], rule.Name) {
				selfOnly = false
			}
		}
	}
	if !prereqDirty && !factDirty {
		return false
	}
	if !rule.NoLoop {
		return true
	}
	if prereqDirty {
		return true
	}
	return !selfOnly
}

func (s *Session) hasPendingWork(dirtyRules map[string]struct{}) bool {
	if s.stablePending {
		return true
	}
	for _, rule := range s.program.rules {
		if s.shouldEvaluate(rule, s.dirtyFacts, s.dirtySources, dirtyRules) {
			return true
		}
	}
	return false
}

func effectiveRuleKillSwitch(header compiler.RuleHeader, rule Rule, bundleID string, view overrides.View) bool {
	killSwitch := header.KillSwitch
	if view == nil {
		return killSwitch
	}
	if ov, ok := view.Rule(bundleID, rule.Name); ok && ov.KillSwitch != nil {
		return *ov.KillSwitch
	}
	return killSwitch
}

func effectiveRuleRollout(header compiler.RuleHeader, rule Rule, bundleID string, view overrides.View) uint8 {
	rollout := header.Rollout
	if view == nil {
		return rollout
	}
	if ov, ok := view.Rule(bundleID, rule.Name); ok && ov.Rollout != nil {
		return *ov.Rollout
	}
	return rollout
}

func (s *Session) markDirtySource(factType, source string) {
	if s == nil || factType == "" {
		return
	}
	if s.dirtyFacts == nil {
		s.dirtyFacts = make(map[string]struct{})
	}
	if s.dirtySources == nil {
		s.dirtySources = make(map[string]map[string]struct{})
	}
	s.dirtyFacts[factType] = struct{}{}
	if _, ok := s.dirtySources[factType]; !ok {
		s.dirtySources[factType] = make(map[string]struct{})
	}
	s.dirtySources[factType][source] = struct{}{}
}

func (s *Session) copyDirtyFacts() map[string]struct{} {
	if len(s.dirtyFacts) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(s.dirtyFacts))
	for factType := range s.dirtyFacts {
		out[factType] = struct{}{}
	}
	return out
}

func (s *Session) clearDirtyFacts() {
	if len(s.dirtyFacts) == 0 {
		s.dirtyFacts = make(map[string]struct{})
		s.dirtySources = make(map[string]map[string]struct{})
		return
	}
	s.dirtyFacts = make(map[string]struct{})
	s.dirtySources = make(map[string]map[string]struct{})
}

// Snapshot returns the current session state without advancing evaluation.
func (s *Session) Snapshot() Result {
	return s.snapshot()
}

func splitMutationParams(params map[string]any) (string, map[string]any, error) {
	keyValue, ok := params["key"]
	if !ok {
		return "", nil, fmt.Errorf("missing key param")
	}
	key := factKeyString(keyValue)
	if key == "" {
		return "", nil, fmt.Errorf("empty key")
	}
	setFields := make(map[string]any)
	for name, value := range params {
		if !strings.HasPrefix(name, modifySetPrefix) {
			continue
		}
		setFields[strings.TrimPrefix(name, modifySetPrefix)] = value
	}
	return key, setFields, nil
}

func (s *Session) copyDirtySources() map[string]map[string]struct{} {
	if len(s.dirtySources) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{}, len(s.dirtySources))
	for factType, sources := range s.dirtySources {
		clone := make(map[string]struct{}, len(sources))
		for source := range sources {
			clone[source] = struct{}{}
		}
		out[factType] = clone
	}
	return out
}

func changedOnlyByRule(sources map[string]struct{}, ruleName string) bool {
	if len(sources) == 0 {
		return false
	}
	for source := range sources {
		if source != ruleName {
			return false
		}
	}
	return true
}

func (s *Session) initEvalState() {
	if s == nil {
		return
	}
	s.evalCtx = cloneMap(s.envelope)
	if s.evalCtx == nil {
		s.evalCtx = make(map[string]any)
	}
	s.factsView = make(map[string]any)
	s.evalCtx["facts"] = s.factsView
	s.evalCtx["current_round"] = float64(s.rounds)
	if s.program == nil || s.program.ruleset == nil {
		return
	}
	s.pool = vm.NewStringPool(s.program.ruleset.Constants.Strings())
	s.dc = vm.DataFromMap(s.evalCtx, s.pool)
	s.evaluator = vm.NewEvaluator(s.program.ruleset, s.pool)
}

func (s *Session) refreshContextView(firstPass bool, dirtyFacts map[string]struct{}) {
	if s.evalCtx == nil || s.factsView == nil || s.dc == nil || s.evaluator == nil {
		s.initEvalState()
	}
	s.evalCtx["current_round"] = float64(s.rounds)
	if firstPass {
		dirtyFacts = make(map[string]struct{}, len(s.facts))
		for factType := range s.facts {
			dirtyFacts[factType] = struct{}{}
		}
	}
	for factType := range dirtyFacts {
		byKey, ok := s.facts[factType]
		if !ok || len(byKey) == 0 {
			delete(s.factsView, factType)
			continue
		}
		items := make([]any, 0, len(byKey))
		for _, fact := range byKey {
			items = append(items, factEvalFields(fact))
		}
		s.factsView[factType] = items
	}
}

func (s *Session) sortedFacts() []Fact {
	typeNames := make([]string, 0, len(s.facts))
	for typeName := range s.facts {
		typeNames = append(typeNames, typeName)
	}
	sort.Strings(typeNames)

	out := make([]Fact, 0)
	for _, typeName := range typeNames {
		byKey := s.facts[typeName]
		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fact := byKey[key]
			out = append(out, Fact{
				Type:          fact.Type,
				Key:           fact.Key,
				Fields:        cloneMap(fact.Fields),
				DerivedBy:     append([]string(nil), fact.DerivedBy...),
				AssertedRound: fact.AssertedRound,
			})
		}
	}
	return out
}

func (s *Session) snapshot() Result {
	return Result{
		Outcomes:    cloneOutcomes(s.outcomes, 0),
		Facts:       s.sortedFacts(),
		Activations: cloneActivations(s.activations, 0),
		Rounds:      s.rounds,
		Mutations:   s.mutations,
		StopReason:  s.stopReason,
	}
}

func cloneOutcomes(src []Outcome, start int) []Outcome {
	if start < 0 {
		start = 0
	}
	if start >= len(src) {
		return nil
	}
	out := make([]Outcome, len(src)-start)
	for i, outcome := range src[start:] {
		out[i] = Outcome{
			Rule:   outcome.Rule,
			Name:   outcome.Name,
			Params: cloneMap(outcome.Params),
		}
	}
	return out
}

func cloneActivations(src []Activation, start int) []Activation {
	if start < 0 {
		start = 0
	}
	if start >= len(src) {
		return nil
	}
	out := make([]Activation, len(src)-start)
	for i, activation := range src[start:] {
		out[i] = Activation{
			Round:   activation.Round,
			Rule:    activation.Rule,
			Kind:    activation.Kind,
			Target:  activation.Target,
			Params:  cloneMap(activation.Params),
			Changed: activation.Changed,
			Detail:  activation.Detail,
		}
	}
	return out
}

func cloneFact(src Fact) Fact {
	return Fact{
		Type:          src.Type,
		Key:           src.Key,
		Fields:        cloneMap(src.Fields),
		DerivedBy:     append([]string(nil), src.DerivedBy...),
		AssertedRound: src.AssertedRound,
	}
}

func factEvalFields(fact Fact) map[string]any {
	fields := cloneMap(fact.Fields)
	if fields == nil {
		fields = make(map[string]any, 1)
	}
	fields["__round"] = float64(fact.AssertedRound)
	return fields
}

func sortedModifications(records map[string]modifyRecord, ruleName string, kind ActionKind) []modifyRecord {
	out := make([]modifyRecord, 0, len(records))
	for _, record := range records {
		if kind == ActionModify && record.Rule == ruleName {
			continue
		}
		out = append(out, modifyRecord{
			Instance:  record.Instance,
			Rule:      record.Rule,
			Priority:  record.Priority,
			FactType:  record.FactType,
			FactKey:   record.FactKey,
			SetFields: cloneMap(record.SetFields),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Instance < out[j].Instance
	})
	return out
}

// mutationInstanceKey distinguishes repeated firings of one rule across target facts.
func mutationInstanceKey(ruleName, factKey string) string {
	return ruleName + "\x00" + factKey
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func factKeyString(v any) string {
	switch key := v.(type) {
	case string:
		return key
	case json.Number:
		return key.String()
	default:
		return fmt.Sprint(v)
	}
}

func stableKey(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}
