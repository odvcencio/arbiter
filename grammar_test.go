package arbiter

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func parseArb(t *testing.T, input string) string {
	t.Helper()
	lang, err := getArbiterLanguage()
	if err != nil {
		t.Fatalf("language generation: %v", err)
	}
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	sexp := root.SExpr(lang)
	if root.HasError() {
		t.Errorf("parse has ERROR nodes: %s", sexp)
	}
	return sexp
}

func transpileArb(t *testing.T, input string) map[string]any {
	t.Helper()
	out, err := Transpile([]byte(input))
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	t.Logf("JSON:\n%s", out)
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return result
}

// getCondition extracts the condition from the first rule in transpile output.
func getCondition(t *testing.T, input string) map[string]any {
	t.Helper()
	result := transpileArb(t, input)
	rules := result["rules"].([]any)
	rule := rules[0].(map[string]any)
	cond := rule["condition"]
	if cond == nil {
		t.Fatal("no condition found")
	}
	return cond.(map[string]any)
}

// =============================================================================
// GRAMMAR PARSE TESTS
// =============================================================================

func TestParseMinimal(t *testing.T) {
	sexp := parseArb(t, `rule Test { when { true } then Action {} }`)
	if !strings.Contains(sexp, "rule_declaration") {
		t.Error("expected rule_declaration")
	}
}

func TestParseConst(t *testing.T) {
	sexp := parseArb(t, `const LIMIT = 100`)
	if !strings.Contains(sexp, "const_declaration") {
		t.Error("expected const_declaration")
	}
}

func TestParseConstList(t *testing.T) {
	sexp := parseArb(t, `const TIERS = ["gold", "platinum"]`)
	if !strings.Contains(sexp, "list_literal") {
		t.Error("expected list_literal in const")
	}
}

func TestParseFeature(t *testing.T) {
	sexp := parseArb(t, `feature user from "user-service" { age: number name: string active: bool }`)
	if !strings.Contains(sexp, "feature_declaration") {
		t.Error("expected feature_declaration")
	}
	if strings.Count(sexp, "field_declaration") != 3 {
		t.Errorf("expected 3 fields, got: %s", sexp)
	}
}

func TestParseComparison(t *testing.T) {
	sexp := parseArb(t, `rule T { when { x >= 18 } then A {} }`)
	if !strings.Contains(sexp, "comparison_expr") {
		t.Error("expected comparison_expr")
	}
}

func TestParseAnd(t *testing.T) {
	sexp := parseArb(t, `rule T { when { a > 1 and b < 2 } then A {} }`)
	if !strings.Contains(sexp, "and_expr") {
		t.Error("expected and_expr")
	}
	// Must have TWO comparison_expr children
	if strings.Count(sexp, "comparison_expr") != 2 {
		t.Errorf("expected 2 comparisons in and_expr, got: %s", sexp)
	}
}

func TestParseOr(t *testing.T) {
	sexp := parseArb(t, `rule T { when { a > 1 or b < 2 } then A {} }`)
	if !strings.Contains(sexp, "or_expr") {
		t.Error("expected or_expr")
	}
}

func TestParseNot(t *testing.T) {
	sexp := parseArb(t, `rule T { when { not x > 0 } then A {} }`)
	if !strings.Contains(sexp, "not_expr") {
		t.Error("expected not_expr")
	}
}

func TestParseIn(t *testing.T) {
	sexp := parseArb(t, `rule T { when { role in ["admin", "mod"] } then A {} }`)
	if !strings.Contains(sexp, "in_expr") {
		t.Error("expected in_expr")
	}
}

func TestParseNotIn(t *testing.T) {
	sexp := parseArb(t, `rule T { when { role not in ["banned"] } then A {} }`)
	if !strings.Contains(sexp, "not_in_expr") {
		t.Error("expected not_in_expr")
	}
}

func TestParseContains(t *testing.T) {
	sexp := parseArb(t, `rule T { when { tags contains "vip" } then A {} }`)
	if !strings.Contains(sexp, "contains_expr") {
		t.Error("expected contains_expr")
	}
}

func TestParseStartsWith(t *testing.T) {
	sexp := parseArb(t, `rule T { when { name starts_with "Dr" } then A {} }`)
	if !strings.Contains(sexp, "starts_with_expr") {
		t.Error("expected starts_with_expr")
	}
}

func TestParseEndsWith(t *testing.T) {
	sexp := parseArb(t, `rule T { when { email ends_with ".edu" } then A {} }`)
	if !strings.Contains(sexp, "ends_with_expr") {
		t.Error("expected ends_with_expr")
	}
}

func TestParseMatches(t *testing.T) {
	sexp := parseArb(t, `rule T { when { code matches "^[A-Z]{3}$" } then A {} }`)
	if !strings.Contains(sexp, "matches_expr") {
		t.Error("expected matches_expr")
	}
}

func TestParseIsNull(t *testing.T) {
	sexp := parseArb(t, `rule T { when { val is null } then A {} }`)
	if !strings.Contains(sexp, "is_null_expr") {
		t.Error("expected is_null_expr")
	}
}

func TestParseIsNotNull(t *testing.T) {
	sexp := parseArb(t, `rule T { when { val is not null } then A {} }`)
	if !strings.Contains(sexp, "is_not_null_expr") {
		t.Error("expected is_not_null_expr")
	}
}

func TestParseMemberExpr(t *testing.T) {
	sexp := parseArb(t, `rule T { when { user.profile.age > 18 } then A {} }`)
	if strings.Count(sexp, "member_expr") < 1 {
		t.Error("expected member_expr for nested access")
	}
}

func TestParseMath(t *testing.T) {
	sexp := parseArb(t, `rule T { when { x + 1 > 10 } then A {} }`)
	if !strings.Contains(sexp, "math_expr") {
		t.Error("expected math_expr")
	}
}

func TestParseQuantifier(t *testing.T) {
	sexp := parseArb(t, `rule T { when { any item in items { item > 0 } } then A {} }`)
	if !strings.Contains(sexp, "quantifier_expr") {
		t.Error("expected quantifier_expr")
	}
}

func TestParseParen(t *testing.T) {
	sexp := parseArb(t, `rule T { when { (a > 1 or b < 2) and c == 3 } then A {} }`)
	if !strings.Contains(sexp, "paren_expr") {
		t.Error("expected paren_expr")
	}
	if !strings.Contains(sexp, "and_expr") {
		t.Error("expected and_expr wrapping paren + comparison")
	}
}

func TestParsePriority(t *testing.T) {
	sexp := parseArb(t, `rule T priority 5 { when { true } then A {} }`)
	if !strings.Contains(sexp, "number_literal") {
		t.Error("expected priority number")
	}
}

func TestParseOtherwise(t *testing.T) {
	sexp := parseArb(t, `rule T { when { true } then A {} otherwise B {} }`)
	if !strings.Contains(sexp, "otherwise_block") {
		t.Error("expected otherwise_block")
	}
}

func TestParseActionParams(t *testing.T) {
	sexp := parseArb(t, `rule T { when { true } then Act { key: "val", num: 42 } }`)
	if strings.Count(sexp, "param_assignment") != 2 {
		t.Errorf("expected 2 param_assignments, got: %s", sexp)
	}
}

func TestParseComment(t *testing.T) {
	sexp := parseArb(t, `# this is a comment
rule T { when { true } then A {} }
`)
	if !strings.Contains(sexp, "rule_declaration") {
		t.Error("comment should not break parsing")
	}
}

func TestParseThreeWayAnd(t *testing.T) {
	sexp := parseArb(t, `rule T { when { a > 1 and b > 2 and c > 3 } then A {} }`)
	// Should have 3 comparison_expr nodes
	if strings.Count(sexp, "comparison_expr") != 3 {
		t.Errorf("expected 3 comparisons, got: %s", sexp)
	}
}

func TestParseMixedAndOr(t *testing.T) {
	sexp := parseArb(t, `rule T { when { a > 1 and b > 2 or c > 3 } then A {} }`)
	if !strings.Contains(sexp, "or_expr") {
		t.Error("expected or_expr at top level")
	}
	if !strings.Contains(sexp, "and_expr") {
		t.Error("expected and_expr nested inside or")
	}
}

// =============================================================================
// SEGMENT & FLAG PARSE TESTS
// =============================================================================

func TestParseSegment(t *testing.T) {
	input := `segment enterprise_us {
		plan == "enterprise" and country == "US"
	}`
	result := parseArb(t, input)
	if !strings.Contains(result, "segment_declaration") {
		t.Error("expected segment_declaration node")
	}
}

func TestParseFlag(t *testing.T) {
	input := `flag checkout_v2 type multivariate default "control" {
		owner: "oscar"
		ticket: "ENG-1234"
		requires payments_enabled
		when internal then "treatment"
		when enterprise_us rollout 20 then "treatment"
	}`
	result := parseArb(t, input)
	if !strings.Contains(result, "flag_declaration") {
		t.Error("expected flag_declaration node")
	}
}

func TestParseFlagKillSwitch(t *testing.T) {
	input := `flag dark_mode type boolean default false kill_switch {
		owner: "design-team"
	}`
	result := parseArb(t, input)
	if !strings.Contains(result, "kill_switch") {
		t.Error("expected kill_switch in parse tree")
	}
}

func TestParseFlagBoolean(t *testing.T) {
	input := `flag payments type boolean default false {
		when enterprise_us then true
	}`
	result := parseArb(t, input)
	if !strings.Contains(result, "flag_declaration") {
		t.Error("expected flag_declaration node")
	}
}

func TestParseSegmentAndFlag(t *testing.T) {
	input := `
segment internal {
	user.email ends_with "@m31labs.dev"
}

segment enterprise_us {
	user.plan == "enterprise" and user.country == "US"
}

flag checkout_v2 type multivariate default "control" {
	owner: "oscar"
	ticket: "ENG-1234"
	expires: "2026-06-01"
	requires payments_enabled
	when internal then "treatment"
	when enterprise_us rollout 20 then "treatment"
}

flag payments_enabled type boolean default false {
	owner: "oscar"
	when enterprise_us then true
}

flag dark_mode type boolean default false kill_switch {
	owner: "design-team"
}
`
	result := parseArb(t, input)
	if !strings.Contains(result, "segment_declaration") {
		t.Error("expected segment_declaration")
	}
	if !strings.Contains(result, "flag_declaration") {
		t.Error("expected flag_declaration")
	}
}

// =============================================================================
// TRANSPILE TESTS
// =============================================================================

func TestTranspileMinimal(t *testing.T) {
	result := transpileArb(t, `rule Test { when { true } then Action {} }`)
	rules := result["rules"].([]any)
	rule := rules[0].(map[string]any)
	if rule["name"] != "Test" {
		t.Errorf("expected name Test, got %v", rule["name"])
	}
}

func TestTranspileComparison(t *testing.T) {
	cond := getCondition(t, `rule T { when { user.age >= 18 } then A {} }`)
	if cond["Operator"] == "" {
		t.Error("expected operator to be set")
	}
	lhs := cond["Lhs"].(map[string]any)
	if lhs["VarExpr"] != "user.age" {
		t.Errorf("expected VarExpr user.age, got %v", lhs["VarExpr"])
	}
}

func TestTranspileAllComparisonOps(t *testing.T) {
	ops := []struct{ arb, arishem string }{
		{"==", "=="},
		{"!=", "!="},
		{">", ">"},
		{"<", "<"},
		{">=", ">="},
		{"<=", "<="},
	}
	for _, op := range ops {
		t.Run(op.arb, func(t *testing.T) {
			src := `rule T { when { x ` + op.arb + ` 10 } then A {} }`
			cond := getCondition(t, src)
			got := cond["Operator"].(string)
			if got != op.arishem {
				t.Errorf("expected operator %q, got %q", op.arishem, got)
			}
		})
	}
}

func TestTranspileAnd(t *testing.T) {
	cond := getCondition(t, `rule T { when { a > 1 and b < 2 } then A {} }`)
	if cond["OpLogic"] != "&&" {
		t.Errorf("expected &&, got %v", cond["OpLogic"])
	}
	conditions := cond["Conditions"].([]any)
	if len(conditions) != 2 {
		t.Errorf("expected 2 conditions, got %d", len(conditions))
	}
}

func TestTranspileOr(t *testing.T) {
	cond := getCondition(t, `rule T { when { a > 1 or b < 2 } then A {} }`)
	if cond["OpLogic"] != "||" {
		t.Errorf("expected ||, got %v", cond["OpLogic"])
	}
}

func TestTranspileNot(t *testing.T) {
	cond := getCondition(t, `rule T { when { not x > 0 } then A {} }`)
	if cond["OpLogic"] != "not" {
		t.Errorf("expected not, got %v", cond["OpLogic"])
	}
}

func TestTranspileIn(t *testing.T) {
	cond := getCondition(t, `rule T { when { role in ["admin", "mod"] } then A {} }`)
	if cond["Operator"] != "LIST_IN" {
		t.Errorf("expected LIST_IN, got %v", cond["Operator"])
	}
}

func TestTranspileNotIn(t *testing.T) {
	cond := getCondition(t, `rule T { when { role not in ["banned"] } then A {} }`)
	if cond["Operator"] != "!LIST_IN" {
		t.Errorf("expected !LIST_IN, got %v", cond["Operator"])
	}
}

func TestTranspileContains(t *testing.T) {
	cond := getCondition(t, `rule T { when { tags contains "vip" } then A {} }`)
	if cond["Operator"] != "LIST_CONTAINS" {
		t.Errorf("expected LIST_CONTAINS, got %v", cond["Operator"])
	}
}

func TestTranspileNotContains(t *testing.T) {
	cond := getCondition(t, `rule T { when { tags not contains "spam" } then A {} }`)
	if cond["Operator"] != "!LIST_CONTAINS" {
		t.Errorf("expected !LIST_CONTAINS, got %v", cond["Operator"])
	}
}

func TestTranspileStartsWith(t *testing.T) {
	cond := getCondition(t, `rule T { when { name starts_with "Dr" } then A {} }`)
	if cond["Operator"] != "STRING_START_WITH" {
		t.Errorf("expected STRING_START_WITH, got %v", cond["Operator"])
	}
}

func TestTranspileEndsWith(t *testing.T) {
	cond := getCondition(t, `rule T { when { email ends_with ".edu" } then A {} }`)
	if cond["Operator"] != "STRING_END_WITH" {
		t.Errorf("expected STRING_END_WITH, got %v", cond["Operator"])
	}
}

func TestTranspileMatches(t *testing.T) {
	cond := getCondition(t, `rule T { when { code matches "^[A-Z]+$" } then A {} }`)
	if cond["Operator"] != "CONTAIN_REGEXP" {
		t.Errorf("expected CONTAIN_REGEXP, got %v", cond["Operator"])
	}
}

func TestTranspileIsNull(t *testing.T) {
	cond := getCondition(t, `rule T { when { val is null } then A {} }`)
	if cond["Operator"] != "IS_NULL" {
		t.Errorf("expected IS_NULL, got %v", cond["Operator"])
	}
}

func TestTranspileIsNotNull(t *testing.T) {
	cond := getCondition(t, `rule T { when { val is not null } then A {} }`)
	if cond["Operator"] != "!IS_NULL" {
		t.Errorf("expected !IS_NULL, got %v", cond["Operator"])
	}
}

func TestTranspileRetains(t *testing.T) {
	cond := getCondition(t, `rule T { when { a retains b } then A {} }`)
	if cond["Operator"] != "LIST_RETAIN" {
		t.Errorf("expected LIST_RETAIN, got %v", cond["Operator"])
	}
}

func TestTranspileSubsetOf(t *testing.T) {
	cond := getCondition(t, `rule T { when { a subset_of b } then A {} }`)
	if cond["Operator"] != "SUB_LIST_IN" {
		t.Errorf("expected SUB_LIST_IN, got %v", cond["Operator"])
	}
}

func TestTranspileConstInlining(t *testing.T) {
	cond := getCondition(t, `const LIMIT = 100
rule T { when { x > LIMIT } then A {} }`)
	rhs := cond["Rhs"].(map[string]any)
	constVal := rhs["Const"].(map[string]any)
	if constVal["NumConst"] != float64(100) {
		t.Errorf("expected inlined const 100, got %v", constVal["NumConst"])
	}
}

func TestTranspileConstListInlining(t *testing.T) {
	cond := getCondition(t, `const TIERS = ["gold", "platinum"]
rule T { when { tier in TIERS } then A {} }`)
	rhs := cond["Rhs"].(map[string]any)
	if rhs["ConstList"] == nil {
		t.Error("expected ConstList for inlined list const")
	}
}

func TestTranspileAction(t *testing.T) {
	result := transpileArb(t, `rule T { when { true } then Grant { level: "admin", ttl: 3600 } }`)
	rules := result["rules"].([]any)
	rule := rules[0].(map[string]any)
	action := rule["action"].(map[string]any)
	if action["ActionName"] != "Grant" {
		t.Errorf("expected ActionName Grant, got %v", action["ActionName"])
	}
	params := action["ParamMap"].(map[string]any)
	if params["level"] == nil || params["ttl"] == nil {
		t.Error("expected level and ttl params")
	}
}

func TestTranspileOtherwise(t *testing.T) {
	result := transpileArb(t, `rule T { when { true } then Allow {} otherwise Deny { reason: "nope" } }`)
	rules := result["rules"].([]any)
	rule := rules[0].(map[string]any)
	if rule["fallback"] == nil {
		t.Fatal("expected fallback")
	}
	fallback := rule["fallback"].(map[string]any)
	if fallback["ActionName"] != "Deny" {
		t.Errorf("expected Deny fallback, got %v", fallback["ActionName"])
	}
}

func TestTranspilePriority(t *testing.T) {
	result := transpileArb(t, `rule T priority 5 { when { true } then A {} }`)
	rules := result["rules"].([]any)
	rule := rules[0].(map[string]any)
	if rule["priority"] != float64(5) {
		t.Errorf("expected priority 5, got %v", rule["priority"])
	}
}

func TestTranspileFeature(t *testing.T) {
	result := transpileArb(t, `feature user from "user-svc" { age: number name: string }`)
	features := result["features"].(map[string]any)
	user := features["user"].(map[string]any)
	if user["source"] != "user-svc" {
		t.Errorf("expected source user-svc, got %v", user["source"])
	}
	fields := user["fields"].(map[string]any)
	if fields["age"] != "number" || fields["name"] != "string" {
		t.Errorf("unexpected fields: %v", fields)
	}
}

func TestTranspileThreeWayAnd(t *testing.T) {
	cond := getCondition(t, `rule T { when { a > 1 and b > 2 and c > 3 } then A {} }`)
	if cond["OpLogic"] != "&&" {
		t.Error("expected &&")
	}
	conditions := cond["Conditions"].([]any)
	if len(conditions) != 3 {
		t.Errorf("expected 3 flattened conditions, got %d", len(conditions))
	}
}

func TestTranspileParenGrouping(t *testing.T) {
	cond := getCondition(t, `rule T { when { (a > 1 or b > 2) and c > 3 } then A {} }`)
	if cond["OpLogic"] != "&&" {
		t.Error("expected top-level &&")
	}
	conditions := cond["Conditions"].([]any)
	if len(conditions) != 2 {
		t.Errorf("expected 2 conditions (or-group + comparison), got %d", len(conditions))
	}
	// First should be an || group
	orGroup := conditions[0].(map[string]any)
	if orGroup["OpLogic"] != "||" {
		t.Error("expected nested || group")
	}
}

func TestTranspileMultipleRules(t *testing.T) {
	result := transpileArb(t, `
rule A { when { x > 1 } then Act1 {} }
rule B { when { y < 2 } then Act2 {} }
rule C { when { z == 3 } then Act3 {} }
`)
	rules := result["rules"].([]any)
	if len(rules) != 3 {
		t.Errorf("expected 3 rules, got %d", len(rules))
	}
}

func TestTranspileQuantifier(t *testing.T) {
	cond := getCondition(t, `rule T { when { any item in items { item > 0 } } then A {} }`)
	if cond["ForeachOperator"] != "FOREACH" {
		t.Errorf("expected FOREACH, got %v", cond["ForeachOperator"])
	}
	if cond["ForeachLogic"] != "||" {
		t.Errorf("expected || for any, got %v", cond["ForeachLogic"])
	}
}

func TestTranspileQuantifierAll(t *testing.T) {
	cond := getCondition(t, `rule T { when { all item in items { item > 0 } } then A {} }`)
	if cond["ForeachLogic"] != "&&" {
		t.Errorf("expected && for all, got %v", cond["ForeachLogic"])
	}
}

func TestTranspileQuantifierNone(t *testing.T) {
	cond := getCondition(t, `rule T { when { none item in items { item > 0 } } then A {} }`)
	if cond["ForeachLogic"] != "!||" {
		t.Errorf("expected !|| for none, got %v", cond["ForeachLogic"])
	}
}

// =============================================================================
// FULL FILE TESTS
// =============================================================================

func TestTranspileFullPricing(t *testing.T) {
	source, err := os.ReadFile("testdata/pricing.arb")
	if err != nil {
		t.Skip("testdata not available")
	}
	out, err := Transpile(source)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	t.Logf("JSON length: %d bytes", len(out))

	for _, expected := range []string{"FreeShipping", "VIPDiscount", "WelcomeOffer", "ApplyShipping", "ApplyDiscount"} {
		if !strings.Contains(out, expected) {
			t.Errorf("missing %s in output", expected)
		}
	}
}

func TestTranspileFullFraud(t *testing.T) {
	source, err := os.ReadFile("testdata/fraud.arb")
	if err != nil {
		t.Skip("testdata not available")
	}
	out, err := Transpile(source)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	t.Logf("JSON length: %d bytes", len(out))
	if !strings.Contains(out, "InstantBlock") {
		t.Error("missing InstantBlock rule")
	}
}

func TestTranspileFullModeration(t *testing.T) {
	source, err := os.ReadFile("testdata/moderation.arb")
	if err != nil {
		t.Skip("testdata not available")
	}
	out, err := Transpile(source)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	t.Logf("JSON length: %d bytes", len(out))
	if !strings.Contains(out, "AutoBlock") {
		t.Error("missing AutoBlock rule")
	}
}
