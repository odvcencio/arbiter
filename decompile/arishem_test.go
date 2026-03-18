package decompile

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSimpleCondition(t *testing.T) {
	// The example from the Arishem issue
	condJSON := `{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"10549"},{"StrConst":"1"}]}}]}`
	actJSON := `{"ActionName":"Greeting","ParamMap":{"SupplyType":{"Const":{"StrConst":"hello"}}}}`

	rules := []ArishemRule{
		{
			Name:      "TestRule",
			Priority:  1,
			Condition: condJSON,
			Action:    actJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	// Verify key parts
	if !strings.Contains(arb, "rule TestRule priority 1") {
		t.Error("missing rule header")
	}
	if !strings.Contains(arb, `fromId == "HuangShan"`) {
		t.Error("missing fromId comparison")
	}
	if !strings.Contains(arb, `customerGroupId in ["10549", "1"]`) {
		t.Error("missing customerGroupId in list")
	}
	if !strings.Contains(arb, "and ") {
		t.Error("missing 'and' keyword")
	}
	if !strings.Contains(arb, "then Greeting") {
		t.Error("missing action")
	}
	if !strings.Contains(arb, `SupplyType: "hello"`) {
		t.Error("missing action param")
	}
}

func TestOrCondition(t *testing.T) {
	condJSON := `{"OpLogic":"||","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"status"},"Rhs":{"Const":{"StrConst":"active"}}},{"Operator":">","Lhs":{"VarExpr":"score"},"Rhs":{"Const":{"NumConst":100}}}]}`

	rules := []ArishemRule{
		{
			Name:      "OrRule",
			Priority:  5,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, `status == "active"`) {
		t.Error("missing status comparison")
	}
	if !strings.Contains(arb, "or score > 100") {
		t.Error("missing or condition")
	}
}

func TestNotCondition(t *testing.T) {
	condJSON := `{"OpLogic":"not","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"active"},"Rhs":{"Const":{"BoolConst":true}}}]}`

	rules := []ArishemRule{
		{
			Name:      "NotRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "not (active == true)") {
		t.Errorf("expected not expression, got:\n%s", arb)
	}
}

func TestNullCheck(t *testing.T) {
	condJSON := `{"Operator":"IS_NULL","Lhs":{"VarExpr":"email"}}`

	rules := []ArishemRule{
		{
			Name:      "NullRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "email is null") {
		t.Error("missing is null")
	}
}

func TestBetweenExpr(t *testing.T) {
	condJSON := `{"Operator":"BETWEEN_ALL_CLOSE","Lhs":{"VarExpr":"age"},"Rhs":{"ConstList":[{"NumConst":18},{"NumConst":65}]}}`

	rules := []ArishemRule{
		{
			Name:      "BetweenRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "age between [18, 65]") {
		t.Errorf("expected between expression, got:\n%s", arb)
	}
}

func TestMathExpr(t *testing.T) {
	condJSON := `{"Operator":">","Lhs":{"VarExpr":"amount"},"Rhs":{"MathExpr":{"Operator":"*","Lhs":{"VarExpr":"avg"},"Rhs":{"Const":{"NumConst":5}}}}}`

	rules := []ArishemRule{
		{
			Name:      "MathRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "amount > avg * 5") {
		t.Errorf("expected math expression, got:\n%s", arb)
	}
}

func TestFallbackAction(t *testing.T) {
	condJSON := `{"Operator":">=","Lhs":{"VarExpr":"total"},"Rhs":{"Const":{"NumConst":35}}}`
	actJSON := `{"ActionName":"ApplyShipping","ParamMap":{"cost":{"Const":{"NumConst":0}},"method":{"Const":{"StrConst":"standard"}}}}`
	fbJSON := `{"ActionName":"ApplyShipping","ParamMap":{"cost":{"Const":{"NumConst":5.99}},"method":{"Const":{"StrConst":"standard"}}}}`

	rules := []ArishemRule{
		{
			Name:      "ShippingRule",
			Priority:  1,
			Condition: condJSON,
			Action:    actJSON,
			Fallback:  fbJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "then ApplyShipping") {
		t.Error("missing then action")
	}
	if !strings.Contains(arb, "otherwise ApplyShipping") {
		t.Error("missing otherwise action")
	}
	if !strings.Contains(arb, "cost: 0,") {
		t.Error("missing cost: 0 in then")
	}
	if !strings.Contains(arb, "cost: 5.99,") {
		t.Error("missing cost: 5.99 in otherwise")
	}
}

func TestMultipleRules(t *testing.T) {
	rules := []ArishemRule{
		{
			Name:      "RuleA",
			Priority:  1,
			Condition: `{"Operator":"==","Lhs":{"VarExpr":"x"},"Rhs":{"Const":{"NumConst":1}}}`,
		},
		{
			Name:      "RuleB",
			Priority:  2,
			Condition: `{"Operator":"!=","Lhs":{"VarExpr":"y"},"Rhs":{"Const":{"StrConst":"foo"}}}`,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "rule RuleA") {
		t.Error("missing RuleA")
	}
	if !strings.Contains(arb, "rule RuleB") {
		t.Error("missing RuleB")
	}
}

func TestNestedLogic(t *testing.T) {
	// and with nested or: a and (b or c)
	condJSON := `{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"a"},"Rhs":{"Const":{"NumConst":1}}},{"OpLogic":"||","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"b"},"Rhs":{"Const":{"NumConst":2}}},{"Operator":"==","Lhs":{"VarExpr":"c"},"Rhs":{"Const":{"NumConst":3}}}]}]}`

	rules := []ArishemRule{
		{
			Name:      "NestedRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	// The nested or should be parenthesized
	if !strings.Contains(arb, "(b == 2 or c == 3)") {
		t.Errorf("expected parenthesized nested or, got:\n%s", arb)
	}
}

func TestStringOperators(t *testing.T) {
	condJSON := `{"OpLogic":"&&","Conditions":[{"Operator":"STRING_START_WITH","Lhs":{"VarExpr":"name"},"Rhs":{"Const":{"StrConst":"Dr."}}},{"Operator":"CONTAIN_REGEXP","Lhs":{"VarExpr":"email"},"Rhs":{"Const":{"StrConst":".*@example\\.com"}}}]}`

	rules := []ArishemRule{
		{
			Name:      "StringRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, `name starts_with "Dr."`) {
		t.Error("missing starts_with")
	}
	if !strings.Contains(arb, `email matches ".*@example\\.com"`) {
		t.Error("missing matches")
	}
}

func TestForeachQuantifier(t *testing.T) {
	condJSON := `{"ForeachOperator":"FOREACH","ForeachLogic":"||","ForeachVar":"item","Lhs":{"VarExpr":"cart.items"},"Condition":{"Operator":">","Lhs":{"VarExpr":"item.price"},"Rhs":{"Const":{"NumConst":100}}}}`

	rules := []ArishemRule{
		{
			Name:      "QuantRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "any item in cart.items { item.price > 100 }") {
		t.Errorf("expected any quantifier, got:\n%s", arb)
	}
}

// TestRoundTripStructure verifies that decompiled output has proper structure.
// A true round-trip test would require the full arbiter Transpile function,
// which depends on tree-sitter. We test structural equivalence instead.
func TestRoundTripStructure(t *testing.T) {
	// Build Arishem JSON that matches the pricing.arb FreeShipping rule
	condJSON := `{"OpLogic":"&&","Conditions":[{"Operator":">=","Lhs":{"VarExpr":"user.cart_total"},"Rhs":{"Const":{"NumConst":35}}},{"Operator":"!=","Lhs":{"VarExpr":"user.region"},"Rhs":{"Const":{"StrConst":"XX"}}}]}`
	actJSON := `{"ActionName":"ApplyShipping","ParamMap":{"cost":{"Const":{"NumConst":0}},"method":{"Const":{"StrConst":"standard"}}}}`
	fbJSON := `{"ActionName":"ApplyShipping","ParamMap":{"cost":{"Const":{"NumConst":5.99}},"method":{"Const":{"StrConst":"standard"}}}}`

	rules := []ArishemRule{
		{
			Name:      "FreeShipping",
			Priority:  1,
			Condition: condJSON,
			Action:    actJSON,
			Fallback:  fbJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	// Verify it matches the structure of pricing.arb's FreeShipping rule
	expected := []string{
		"rule FreeShipping priority 1 {",
		"when {",
		`user.cart_total >= 35`,
		`and user.region != "XX"`,
		"then ApplyShipping {",
		`cost: 0,`,
		`method: "standard",`,
		"otherwise ApplyShipping {",
		`cost: 5.99,`,
	}

	for _, exp := range expected {
		if !strings.Contains(arb, exp) {
			t.Errorf("expected to contain %q, got:\n%s", exp, arb)
		}
	}
}

// TestJSONConditionParsing tests that we can handle the exact JSON format
// that the transpiler produces.
func TestJSONConditionParsing(t *testing.T) {
	// This is the format transpile.go produces for conditions
	condition := map[string]any{
		"OpLogic": "&&",
		"Conditions": []any{
			map[string]any{
				"Operator": ">=",
				"Lhs":      map[string]any{"VarExpr": "user.cart_total"},
				"Rhs":      map[string]any{"Const": map[string]any{"NumConst": float64(35)}},
			},
			map[string]any{
				"Operator": "!=",
				"Lhs":      map[string]any{"VarExpr": "user.region"},
				"Rhs":      map[string]any{"Const": map[string]any{"StrConst": "XX"}},
			},
		},
	}

	condBytes, _ := json.Marshal(condition)

	rules := []ArishemRule{
		{
			Name:      "Test",
			Priority:  1,
			Condition: string(condBytes),
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled:\n%s", arb)

	if !strings.Contains(arb, "user.cart_total >= 35") {
		t.Error("missing cart_total comparison")
	}
	if !strings.Contains(arb, `user.region != "XX"`) {
		t.Error("missing region comparison")
	}
}

func TestBoolConst(t *testing.T) {
	condJSON := `{"Operator":"==","Lhs":{"VarExpr":"active"},"Rhs":{"Const":{"BoolConst":true}}}`

	rules := []ArishemRule{
		{
			Name:      "BoolRule",
			Priority:  1,
			Condition: condJSON,
		},
	}

	arb, err := ArishemToArb(rules)
	if err != nil {
		t.Fatalf("ArishemToArb failed: %v", err)
	}

	t.Logf("Decompiled output:\n%s", arb)

	if !strings.Contains(arb, "active == true") {
		t.Error("missing bool comparison")
	}
}
