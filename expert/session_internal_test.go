package expert

import (
	"context"
	"testing"

	arbiter "github.com/odvcencio/arbiter"
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
