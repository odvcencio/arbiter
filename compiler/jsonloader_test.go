// compiler/jsonloader_test.go
package compiler

import (
	"fmt"
	"testing"
)

func TestCompileJSONSimple(t *testing.T) {
	condition := `{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"10549"},{"StrConst":"1"}]}}]}`
	action := `{"ActionName":"Greeting","ParamMap":{"SupplyType":{"Const":{"StrConst":"hello"}}}}`

	rs, err := CompileJSONRule("test", 1, condition, action)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}
	if rs.Constants.StringCount() == 0 {
		t.Error("expected interned strings from JSON")
	}
}

func TestCompileJSONBatch(t *testing.T) {
	rules := make([]JSONRuleInput, 100)
	for i := range rules {
		rules[i] = JSONRuleInput{
			Name:      fmt.Sprintf("rule%d", i),
			Priority:  i,
			Condition: `{"Operator":"==","Lhs":{"VarExpr":"x"},"Rhs":{"Const":{"NumConst":1}}}`,
			Action:    `{"ActionName":"DoIt"}`,
		}
	}
	rs, err := CompileJSONBatch(rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 100 {
		t.Fatalf("expected 100 rules, got %d", len(rs.Rules))
	}
	// "x" and "DoIt" should each be interned once, not 100 times.
	// 100 unique rule names + "x" + "DoIt" = 102 strings total.
	// Without interning we'd have 300 (100 names + 100 "x" + 100 "DoIt").
	strCount := rs.Constants.StringCount()
	if strCount > 110 {
		t.Errorf("expected interning to deduplicate shared strings, got %d unique (want ~102)", strCount)
	}
}
