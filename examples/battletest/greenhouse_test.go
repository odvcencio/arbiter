package battletest

import (
	"context"
	"testing"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
)

func TestGreenhouse(t *testing.T) {
	client := connect(t)
	bundle := publish(t, client, "greenhouse", "greenhouse.arb")

	type scenario struct {
		name           string
		soil           map[string]any
		climate        map[string]any
		wantOutcomes   []string // outcome names that must appear
		rejectOutcomes []string // outcome names that must NOT appear
		wantFacts      []string // fact types that must exist
	}

	tests := []scenario{
		{
			name:    "optimal conditions - all clear",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"StatusReport"},
			rejectOutcomes: []string{"WaterAction", "FeedAction", "ClimateAction", "MistAction"},
			wantFacts:      []string{"SoilStatus", "ClimateStatus"},
		},
		{
			name:    "dry soil triggers irrigation",
			soil:    map[string]any{"moisture_pct": float64(15), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"WaterAction"},
			rejectOutcomes: []string{"StatusReport"},
			wantFacts:      []string{"SoilStatus", "ClimateStatus", "PlantStress"},
		},
		{
			name:    "hot climate triggers cooling",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(40), "light_lux": float64(8000)},
			wantOutcomes:   []string{"ClimateAction"},
			rejectOutcomes: []string{"StatusReport"},
		},
		{
			name:    "nutrient deficiency triggers fertilizer",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(5), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"FeedAction"},
			rejectOutcomes: []string{"StatusReport"},
		},
		{
			name:    "drought suppresses fertilizer (excludes)",
			soil:    map[string]any{"moisture_pct": float64(10), "nitrogen_ppm": float64(5), "phosphorus_ppm": float64(5), "potassium_ppm": float64(5)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"WaterAction"},
			rejectOutcomes: []string{"FeedAction"},
		},
		{
			name:    "high humidity triggers ventilation not misting",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(90), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"ClimateAction"},
			rejectOutcomes: []string{"MistAction", "StatusReport"},
		},
		{
			name:    "dry air triggers misting when no fungal risk",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(30), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"MistAction"},
			rejectOutcomes: []string{"ClimateAction"},
		},
		{
			name:    "low CO2 triggers injection",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(300)},
			wantOutcomes:   []string{"CO2Action"},
			rejectOutcomes: []string{"StatusReport"},
		},
		{
			name:    "high CO2 triggers emergency ventilation",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(2000)},
			wantOutcomes:   []string{"ClimateAction"},
			rejectOutcomes: []string{"StatusReport"},
		},
		{
			name:    "normal CO2 no action",
			soil:    map[string]any{"moisture_pct": float64(50), "nitrogen_ppm": float64(40), "phosphorus_ppm": float64(25), "potassium_ppm": float64(30)},
			climate: map[string]any{"humidity_pct": float64(60), "temp_c": float64(24), "light_lux": float64(5000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"StatusReport"},
			rejectOutcomes: []string{"CO2Action"},
		},
		{
			name:    "multiple stresses compound",
			soil:    map[string]any{"moisture_pct": float64(10), "nitrogen_ppm": float64(5), "phosphorus_ppm": float64(5), "potassium_ppm": float64(5)},
			climate: map[string]any{"humidity_pct": float64(30), "temp_c": float64(40), "light_lux": float64(10000), "co2_ppm": float64(800)},
			wantOutcomes:   []string{"WaterAction", "ClimateAction"},
			rejectOutcomes: []string{"StatusReport", "FeedAction"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := map[string]any{
				"soil":    tt.soil,
				"climate": tt.climate,
			}
			sess, err := client.StartSession(context.Background(), &arbiterv1.StartSessionRequest{
				BundleId: bundle,
				Envelope: mustStruct(envelope),
			})
			if err != nil {
				t.Fatalf("start: %v", err)
			}

			run, err := client.RunSession(context.Background(), &arbiterv1.RunSessionRequest{
				SessionId: sess.SessionId,
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			t.Logf("quiesced: %s in %d rounds, %d mutations", run.StopReason, run.Rounds, run.Mutations)

			outcomeNames := map[string]bool{}
			for _, o := range run.Outcomes {
				outcomeNames[o.Name] = true
				t.Logf("  outcome: %s (%v)", o.Name, o.Params.AsMap())
			}

			factTypes := map[string]bool{}
			for _, f := range run.Facts {
				factTypes[f.Type] = true
			}

			for _, want := range tt.wantOutcomes {
				if !outcomeNames[want] {
					t.Errorf("missing outcome: %s (have: %v)", want, outcomeNames)
				}
			}
			for _, reject := range tt.rejectOutcomes {
				if outcomeNames[reject] {
					t.Errorf("unexpected outcome: %s should not fire", reject)
				}
			}
			for _, want := range tt.wantFacts {
				if !factTypes[want] {
					t.Errorf("missing fact: %s", want)
				}
			}

			client.CloseSession(context.Background(), &arbiterv1.CloseSessionRequest{SessionId: sess.SessionId})
		})
	}
}
