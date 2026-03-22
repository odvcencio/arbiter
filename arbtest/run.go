package arbtest

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	arbiter "github.com/odvcencio/arbiter"
	dec "github.com/odvcencio/arbiter/decimal"
	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/flags"
	"github.com/odvcencio/arbiter/vm"
	"github.com/odvcencio/arbiter/workflow"
)

type timedOutcome struct {
	Arbiter string
	At      time.Time
	Outcome expert.Outcome
}

// RunFile parses and executes one .test.arb file.
func RunFile(path string, opts Options) (*FileResult, error) {
	suite, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	bundlePath, err := bundlePath(path)
	if err != nil {
		return nil, err
	}

	result := &FileResult{
		File:    suite.File,
		Bundle:  bundlePath,
		Verbose: opts.Verbose,
	}

	var (
		full        *arbiter.CompileResult
		flagSet     *flags.Flags
		expertProg  *expert.Program
		flagsLoaded bool
	)

	if len(suite.Tests) > 0 || suiteNeedsContinuous(suite) {
		full, err = arbiter.CompileFullFile(bundlePath)
		if err != nil {
			return nil, err
		}
	}

	if len(suite.Tests) > 0 {
		if suiteNeedsFlags(suite) {
			flagSet, err = flags.LoadFile(bundlePath)
			if err != nil {
				return nil, err
			}
			flagsLoaded = true
		}
	}

	if len(suite.Scenarios) > 0 {
		expertProg, err = expert.CompileFile(bundlePath)
		if err != nil {
			return nil, err
		}
	}

	for _, test := range suite.Tests {
		caseResult := runTestCase(test, full, flagSet, flagsLoaded)
		result.Cases = append(result.Cases, caseResult)
	}
	for _, scenario := range suite.Scenarios {
		caseResult := runScenario(scenario, bundlePath, full, expertProg)
		result.Cases = append(result.Cases, caseResult)
	}

	for _, item := range result.Cases {
		if item.Passed {
			result.Passed++
		} else {
			result.Failed++
		}
	}

	return result, nil
}

func runTestCase(test TestCase, full *arbiter.CompileResult, flagSet *flags.Flags, flagsLoaded bool) CaseResult {
	result := CaseResult{Name: test.Name, Kind: "test", Passed: true}
	if full == nil || full.Ruleset == nil {
		result.Passed = false
		result.Error = "missing compiled bundle"
		return result
	}

	dc := arbiter.DataFromMap(test.Given, full.Ruleset)
	matched, _, err := arbiter.EvalGoverned(full.Ruleset, dc, full.Segments, test.Given)
	if err != nil {
		result.Passed = false
		result.Error = err.Error()
		return result
	}

	for _, expectation := range test.Expectations {
		if expectation.Kind == ExpectFlag && !flagsLoaded {
			result.Passed = false
			result.Error = "flag expectation requires loaded flags"
			return result
		}
		details, err := evaluateStatelessExpectation(expectation, matched, flagSet, test.Given)
		if err != nil {
			result.Passed = false
			result.Details = append(result.Details, details...)
			result.Error = err.Error()
			return result
		}
		result.Details = append(result.Details, details...)
	}

	return result
}

func runScenario(scenario Scenario, bundlePath string, full *arbiter.CompileResult, program *expert.Program) CaseResult {
	if scenarioUsesContinuous(scenario) {
		return runContinuousScenario(scenario, bundlePath, full)
	}
	return runExpertScenario(scenario, program)
}

func runExpertScenario(scenario Scenario, program *expert.Program) CaseResult {
	result := CaseResult{Name: scenario.Name, Kind: "scenario", Passed: true}
	if program == nil {
		result.Passed = false
		result.Error = "missing compiled expert program"
		return result
	}

	baseTime := time.Unix(0, 0).UTC()
	currentTime := baseTime
	envelope := cloneMap(scenario.Given)
	session := expert.NewSession(program, envelope, nil, expert.Options{
		Now: func() time.Time { return currentTime },
	})

	for _, fact := range scenario.Assertions {
		if err := session.Assert(expert.Fact{Type: fact.Type, Key: fact.Key, Fields: cloneMap(fact.Fields)}); err != nil {
			result.Passed = false
			result.Error = err.Error()
			return result
		}
	}

	for _, step := range scenario.Steps {
		mark := session.Checkpoint()
		if step.HasAtOffset {
			currentTime = baseTime.Add(step.AtOffset)
		}
		if len(step.Given) > 0 {
			mergeMap(envelope, step.Given)
			session.SetEnvelope(envelope)
		}
		for _, fact := range step.Assertions {
			if err := session.Assert(expert.Fact{Type: fact.Type, Key: fact.Key, Fields: cloneMap(fact.Fields)}); err != nil {
				result.Passed = false
				result.Error = err.Error()
				return result
			}
		}
		snapshot, err := session.Run(context.Background())
		if err != nil {
			result.Passed = false
			result.Error = err.Error()
			return result
		}
		delta := session.DeltaSince(mark)
		for _, expectation := range step.Expectations {
			details, err := evaluateScenarioExpectation(expectation, snapshot, delta)
			if err != nil {
				result.Passed = false
				result.Details = append(result.Details, details...)
				result.Error = err.Error()
				return result
			}
			result.Details = append(result.Details, details...)
		}
	}

	return result
}

func runContinuousScenario(scenario Scenario, bundlePath string, full *arbiter.CompileResult) CaseResult {
	result := CaseResult{Name: scenario.Name, Kind: "scenario", Passed: true}
	if full == nil {
		result.Passed = false
		result.Error = "missing compiled bundle"
		return result
	}
	if err := validateContinuousScenario(scenario); err != nil {
		result.Passed = false
		result.Error = err.Error()
		return result
	}

	currentTime := time.Unix(0, 0).UTC()
	w, err := workflow.CompileFile(bundlePath, workflow.Options{
		Envelope: func(arbiter.ArbiterDeclaration) map[string]any {
			return cloneMap(scenario.Given)
		},
		SessionOptions: func(arbiter.ArbiterDeclaration) expert.Options {
			return expert.Options{
				Now: func() time.Time { return currentTime },
			}
		},
	})
	if err != nil {
		result.Passed = false
		result.Error = err.Error()
		return result
	}

	streamTargets := scenarioStreamTargets(full.Arbiters)
	history := make([]timedOutcome, 0)
	seenEvent := false
	eventCount := 0
	streamFacts := make(map[string][]expert.Fact)

	for _, step := range scenario.Steps {
		switch {
		case len(step.StreamEvents) > 0:
			for _, event := range step.StreamEvents {
				if seenEvent {
					currentTime = currentTime.Add(time.Second)
				}
				seenEvent = true
				targets := streamTargets[event.Name]
				if len(targets) == 0 {
					result.Passed = false
					result.Error = fmt.Sprintf("stream event %q does not match any arbiter trigger", event.Name)
					return result
				}
				if err := applyContinuousEventEnvelopes(w, full.Arbiters, scenario.Given, event, targets); err != nil {
					result.Passed = false
					result.Error = err.Error()
					return result
				}
				eventCount++
				fact, err := streamEventFact(event, eventCount)
				if err != nil {
					result.Passed = false
					result.Error = err.Error()
					return result
				}
				streamFacts[event.Name] = append(streamFacts[event.Name], fact)
				if err := w.SetSourceFacts(event.Name, streamFacts[event.Name]); err != nil && !strings.Contains(err.Error(), "is not declared") {
					result.Passed = false
					result.Error = err.Error()
					return result
				}
				runResult, err := w.Run(context.Background())
				if err != nil {
					result.Passed = false
					result.Error = err.Error()
					return result
				}
				history = append(history, collectTimedOutcomes(runResult, currentTime)...)
			}
		case step.HasWithin:
			windowStart := currentTime.Add(-step.WithinWindow)
			for _, expectation := range step.Expectations {
				details, err := evaluateContinuousExpectation(expectation, history, windowStart, currentTime)
				if err != nil {
					result.Passed = false
					result.Details = append(result.Details, details...)
					result.Error = err.Error()
					return result
				}
				result.Details = append(result.Details, details...)
			}
		}
	}

	return result
}

func evaluateStatelessExpectation(expectation Expectation, matched []vm.MatchedRule, flagSet *flags.Flags, ctx map[string]any) ([]string, error) {
	switch expectation.Kind {
	case ExpectRule:
		ruleMatched := false
		for _, item := range matched {
			if item.Name == expectation.Target && !item.Fallback {
				ruleMatched = true
				break
			}
		}
		if ruleMatched != expectation.RuleMatched {
			want := "matched"
			if !expectation.RuleMatched {
				want = "not matched"
			}
			return nil, fmt.Errorf("expected rule %s %s", expectation.Target, want)
		}
		return []string{fmt.Sprintf("rule %s ok", expectation.Target)}, nil
	case ExpectAction:
		for _, item := range matched {
			if item.Action != expectation.Target {
				continue
			}
			ok, err := matchFields(item.Params, expectation.Fields)
			if err != nil {
				return nil, err
			}
			if ok {
				return []string{fmt.Sprintf("action %s ok", expectation.Target)}, nil
			}
		}
		return nil, fmt.Errorf("expected action %s", expectation.Target)
	case ExpectFlag:
		if flagSet == nil {
			return nil, fmt.Errorf("flag set is not available")
		}
		got := flagSet.VariantName(expectation.Target, ctx)
		if !valuesEqual(got, expectation.Value) {
			return nil, fmt.Errorf("expected flag %s == %v, got %v", expectation.Target, expectation.Value, got)
		}
		return []string{fmt.Sprintf("flag %s ok", expectation.Target)}, nil
	default:
		return nil, fmt.Errorf("unsupported stateless expectation kind %s", expectation.Kind)
	}
}

func evaluateScenarioExpectation(expectation Expectation, snapshot expert.Result, delta expert.Result) ([]string, error) {
	switch expectation.Kind {
	case ExpectFact:
		match := false
		for _, fact := range snapshot.Facts {
			if fact.Type != expectation.Target {
				continue
			}
			candidate := cloneMap(fact.Fields)
			if candidate == nil {
				candidate = make(map[string]any)
			}
			candidate["key"] = fact.Key
			ok, err := matchFields(candidate, expectation.Fields)
			if err != nil {
				return nil, err
			}
			if ok {
				match = true
				break
			}
		}
		if expectation.Negated && match {
			return nil, fmt.Errorf("expected no fact %s", expectation.Target)
		}
		if !expectation.Negated && !match {
			return nil, fmt.Errorf("expected fact %s", expectation.Target)
		}
		return []string{fmt.Sprintf("fact %s ok", expectation.Target)}, nil
	case ExpectOutcome:
		match := false
		for _, outcome := range delta.Outcomes {
			if outcome.Name != expectation.Target {
				continue
			}
			ok, err := matchFields(outcome.Params, expectation.Fields)
			if err != nil {
				return nil, err
			}
			if ok {
				match = true
				break
			}
		}
		if expectation.Negated && match {
			return nil, fmt.Errorf("expected no outcome %s", expectation.Target)
		}
		if !expectation.Negated && !match {
			return nil, fmt.Errorf("expected outcome %s", expectation.Target)
		}
		return []string{fmt.Sprintf("outcome %s ok", expectation.Target)}, nil
	default:
		return nil, fmt.Errorf("unsupported scenario expectation kind %s", expectation.Kind)
	}
}

func evaluateContinuousExpectation(expectation Expectation, history []timedOutcome, windowStart time.Time, windowEnd time.Time) ([]string, error) {
	if expectation.Kind != ExpectOutcome {
		return nil, fmt.Errorf("continuous scenario windows only support outcome expectations")
	}
	match := false
	for _, item := range history {
		if item.At.Before(windowStart) || item.At.After(windowEnd) {
			continue
		}
		if item.Outcome.Name != expectation.Target {
			continue
		}
		ok, err := matchFields(item.Outcome.Params, expectation.Fields)
		if err != nil {
			return nil, err
		}
		if ok {
			match = true
			break
		}
	}
	if expectation.Negated && match {
		return nil, fmt.Errorf("expected no outcome %s within %s", expectation.Target, windowEnd.Sub(windowStart))
	}
	if !expectation.Negated && !match {
		return nil, fmt.Errorf("expected outcome %s within %s", expectation.Target, windowEnd.Sub(windowStart))
	}
	return []string{fmt.Sprintf("outcome %s ok within %s", expectation.Target, windowEnd.Sub(windowStart))}, nil
}

func matchFields(actual map[string]any, expected map[string]FieldExpectation) (bool, error) {
	if len(expected) == 0 {
		return true, nil
	}
	for key, field := range expected {
		value, ok := actual[key]
		if !ok {
			return false, nil
		}
		match, err := matchField(value, field)
		if err != nil {
			return false, err
		}
		if !match {
			return false, nil
		}
	}
	return true, nil
}

func matchField(actual any, field FieldExpectation) (bool, error) {
	switch field.Kind {
	case FieldExact:
		return valuesEqual(actual, field.Value), nil
	case FieldGt, FieldGte, FieldLt, FieldLte:
		cmp, err := orderedCompare(actual, field.Value)
		if err != nil {
			return false, err
		}
		switch field.Kind {
		case FieldGt:
			return cmp > 0, nil
		case FieldGte:
			return cmp >= 0, nil
		case FieldLt:
			return cmp < 0, nil
		default:
			return cmp <= 0, nil
		}
	case FieldBetween:
		lowCmp, err := orderedCompare(actual, field.Value)
		if err != nil {
			return false, err
		}
		highCmp, err := orderedCompare(actual, field.High)
		if err != nil {
			return false, err
		}
		return lowCmp >= 0 && highCmp <= 0, nil
	default:
		return false, fmt.Errorf("unsupported field matcher %s", field.Kind)
	}
}

func valuesEqual(left, right any) bool {
	switch l := left.(type) {
	case float64:
		r, ok := numericValue(right)
		return ok && abs(l-r) < 1e-9
	case dec.Value:
		r, ok := right.(dec.Value)
		return ok && l.Equal(r)
	case []any:
		r, ok := right.([]any)
		return ok && reflect.DeepEqual(l, r)
	default:
		return reflect.DeepEqual(normalizeValue(left), normalizeValue(right))
	}
}

func orderedCompare(left, right any) (int, error) {
	switch l := left.(type) {
	case dec.Value:
		r, ok := right.(dec.Value)
		if !ok {
			return 0, fmt.Errorf("expected decimal comparison against decimal, got %T", right)
		}
		return l.Cmp(r)
	case string:
		r, ok := right.(string)
		if !ok {
			return 0, fmt.Errorf("expected string comparison against string, got %T", right)
		}
		return strings.Compare(l, r), nil
	default:
		ln, lok := numericValue(left)
		rn, rok := numericValue(right)
		if !lok || !rok {
			return 0, fmt.Errorf("expected ordered values, got %T and %T", left, right)
		}
		switch {
		case ln < rn:
			return -1, nil
		case ln > rn:
			return 1, nil
		default:
			return 0, nil
		}
	}
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}

func normalizeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out[key] = normalizeValue(v[key])
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = normalizeValue(v[i])
		}
		return out
	default:
		return value
	}
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		nested, ok := value.(map[string]any)
		if ok {
			dst[key] = cloneMap(nested)
			continue
		}
		dst[key] = value
	}
	return dst
}

func suiteNeedsContinuous(suite *Suite) bool {
	if suite == nil {
		return false
	}
	for _, scenario := range suite.Scenarios {
		if scenarioUsesContinuous(scenario) {
			return true
		}
	}
	return false
}

func scenarioUsesContinuous(scenario Scenario) bool {
	for _, step := range scenario.Steps {
		if len(step.StreamEvents) > 0 || step.HasWithin {
			return true
		}
	}
	return false
}

func validateContinuousScenario(scenario Scenario) error {
	if len(scenario.Assertions) > 0 {
		return fmt.Errorf("continuous scenarios do not support top-level assert blocks")
	}
	for _, step := range scenario.Steps {
		if step.HasAtOffset || step.Stabilize {
			return fmt.Errorf("continuous scenarios do not support at T+... or after stabilization steps")
		}
		if len(step.Assertions) > 0 {
			return fmt.Errorf("continuous scenarios do not support assert steps")
		}
		if len(step.Given) > 0 {
			return fmt.Errorf("continuous scenarios do not support per-step given assignments")
		}
		if !step.HasWithin && len(step.StreamEvents) == 0 {
			return fmt.Errorf("continuous scenarios only support stream and within steps")
		}
	}
	return nil
}

func scenarioStreamTargets(arbiters []arbiter.ArbiterDeclaration) map[string][]string {
	out := make(map[string][]string)
	for _, decl := range arbiters {
		for _, trigger := range decl.Triggers {
			if trigger.Kind != arbiter.ArbiterTriggerStream {
				continue
			}
			out[trigger.Target] = append(out[trigger.Target], decl.Name)
		}
	}
	return out
}

func applyContinuousEventEnvelopes(w *workflow.Workflow, arbiters []arbiter.ArbiterDeclaration, base map[string]any, event StreamEvent, targets []string) error {
	targetSet := make(map[string]struct{}, len(targets))
	for _, name := range targets {
		targetSet[name] = struct{}{}
	}
	for _, decl := range arbiters {
		envelope := cloneMap(base)
		if _, ok := targetSet[decl.Name]; ok {
			if envelope == nil {
				envelope = make(map[string]any)
			}
			envelope[event.Name] = cloneMap(event.Fields)
		}
		if err := w.SetArbiterEnvelope(decl.Name, envelope); err != nil {
			return err
		}
	}
	return nil
}

func streamEventFact(event StreamEvent, ordinal int) (expert.Fact, error) {
	fields := cloneMap(event.Fields)
	if fields == nil {
		fields = make(map[string]any)
	}
	key := fmt.Sprintf("%s-%d", event.Name, ordinal)
	if raw, ok := fields["key"]; ok {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return expert.Fact{}, fmt.Errorf("stream event %q key must be a non-empty string", event.Name)
		}
		key = text
		delete(fields, "key")
	}
	return expert.Fact{
		Type:   streamEventFactType(event.Name),
		Key:    key,
		Fields: fields,
	}, nil
}

func streamEventFactType(name string) string {
	var b strings.Builder
	upperNext := true
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			upperNext = true
			continue
		}
		if upperNext {
			b.WriteRune(unicode.ToUpper(r))
			upperNext = false
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "Event"
	}
	return b.String()
}

func collectTimedOutcomes(result workflow.Result, at time.Time) []timedOutcome {
	if len(result.Order) == 0 {
		return nil
	}
	out := make([]timedOutcome, 0)
	for _, name := range result.Order {
		run := result.Arbiters[name]
		for _, outcome := range run.Delta.Outcomes {
			out = append(out, timedOutcome{
				Arbiter: name,
				At:      at,
				Outcome: outcome,
			})
		}
	}
	return out
}

func suiteNeedsFlags(suite *Suite) bool {
	if suite == nil {
		return false
	}
	for _, test := range suite.Tests {
		for _, expectation := range test.Expectations {
			if expectation.Kind == ExpectFlag {
				return true
			}
		}
	}
	return false
}

func bundlePath(testPath string) (string, error) {
	clean := filepath.Clean(testPath)
	if !strings.HasSuffix(clean, ".test.arb") {
		return "", fmt.Errorf("test file %s must end with .test.arb", clean)
	}
	return strings.TrimSuffix(clean, ".test.arb") + ".arb", nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
