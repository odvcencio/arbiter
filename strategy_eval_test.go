package arbiter

import (
	"strings"
	"testing"

	"github.com/odvcencio/arbiter/ir"
)

func TestCompileStrategiesHelpers(t *testing.T) {
	strategies, err := CompileStrategies([]byte(`
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		user.country == "US"
	} then Domestic {
		target: "domestic",
	}

	else Global {
		target: "global",
	}
}
`))
	if err != nil {
		t.Fatalf("CompileStrategies: %v", err)
	}
	if strategies == nil {
		t.Fatal("CompileStrategies returned nil strategies")
	}
	if !strategies.Has("CheckoutRouting") {
		t.Fatalf("expected strategy set to contain CheckoutRouting, got %v", strategies.Names())
	}
}

func TestValidateStrategyRejectsGovernedElseArm(t *testing.T) {
	program := &ir.Program{
		OutcomeSchemas: []ir.OutcomeSchema{
			{
				Name: "CheckoutPath",
				Fields: []ir.SchemaField{
					{
						Name:     "target",
						Type:     ir.FieldType{Base: schemaBaseString},
						Required: true,
					},
				},
			},
		},
		Strategies: []ir.Strategy{
			{
				Name:    "CheckoutRouting",
				Returns: "CheckoutPath",
				Candidates: []ir.StrategyCandidate{
					{
						Label:        "Domestic",
						HasCondition: true,
						Condition:    0,
						Params: []ir.ActionParam{
							{Key: "target", Value: 1},
						},
					},
					{
						Label:  "Global",
						IsElse: true,
						Rollout: &ir.Rollout{
							Bps:    5000,
							HasBps: true,
						},
						Params: []ir.ActionParam{
							{Key: "target", Value: 2},
						},
					},
				},
			},
		},
		Exprs: []ir.Expr{
			{Kind: ir.ExprBoolLit, Bool: true},
			{Kind: ir.ExprStringLit, String: "domestic"},
			{Kind: ir.ExprStringLit, String: "global"},
		},
	}

	err := validateProgram(program)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "else arm cannot declare conditions or governance") {
		t.Fatalf("unexpected error: %v", err)
	}
}
