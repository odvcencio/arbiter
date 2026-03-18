// Package decompile converts Arishem JSON (ByteDance rule engine format)
// back into readable .arb source code.
package decompile

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ArishemRule holds the raw JSON strings for one rule to decompile.
type ArishemRule struct {
	Name      string
	Priority  int
	Condition string
	Action    string
	Fallback  string
}

// ArishemToArb converts a slice of Arishem JSON rules into formatted .arb source.
func ArishemToArb(rules []ArishemRule) (string, error) {
	var buf strings.Builder

	for i, r := range rules {
		if i > 0 {
			buf.WriteString("\n")
		}

		// Header
		buf.WriteString(fmt.Sprintf("rule %s priority %d {\n", r.Name, r.Priority))

		// Condition
		if r.Condition != "" {
			var cond map[string]any
			if err := json.Unmarshal([]byte(r.Condition), &cond); err != nil {
				return "", fmt.Errorf("rule %s: parse condition: %w", r.Name, err)
			}
			buf.WriteString("    when {\n")
			condStr := emitCondition(cond, 2)
			buf.WriteString(condStr)
			buf.WriteString("    }\n")
		}

		// Action
		if r.Action != "" {
			var act map[string]any
			if err := json.Unmarshal([]byte(r.Action), &act); err != nil {
				return "", fmt.Errorf("rule %s: parse action: %w", r.Name, err)
			}
			buf.WriteString(emitAction(act, "then", 1))
		}

		// Fallback
		if r.Fallback != "" {
			var fb map[string]any
			if err := json.Unmarshal([]byte(r.Fallback), &fb); err != nil {
				return "", fmt.Errorf("rule %s: parse fallback: %w", r.Name, err)
			}
			buf.WriteString(emitAction(fb, "otherwise", 1))
		}

		buf.WriteString("}\n")
	}

	return buf.String(), nil
}

// indent returns n levels of 4-space indentation.
func indent(n int) string {
	return strings.Repeat("    ", n)
}

// emitCondition renders a condition node as .arb when-block lines.
// depth is the indentation level for each line.
func emitCondition(node map[string]any, depth int) string {
	// Top-level logical operator: flatten into when-block lines
	if opLogic, ok := node["OpLogic"].(string); ok {
		conditions, _ := node["Conditions"].([]any)
		// "not" is unary, render as a single expression
		if opLogic == "not" {
			return indent(depth) + emitExpr(node) + "\n"
		}
		return emitLogicalBlock(opLogic, conditions, depth)
	}

	// Foreach at top level
	if _, ok := node["ForeachOperator"]; ok {
		return indent(depth) + emitExpr(node) + "\n"
	}

	// Single expression
	return indent(depth) + emitExpr(node) + "\n"
}

// emitLogicalBlock renders a logical operator's conditions as indented lines
// in a when-block. For the top level, conditions are separated by the operator
// keyword on each subsequent line.
func emitLogicalBlock(opLogic string, conditions []any, depth int) string {
	if len(conditions) == 0 {
		return ""
	}

	keyword := logicKeyword(opLogic)
	var buf strings.Builder

	for i, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}

		line := emitExpr(cond)
		// Parenthesize nested logic of a different type
		if innerOp, ok := cond["OpLogic"].(string); ok && innerOp != opLogic && innerOp != "not" {
			line = "(" + line + ")"
		}

		if i == 0 {
			buf.WriteString(indent(depth) + line + "\n")
		} else {
			buf.WriteString(indent(depth) + keyword + " " + line + "\n")
		}
	}

	return buf.String()
}

// emitExpr renders an expression node as a single-line .arb expression string.
func emitExpr(node map[string]any) string {
	// Logical operator (nested)
	if opLogic, ok := node["OpLogic"].(string); ok {
		conditions, _ := node["Conditions"].([]any)
		return emitLogicalExpr(opLogic, conditions)
	}

	// Foreach / quantifier
	if _, ok := node["ForeachOperator"]; ok {
		return emitForeach(node)
	}

	// Comparison / collection / string operator
	if operator, ok := node["Operator"].(string); ok {
		return emitOperatorExpr(operator, node)
	}

	// Math expression
	if mathExpr, ok := node["MathExpr"].(map[string]any); ok {
		return emitMathExpr(mathExpr)
	}

	// Value nodes
	return emitValue(node)
}

// emitLogicalExpr renders a nested logical expression with parenthesization.
func emitLogicalExpr(opLogic string, conditions []any) string {
	if opLogic == "not" && len(conditions) > 0 {
		inner, ok := conditions[0].(map[string]any)
		if !ok {
			return "not ???"
		}
		return "not (" + emitExpr(inner) + ")"
	}

	keyword := logicKeyword(opLogic)
	parts := make([]string, 0, len(conditions))
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		part := emitExpr(cond)
		// Parenthesize nested logic of different precedence
		if innerOp, ok := cond["OpLogic"].(string); ok && innerOp != opLogic {
			part = "(" + part + ")"
		}
		parts = append(parts, part)
	}

	sep := " " + keyword + " "
	result := strings.Join(parts, sep)

	return result
}

// emitOperatorExpr renders a comparison/collection/string operator expression.
func emitOperatorExpr(operator string, node map[string]any) string {
	arbOp := mapOperatorToArb(operator)

	// Unary operators (null checks)
	if operator == "IS_NULL" || operator == "!IS_NULL" {
		lhs := emitValue(toMap(node["Lhs"]))
		return lhs + " " + arbOp
	}

	// Between operators
	if isBetweenOp(operator) {
		return emitBetweenExpr(operator, node)
	}

	lhs := emitValue(toMap(node["Lhs"]))
	rhs := emitValue(toMap(node["Rhs"]))

	return lhs + " " + arbOp + " " + rhs
}

// emitBetweenExpr renders a between expression with proper bracket notation.
func emitBetweenExpr(operator string, node map[string]any) string {
	lhs := emitValue(toMap(node["Lhs"]))

	rhsNode := toMap(node["Rhs"])
	var low, high string

	// Rhs is a ConstList with two elements [low, high]
	if constList, ok := rhsNode["ConstList"].([]any); ok && len(constList) >= 2 {
		low = emitValue(toMap(constList[0]))
		high = emitValue(toMap(constList[1]))
	}

	open, close := betweenBrackets(operator)
	return lhs + " between " + open + low + ", " + high + close
}

// emitMathExpr renders a math expression.
func emitMathExpr(node map[string]any) string {
	op, _ := node["Operator"].(string)
	lhs := emitValue(toMap(node["Lhs"]))
	rhs := emitValue(toMap(node["Rhs"]))

	// Parenthesize nested math of lower precedence
	if lhsNode := toMap(node["Lhs"]); lhsNode != nil {
		if _, ok := lhsNode["MathExpr"]; ok {
			innerOp := toMap(lhsNode["MathExpr"])
			if innerOp != nil {
				if needsMathParens(innerOp["Operator"], op) {
					lhs = "(" + lhs + ")"
				}
			}
		}
	}
	if rhsNode := toMap(node["Rhs"]); rhsNode != nil {
		if _, ok := rhsNode["MathExpr"]; ok {
			innerOp := toMap(rhsNode["MathExpr"])
			if innerOp != nil {
				if needsMathParens(innerOp["Operator"], op) {
					rhs = "(" + rhs + ")"
				}
			}
		}
	}

	return lhs + " " + op + " " + rhs
}

// emitForeach renders a quantifier expression.
func emitForeach(node map[string]any) string {
	logic, _ := node["ForeachLogic"].(string)
	varName, _ := node["ForeachVar"].(string)
	collection := emitValue(toMap(node["Lhs"]))
	body := emitExpr(toMap(node["Condition"]))

	quantifier := "all"
	if logic == "||" {
		quantifier = "any"
	} else if logic == "!||" {
		quantifier = "none"
	}

	return quantifier + " " + varName + " in " + collection + " { " + body + " }"
}

// emitValue renders a value node (variable, constant, list, math).
func emitValue(node map[string]any) string {
	if node == nil {
		return "null"
	}

	// Variable reference
	if varExpr, ok := node["VarExpr"].(string); ok {
		return varExpr
	}

	// Constant
	if constVal, ok := node["Const"].(map[string]any); ok {
		return emitConst(constVal)
	}
	// Const is nil (null)
	if _, hasConst := node["Const"]; hasConst {
		if node["Const"] == nil {
			return "null"
		}
	}

	// Constant list
	if constList, ok := node["ConstList"].([]any); ok {
		return emitConstList(constList)
	}

	// Math expression
	if mathExpr, ok := node["MathExpr"].(map[string]any); ok {
		return emitMathExpr(mathExpr)
	}

	// Logical / operator expression inside a value position
	if _, ok := node["OpLogic"]; ok {
		return emitExpr(node)
	}
	if _, ok := node["Operator"]; ok {
		return emitExpr(node)
	}

	// Bare constant (not wrapped in Const): e.g. {"NumConst": 42}
	// This happens inside ConstList items.
	if _, ok := node["StrConst"]; ok {
		return emitConst(node)
	}
	if _, ok := node["NumConst"]; ok {
		return emitConst(node)
	}
	if _, ok := node["BoolConst"]; ok {
		return emitConst(node)
	}

	return "???"
}

// emitConst renders a constant value.
func emitConst(node map[string]any) string {
	if s, ok := node["StrConst"].(string); ok {
		return fmt.Sprintf("%q", s)
	}
	if n, ok := node["NumConst"].(float64); ok {
		return formatNumber(n)
	}
	if b, ok := node["BoolConst"].(bool); ok {
		if b {
			return "true"
		}
		return "false"
	}
	return "null"
}

// emitConstList renders a list literal.
func emitConstList(items []any) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		m := toMap(item)
		if m == nil {
			parts = append(parts, "null")
			continue
		}
		// Items in ConstList are {StrConst:...}, {NumConst:...}, etc. directly
		// (not wrapped in Const), or they may be wrapped.
		if _, ok := m["Const"]; ok {
			parts = append(parts, emitValue(m))
		} else {
			parts = append(parts, emitConst(m))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// emitAction renders a then/otherwise action block.
func emitAction(node map[string]any, keyword string, depth int) string {
	name, _ := node["ActionName"].(string)
	var buf strings.Builder
	buf.WriteString(indent(depth) + keyword + " " + name + " {\n")

	if paramMap, ok := node["ParamMap"].(map[string]any); ok {
		// Sort keys for deterministic output
		keys := make([]string, 0, len(paramMap))
		for k := range paramMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			val := paramMap[key]
			valStr := emitValue(toMap(val))
			buf.WriteString(indent(depth+1) + key + ": " + valStr + ",\n")
		}
	}

	buf.WriteString(indent(depth) + "}\n")
	return buf.String()
}

// --- Operator mapping ---

func logicKeyword(op string) string {
	switch op {
	case "&&":
		return "and"
	case "||":
		return "or"
	case "not":
		return "not"
	}
	return op
}

func mapOperatorToArb(op string) string {
	switch op {
	case "==":
		return "=="
	case "!=":
		return "!="
	case ">":
		return ">"
	case ">=":
		return ">="
	case "<":
		return "<"
	case "<=":
		return "<="
	case "LIST_IN":
		return "in"
	case "!LIST_IN":
		return "not in"
	case "LIST_CONTAINS":
		return "contains"
	case "!LIST_CONTAINS":
		return "not contains"
	case "LIST_RETAIN":
		return "retains"
	case "!LIST_RETAIN":
		return "not retains"
	case "LIST_VAGUE_CONTAINS":
		return "~contains"
	case "SUBSET_OF", "SUB_LIST_IN":
		return "subset_of"
	case "SUPERSET_OF", "SUB_LIST_CONTAINS":
		return "superset_of"
	case "STRING_START_WITH":
		return "starts_with"
	case "STRING_END_WITH":
		return "ends_with"
	case "CONTAIN_REGEXP":
		return "matches"
	case "IS_NULL":
		return "is null"
	case "!IS_NULL":
		return "is not null"
	}
	return op
}

func isBetweenOp(op string) bool {
	switch op {
	case "BETWEEN_ALL_CLOSE", "BETWEEN_ALL_OPEN",
		"BETWEEN_LEFT_CLOSE_RIGHT_OPEN", "BETWEEN_LEFT_OPEN_RIGHT_CLOSE":
		return true
	}
	return false
}

func betweenBrackets(op string) (string, string) {
	switch op {
	case "BETWEEN_ALL_CLOSE":
		return "[", "]"
	case "BETWEEN_ALL_OPEN":
		return "(", ")"
	case "BETWEEN_LEFT_CLOSE_RIGHT_OPEN":
		return "[", ")"
	case "BETWEEN_LEFT_OPEN_RIGHT_CLOSE":
		return "(", "]"
	}
	return "[", "]"
}

func needsMathParens(innerOp, outerOp any) bool {
	innerPrec := mathPrecedence(innerOp)
	outerPrec := mathPrecedence(outerOp)
	return innerPrec < outerPrec
}

func mathPrecedence(op any) int {
	s, _ := op.(string)
	switch s {
	case "+", "-":
		return 1
	case "*", "/", "%":
		return 2
	}
	return 0
}

// --- Helpers ---

func toMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func formatNumber(n float64) string {
	if n == math.Trunc(n) && !math.IsInf(n, 0) && !math.IsNaN(n) {
		return fmt.Sprintf("%g", n)
	}
	return fmt.Sprintf("%g", n)
}
