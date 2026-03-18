package emit

import (
	"fmt"
	"strings"

	"github.com/odvcencio/arbiter"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// ToRego converts .arb source to Rego (OPA) policy text.
func ToRego(source []byte) (string, error) {
	lang, err := arbiter.GetLanguage()
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

	e := &regoEmitter{
		src:    source,
		lang:   lang,
		consts: make(map[string]string),
	}

	return e.emitSourceFile(root), nil
}

type regoEmitter struct {
	src    []byte
	lang   *gotreesitter.Language
	consts map[string]string // const name -> rego literal
}

func (e *regoEmitter) text(n *gotreesitter.Node) string {
	return string(e.src[n.StartByte():n.EndByte()])
}

func (e *regoEmitter) nodeType(n *gotreesitter.Node) string {
	return n.Type(e.lang)
}

func (e *regoEmitter) childByField(n *gotreesitter.Node, field string) *gotreesitter.Node {
	return n.ChildByFieldName(field, e.lang)
}

func (e *regoEmitter) emitSourceFile(n *gotreesitter.Node) string {
	var buf strings.Builder
	buf.WriteString("package rules\n\nimport rego.v1\n")

	// First pass: collect consts
	e.collectConsts(n)

	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if e.nodeType(c) == "rule_declaration" {
			buf.WriteString("\n")
			buf.WriteString(e.emitRule(c))
		}
	}

	return buf.String()
}

func (e *regoEmitter) collectConsts(root *gotreesitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if e.nodeType(c) == "const_declaration" {
			name := e.text(e.childByField(c, "name"))
			value := e.emitValue(e.childByField(c, "value"))
			e.consts[name] = value
		}
	}
}

func (e *regoEmitter) emitRule(n *gotreesitter.Node) string {
	name := ""
	if nameNode := e.childByField(n, "name"); nameNode != nil {
		name = toSnakeCase(e.text(nameNode))
	}

	whenNode := e.childByField(n, "condition")
	if whenNode == nil {
		return ""
	}
	exprNode := e.childByField(whenNode, "expr")
	if exprNode == nil {
		return ""
	}

	// Check if the top-level expression is an OR — emit multiple rule bodies
	if e.nodeType(exprNode) == "or_expr" {
		branches := e.flattenOr(exprNode)
		var buf strings.Builder
		for _, branch := range branches {
			buf.WriteString(name + " if {\n")
			lines := e.emitConditionLines(branch)
			for _, line := range lines {
				buf.WriteString("    " + line + "\n")
			}
			buf.WriteString("}\n\n")
		}
		return buf.String()
	}

	var buf strings.Builder
	buf.WriteString(name + " if {\n")
	lines := e.emitConditionLines(exprNode)
	for _, line := range lines {
		buf.WriteString("    " + line + "\n")
	}
	buf.WriteString("}\n")
	return buf.String()
}

// flattenOr collects all branches of nested or_expr nodes.
func (e *regoEmitter) flattenOr(n *gotreesitter.Node) []*gotreesitter.Node {
	if e.nodeType(n) != "or_expr" {
		return []*gotreesitter.Node{n}
	}
	left := e.childByField(n, "left")
	right := e.childByField(n, "right")
	var result []*gotreesitter.Node
	result = append(result, e.flattenOr(left)...)
	result = append(result, e.flattenOr(right)...)
	return result
}

// emitConditionLines returns one line per AND-ed condition.
func (e *regoEmitter) emitConditionLines(n *gotreesitter.Node) []string {
	if e.nodeType(n) == "and_expr" {
		parts := e.flattenAnd(n)
		var lines []string
		for _, part := range parts {
			lines = append(lines, e.emitConditionLines(part)...)
		}
		return lines
	}
	return []string{e.emitExpr(n)}
}

func (e *regoEmitter) flattenAnd(n *gotreesitter.Node) []*gotreesitter.Node {
	if e.nodeType(n) != "and_expr" {
		return []*gotreesitter.Node{n}
	}
	left := e.childByField(n, "left")
	right := e.childByField(n, "right")
	var result []*gotreesitter.Node
	result = append(result, e.flattenAnd(left)...)
	result = append(result, e.flattenAnd(right)...)
	return result
}

func (e *regoEmitter) emitExpr(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}

	switch e.nodeType(n) {
	case "or_expr":
		// Within a nested context, emit with explicit or
		left := e.emitExpr(e.childByField(n, "left"))
		right := e.emitExpr(e.childByField(n, "right"))
		return left + " # or " + right

	case "and_expr":
		parts := e.flattenAnd(n)
		var strs []string
		for _, p := range parts {
			strs = append(strs, e.emitExpr(p))
		}
		return strings.Join(strs, "; ")

	case "not_expr":
		operand := e.emitExpr(e.childByField(n, "operand"))
		return "not " + operand

	case "comparison_expr":
		return e.emitComparison(n)

	case "in_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + " in " + right

	case "not_in_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return "not " + left + " in " + right

	case "starts_with_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return "startswith(" + left + ", " + right + ")"

	case "ends_with_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return "endswith(" + left + ", " + right + ")"

	case "matches_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return "regex.match(" + right + ", " + left + ")"

	case "is_null_expr":
		left := e.emitValue(e.childByField(n, "left"))
		return left + " == null"

	case "is_not_null_expr":
		left := e.emitValue(e.childByField(n, "left"))
		return left + " != null"

	case "contains_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return right + " in " + left

	case "between_expr":
		left := e.emitValue(e.childByField(n, "left"))
		low := e.emitValue(e.childByField(n, "low"))
		high := e.emitValue(e.childByField(n, "high"))
		open := e.text(e.childByField(n, "open"))
		close := e.text(e.childByField(n, "close"))
		var lowOp, highOp string
		if open == "[" {
			lowOp = ">="
		} else {
			lowOp = ">"
		}
		if close == "]" {
			highOp = "<="
		} else {
			highOp = "<"
		}
		return left + " " + lowOp + " " + low + "; " + left + " " + highOp + " " + high

	case "paren_expr":
		return "(" + e.emitExpr(e.childByField(n, "expr")) + ")"

	default:
		return e.emitValue(n)
	}
}

func (e *regoEmitter) emitComparison(n *gotreesitter.Node) string {
	left := e.emitValue(e.childByField(n, "left"))
	right := e.emitValue(e.childByField(n, "right"))
	op := e.extractComparisonOp(n)
	return left + " " + op + " " + right
}

func (e *regoEmitter) extractComparisonOp(n *gotreesitter.Node) string {
	if opNode := e.childByField(n, "op"); opNode != nil {
		return e.text(opNode)
	}
	leftNode := e.childByField(n, "left")
	rightNode := e.childByField(n, "right")
	if leftNode != nil && rightNode != nil {
		between := strings.TrimSpace(string(e.src[leftNode.EndByte():rightNode.StartByte()]))
		if between != "" {
			return between
		}
	}
	return "=="
}

func (e *regoEmitter) emitValue(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}

	switch e.nodeType(n) {
	case "member_expr":
		return "input." + e.text(n)
	case "identifier":
		name := e.text(n)
		if val, ok := e.consts[name]; ok {
			return val
		}
		if name == "true" || name == "false" || name == "null" {
			return name
		}
		return "input." + name
	case "number_literal":
		return e.text(n)
	case "string_literal":
		return e.text(n)
	case "bool_literal":
		return e.text(n)
	case "list_literal":
		return e.emitList(n)
	case "math_expr":
		left := e.emitValue(e.childByField(n, "left"))
		op := e.text(e.childByField(n, "op"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + " " + op + " " + right
	case "paren_expr":
		return "(" + e.emitValue(e.childByField(n, "expr")) + ")"
	default:
		// Recurse into single-child wrapper nodes
		if n.NamedChildCount() == 1 {
			return e.emitValue(n.NamedChild(0))
		}
		return e.text(n)
	}
}

func (e *regoEmitter) emitList(n *gotreesitter.Node) string {
	var items []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		items = append(items, e.emitValue(n.NamedChild(i)))
	}
	return "[" + strings.Join(items, ", ") + "]"
}

// toSnakeCase converts PascalCase/camelCase to snake_case.
func toSnakeCase(s string) string {
	var buf strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				buf.WriteByte('_')
			}
			buf.WriteByte(byte(r - 'A' + 'a'))
		} else {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}
