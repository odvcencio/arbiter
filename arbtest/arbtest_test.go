package arbtest_test

import (
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/arbiter/arbtest"
)

func TestRunFileStrategy(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bundle.arb")
	testPath := filepath.Join(dir, "bundle.test.arb")

	bundle := `
outcome CheckoutPath {
	target: string
	currency: string
}

strategy CheckoutRouting returns CheckoutPath {
	when {
		user.country == "US"
	} then Domestic {
		target: "us-checkout",
		currency: "USD",
	}

	when {
		user.country == "GB"
	} then UK {
		target: "uk-checkout",
		currency: "GBP",
	}

	else Global {
		target: "global-checkout",
		currency: "USD",
	}
}
`
	testSuite := `
test "US user routes to domestic checkout" {
	given {
		user.country: "US"
	}
	expect strategy CheckoutRouting selected Domestic { target: "us-checkout", currency: "USD" }
}

test "GB user routes to UK checkout" {
	given {
		user.country: "GB"
	}
	expect strategy CheckoutRouting selected UK { target: "uk-checkout" }
}

test "unknown country falls through to global" {
	given {
		user.country: "BR"
	}
	expect strategy CheckoutRouting selected Global
}
`

	if err := os.WriteFile(bundlePath, []byte(bundle), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := os.WriteFile(testPath, []byte(testSuite), 0o644); err != nil {
		t.Fatalf("write test suite: %v", err)
	}

	result, err := arbtest.RunFile(testPath, arbtest.Options{})
	if err != nil {
		t.Fatalf("RunFile: %v", err)
	}
	if result.Failed != 0 {
		for _, c := range result.Cases {
			if !c.Passed {
				t.Errorf("[FAIL] %s: %s", c.Name, c.Error)
			}
		}
		t.Fatalf("expected no failures, got %d failed", result.Failed)
	}
	if result.Passed != 3 {
		t.Fatalf("expected 3 passed cases, got %d", result.Passed)
	}
}

func TestRunFileImportedNamespacedRule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "arbiter.toml"),
		[]byte("[project]\nname = \"t\"\nversion = \"0.1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "rules.arb"), []byte(`input { user: { age: number } }
outcome Access { tier: string }
rule AdultUS { when { user.age >= 18 } then Access { tier: "full" } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.arb"), []byte("import \"lib/rules\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(dir, "main.test.arb")
	if err := os.WriteFile(testPath, []byte(`test "adult matches imported rule" {
	given { user.age: 21 }
	expect rule rules.AdultUS matched
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A test must be able to reference an imported rule by its namespaced name.
	result, err := arbtest.RunFile(testPath, arbtest.Options{})
	if err != nil {
		t.Fatalf("RunFile: %v", err)
	}
	if result.Failed != 0 || result.Passed != 1 {
		for _, c := range result.Cases {
			if !c.Passed {
				t.Errorf("[FAIL] %s: %s", c.Name, c.Error)
			}
		}
		t.Fatalf("expected 1 passed / 0 failed for namespaced imported rule; got %d passed / %d failed", result.Passed, result.Failed)
	}
}

func TestRunFileImportedNamespacedStrategyAndFlag(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "arbiter.toml"),
		[]byte("[project]\nname = \"t\"\nversion = \"0.1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "policy.arb"), []byte(`input { user: { country: string plan: string } }
outcome Route { target: string }
strategy Pick returns Route {
	when { user.country == "US" } then Dom { target: "us" }
	else Glob { target: "global" }
}
flag promo type boolean default false {
	when { user.plan == "pro" } then true
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.arb"), []byte("import \"lib/policy\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(dir, "main.test.arb")
	if err := os.WriteFile(testPath, []byte(`test "namespaced strategy and flag" {
	given {
		user.country: "US"
		user.plan: "pro"
	}
	expect strategy policy.Pick selected Dom
	expect flag policy.promo == "true"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := arbtest.RunFile(testPath, arbtest.Options{})
	if err != nil {
		t.Fatalf("RunFile: %v", err)
	}
	if result.Failed != 0 || result.Passed != 1 {
		for _, c := range result.Cases {
			if !c.Passed {
				t.Errorf("[FAIL] %s: %s", c.Name, c.Error)
			}
		}
		t.Fatalf("expected 1 passed / 0 failed for namespaced strategy+flag; got %d passed / %d failed", result.Passed, result.Failed)
	}
}

func TestRunFile(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bundle.arb")
	testPath := filepath.Join(dir, "bundle.test.arb")

	bundle := `
rule FreeShipping {
	when {
		user.lifetime_spend >= 15000
		and user.cart_total >= 35
	}
	then ApplyShipping {
		cost: 0,
		billed_total: user.cart_total,
		method: "standard",
	}
}

flag checkout_v2 type multivariate default "control" {
	variant "treatment" {
		label: "new"
	}

	when { user.segment == "beta" } then "treatment"
}

fact SensorReading {
	temperature: number<temperature>
}

outcome HeatWarning {
	zone: string
}

fact Event {
	user: string
	amount: decimal<currency>
	type: string
}

outcome FraudAlert {
	key: string
	user: string
	reason: string
}

arbiter fraud_monitor {
	stream event
	source event
}

expert rule HeatStress {
	when {
		any reading in facts.SensorReading {
			reading.temperature > 28 C
		}
	} for 10m
	then emit HeatWarning {
		zone: "zone-A",
	}
}

expert rule VelocityFraud priority 10 {
	when { event.type == "purchase" }
	then emit FraudAlert {
		key: event.user,
		user: event.user,
		reason: "velocity",
	}
}
`
	testSuite := `
test "free shipping for high-value customers" {
	given {
		user.lifetime_spend: 15000
		user.cart_total: 50
	}
	expect rule FreeShipping matched
	expect action ApplyShipping { cost: 0, billed_total: between 40 60, method: "standard" }
}

test "checkout flag routes beta users" {
	given {
		user.segment: "beta"
	}
	expect flag checkout_v2 == "treatment"
}

scenario "sustained heat triggers alert" {
	at T+0 {
		assert SensorReading { key: "zone-A", temperature: 30 C }
	}

	at T+5m {
		assert SensorReading { key: "zone-A", temperature: 31 C }
		expect no outcome HeatWarning
	}

	at T+10m {
		assert SensorReading { key: "zone-A", temperature: 29 C }
		expect outcome HeatWarning { zone: "zone-A" }
	}
}

scenario "fraud monitor triggers on velocity" {
	stream event { key: "p-1", type: "purchase", amount: 500 USD, user: "u-123" }
	stream event { key: "p-2", type: "purchase", amount: 600 USD, user: "u-123" }
	stream event { key: "p-3", type: "purchase", amount: 700 USD, user: "u-123" }

	within 1m {
		expect outcome FraudAlert { user: "u-123", reason: "velocity" }
	}
}
`

	if err := os.WriteFile(bundlePath, []byte(bundle), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := os.WriteFile(testPath, []byte(testSuite), 0o644); err != nil {
		t.Fatalf("write test suite: %v", err)
	}

	result, err := arbtest.RunFile(testPath, arbtest.Options{})
	if err != nil {
		t.Fatalf("RunFile: %v", err)
	}
	if result.Failed != 0 {
		t.Fatalf("expected no failures, got %+v", result)
	}
	if result.Passed != 4 {
		t.Fatalf("expected 4 passed cases, got %+v", result)
	}
}
