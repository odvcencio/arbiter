package arbiter_test

import (
	"strings"
	"testing"

	arbiter "m31labs.dev/arbiter"
)

func TestConvertJSON(t *testing.T) {
	condJSON := `{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"10549"},{"StrConst":"1"}]}}]}`
	actJSON := `{"ActionName":"Greeting","ParamMap":{"SupplyType":{"Const":{"StrConst":"hello"}}}}`

	src, err := arbiter.ConvertJSON(condJSON, actJSON)
	if err != nil {
		t.Fatalf("ConvertJSON: %v", err)
	}

	t.Logf("ConvertJSON output:\n%s", src)

	text := string(src)
	if !strings.Contains(text, "rule rule0") {
		t.Error("expected rule header with name 'rule0'")
	}
	if !strings.Contains(text, `fromId == "HuangShan"`) {
		t.Error("expected fromId equality condition")
	}
	if !strings.Contains(text, `customerGroupId in ["10549", "1"]`) {
		t.Error("expected customerGroupId in list condition")
	}
	if !strings.Contains(text, "then Greeting") {
		t.Error("expected Greeting action")
	}

	// Round-trip: the produced .arb text must compile without error.
	prog, err := arbiter.Compile(src)
	if err != nil {
		t.Fatalf("Compile(ConvertJSON output): %v", err)
	}
	if prog == nil {
		t.Fatal("expected non-nil program")
	}
	if prog.Ruleset == nil || len(prog.Ruleset.Rules) != 1 {
		t.Errorf("expected 1 compiled rule, got %v", prog.Ruleset)
	}
}

func TestConvertJSONRules(t *testing.T) {
	rules := []arbiter.JSONRule{
		{
			Name:      "PriceRule",
			Priority:  1,
			Condition: `{"Operator":">=","Lhs":{"VarExpr":"user.cart_total"},"Rhs":{"Const":{"NumConst":35}}}`,
			Action:    `{"ActionName":"ApplyShipping","ParamMap":{"cost":{"Const":{"NumConst":0}}}}`,
		},
		{
			Name:      "FraudRule",
			Priority:  2,
			Condition: `{"Operator":"==","Lhs":{"VarExpr":"status"},"Rhs":{"Const":{"StrConst":"flagged"}}}`,
			Action:    `{"ActionName":"Block"}`,
		},
	}

	src, err := arbiter.ConvertJSONRules(rules)
	if err != nil {
		t.Fatalf("ConvertJSONRules: %v", err)
	}

	t.Logf("ConvertJSONRules output:\n%s", src)

	text := string(src)
	if !strings.Contains(text, "rule PriceRule priority 1") {
		t.Error("expected PriceRule header")
	}
	if !strings.Contains(text, "rule FraudRule priority 2") {
		t.Error("expected FraudRule header")
	}
	if !strings.Contains(text, "user.cart_total >= 35") {
		t.Error("expected cart_total condition")
	}
	if !strings.Contains(text, `status == "flagged"`) {
		t.Error("expected status condition")
	}
	if !strings.Contains(text, "then ApplyShipping") {
		t.Error("expected ApplyShipping action")
	}
	if !strings.Contains(text, "then Block") {
		t.Error("expected Block action")
	}

	// Round-trip: the produced .arb text must compile without error.
	prog, err := arbiter.Compile(src)
	if err != nil {
		t.Fatalf("Compile(ConvertJSONRules output): %v", err)
	}
	if prog == nil {
		t.Fatal("expected non-nil program")
	}
	if prog.Ruleset == nil || len(prog.Ruleset.Rules) != 2 {
		t.Errorf("expected 2 compiled rules, got %v", prog.Ruleset)
	}
}

func TestConvertJSONEmptyAction(t *testing.T) {
	condJSON := `{"Operator":"==","Lhs":{"VarExpr":"x"},"Rhs":{"Const":{"NumConst":1}}}`

	// ConvertJSON with an empty action string should still produce parseable
	// .arb. `then` is a required clause, so the converter must fall back to a
	// placeholder action rather than omitting the clause.
	src, err := arbiter.ConvertJSON(condJSON, "")
	if err != nil {
		t.Fatalf("ConvertJSON(empty action): %v", err)
	}

	t.Logf("ConvertJSON empty action output:\n%s", src)

	if !strings.Contains(string(src), "then Noop") {
		t.Fatalf("expected a placeholder then-clause, got:\n%s", src)
	}

	prog, err := arbiter.Compile(src)
	if err != nil {
		t.Fatalf("Compile(ConvertJSON empty action): %v", err)
	}
	if prog == nil || prog.Ruleset == nil || len(prog.Ruleset.Rules) != 1 {
		t.Fatalf("expected 1 compiled rule, got %v", prog)
	}
}

func TestConvertJSONInvalidCondition(t *testing.T) {
	_, err := arbiter.ConvertJSON("not valid json", "")
	if err == nil {
		t.Fatal("expected error for invalid condition JSON")
	}
}
