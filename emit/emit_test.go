package emit

import (
	"os"
	"strings"
	"testing"
)

func readTestdata(t *testing.T) []byte {
	t.Helper()
	source, err := os.ReadFile("../testdata/pricing.arb")
	if err != nil {
		t.Fatalf("read pricing.arb: %v", err)
	}
	return source
}

func TestToRego(t *testing.T) {
	source := readTestdata(t)
	out, err := ToRego(source)
	if err != nil {
		t.Fatalf("ToRego: %v", err)
	}
	t.Logf("Rego output:\n%s", out)

	checks := []string{
		"package rules",
		"import rego.v1",
		"free_shipping if {",
		"input.user.cart_total >= 35",
		`input.user.region != "XX"`,
		"v_i_p_discount if {",
		"input.user.tier in",
		"input.user.purchase_count > 10",
		"input.user.cart_total >= 1000",
		"welcome_offer if {",
		"input.user.is_first_order == true",
		"input.user.cart_total >= 25",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("Rego output missing %q", c)
		}
	}
}

func TestToCEL(t *testing.T) {
	source := readTestdata(t)
	out, err := ToCEL(source)
	if err != nil {
		t.Fatalf("ToCEL: %v", err)
	}
	t.Logf("CEL output:\n%s", out)

	checks := []string{
		"// FreeShipping",
		"user.cart_total >= 35",
		`user.region != "XX"`,
		"&&",
		"// VIPDiscount",
		"user.tier in",
		"user.purchase_count > 10",
		"user.cart_total >= 1000",
		"// WelcomeOffer",
		"user.is_first_order == true",
		"user.cart_total >= 25",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("CEL output missing %q", c)
		}
	}
}

func TestToDRL(t *testing.T) {
	source := readTestdata(t)
	out, err := ToDRL(source)
	if err != nil {
		t.Fatalf("ToDRL: %v", err)
	}
	t.Logf("DRL output:\n%s", out)

	checks := []string{
		`rule "FreeShipping"`,
		"salience 1",
		"when",
		"then",
		"end",
		"cartTotal >= 35",
		`region != "XX"`,
		`rule "VIPDiscount"`,
		"salience 2",
		"purchaseCount > 10",
		"cartTotal >= 1000",
		`rule "WelcomeOffer"`,
		"salience 3",
		"isFirstOrder == true",
		"cartTotal >= 25",
		"applyShipping(",
		"applyDiscount(",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("DRL output missing %q", c)
		}
	}
}
