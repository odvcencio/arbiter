package arbiter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompileAndEval(t *testing.T) {
	src := []byte(`
rule HighValue priority 1 {
    when {
        order.amount > 100
        and order.region == "US"
    }
    then ApplyDiscount {
        type: "percentage",
        amount: 10,
    }
}
`)

	rs, err := Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}

	dc, err := DataFromJSON(`{"order":{"amount":200,"region":"US"}}`, rs)
	if err != nil {
		t.Fatalf("DataFromJSON: %v", err)
	}

	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Name != "HighValue" {
		t.Errorf("expected rule name HighValue, got %s", matched[0].Name)
	}
	if matched[0].Action != "ApplyDiscount" {
		t.Errorf("expected action ApplyDiscount, got %s", matched[0].Action)
	}
	if matched[0].Fallback {
		t.Error("expected match, not fallback")
	}
}

func TestCompileAndEvalFallback(t *testing.T) {
	src := []byte(`
rule FreeShipping priority 1 {
    when {
        cart.total >= 50
    }
    then ApplyShipping {
        cost: 0,
    }
    otherwise ApplyShipping {
        cost: 9.99,
    }
}
`)

	rs, err := Compile(src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// cart.total = 20, should NOT match condition, should fire fallback
	dc, err := DataFromJSON(`{"cart":{"total":20}}`, rs)
	if err != nil {
		t.Fatalf("DataFromJSON: %v", err)
	}

	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (fallback), got %d", len(matched))
	}
	if !matched[0].Fallback {
		t.Error("expected fallback=true")
	}
	if matched[0].Action != "ApplyShipping" {
		t.Errorf("expected action ApplyShipping, got %s", matched[0].Action)
	}
}

func TestCompileJSONRulesAndEval(t *testing.T) {
	condJSON := `{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"10549"},{"StrConst":"N"}]}}]}`
	actJSON := `{"ActionName":"approve","ParamMap":{"reason":{"Const":{"StrConst":"matched"}}}}`

	rules := []JSONRule{
		{Name: "rule1", Priority: 0, Condition: condJSON, Action: actJSON},
	}

	rs, err := CompileJSONRules(rules)
	if err != nil {
		t.Fatalf("CompileJSONRules: %v", err)
	}
	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}

	dc, err := DataFromJSON(`{"fromId":"HuangShan","customerGroupId":"10549"}`, rs)
	if err != nil {
		t.Fatalf("DataFromJSON: %v", err)
	}

	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Name != "rule1" {
		t.Errorf("expected rule name rule1, got %s", matched[0].Name)
	}
	if matched[0].Action != "approve" {
		t.Errorf("expected action approve, got %s", matched[0].Action)
	}
}

func TestCompileJSONRulesAndEvalQuantifier(t *testing.T) {
	condJSON := `{"ForeachOperator":"FOREACH","ForeachLogic":"||","ForeachVar":"item","Lhs":{"VarExpr":"items"},"Condition":{"Operator":">","Lhs":{"VarExpr":"item"},"Rhs":{"Const":{"NumConst":0}}}}`
	actJSON := `{"ActionName":"approve"}`

	rs, err := CompileJSONRules([]JSONRule{{
		Name:      "rule1",
		Priority:  0,
		Condition: condJSON,
		Action:    actJSON,
	}})
	if err != nil {
		t.Fatalf("CompileJSONRules: %v", err)
	}

	dc, err := DataFromJSON(`{"items":[-1,2]}`, rs)
	if err != nil {
		t.Fatalf("DataFromJSON: %v", err)
	}

	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
}

func TestEvalDebugUsesWrappedPool(t *testing.T) {
	rs, err := Compile([]byte(`rule T { when { name == "alice" } then A {} }`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	dc := DataFromMap(map[string]any{"name": "bob"}, rs)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("EvalDebug should not panic, got %v", r)
		}
	}()

	debug := EvalDebug(rs, dc)
	if len(debug.Matched) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(debug.Matched))
	}
	if len(debug.Failed) != 1 {
		t.Fatalf("expected 1 failed rule, got %d", len(debug.Failed))
	}
}

func TestCompileTestdataRoundTrip(t *testing.T) {
	files := []string{"pricing.arb", "fraud.arb", "moderation.arb"}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			path := filepath.Join("testdata", f)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			rs, err := Compile(src)
			if err != nil {
				t.Fatalf("Compile(%s): %v", f, err)
			}
			if len(rs.Rules) == 0 {
				t.Errorf("expected rules in %s, got 0", f)
			}
			t.Logf("%s: %d rules, %d instructions bytes, %d strings, %d numbers",
				f, len(rs.Rules), len(rs.Instructions),
				rs.Constants.StringCount(), rs.Constants.NumberCount())
		})
	}
}
