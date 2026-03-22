package ir_test

import (
	"testing"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/ir"
)

func lowerSource(t *testing.T, source string) *ir.Program {
	t.Helper()
	parsed, err := arbiter.ParseSource([]byte(source))
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	program, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	return program
}

func TestLowerCollectsTopLevelDeclarations(t *testing.T) {
	program := lowerSource(t, `
const MIN_AGE = 18

feature user from "user-service" {
    age: number
    country: string
}

segment US {
    user.country == "US"
}

rule AgeCheck priority 10 {
    requires Upstream
    excludes Blocked
    when segment US {
        user.age >= MIN_AGE
    }
    then approve {
        reason: "adult"
    }
    otherwise deny {}
    rollout percent 50 by user.id namespace "beta"
}
`)

	if got := len(program.Consts); got != 1 {
		t.Fatalf("len(Consts) = %d, want 1", got)
	}
	if got := len(program.Features); got != 1 {
		t.Fatalf("len(Features) = %d, want 1", got)
	}
	if got := len(program.Segments); got != 1 {
		t.Fatalf("len(Segments) = %d, want 1", got)
	}
	if got := len(program.Rules); got != 1 {
		t.Fatalf("len(Rules) = %d, want 1", got)
	}

	rule := program.Rules[0]
	if rule.Name != "AgeCheck" {
		t.Fatalf("rule.Name = %q, want AgeCheck", rule.Name)
	}
	if rule.Priority != 10 {
		t.Fatalf("rule.Priority = %d, want 10", rule.Priority)
	}
	if rule.Segment != "US" {
		t.Fatalf("rule.Segment = %q, want US", rule.Segment)
	}
	if len(rule.Prereqs) != 1 || rule.Prereqs[0] != "Upstream" {
		t.Fatalf("rule.Prereqs = %v, want [Upstream]", rule.Prereqs)
	}
	if len(rule.Excludes) != 1 || rule.Excludes[0] != "Blocked" {
		t.Fatalf("rule.Excludes = %v, want [Blocked]", rule.Excludes)
	}
	if rule.Rollout == nil {
		t.Fatal("rule.Rollout = nil, want rollout")
	}
	if !rule.Rollout.HasBps || rule.Rollout.Bps != 5000 {
		t.Fatalf("rule.Rollout.Bps = %d (has=%v), want 5000", rule.Rollout.Bps, rule.Rollout.HasBps)
	}
	if !rule.Rollout.HasSubject || rule.Rollout.Subject != "user.id" {
		t.Fatalf("rule.Rollout.Subject = %q (has=%v), want user.id", rule.Rollout.Subject, rule.Rollout.HasSubject)
	}
	if !rule.Rollout.HasNamespace || rule.Rollout.Namespace != "beta" {
		t.Fatalf("rule.Rollout.Namespace = %q (has=%v), want beta", rule.Rollout.Namespace, rule.Rollout.HasNamespace)
	}
	if rule.Action.Name != "approve" {
		t.Fatalf("rule.Action.Name = %q, want approve", rule.Action.Name)
	}
	if rule.Fallback == nil || rule.Fallback.Name != "deny" {
		t.Fatalf("rule.Fallback = %#v, want deny", rule.Fallback)
	}
}

func TestLowerResolvesLocalAndConstRefs(t *testing.T) {
	program := lowerSource(t, `
const MAX = 100

rule Shadowed {
    when {
        let LIMIT = 5
        let floor = LIMIT
        any item in cart.items { item.price > floor and floor < MAX }
    }
    then Approve {
        floor: floor
    }
}
`)

	var (
		sawConstRef      bool
		sawLocalRefLimit bool
		sawLocalRefFloor bool
	)
	for _, expr := range program.Exprs {
		if expr.Kind == ir.ExprConstRef && expr.Name == "MAX" {
			sawConstRef = true
		}
		if expr.Kind == ir.ExprLocalRef && expr.Name == "LIMIT" {
			sawLocalRefLimit = true
		}
		if expr.Kind == ir.ExprLocalRef && expr.Name == "floor" {
			sawLocalRefFloor = true
		}
	}

	if !sawConstRef {
		t.Fatal("expected at least one const ref to MAX")
	}
	if !sawLocalRefLimit {
		t.Fatal("expected let shadowing to produce a local ref for LIMIT")
	}
	if !sawLocalRefFloor {
		t.Fatal("expected floor to lower as a local ref")
	}
}

func TestLowerCollectsSchemas(t *testing.T) {
	program := lowerSource(t, `
fact PlantStress {
		temperature: number<temperature>
		note?: string
}

outcome WaterAction {
		zone: string
		liters: number
}
`)

	if got := len(program.FactSchemas); got != 1 {
		t.Fatalf("len(FactSchemas) = %d, want 1", got)
	}
	if got := len(program.OutcomeSchemas); got != 1 {
		t.Fatalf("len(OutcomeSchemas) = %d, want 1", got)
	}

	fact := program.FactSchemas[0]
	if fact.Name != "PlantStress" {
		t.Fatalf("fact.Name = %q, want PlantStress", fact.Name)
	}
	if len(fact.Fields) != 2 {
		t.Fatalf("len(fact.Fields) = %d, want 2", len(fact.Fields))
	}
	if fact.Fields[0].Name != "temperature" || fact.Fields[0].Type.Base != "number" || fact.Fields[0].Type.Dimension != "temperature" || !fact.Fields[0].Required {
		t.Fatalf("unexpected first fact field: %+v", fact.Fields[0])
	}
	if fact.Fields[1].Name != "note" || fact.Fields[1].Type.Base != "string" || fact.Fields[1].Required {
		t.Fatalf("unexpected optional fact field: %+v", fact.Fields[1])
	}

	outcome := program.OutcomeSchemas[0]
	if outcome.Name != "WaterAction" {
		t.Fatalf("outcome.Name = %q, want WaterAction", outcome.Name)
	}
	if len(outcome.Fields) != 2 {
		t.Fatalf("len(outcome.Fields) = %d, want 2", len(outcome.Fields))
	}
	if outcome.Fields[1].Name != "liters" || outcome.Fields[1].Type.Base != "number" {
		t.Fatalf("unexpected outcome field: %+v", outcome.Fields[1])
	}
}

func TestLowerQuantityLiteral(t *testing.T) {
	program := lowerSource(t, `rule HeatStress { when { reading.temperature > 28 C } then Alert {} }`)
	found := false
	for _, expr := range program.Exprs {
		if expr.Kind == ir.ExprQuantityLit {
			found = true
			if expr.Number != 28 || expr.Unit != "C" {
				t.Fatalf("unexpected quantity literal: %+v", expr)
			}
		}
	}
	if !found {
		t.Fatal("expected quantity literal in lowered expressions")
	}
}

func TestLowerBuiltinCallAndTimestampLiteral(t *testing.T) {
	program := lowerSource(t, `rule T { when { now() > 2026-01-01T00:00:00Z and abs(sensor.delta) > 5 } then A {} }`)
	var sawNow, sawAbs, sawTimestamp bool
	for _, expr := range program.Exprs {
		switch expr.Kind {
		case ir.ExprBuiltinCall:
			if expr.FuncName == "now" {
				sawNow = true
			}
			if expr.FuncName == "abs" {
				sawAbs = true
			}
		case ir.ExprTimestampLit:
			if expr.String == "2026-01-01T00:00:00Z" {
				sawTimestamp = true
			}
		}
	}
	if !sawNow || !sawAbs || !sawTimestamp {
		t.Fatalf("expected now, abs, and timestamp literal in lowered expressions, got %+v", program.Exprs)
	}
}

func TestLowerTemporalDurationLiteralInExpr(t *testing.T) {
	program := lowerSource(t, `rule T { when { recorded + 5m > now() } then A {} }`)
	for _, expr := range program.Exprs {
		if expr.Kind == ir.ExprQuantityLit && expr.Unit == "min" && expr.Number == 5 {
			return
		}
	}
	t.Fatal("expected 5m to lower into a time quantity literal")
}

func TestLowerJoinExprDesugarsToNestedQuantifiers(t *testing.T) {
	program := lowerSource(t, `expert rule T { when { join a: Sensor, b: Sensor on .zone { abs(a.temperature - b.temperature) > 5 C } } then emit Alert {} }`)
	quantifiers := 0
	selfExclusion := false
	for _, expr := range program.Exprs {
		if expr.Kind == ir.ExprQuantifier {
			quantifiers++
		}
		if expr.Kind == ir.ExprBinary && expr.BinaryOp == ir.BinaryNeq {
			left := program.Expr(expr.Left)
			right := program.Expr(expr.Right)
			if left != nil && right != nil && left.Path == "a.key" && right.Path == "b.key" {
				selfExclusion = true
			}
		}
	}
	if quantifiers < 2 {
		t.Fatalf("expected nested quantifiers from join lowering, got %d in %+v", quantifiers, program.Exprs)
	}
	if !selfExclusion {
		t.Fatalf("expected self-join exclusion in lowered expressions, got %+v", program.Exprs)
	}
}
