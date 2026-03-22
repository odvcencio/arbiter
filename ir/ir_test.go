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
