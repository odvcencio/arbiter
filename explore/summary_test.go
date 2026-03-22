package explore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/arbiter/explore"
	"github.com/odvcencio/arbiter/ir"
)

func TestBuildSummaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "greenhouse.arb")
	source := []byte(`
const SAFE_TEMP = 28 C

fact SensorReading {
	temperature: number<temperature>
}

outcome HeatWarning {
	zone: string
}

rule CheckTemp {
	when { sensor.temperature > SAFE_TEMP }
	then Alert {}
}

expert rule HeatStress cooldown 15m {
	when { input.hot == true } for 10m
	then emit HeatWarning {
		zone: "zone-a",
	}
}
`)
	if err := os.WriteFile(path, source, 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	summary, err := explore.BuildSummaryFile(path)
	if err != nil {
		t.Fatalf("BuildSummaryFile: %v", err)
	}
	if summary.Source != path {
		t.Fatalf("summary.Source = %q, want %q", summary.Source, path)
	}
	if len(summary.FactSchemas) != 1 || summary.FactSchemas[0].Name != "SensorReading" {
		t.Fatalf("unexpected fact schemas: %+v", summary.FactSchemas)
	}
	if len(summary.OutcomeSchemas) != 1 || summary.OutcomeSchemas[0].Name != "HeatWarning" {
		t.Fatalf("unexpected outcome schemas: %+v", summary.OutcomeSchemas)
	}
	if len(summary.Constants) != 1 || summary.Constants[0].Raw != "28 C" {
		t.Fatalf("unexpected constants: %+v", summary.Constants)
	}
	if len(summary.Rules) != 1 || summary.Rules[0].Name != "CheckTemp" {
		t.Fatalf("unexpected rules: %+v", summary.Rules)
	}
	if len(summary.ExpertRules) != 1 {
		t.Fatalf("unexpected expert rules: %+v", summary.ExpertRules)
	}
	if summary.ExpertRules[0].For != "10m" || summary.ExpertRules[0].Cooldown != "15m" {
		t.Fatalf("expected temporal metadata in expert summary, got %+v", summary.ExpertRules[0])
	}
	if len(summary.UsedUnits) == 0 {
		t.Fatalf("expected used units in summary, got %+v", summary)
	}
}

func TestBuildSummaryIncludesDecimalUnits(t *testing.T) {
	summary := explore.BuildSummary(&ir.Program{
		FactSchemas: []ir.FactSchema{{
			Name: "Transaction",
			Fields: []ir.SchemaField{{
				Name:     "amount",
				Type:     ir.FieldType{Base: "decimal", Dimension: "currency"},
				Required: true,
			}},
		}},
		Exprs: []ir.Expr{{
			Kind:   ir.ExprDecimalLit,
			String: "1000.25",
			Unit:   "USD",
		}},
	})

	found := false
	for _, dimension := range summary.UsedUnits {
		if dimension.Dimension == "currency" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected currency units in summary, got %+v", summary.UsedUnits)
	}
}
