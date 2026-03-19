package runtimebench

import (
	"context"
	"testing"

	"github.com/google/cel-go/cel"
	arbiter "github.com/odvcencio/arbiter"
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
