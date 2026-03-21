package expert

import (
	"context"
	"strings"
	"testing"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/govern"
)

func TestStableRulesDeferUntilAfterAQuiescentRound(t *testing.T) {
	program := mustManualProgram(t, []byte(`
rule SeedMarker {
	when { input.go == true }
	then SeedAction {
		key: "marker-1",
		level: "high",
	}
}

rule CheckClear {
	when { true }
	then ClearAction {
		status: "clear",
	}
}
`), []Rule{
		{
			Name:   "SeedMarker",
			Kind:   ActionAssert,
			Target: "Marker",
		},
		{
			Name:   "CheckClear",
			Kind:   ActionEmit,
			Target: "AllClear",
			Stable: true,
		},
	})

	result, err := NewSession(program, map[string]any{
		"input": map[string]any{"go": true},
	}, nil, Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Rounds != 3 {
		t.Fatalf("expected stable rule to wait until round 3, got %d", result.Rounds)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Name != "AllClear" {
		t.Fatalf("expected one stable outcome, got %+v", result.Outcomes)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("expected one asserted fact, got %+v", result.Facts)
	}
	if result.Facts[0].AssertedRound != 1 {
		t.Fatalf("expected asserted fact round 1, got %+v", result.Facts[0])
	}
}

func TestTemporalFactsExposeAssertedRoundAndCurrentRound(t *testing.T) {
	program, err := Compile([]byte(`
expert rule SeedMarker {
	when { input.go == true }
	then assert Marker {
		key: "marker-1",
		level: "high",
	}
}

expert rule CheckAge {
	when {
		any marker in facts.Marker {
			marker.__round < current_round
		}
	}
	then emit Aged {
		rounds_old: current_round,
	}
}
`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	result, err := NewSession(program, map[string]any{
		"input": map[string]any{"go": true},
	}, nil, Options{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Rounds != 2 {
		t.Fatalf("expected two rounds, got %d", result.Rounds)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Name != "Aged" {
		t.Fatalf("expected one temporal outcome, got %+v", result.Outcomes)
	}
	if got, ok := result.Outcomes[0].Params["rounds_old"].(float64); !ok || got != 2 {
		t.Fatalf("expected current_round to be 2, got %+v", result.Outcomes[0].Params["rounds_old"])
	}
	if len(result.Facts) != 1 {
		t.Fatalf("expected one fact, got %+v", result.Facts)
	}
	if result.Facts[0].AssertedRound != 1 {
		t.Fatalf("expected asserted round 1, got %+v", result.Facts[0])
	}
	if result.Facts[0].AssertedAt == 0 {
		t.Fatalf("expected asserted_at to be populated, got %+v", result.Facts[0])
	}
}

func TestTemporalWindowsWakeOnClockAdvance(t *testing.T) {
	program, err := Compile([]byte(`
expert rule SeedMarker {
	when { input.go == true }
	then assert Marker {
		key: "marker-1",
		level: "high",
	}
}

expert rule CheckAge {
	when {
		any marker in facts.Marker {
			marker.__age_seconds >= 60
		}
	}
	then emit Aged {
		age_seconds: marker.__age_seconds,
		now: __now,
		asserted_at: marker.__asserted_at,
	}
}
`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	now := time.Unix(1_000, 0).UTC()
	session := NewSession(program, map[string]any{
		"input": map[string]any{"go": true},
	}, nil, Options{
		Now: func() time.Time { return now },
	})

	first, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if len(first.Outcomes) != 0 {
		t.Fatalf("expected no aged outcome on first run, got %+v", first.Outcomes)
	}
	if len(first.Facts) != 1 {
		t.Fatalf("expected one fact on first run, got %+v", first.Facts)
	}
	if first.Facts[0].AssertedAt != 1_000 {
		t.Fatalf("expected asserted_at 1000, got %+v", first.Facts[0])
	}

	now = time.Unix(1_070, 0).UTC()
	second, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(second.Outcomes) != 1 || second.Outcomes[0].Name != "Aged" {
		t.Fatalf("expected one aged outcome after clock advance, got %+v", second.Outcomes)
	}
	if got := second.Outcomes[0].Params["age_seconds"]; got != float64(70) {
		t.Fatalf("expected age_seconds 70, got %#v", got)
	}
	if got := second.Outcomes[0].Params["now"]; got != float64(1070) {
		t.Fatalf("expected __now 1070, got %#v", got)
	}
	if got := second.Outcomes[0].Params["asserted_at"]; got != float64(1000) {
		t.Fatalf("expected asserted_at 1000, got %#v", got)
	}
}

func TestSetEnvelopeForcesReevaluationWithoutFactChanges(t *testing.T) {
	program, err := Compile([]byte(`
expert rule HaltOnSourceFailure {
	when {
		source.feed.available == false
	}
	then emit Halt {
		reason: "source unavailable",
	}
}
`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := NewSession(program, map[string]any{
		"source": map[string]any{
			"feed": map[string]any{
				"available": true,
			},
		},
	}, nil, Options{})

	first, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if len(first.Outcomes) != 0 {
		t.Fatalf("expected no outcomes on first run, got %+v", first.Outcomes)
	}

	session.SetEnvelope(map[string]any{
		"source": map[string]any{
			"feed": map[string]any{
				"available": false,
			},
		},
	})
	second, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(second.Outcomes) != 1 || second.Outcomes[0].Name != "Halt" {
		t.Fatalf("expected one Halt outcome after envelope update, got %+v", second.Outcomes)
	}
}

func TestSyncFactsAddsUpdatesAndRetractsExternalFacts(t *testing.T) {
	program, err := Compile([]byte(`
expert rule FlagHighScore {
	when {
		any lead in facts.Lead {
			lead.score >= 90
		}
	}
	then assert QualifiedLead {
		key: lead.key,
		score: lead.score,
	}
}
`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	session := NewSession(program, nil, nil, Options{})
	first, err := session.SyncFacts([]Fact{{
		Type: "Lead",
		Key:  "a",
		Fields: map[string]any{
			"score": float64(95),
		},
	}})
	if err != nil {
		t.Fatalf("first SyncFacts: %v", err)
	}
	if first.Added != 1 || first.Updated != 0 || first.Retracted != 0 || !first.Changed {
		t.Fatalf("unexpected first sync summary: %+v", first)
	}

	result, err := session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run after first sync: %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("expected lead and qualified lead after first sync, got %+v", result.Facts)
	}

	second, err := session.SyncFacts([]Fact{{
		Type: "Lead",
		Key:  "a",
		Fields: map[string]any{
			"score": float64(80),
		},
	}})
	if err != nil {
		t.Fatalf("second SyncFacts: %v", err)
	}
	if second.Added != 0 || second.Updated != 1 || second.Retracted != 0 || !second.Changed {
		t.Fatalf("unexpected second sync summary: %+v", second)
	}

	result, err = session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run after second sync: %v", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Type != "Lead" {
		t.Fatalf("expected only updated lead after lowering score, got %+v", result.Facts)
	}

	third, err := session.SyncFacts(nil)
	if err != nil {
		t.Fatalf("third SyncFacts: %v", err)
	}
	if third.Added != 0 || third.Updated != 0 || third.Retracted != 1 || !third.Changed {
		t.Fatalf("unexpected third sync summary: %+v", third)
	}

	result, err = session.Run(context.Background())
	if err != nil {
		t.Fatalf("Run after retract sync: %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("expected no facts after retract sync, got %+v", result.Facts)
	}
}

func TestSyncFactsRejectsDuplicateInput(t *testing.T) {
	session := NewSession(&Program{}, nil, nil, Options{})
	_, err := session.SyncFacts([]Fact{
		{Type: "Lead", Key: "dup"},
		{Type: "Lead", Key: "dup"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate fact Lead/dup") {
		t.Fatalf("expected duplicate fact error, got %v", err)
	}
}

func TestShouldEvaluateSkipsSelfOnlyNoLoopFactChanges(t *testing.T) {
	session := &Session{}
	rule := Rule{
		Name:     "RefreshQualifiedLead",
		FactDeps: []string{"Lead"},
		NoLoop:   true,
	}

	shouldEval := session.shouldEvaluate(
		rule,
		map[string]struct{}{"Lead": {}},
		map[string]map[string]struct{}{
			"Lead": {"RefreshQualifiedLead": {}},
		},
		nil,
	)
	if shouldEval {
		t.Fatal("expected no_loop rule to skip self-only fact changes")
	}
}

func TestShouldEvaluateAllowsExternalFactChangesForNoLoopRules(t *testing.T) {
	session := &Session{}
	rule := Rule{
		Name:     "RefreshQualifiedLead",
		FactDeps: []string{"Lead"},
		NoLoop:   true,
	}

	shouldEval := session.shouldEvaluate(
		rule,
		map[string]struct{}{"Lead": {}},
		map[string]map[string]struct{}{
			"Lead": {
				"RefreshQualifiedLead": {},
				"SyncFacts":            {},
			},
		},
		nil,
	)
	if !shouldEval {
		t.Fatal("expected no_loop rule to reevaluate when another source dirties the fact dependency")
	}
}

func TestShouldEvaluateAllowsDirtyPrereqsEvenForNoLoopRules(t *testing.T) {
	session := &Session{}
	rule := Rule{
		Name:     "EscalateLead",
		Prereqs:  []string{"QualifiedLead"},
		FactDeps: []string{"Lead"},
		NoLoop:   true,
	}

	shouldEval := session.shouldEvaluate(
		rule,
		nil,
		nil,
		map[string]struct{}{"QualifiedLead": {}},
	)
	if !shouldEval {
		t.Fatal("expected dirty prereq to force reevaluation for no_loop rule")
	}
}

func TestSetDerivedSupportPreservesAssertionMetadataForUnchangedFacts(t *testing.T) {
	session := NewSession(&Program{}, nil, nil, Options{})
	rule := Rule{Name: "PrimarySupport", Priority: 20}

	session.rounds = 1
	session.evalNow = 100
	changed := session.setDerivedSupport(rule, Fact{
		Type: "RiskFlag",
		Key:  "shared",
		Fields: map[string]any{
			"level": "high",
		},
	})
	if !changed {
		t.Fatal("expected first support insertion to change working memory")
	}
	first, ok := session.currentFact("RiskFlag", "shared")
	if !ok {
		t.Fatal("expected derived fact after first support")
	}
	if first.AssertedRound != 1 || first.AssertedAt != 100 {
		t.Fatalf("expected first assertion metadata to be recorded, got %+v", first)
	}

	session.clearDirtyFacts()
	session.rounds = 2
	session.evalNow = 200
	changed = session.setDerivedSupport(rule, Fact{
		Type: "RiskFlag",
		Key:  "shared",
		Fields: map[string]any{
			"level": "high",
		},
	})
	if changed {
		t.Fatal("expected unchanged support update to avoid a working-memory mutation")
	}
	second, ok := session.currentFact("RiskFlag", "shared")
	if !ok {
		t.Fatal("expected derived fact after repeated support")
	}
	if second.AssertedRound != 1 || second.AssertedAt != 100 {
		t.Fatalf("expected assertion metadata to be preserved, got %+v", second)
	}
}

func TestRecomputeFactUpdatesDerivedByWithoutResettingAssertionMetadata(t *testing.T) {
	session := NewSession(&Program{}, nil, nil, Options{})
	session.facts["RiskFlag"] = map[string]Fact{
		"shared": {
			Type:          "RiskFlag",
			Key:           "shared",
			Fields:        map[string]any{"level": "high"},
			DerivedBy:     []string{"AlphaSupport"},
			AssertedRound: 3,
			AssertedAt:    100,
		},
	}
	session.addSupportRecord(supportRecord{
		Instance: "alpha:shared",
		Rule:     "AlphaSupport",
		Priority: 20,
		Fact: Fact{
			Type:          "RiskFlag",
			Key:           "shared",
			Fields:        map[string]any{"level": "high"},
			AssertedRound: 3,
			AssertedAt:    100,
		},
	})
	session.addSupportRecord(supportRecord{
		Instance: "beta:shared",
		Rule:     "BetaSupport",
		Priority: 10,
		Fact: Fact{
			Type:          "RiskFlag",
			Key:           "shared",
			Fields:        map[string]any{"level": "high"},
			AssertedRound: 4,
			AssertedAt:    200,
		},
	})
	session.clearDirtyFacts()

	changed := session.recomputeFact("RiskFlag", "shared", "AlphaSupport")
	if changed {
		t.Fatal("expected provenance-only recompute to avoid a working-memory mutation")
	}
	current, ok := session.currentFact("RiskFlag", "shared")
	if !ok {
		t.Fatal("expected recomputed derived fact")
	}
	if current.AssertedRound != 3 || current.AssertedAt != 100 {
		t.Fatalf("expected assertion metadata to be preserved, got %+v", current)
	}
	if len(current.DerivedBy) != 2 || current.DerivedBy[0] != "AlphaSupport" || current.DerivedBy[1] != "BetaSupport" {
		t.Fatalf("expected updated support provenance, got %+v", current)
	}
	if len(session.dirtyFacts) != 0 {
		t.Fatalf("expected provenance-only recompute to leave dirty facts empty, got %+v", session.dirtyFacts)
	}
}

func TestRefreshContextViewFirstPassRebuildsAllFactTypes(t *testing.T) {
	now := time.Unix(120, 0).UTC()
	session := NewSession(&Program{}, map[string]any{"tenant": "acme"}, nil, Options{
		Now: func() time.Time { return now },
	})
	session.rounds = 2
	session.evalNow = now.Unix()
	session.facts = map[string]map[string]Fact{
		"Lead": {
			"a": {
				Type:          "Lead",
				Key:           "a",
				Fields:        map[string]any{"score": float64(95)},
				AssertedRound: 1,
				AssertedAt:    100,
			},
		},
		"Marker": {
			"m1": {
				Type:          "Marker",
				Key:           "m1",
				Fields:        map[string]any{"kind": "high"},
				AssertedRound: 2,
				AssertedAt:    110,
			},
		},
	}

	session.refreshContextView(true, nil)

	if _, ok := session.factsView["Lead"]; !ok {
		t.Fatalf("expected Lead facts in view, got %+v", session.factsView)
	}
	if _, ok := session.factsView["Marker"]; !ok {
		t.Fatalf("expected Marker facts in view, got %+v", session.factsView)
	}
	factsValue, ok := session.evalCtx["facts"].(map[string]any)
	if !ok {
		t.Fatalf("expected facts map in eval context, got %+v", session.evalCtx["facts"])
	}
	if _, ok := factsValue["Lead"]; !ok {
		t.Fatalf("expected Lead in eval context facts, got %+v", factsValue)
	}
	if got := session.evalCtx["current_round"]; got != float64(2) {
		t.Fatalf("expected current_round 2, got %#v", got)
	}
	if got := session.evalCtx["__now"]; got != float64(120) {
		t.Fatalf("expected __now 120, got %#v", got)
	}
}

func TestRefreshContextViewDeletesMissingDirtyFactTypes(t *testing.T) {
	session := NewSession(&Program{}, nil, nil, Options{})
	session.factsView["Lead"] = []any{map[string]any{"key": "stale"}}
	session.facts = map[string]map[string]Fact{}

	session.refreshContextView(false, map[string]struct{}{"Lead": {}})

	if _, ok := session.factsView["Lead"]; ok {
		t.Fatalf("expected stale Lead view to be removed, got %+v", session.factsView)
	}
}

func TestEvalRuleSkipsWhenSegmentDoesNotMatch(t *testing.T) {
	program := mustCompiledProgram(t, []byte(`
segment high_risk { applicant.score < 600 }

expert rule RouteReview {
	when segment high_risk { true }
	then emit ManualReview {
		queue: "risk",
	}
}
`))

	session := NewSession(program, map[string]any{
		"applicant": map[string]any{
			"score": float64(650),
		},
	}, nil, Options{})
	session.refreshContextView(true, nil)

	ok, match, err := session.evalRule(
		program.rules[0],
		program.ruleset.Rules[0],
		session.evaluator,
		session.dc,
		govern.NewRequestCache(program.segments, session.evalCtx),
	)
	if err != nil {
		t.Fatalf("evalRule: %v", err)
	}
	if ok {
		t.Fatalf("expected segment mismatch to skip rule, got match %+v", match)
	}
}

func TestEvalRuleSkipsWhenRolloutSubjectMissing(t *testing.T) {
	program := mustCompiledProgram(t, []byte(`
expert rule RouteReview {
	when { true }
	then emit ManualReview {
		queue: "risk",
	}
	rollout percent 100 by applicant.id namespace "expert.test"
}
`))

	session := NewSession(program, nil, nil, Options{})
	session.refreshContextView(true, nil)

	ok, match, err := session.evalRule(
		program.rules[0],
		program.ruleset.Rules[0],
		session.evaluator,
		session.dc,
		govern.NewRequestCache(program.segments, session.evalCtx),
	)
	if err != nil {
		t.Fatalf("evalRule: %v", err)
	}
	if ok {
		t.Fatalf("expected missing rollout subject to skip rule, got match %+v", match)
	}
}

func mustManualProgram(t *testing.T, source []byte, rules []Rule) *Program {
	t.Helper()

	compiled, err := arbiter.CompileFull(source)
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
	if compiled == nil || compiled.Ruleset == nil {
		t.Fatal("expected compiled ruleset")
	}
	if len(compiled.Ruleset.Rules) != len(rules) {
		t.Fatalf("expected %d compiled rules, got %d", len(rules), len(compiled.Ruleset.Rules))
	}

	byName := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		byName[rule.Name] = rule
	}

	return &Program{
		ruleset:    compiled.Ruleset,
		segments:   compiled.Segments,
		rules:      append([]Rule(nil), rules...),
		ruleByName: byName,
	}
}

func mustCompiledProgram(t *testing.T, source []byte) *Program {
	t.Helper()

	program, err := Compile(source)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if program == nil || program.ruleset == nil || len(program.rules) == 0 {
		t.Fatalf("expected compiled expert program, got %+v", program)
	}
	return program
}
