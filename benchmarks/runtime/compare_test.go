package runtimebench

import (
	"context"
	"testing"

	"github.com/google/cel-go/cel"
	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/flags"
	"github.com/open-policy-agent/opa/rego"
)

const compareSource = `
rule FreeShipping {
	when {
		cart_total >= 35
		and region != "XX"
	}
	then ApplyShipping {}
}
`

const governedSource = `
segment enterprise {
	user.plan == "enterprise"
}

rule BaseDecision {
	when { user.score >= 600 }
	then Seed { seeded: true }
}

rule EnterpriseDecision {
	requires BaseDecision
	when segment enterprise { user.cart_total >= 100 }
	then Approved { tier: "gold" }
	rollout 100
}
`

const flagSource = `
segment enterprise {
	user.plan == "enterprise"
}

flag checkout_v2 type multivariate default "control" {
	variant "treatment" {
		layout: "single_page",
	}
	when enterprise then "treatment"
}
`

const expertSource = `
expert rule SeedHighRisk {
	when { applicant.score < 600 }
	then assert RiskFlag {
		key: "high_risk",
		level: "high",
	}
}

expert rule RouteManualReview {
	when {
		any risk in facts.RiskFlag { risk.level == "high" }
	}
	then emit ManualReview {
		queue: "risk",
	}
}
`

func BenchmarkCompareArbiterCELOPA(b *testing.B) {
	ctxMap := map[string]any{
		"cart_total": 50.0,
		"region":     "US",
	}

	b.Run("arbiter", func(b *testing.B) {
		rs, err := arbiter.Compile([]byte(compareSource))
		if err != nil {
			b.Fatal(err)
		}
		dc := arbiter.DataFromMap(ctxMap, rs)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			matched, err := arbiter.Eval(rs, dc)
			if err != nil {
				b.Fatal(err)
			}
			if len(matched) != 1 {
				b.Fatalf("expected 1 match, got %d", len(matched))
			}
		}
	})

	b.Run("cel", func(b *testing.B) {
		env, err := cel.NewEnv(
			cel.Variable("cart_total", cel.DoubleType),
			cel.Variable("region", cel.StringType),
		)
		if err != nil {
			b.Fatal(err)
		}
		ast, issues := env.Compile(`cart_total >= 35.0 && region != "XX"`)
		if issues != nil && issues.Err() != nil {
			b.Fatal(issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			b.Fatal(err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, _, err := program.Eval(ctxMap)
			if err != nil {
				b.Fatal(err)
			}
			ok, _ := out.Value().(bool)
			if !ok {
				b.Fatal("expected CEL expression to be true")
			}
		}
	})

	b.Run("opa", func(b *testing.B) {
		query, err := rego.New(
			rego.Query("data.rules.allow"),
			rego.Module("rules.rego", `
package rules
import rego.v1

allow if {
	input.cart_total >= 35
	input.region != "XX"
}
`),
		).PrepareForEval(context.Background())
		if err != nil {
			b.Fatal(err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			results, err := query.Eval(context.Background(), rego.EvalInput(ctxMap))
			if err != nil {
				b.Fatal(err)
			}
			if len(results) == 0 {
				b.Fatal("expected OPA query to allow")
			}
		}
	})
}

func BenchmarkArbiterGovernedRules(b *testing.B) {
	full, err := arbiter.CompileFull([]byte(governedSource))
	if err != nil {
		b.Fatal(err)
	}
	ctxMap := map[string]any{
		"user": map[string]any{
			"plan":       "enterprise",
			"score":      710.0,
			"cart_total": 150.0,
		},
		"user.id": "u_123",
	}
	dc := arbiter.DataFromMap(ctxMap, full.Ruleset)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matched, _, err := arbiter.EvalGoverned(full.Ruleset, dc, full.Segments, ctxMap)
		if err != nil {
			b.Fatal(err)
		}
		if len(matched) != 2 {
			b.Fatalf("expected 2 matches, got %d", len(matched))
		}
	}
}

func BenchmarkArbiterFlagExplain(b *testing.B) {
	f, err := flags.Load([]byte(flagSource))
	if err != nil {
		b.Fatal(err)
	}
	ctxMap := map[string]any{
		"user": map[string]any{
			"plan": "enterprise",
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eval := f.Explain("checkout_v2", ctxMap)
		if eval.Variant.Name != "treatment" {
			b.Fatalf("expected treatment, got %q", eval.Variant.Name)
		}
	}
}

func BenchmarkArbiterExpertSession(b *testing.B) {
	program, err := expert.Compile([]byte(expertSource))
	if err != nil {
		b.Fatal(err)
	}
	envelope := map[string]any{
		"applicant": map[string]any{
			"score": 540.0,
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session := expert.NewSession(program, envelope, nil, expert.Options{})
		result, err := session.Run(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Outcomes) != 1 {
			b.Fatalf("expected 1 outcome, got %d", len(result.Outcomes))
		}
	}
}

func BenchmarkArbiterBundleCompileArtifacts(b *testing.B) {
	source := []byte(governedSource + "\n" + flagSource + "\n" + expertSource)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := arbiter.CompileFull(source); err != nil {
			b.Fatal(err)
		}
		if _, err := flags.Load(source); err != nil {
			b.Fatal(err)
		}
		if _, err := expert.Compile(source); err != nil {
			b.Fatal(err)
		}
	}
}
