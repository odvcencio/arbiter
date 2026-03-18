package arbiter

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

var (
	arbLangOnce   sync.Once
	arbLangCached *gotreesitter.Language
	arbLangErr    error
)

func getArbiterLanguage() (*gotreesitter.Language, error) {
	arbLangOnce.Do(func() {
		arbLangCached, arbLangErr = GenerateLanguage(ArbiterGrammar())
	})
	return arbLangCached, arbLangErr
}

// TranspileResult holds the parsed output of an .arb file.
type TranspileResult struct {
	Features map[string]Feature       `json:"features,omitempty"`
	Consts   map[string]any           `json:"consts,omitempty"`
	Rules    []RuleOutput             `json:"rules"`
}

type Feature struct {
	Name   string            `json:"name"`
	Source string            `json:"source"`
	Fields map[string]string `json:"fields"`
}

type RuleOutput struct {
	Name      string `json:"name"`
	Priority  int    `json:"priority,omitempty"`
	Condition any    `json:"condition"`
	Action    any    `json:"action"`
	Fallback  any    `json:"fallback,omitempty"`
}

// Transpile converts .arb source to Arishem-compatible JSON.
func Transpile(source []byte) (string, error) {
	lang, err := getArbiterLanguage()
	if err != nil {
		return "", fmt.Errorf("generate arbiter language: %w", err)
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}

	root := tree.RootNode()
	if root.HasError() {
		return "", fmt.Errorf("parse errors in arbiter source")
	}

	t := &arbTranspiler{
		src:    source,
		lang:   lang,
		consts: make(map[string]any),
	}

	result := t.emitSourceFile(root)

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal JSON: %w", err)
	}
	return string(out), nil
}

// TranspileRule converts a single rule's condition to Arishem JSON (no wrapper).
func TranspileRule(source []byte, ruleName string) (string, error) {
	lang, err := getArbiterLanguage()
	if err != nil {
		return "", fmt.Errorf("generate arbiter language: %w", err)
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}

	root := tree.RootNode()
	if root.HasError() {
		return "", fmt.Errorf("parse errors in arbiter source")
	}

	t := &arbTranspiler{
		src:    source,
		lang:   lang,
		consts: make(map[string]any),
	}

	// First pass: collect consts
	t.collectConsts(root)

	// Find the named rule
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if t.nodeType(c) == "rule_declaration" {
			nameNode := t.childByField(c, "name")
			if nameNode != nil && t.text(nameNode) == ruleName {
				rule := t.emitRule(c)
				out, err := json.MarshalIndent(rule.Condition, "", "  ")
				if err != nil {
					return "", err
				}
				return string(out), nil
			}
		}
	}
	return "", fmt.Errorf("rule %q not found", ruleName)
}

type arbTranspiler struct {
	src    []byte
	lang   *gotreesitter.Language
	consts map[string]any
}

func (t *arbTranspiler) text(n *gotreesitter.Node) string {
	return string(t.src[n.StartByte():n.EndByte()])
}

func (t *arbTranspiler) nodeType(n *gotreesitter.Node) string {
	return n.Type(t.lang)
}

func (t *arbTranspiler) childByField(n *gotreesitter.Node, field string) *gotreesitter.Node {
	return n.ChildByFieldName(field, t.lang)
}

func (t *arbTranspiler) emitSourceFile(n *gotreesitter.Node) *TranspileResult {
	result := &TranspileResult{
		Features: make(map[string]Feature),
		Consts:   make(map[string]any),
	}

	// First pass: collect consts
	t.collectConsts(n)

	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		switch t.nodeType(c) {
		case "feature_declaration":
			f := t.emitFeature(c)
			result.Features[f.Name] = f
		case "const_declaration":
			name := t.text(t.childByField(c, "name"))
			result.Consts[name] = t.consts[name]
		case "rule_declaration":
			result.Rules = append(result.Rules, t.emitRule(c))
		}
	}

	return result
}

func (t *arbTranspiler) collectConsts(root *gotreesitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if t.nodeType(c) == "const_declaration" {
			name := t.text(t.childByField(c, "name"))
			value := t.emitExpr(t.childByField(c, "value"))
			t.consts[name] = value
		}
	}
}

func (t *arbTranspiler) emitFeature(n *gotreesitter.Node) Feature {
	f := Feature{
		Fields: make(map[string]string),
	}
	if nameNode := t.childByField(n, "name"); nameNode != nil {
		f.Name = t.text(nameNode)
	}
	if srcNode := t.childByField(n, "source"); srcNode != nil {
		f.Source = stripQuotes(t.text(srcNode))
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if t.nodeType(c) == "field_declaration" {
			fieldName := t.text(t.childByField(c, "name"))
			fieldType := t.text(t.childByField(c, "type"))
			f.Fields[fieldName] = fieldType
		}
	}
	return f
}

func (t *arbTranspiler) emitRule(n *gotreesitter.Node) RuleOutput {
	rule := RuleOutput{}

	if nameNode := t.childByField(n, "name"); nameNode != nil {
		rule.Name = t.text(nameNode)
	}
	if priNode := t.childByField(n, "priority"); priNode != nil {
		rule.Priority = parseInt(t.text(priNode))
	}

	// Condition
	if whenNode := t.childByField(n, "condition"); whenNode != nil {
		if exprNode := t.childByField(whenNode, "expr"); exprNode != nil {
			rule.Condition = t.emitExpr(exprNode)
		}
	}

	// Action
	if thenNode := t.childByField(n, "action"); thenNode != nil {
		rule.Action = t.emitAction(thenNode)
	}

	// Fallback
	if fallbackNode := t.childByField(n, "fallback"); fallbackNode != nil {
		rule.Fallback = t.emitAction(fallbackNode)
	}

	return rule
}

func (t *arbTranspiler) emitAction(n *gotreesitter.Node) map[string]any {
	action := map[string]any{}
	if nameNode := t.childByField(n, "action_name"); nameNode != nil {
		action["ActionName"] = t.text(nameNode)
	}

	params := map[string]any{}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if t.nodeType(c) == "param_assignment" {
			key := t.text(t.childByField(c, "key"))
			value := t.emitExpr(t.childByField(c, "value"))
			params[key] = value
		}
	}
	if len(params) > 0 {
		action["ParamMap"] = params
	}
	return action
}

func (t *arbTranspiler) emitExpr(n *gotreesitter.Node) any {
	if n == nil {
		return nil
	}

	switch t.nodeType(n) {
	// --- Logical ---
	case "or_expr":
		return t.emitLogical(n, "||")
	case "and_expr":
		return t.emitLogical(n, "&&")
	case "not_expr":
		operand := t.emitExpr(t.childByField(n, "operand"))
		return map[string]any{
			"OpLogic":    "not",
			"Conditions": []any{operand},
		}

	// --- Comparison ---
	case "comparison_expr":
		return t.emitComparison(n)

	// --- Collection operators ---
	case "in_expr":
		return t.emitBinaryOp(n, "LIST_IN")
	case "not_in_expr":
		return t.emitBinaryOp(n, "!LIST_IN")
	case "contains_expr":
		return t.emitBinaryOp(n, "LIST_CONTAINS")
	case "not_contains_expr":
		return t.emitBinaryOp(n, "!LIST_CONTAINS")
	case "retains_expr":
		return t.emitBinaryOp(n, "LIST_RETAIN")
	case "not_retains_expr":
		return t.emitBinaryOp(n, "!LIST_RETAIN")
	case "subset_of_expr":
		return t.emitBinaryOp(n, "SUB_LIST_IN")
	case "superset_of_expr":
		return t.emitBinaryOp(n, "SUB_LIST_CONTAINS")
	case "vague_contains_expr":
		return t.emitBinaryOp(n, "LIST_VAGUE_CONTAINS")

	// --- String operators ---
	case "starts_with_expr":
		return t.emitBinaryOp(n, "STRING_START_WITH")
	case "ends_with_expr":
		return t.emitBinaryOp(n, "STRING_END_WITH")
	case "matches_expr":
		return t.emitBinaryOp(n, "CONTAIN_REGEXP")

	// --- Range ---
	case "between_expr":
		return t.emitBetween(n)

	// --- Null checks ---
	case "is_null_expr":
		return map[string]any{
			"Operator": "IS_NULL",
			"Lhs":      t.emitExpr(t.childByField(n, "left")),
		}
	case "is_not_null_expr":
		return map[string]any{
			"Operator": "!IS_NULL",
			"Lhs":      t.emitExpr(t.childByField(n, "left")),
		}

	// --- Math ---
	case "math_expr":
		return t.emitMath(n)

	// --- Quantifiers ---
	case "quantifier_expr":
		return t.emitQuantifier(n)

	// --- Primaries ---
	case "member_expr":
		return t.emitMember(n)
	case "identifier":
		return t.emitIdentifier(n)
	case "number_literal":
		return t.emitNumber(n)
	case "string_literal":
		return t.emitString(n)
	case "bool_literal":
		return t.emitBool(n)
	case "list_literal":
		return t.emitList(n)
	case "paren_expr":
		return t.emitExpr(t.childByField(n, "expr"))

	default:
		// Recurse into single-child wrapper nodes
		if n.NamedChildCount() == 1 {
			return t.emitExpr(n.NamedChild(0))
		}
		return map[string]any{"_raw": t.text(n)}
	}
}

func (t *arbTranspiler) emitLogical(n *gotreesitter.Node, opLogic string) map[string]any {
	left := t.emitExpr(t.childByField(n, "left"))
	right := t.emitExpr(t.childByField(n, "right"))

	// Flatten same-level logic: (a && b) && c → [a, b, c]
	conditions := []any{}
	for _, side := range []any{left, right} {
		if m, ok := side.(map[string]any); ok && m["OpLogic"] == opLogic {
			if cs, ok := m["Conditions"].([]any); ok {
				conditions = append(conditions, cs...)
				continue
			}
		}
		conditions = append(conditions, side)
	}

	return map[string]any{
		"OpLogic":    opLogic,
		"Conditions": conditions,
	}
}

func (t *arbTranspiler) emitComparison(n *gotreesitter.Node) map[string]any {
	left := t.emitExpr(t.childByField(n, "left"))
	opNode := t.childByField(n, "op")
	right := t.emitExpr(t.childByField(n, "right"))

	opStr := ""
	if opNode != nil {
		opStr = t.text(opNode)
	} else {
		// The operator is an anonymous token between the left and right children.
		// Scan the source bytes between left and right to extract it.
		leftNode := t.childByField(n, "left")
		rightNode := t.childByField(n, "right")
		if leftNode != nil && rightNode != nil {
			between := strings.TrimSpace(string(t.src[leftNode.EndByte():rightNode.StartByte()]))
			opStr = between
		}
		if opStr == "" {
			// Last resort: scan all children
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				txt := t.text(c)
				if txt == "==" || txt == "!=" || txt == ">" || txt == "<" || txt == ">=" || txt == "<=" {
					opStr = txt
					break
				}
			}
		}
	}
	arishemOp := mapComparisonOp(opStr)

	return map[string]any{
		"Operator": arishemOp,
		"Lhs":      left,
		"Rhs":      right,
	}
}

func (t *arbTranspiler) emitBinaryOp(n *gotreesitter.Node, operator string) map[string]any {
	left := t.emitExpr(t.childByField(n, "left"))
	right := t.emitExpr(t.childByField(n, "right"))
	return map[string]any{
		"Operator": operator,
		"Lhs":      left,
		"Rhs":      right,
	}
}

func (t *arbTranspiler) emitBetween(n *gotreesitter.Node) map[string]any {
	left := t.emitExpr(t.childByField(n, "left"))
	low := t.emitExpr(t.childByField(n, "low"))
	high := t.emitExpr(t.childByField(n, "high"))
	open := t.text(t.childByField(n, "open"))
	close := t.text(t.childByField(n, "close"))

	op := "BETWEEN_ALL_CLOSE" // [a, b]
	if open == "(" && close == ")" {
		op = "BETWEEN_ALL_OPEN" // (a, b)
	} else if open == "(" && close == "]" {
		op = "BETWEEN_LEFT_OPEN_RIGHT_CLOSE" // (a, b]
	} else if open == "[" && close == ")" {
		op = "BETWEEN_LEFT_CLOSE_RIGHT_OPEN" // [a, b)
	}

	return map[string]any{
		"Operator": op,
		"Lhs":      left,
		"Rhs":      map[string]any{"ConstList": []any{low, high}},
	}
}

func (t *arbTranspiler) emitMath(n *gotreesitter.Node) map[string]any {
	left := t.emitExpr(t.childByField(n, "left"))
	op := t.text(t.childByField(n, "op"))
	right := t.emitExpr(t.childByField(n, "right"))
	return map[string]any{
		"MathExpr": map[string]any{
			"Operator": op,
			"Lhs":      left,
			"Rhs":      right,
		},
	}
}

func (t *arbTranspiler) emitQuantifier(n *gotreesitter.Node) map[string]any {
	quantifier := t.text(t.childByField(n, "quantifier"))
	varName := t.text(t.childByField(n, "var"))
	collection := t.emitExpr(t.childByField(n, "collection"))
	body := t.emitExpr(t.childByField(n, "body"))

	// Map quantifier to Arishem foreach logic
	foreachLogic := "&&" // all
	if quantifier == "any" {
		foreachLogic = "||"
	} else if quantifier == "none" {
		foreachLogic = "!||"
	}

	return map[string]any{
		"ForeachOperator": "FOREACH",
		"ForeachLogic":    foreachLogic,
		"ForeachVar":      varName,
		"Lhs":             collection,
		"Condition":        body,
	}
}

func (t *arbTranspiler) emitMember(n *gotreesitter.Node) map[string]any {
	path := t.text(n) // e.g. "user.age"
	return map[string]any{"VarExpr": path}
}

func (t *arbTranspiler) emitIdentifier(n *gotreesitter.Node) any {
	name := t.text(n)

	// Check if it's a const reference
	if val, ok := t.consts[name]; ok {
		return val
	}

	// Check for boolean literals
	if name == "true" {
		return map[string]any{"Const": map[string]any{"BoolConst": true}}
	}
	if name == "false" {
		return map[string]any{"Const": map[string]any{"BoolConst": false}}
	}
	if name == "null" {
		return map[string]any{"Const": nil}
	}

	// Treat as a variable reference
	return map[string]any{"VarExpr": name}
}

func (t *arbTranspiler) emitNumber(n *gotreesitter.Node) map[string]any {
	text := t.text(n)
	num := parseFloat(text)
	return map[string]any{"Const": map[string]any{"NumConst": num}}
}

func (t *arbTranspiler) emitString(n *gotreesitter.Node) map[string]any {
	text := stripQuotes(t.text(n))
	return map[string]any{"Const": map[string]any{"StrConst": text}}
}

func (t *arbTranspiler) emitBool(n *gotreesitter.Node) map[string]any {
	text := t.text(n)
	return map[string]any{"Const": map[string]any{"BoolConst": text == "true"}}
}

func (t *arbTranspiler) emitList(n *gotreesitter.Node) map[string]any {
	var items []any
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		items = append(items, t.emitExpr(c))
	}
	return map[string]any{"ConstList": items}
}

// --- Helpers ---

func mapComparisonOp(op string) string {
	switch strings.TrimSpace(op) {
	case "==":
		return "=="
	case "!=":
		return "!="
	case ">":
		return ">"
	case "<":
		return "<"
	case ">=":
		return ">="
	case "<=":
		return "<="
	default:
		return op
	}
}

func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func parseFloat(s string) float64 {
	negative := false
	if len(s) > 0 && s[0] == '-' {
		negative = true
		s = s[1:]
	}

	var result float64
	decimal := false
	divisor := 1.0

	for _, c := range s {
		if c == '.' {
			decimal = true
			continue
		}
		if c >= '0' && c <= '9' {
			if decimal {
				divisor *= 10
				result += float64(c-'0') / divisor
			} else {
				result = result*10 + float64(c-'0')
			}
		}
	}

	if negative {
		result = -result
	}
	return result
}
