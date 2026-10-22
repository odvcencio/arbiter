package expert_test

import (
	"context"
	"fmt"
	"math"
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

func TestIntegerProfileAppliesToSegments(t *testing.T) {
	src := `
segment half { zone.n / 2 + 1 > 1 }

expert rule R {
	when segment half { true }
	then emit Out { k: 1 }
}
`
	p, err := expert.Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if v := p.NumericProfileViolations(); len(v) == 0 {
		t.Fatal("static check must flag the unrounded division in the segment")
	}
	run := func(profile vm.NumericProfile) error {
		_, err := expert.NewSession(p, map[string]any{"zone": map[string]any{"n": 5.0}}, nil, expert.Options{NumericProfile: profile}).Run(context.Background())
		return err
	}
	if err := run(vm.NumericInteger); err == nil || !strings.Contains(err.Error(), "integer profile") {
		t.Fatalf("segment must run under the integer profile, got %v", err)
	}
	if err := run(vm.NumericDefault); err != nil {
		t.Fatalf("default profile must accept it: %v", err)
	}
}

func TestIntegerProfileErrorsReachPerFactRules(t *testing.T) {
	src := `
expert rule Each per_fact {
	when { any f in facts.Item { f.n + 1 > 0 } }
	then emit Out { key: f.key }
}
`
	p, err := expert.Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	facts := []expert.Fact{{Type: "Item", Key: "a", Fields: map[string]any{"n": 1.0}}, {Type: "Item", Key: "b", Fields: map[string]any{"n": 0.5}}}
	_, err = expert.NewSession(p, nil, facts, expert.Options{NumericProfile: vm.NumericInteger}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "integer profile") {
		t.Fatalf("expected the integer profile error, got %v", err)
	}
}

// An expert session reuses one evaluator for its whole life. Before the fix its
// instruction budget accumulated across Run calls and failed after a few
// hundred ticks with 1,000 facts.
func TestSessionBudgetResetsEveryRun(t *testing.T) {
	p, err := expert.Compile([]byte(`
expert rule Total {
	when { sum(f.n for f in facts.Item) > 0 }
	then assert Summary { key: "s", total: sum(f.n for f in facts.Item) }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	var facts []expert.Fact
	for i := 0; i < 1000; i++ {
		facts = append(facts, expert.Fact{Type: "Item", Key: fmt.Sprintf("k%04d", i), Fields: map[string]any{"n": 1.0}})
	}
	s := expert.NewSession(p, nil, facts, expert.Options{})
	for tick := 0; tick < 700; tick++ {
		if err := s.Assert(expert.Fact{Type: "Item", Key: "tick", Fields: map[string]any{"n": float64(tick)}}); err != nil {
			t.Fatalf("tick %d assert: %v", tick, err)
		}
		if _, err := s.Run(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}
}

func TestCompileRejectsDurationsThatOverflowInt64(t *testing.T) {
	for _, d := range []string{"200000d", "9223372036854775807s"} {
		src := fmt.Sprintf("expert rule R { when { true } for %s\n\tthen emit Out { k: 1 } }", d)
		if _, err := expert.Compile([]byte(src)); err == nil {
			t.Fatalf("duration %s must be rejected", d)
		}
	}
	if _, err := expert.Compile([]byte("expert rule R { when { true } for 100000d then emit Out { k: 1 } }")); err != nil {
		t.Fatalf("a duration under the limit must compile: %v", err)
	}
}

func TestTimestampFieldsRejectOutOfRangeFloats(t *testing.T) {
	p, err := expert.Compile([]byte(`
fact Ev {
	key: string
	at: timestamp
}
expert rule R { when { true } then emit Out { k: 1 } }
`))
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]float64{"NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1), "2^63": 1 << 63, "1e30": 1e30, "-1e30": -1e30} {
		s := expert.NewSession(p, nil, nil, expert.Options{})
		if err := s.Assert(expert.Fact{Type: "Ev", Key: "a", Fields: map[string]any{"at": v}}); err == nil {
			t.Fatalf("%s must be rejected for a timestamp field", name)
		}
	}
	s := expert.NewSession(p, nil, nil, expert.Options{})
	if err := s.Assert(expert.Fact{Type: "Ev", Key: "a", Fields: map[string]any{"at": 1.7e9}}); err != nil {
		t.Fatalf("a normal timestamp must be accepted: %v", err)
	}
}
