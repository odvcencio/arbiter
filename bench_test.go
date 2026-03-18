package arbiter

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/odvcencio/arbiter/compiler"
)

// makeJSONRules generates n Arishem JSON rules reproducing the issue #28 workload.
func makeJSONRules(n int) []compiler.JSONRuleInput {
	rules := make([]compiler.JSONRuleInput, n)
	for i := range rules {
		condJSON := fmt.Sprintf(
			`{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"%d"},{"StrConst":"N"}]}}]}`,
			10549+i,
		)
		actJSON := `{"ActionName":"approve","ParamMap":{"reason":{"Const":{"StrConst":"matched"}}}}`
		rules[i] = compiler.JSONRuleInput{
			Name:      fmt.Sprintf("rule_%d", i),
			Priority:  i,
			Condition: condJSON,
			Action:    actJSON,
		}
	}
	return rules
}

func TestMemory10KRules(t *testing.T) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	rules := makeJSONRules(10_000)
	rs, err := CompileJSONRules(rules)
	if err != nil {
		t.Fatalf("CompileJSONRules: %v", err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	allocMB := float64(after.TotalAlloc-before.TotalAlloc) / (1024 * 1024)
	heapMB := float64(after.HeapInuse) / (1024 * 1024)

	t.Logf("10K rules compiled:")
	t.Logf("  rules:        %d", len(rs.Rules))
	t.Logf("  instructions: %d bytes", len(rs.Instructions))
	t.Logf("  strings:      %d", rs.Constants.StringCount())
	t.Logf("  numbers:      %d", rs.Constants.NumberCount())
	t.Logf("  total alloc:  %.2f MB", allocMB)
	t.Logf("  heap in use:  %.2f MB", heapMB)

	if allocMB > 100 {
		t.Errorf("memory usage %.2f MB exceeds 100 MB limit", allocMB)
	}
}

func BenchmarkCompile10KRules(b *testing.B) {
	rules := makeJSONRules(10_000)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		rs, err := CompileJSONRules(rules)
		if err != nil {
			b.Fatal(err)
		}
		_ = rs
	}
}

func BenchmarkEval5KRules(b *testing.B) {
	rules := makeJSONRules(5_000)
	rs, err := CompileJSONRules(rules)
	if err != nil {
		b.Fatal(err)
	}

	dc, err := DataFromJSON(`{"fromId":"HuangShan","customerGroupId":"10549"}`, rs)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		matched, err := Eval(rs, dc)
		if err != nil {
			b.Fatal(err)
		}
		_ = matched
	}
}

func BenchmarkEvalSingleRule(b *testing.B) {
	rules := []compiler.JSONRuleInput{{
		Name:     "test",
		Priority: 1,
		Condition: `{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"10549"},{"StrConst":"1"}]}}]}`,
		Action:   `{"ActionName":"Greeting"}`,
	}}
	rs, _ := CompileJSONRules(rules)
	dc := DataFromMap(map[string]any{"fromId": "HuangShan", "customerGroupId": "10549"}, rs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Eval(rs, dc)
	}
}

func BenchmarkEval100Rules(b *testing.B) {
	rules := make([]compiler.JSONRuleInput, 100)
	for i := range rules {
		rules[i] = compiler.JSONRuleInput{
			Name:     fmt.Sprintf("rule%d", i),
			Priority: i,
			Condition: fmt.Sprintf(`{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"10549"},{"StrConst":"%d"}]}}]}`, i),
			Action:   `{"ActionName":"Greeting"}`,
		}
	}
	rs, _ := CompileJSONRules(rules)
	dc := DataFromMap(map[string]any{"fromId": "HuangShan", "customerGroupId": "10549"}, rs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Eval(rs, dc)
	}
}

func BenchmarkEval10KRules(b *testing.B) {
	rules := make([]compiler.JSONRuleInput, 10000)
	for i := range rules {
		rules[i] = compiler.JSONRuleInput{
			Name:     fmt.Sprintf("rule%d", i),
			Priority: i,
			Condition: fmt.Sprintf(`{"OpLogic":"&&","Conditions":[{"Operator":"==","Lhs":{"VarExpr":"fromId"},"Rhs":{"Const":{"StrConst":"HuangShan"}}},{"Operator":"LIST_IN","Lhs":{"VarExpr":"customerGroupId"},"Rhs":{"ConstList":[{"StrConst":"10549"},{"StrConst":"%d"}]}}]}`, i),
			Action:   `{"ActionName":"Greeting"}`,
		}
	}
	rs, _ := CompileJSONRules(rules)
	dc := DataFromMap(map[string]any{"fromId": "HuangShan", "customerGroupId": "10549"}, rs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Eval(rs, dc)
	}
}
