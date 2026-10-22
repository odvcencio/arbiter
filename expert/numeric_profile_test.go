package expert_test

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/vm"
)

const integerExpertSource = `
expert rule Total {
	when {
		sum(f.n for f in facts.Item) + bonus > 5
	}
	then emit Out { total: sum(f.n for f in facts.Item) + bonus }
}
`

func TestExpertSessionIntegerProfileGuard(t *testing.T) {
	p, err := expert.Compile([]byte(integerExpertSource))
	if err != nil {
		t.Fatal(err)
	}
	if v := p.NumericProfileViolations(); len(v) != 0 {
		t.Fatalf("source should conform: %v", v)
	}
	run := func(n float64, profile vm.NumericProfile) (expert.Result, error) {
		facts := []expert.Fact{{Type: "Item", Key: "a", Fields: map[string]any{"n": n}}, {Type: "Item", Key: "b", Fields: map[string]any{"n": 4.0}}}
		return expert.NewSession(p, map[string]any{"bonus": 1.0}, facts, expert.Options{NumericProfile: profile}).Run(context.Background())
	}
	res, err := run(2, vm.NumericInteger)
	if err != nil || len(res.Outcomes) != 1 {
		t.Fatalf("integer facts: outcomes=%d err=%v", len(res.Outcomes), err)
	}
	if _, err := run(2.5, vm.NumericInteger); err == nil || !strings.Contains(err.Error(), "integer profile") {
		t.Fatalf("fractional fact must fail under the integer profile, got %v", err)
	}
	if _, err := run(2.5, vm.NumericDefault); err != nil {
		t.Fatalf("default profile must accept fractional facts: %v", err)
	}
}
