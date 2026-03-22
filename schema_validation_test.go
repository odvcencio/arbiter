package arbiter

import (
	"strings"
	"testing"
)

func TestCompileFullNormalizesFactSchemaKey(t *testing.T) {
	result, err := CompileFull([]byte(`
fact PlantStress {
	level: string
	note?: string
}

outcome WaterAction {
	zone: string
	liters: number
}

expert rule SeedStress {
	when { true }
	then assert PlantStress {
		key: "zone-1",
		level: "high",
	}
}

expert rule RouteWater {
	when {
		any stress in facts.PlantStress {
			stress.level == "high" and stress.key == "zone-1"
		}
	}
	then emit WaterAction {
		zone: "zone-1",
		liters: 12,
	}
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}

	if len(result.Program.FactSchemas) != 1 {
		t.Fatalf("expected 1 fact schema, got %d", len(result.Program.FactSchemas))
	}
	schema := result.Program.FactSchemas[0]
	if len(schema.Fields) != 3 {
		t.Fatalf("expected implicit key plus 2 declared fields, got %+v", schema.Fields)
	}
	if schema.Fields[0].Name != "key" || schema.Fields[0].Type.Base != "string" || !schema.Fields[0].Required {
		t.Fatalf("unexpected implicit key field: %+v", schema.Fields[0])
	}
	if schema.Fields[2].Name != "note" || schema.Fields[2].Required {
		t.Fatalf("unexpected optional field metadata: %+v", schema.Fields[2])
	}
}

func TestCompileFullRejectsUnknownSchemaFieldAccess(t *testing.T) {
	_, err := CompileFull([]byte(`
fact PlantStress {
	level: string
}

expert rule RouteWater {
	when {
		any stress in facts.PlantStress {
			stress.deficit_mm == "high"
		}
	}
	then emit WaterAction {}
}
`))
	if err == nil {
		t.Fatal("expected unknown field access to fail compilation")
	}
	if !strings.Contains(err.Error(), `fact PlantStress has no field "deficit_mm"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileFullRejectsSchemaPayloadTypeMismatch(t *testing.T) {
	_, err := CompileFull([]byte(`
outcome WaterAction {
	zone: string
}

expert rule RouteWater {
	when { true }
	then emit WaterAction {
		zone: 42,
	}
}
`))
	if err == nil {
		t.Fatal("expected schema payload mismatch to fail compilation")
	}
	if !strings.Contains(err.Error(), `field "zone" expects string`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileFullRejectsNonStringFactKeyDeclaration(t *testing.T) {
	_, err := CompileFull([]byte(`
fact PlantStress {
	key: number
	level: string
}
`))
	if err == nil {
		t.Fatal("expected non-string key declaration to fail compilation")
	}
	if !strings.Contains(err.Error(), "key must have type string") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileFullAcceptsQuantitySchemasAndLiterals(t *testing.T) {
	_, err := CompileFull([]byte(`
fact SensorReading {
	temperature: number<temperature>
}

expert rule HeatStress {
	when {
		any reading in facts.SensorReading {
			reading.temperature > 28 C
		}
	}
	then emit HeatWarning {}
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
}

func TestCompileFullRejectsQuantityDimensionMismatch(t *testing.T) {
	_, err := CompileFull([]byte(`
fact SensorReading {
	temperature: number<temperature>
}

expert rule Broken {
	when {
		any reading in facts.SensorReading {
			reading.temperature > 1200 ppm
		}
	}
	then emit HeatWarning {}
}
`))
	if err == nil {
		t.Fatal("expected dimension mismatch to fail compilation")
	}
	if !strings.Contains(err.Error(), `type mismatch: number<temperature> > number<concentration>`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileFullRejectsDimensionlessAssignmentToQuantityField(t *testing.T) {
	_, err := CompileFull([]byte(`
fact SensorReading {
	temperature: number<temperature>
}

expert rule Broken {
	when { true }
	then assert SensorReading {
		key: "zone-1",
		temperature: 28,
	}
}
`))
	if err == nil {
		t.Fatal("expected dimensionless assignment to quantity field to fail compilation")
	}
	if !strings.Contains(err.Error(), `field "temperature" expects number<temperature>`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileFullAcceptsDecimalSchemasAndLiterals(t *testing.T) {
	_, err := CompileFull([]byte(`
fact Transaction {
	amount: decimal<currency>
}

expert rule LargeTransfer {
	when {
		any tx in facts.Transaction {
			tx.amount > 1000.00 USD
		}
	}
	then emit ManualReview {}
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
}

func TestCompileFullAcceptsStrategy(t *testing.T) {
	_, err := CompileFull([]byte(`
segment US {
	user.country == "US"
}

outcome CheckoutPath {
	target: string
	reason: string
}

strategy CheckoutRouting returns CheckoutPath {
	when segment US {
		let local = user.country == "US"
		local
	} then Domestic {
		target: "domestic",
		reason: "local routing",
	}

	else Global {
		target: "global",
		reason: "fallback",
	}
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
}

func TestCompileFullRejectsStrategyWithoutElse(t *testing.T) {
	_, err := CompileFull([]byte(`
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		true
	} then Domestic {
		target: "domestic"
	}
}
`))
	if err == nil {
		t.Fatal("expected strategy without else to fail compilation")
	}
	if !strings.Contains(err.Error(), "else arm is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileFullRejectsStrategyPayloadMismatch(t *testing.T) {
	_, err := CompileFull([]byte(`
outcome CheckoutPath {
	target: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		true
	} then Domestic {
		target: 42
	}

	else Global {
		target: "global"
	}
}
`))
	if err == nil {
		t.Fatal("expected strategy payload mismatch to fail compilation")
	}
	if !strings.Contains(err.Error(), `field "target" expects string`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileFullRejectsMismatchedDecimalUnits(t *testing.T) {
	_, err := CompileFull([]byte(`
rule Broken {
	when { 100.00 USD + 50.00 EUR > 125.00 USD }
	then Alert {}
}
`))
	if err == nil {
		t.Fatal("expected decimal unit mismatch to fail compilation")
	}
	if !strings.Contains(err.Error(), `decimal<currency>[USD] + decimal<currency>[EUR]`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
