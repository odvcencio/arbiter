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
	Type   string         `json:"type"`
	Key    string         `json:"key"`
	Fields map[string]any `json:"fields,omitempty"`
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

// Session runs an expert program against an envelope and working memory.
type Session struct {
	program      *Program
	envelope     map[string]any
	facts        map[string]map[string]Fact
	outcomes     []Outcome
	activations  []Activation
	emitted      map[string]struct{}
	opts         Options
	mutations    int
	rounds       int
	stopReason   StopReason
	ruleResults  map[string]bool
	dirtyFacts   map[string]struct{}
	dirtySources map[string]map[string]struct{}
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
		program:      p,
		envelope:     cloneMap(envelope),
		facts:        make(map[string]map[string]Fact),
		emitted:      make(map[string]struct{}),
		opts:         opts,
		ruleResults:  make(map[string]bool),
		dirtyFacts:   make(map[string]struct{}),
		dirtySources: make(map[string]map[string]struct{}),
	}
	for _, fact := range facts {
		_, _ = s.upsertFactWithSource(fact, "")
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
	_, err := s.upsertFactWithSource(f, "")
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
	byKey, ok := s.facts[factType]
	if !ok {
		return nil
	}
	delete(byKey, factKey)
	if len(byKey) == 0 {
		delete(s.facts, factType)
	}
	s.markDirtySource(factType, "")
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
		firedGroups := make(map[string]struct{})

		firstPass := s.rounds == 1
		dirtyFacts := s.copyDirtyFacts()
		dirtySources := s.copyDirtySources()
		s.clearDirtyFacts()
		matched, ruleChanges, err := s.runRound(firstPass, dirtyFacts, dirtySources)
		if err != nil {
			return Result{}, err
		}
		mutated := false
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
				changed, _, err := s.applyAssert(round, rule, match)
				if err != nil {
					return Result{}, err
				}
				mutated = mutated || changed
			case ActionEmit:
				_, err := s.applyEmit(round, rule, match)
				if err != nil {
					return Result{}, err
				}
			case ActionRetract:
				changed, _, err := s.applyRetract(round, rule, match)
				if err != nil {
					return Result{}, err
				}
				mutated = mutated || changed
			case ActionModify:
				changed, _, err := s.applyModify(round, rule, match)
				if err != nil {
					return Result{}, err
				}
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

		if !mutated {
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

func (s *Session) applyAssert(round int, rule Rule, match vm.MatchedRule) (bool, string, error) {
	keyValue, ok := match.Params["key"]
	if !ok {
		return false, "", fmt.Errorf("expert rule %s assert %s: missing key param", rule.Name, rule.Target)
	}
	key := factKeyString(keyValue)
	if key == "" {
		return false, "", fmt.Errorf("expert rule %s assert %s: empty key", rule.Name, rule.Target)
	}

	fact := Fact{
		Type:   rule.Target,
		Key:    key,
		Fields: cloneMap(match.Params),
	}
	changed, err := s.upsertFactWithSource(fact, rule.Name)
	if err != nil {
		return false, "", err
	}

	detail := fmt.Sprintf("assert %s/%s", rule.Target, key)
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
	return changed, detail, nil
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

func (s *Session) applyRetract(round int, rule Rule, match vm.MatchedRule) (bool, string, error) {
	key, _, err := splitMutationParams(match.Params)
	if err != nil {
		return false, "", fmt.Errorf("expert rule %s retract %s: %w", rule.Name, rule.Target, err)
	}
	changed := s.deleteFact(rule.Target, key, rule.Name)
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
	return changed, detail, nil
}

func (s *Session) applyModify(round int, rule Rule, match vm.MatchedRule) (bool, string, error) {
	key, setFields, err := splitMutationParams(match.Params)
	if err != nil {
		return false, "", fmt.Errorf("expert rule %s modify %s: %w", rule.Name, rule.Target, err)
	}
	changed := s.modifyFact(rule.Target, key, setFields, rule.Name)
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
	return changed, detail, nil
}

func (s *Session) upsertFactWithSource(f Fact, source string) (bool, error) {
	if f.Type == "" {
		return false, fmt.Errorf("fact type is required")
	}
	if f.Key == "" {
		return false, fmt.Errorf("fact key is required")
	}
	if f.Fields == nil {
		f.Fields = make(map[string]any)
	}

	byKey, ok := s.facts[f.Type]
	if !ok {
		byKey = make(map[string]Fact)
		s.facts[f.Type] = byKey
	}

	next := Fact{
		Type:   f.Type,
		Key:    f.Key,
		Fields: cloneMap(f.Fields),
	}
	if current, ok := byKey[f.Key]; ok && stableKey(current.Fields) == stableKey(next.Fields) {
		return false, nil
	}

	byKey[f.Key] = next
	s.markDirtySource(f.Type, source)
	return true, nil
}

func (s *Session) deleteFact(factType, factKey, source string) bool {
	byKey, ok := s.facts[factType]
	if !ok {
		return false
	}
	if _, ok := byKey[factKey]; !ok {
		return false
	}
	delete(byKey, factKey)
	if len(byKey) == 0 {
		delete(s.facts, factType)
	}
	s.markDirtySource(factType, source)
	return true
}

func (s *Session) modifyFact(factType, factKey string, setFields map[string]any, source string) bool {
	byKey, ok := s.facts[factType]
	if !ok {
		return false
	}
	current, ok := byKey[factKey]
	if !ok {
		return false
	}

	next := Fact{
		Type:   current.Type,
		Key:    current.Key,
		Fields: cloneMap(current.Fields),
	}
	if next.Fields == nil {
		next.Fields = make(map[string]any)
	}
	for key, value := range setFields {
		next.Fields[key] = value
	}
	if _, ok := next.Fields["key"]; !ok {
		next.Fields["key"] = factKey
	}
	if stableKey(current.Fields) == stableKey(next.Fields) {
		return false
	}
	byKey[factKey] = next
	s.markDirtySource(factType, source)
	return true
}

func (s *Session) runRound(firstPass bool, dirtyFacts map[string]struct{}, dirtySources map[string]map[string]struct{}) ([]vm.MatchedRule, map[string]struct{}, error) {
	evalCtx := s.contextView()
	rc := govern.NewRequestCache(s.program.segments, evalCtx)
	for name, matched := range s.ruleResults {
		rc.RecordRuleResult(name, matched)
	}

	sp := vm.NewStringPool(s.program.ruleset.Constants.Strings())
	dc := vm.DataFromMap(evalCtx, sp)
	evaluator := vm.NewEvaluator(s.program.ruleset, sp)
	current := make(map[string]bool, len(s.ruleResults)+len(s.program.rules))
	for name, matched := range s.ruleResults {
		current[name] = matched
	}
	ruleChanges := make(map[string]struct{})
	matched := make([]vm.MatchedRule, 0)

	for i, header := range s.program.ruleset.Rules {
		rule := s.program.rules[i]
		shouldEval := firstPass || s.shouldEvaluate(rule, dirtyFacts, dirtySources, ruleChanges)
		if !shouldEval {
			continue
		}

		result, mr, err := s.evalRule(rule, header, evaluator, dc, rc)
		if err != nil {
			return nil, nil, err
		}
		prev := current[rule.Name]
		current[rule.Name] = result
		if prev != result || firstPass {
			ruleChanges[rule.Name] = struct{}{}
		}
		rc.RecordRuleResult(rule.Name, result)
		if result {
			matched = append(matched, mr)
		}
	}

	s.ruleResults = current
	return matched, ruleChanges, nil
}

func (s *Session) evalRule(rule Rule, header compiler.RuleHeader, evaluator *vm.Evaluator, dc vm.DataContext, rc *govern.RequestCache) (bool, vm.MatchedRule, error) {
	if govern.IsKillSwitched(effectiveRuleKillSwitch(header, rule, s.opts.BundleID, s.opts.Overrides), nil) {
		return false, vm.MatchedRule{}, nil
	}
	if !rc.CheckPrerequisites(rule.Prereqs, nil) {
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

func (s *Session) contextView() map[string]any {
	view := cloneMap(s.envelope)
	factsView := make(map[string]any, len(s.facts))

	typeNames := make([]string, 0, len(s.facts))
	for typeName := range s.facts {
		typeNames = append(typeNames, typeName)
	}
	sort.Strings(typeNames)

	for _, typeName := range typeNames {
		byKey := s.facts[typeName]
		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		items := make([]any, 0, len(keys))
		for _, key := range keys {
			items = append(items, cloneMap(byKey[key].Fields))
		}
		factsView[typeName] = items
	}

	view["facts"] = factsView
	return view
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
				Type:   fact.Type,
				Key:    fact.Key,
				Fields: cloneMap(fact.Fields),
			})
		}
	}
	return out
}

func (s *Session) snapshot() Result {
	outcomes := make([]Outcome, len(s.outcomes))
	for i, outcome := range s.outcomes {
		outcomes[i] = Outcome{
			Rule:   outcome.Rule,
			Name:   outcome.Name,
			Params: cloneMap(outcome.Params),
		}
	}
	return Result{
		Outcomes:    outcomes,
		Facts:       s.sortedFacts(),
		Activations: s.Trace(),
		Rounds:      s.rounds,
		Mutations:   s.mutations,
		StopReason:  s.stopReason,
	}
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
