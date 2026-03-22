package arbtest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/arbiter/arbtest"
)

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
