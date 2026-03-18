package emit

import (
	"fmt"
	"strings"

	"github.com/odvcencio/arbiter"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// ToDRL converts .arb source to Drools Rule Language (DRL).
func ToDRL(source []byte) (string, error) {
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

	e := &drlEmitter{
		src:    source,
		lang:   lang,
		consts: make(map[string]string),
	}

	return e.emitSourceFile(root), nil
}

type drlEmitter struct {
	src    []byte
	lang   *gotreesitter.Language
	consts map[string]string
}

func (e *drlEmitter) text(n *gotreesitter.Node) string {
	return string(e.src[n.StartByte():n.EndByte()])
}

func (e *drlEmitter) nodeType(n *gotreesitter.Node) string {
	return n.Type(e.lang)
}

func (e *drlEmitter) childByField(n *gotreesitter.Node, field string) *gotreesitter.Node {
	return n.ChildByFieldName(field, e.lang)
}

func (e *drlEmitter) emitSourceFile(n *gotreesitter.Node) string {
	// First pass: collect consts
	e.collectConsts(n)

	var buf strings.Builder
	first := true
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if e.nodeType(c) == "rule_declaration" {
			if !first {
				buf.WriteString("\n")
			}
			buf.WriteString(e.emitRule(c))
			first = false
		}
	}

	return buf.String()
}

func (e *drlEmitter) collectConsts(root *gotreesitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if e.nodeType(c) == "const_declaration" {
			name := e.text(e.childByField(c, "name"))
			value := e.emitValue(e.childByField(c, "value"))
			e.consts[name] = value
		}
	}
}

func (e *drlEmitter) emitRule(n *gotreesitter.Node) string {
	name := ""
	if nameNode := e.childByField(n, "name"); nameNode != nil {
		name = e.text(nameNode)
	}

	priority := 0
	if priNode := e.childByField(n, "priority"); priNode != nil {
		priority = parseInt(e.text(priNode))
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("rule \"%s\"\n", name))
	if priority > 0 {
		buf.WriteString(fmt.Sprintf("    salience %d\n", priority))
	}
	buf.WriteString("    when\n")

	// Emit condition constraints
	whenNode := e.childByField(n, "condition")
	if whenNode != nil {
		exprNode := e.childByField(whenNode, "expr")
		if exprNode != nil {
			constraints := e.emitConstraints(exprNode)
			buf.WriteString("        $data : DataContext(" + constraints + ")\n")
		}
	}

	buf.WriteString("    then\n")

	// Emit action
	thenNode := e.childByField(n, "action")
	if thenNode != nil {
		buf.WriteString("        " + e.emitAction(thenNode) + "\n")
	}

	buf.WriteString("end\n")
	return buf.String()
}

// emitConstraints returns comma-separated Drools pattern constraints for AND,
// or `or`-separated for OR.
func (e *drlEmitter) emitConstraints(n *gotreesitter.Node) string {
	if e.nodeType(n) == "and_expr" {
		parts := e.flattenAnd(n)
		var strs []string
		for _, p := range parts {
			strs = append(strs, e.emitConstraints(p))
		}
		return strings.Join(strs, ", ")
	}
	if e.nodeType(n) == "or_expr" {
		left := e.emitConstraints(e.childByField(n, "left"))
		right := e.emitConstraints(e.childByField(n, "right"))
		return left + " || " + right
	}
	return e.emitCondExpr(n)
}

func (e *drlEmitter) flattenAnd(n *gotreesitter.Node) []*gotreesitter.Node {
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

func (e *drlEmitter) emitCondExpr(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}

	switch e.nodeType(n) {
	case "comparison_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		op := e.extractComparisonOp(n)
		return left + " " + op + " " + right

	case "in_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + " in " + right

	case "not_in_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + " not in " + right

	case "not_expr":
		operand := e.emitCondExpr(e.childByField(n, "operand"))
		return "not (" + operand + ")"

	case "starts_with_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + ".startsWith(" + right + ")"

	case "ends_with_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + ".endsWith(" + right + ")"

	case "matches_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + " matches " + right

	case "is_null_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		return left + " == null"

	case "is_not_null_expr":
		left := e.emitDroolsField(e.childByField(n, "left"))
		return left + " != null"

	case "paren_expr":
		return "(" + e.emitCondExpr(e.childByField(n, "expr")) + ")"

	default:
		return e.emitValue(n)
	}
}

func (e *drlEmitter) extractComparisonOp(n *gotreesitter.Node) string {
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

// emitDroolsField converts a member expression like "user.cart_total" to a
// Drools-style camelCase field name like "cartTotal".
func (e *drlEmitter) emitDroolsField(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}
	switch e.nodeType(n) {
	case "member_expr":
		// Extract just the field path after the feature prefix
		raw := e.text(n)
		parts := strings.SplitN(raw, ".", 2)
		if len(parts) == 2 {
			return toCamelCase(parts[1])
		}
		return toCamelCase(raw)
	case "identifier":
		name := e.text(n)
		if val, ok := e.consts[name]; ok {
			return val
		}
		return toCamelCase(name)
	default:
		return e.emitValue(n)
	}
}

func (e *drlEmitter) emitValue(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}

	switch e.nodeType(n) {
	case "member_expr":
		raw := e.text(n)
		parts := strings.SplitN(raw, ".", 2)
		if len(parts) == 2 {
			return toCamelCase(parts[1])
		}
		return toCamelCase(raw)
	case "identifier":
		name := e.text(n)
		if val, ok := e.consts[name]; ok {
			return val
		}
		if name == "true" || name == "false" || name == "null" {
			return name
		}
		return toCamelCase(name)
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
		if n.NamedChildCount() == 1 {
			return e.emitValue(n.NamedChild(0))
		}
		return e.text(n)
	}
}

func (e *drlEmitter) emitList(n *gotreesitter.Node) string {
	var items []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		items = append(items, e.emitValue(n.NamedChild(i)))
	}
	return "(" + strings.Join(items, ", ") + ")"
}

func (e *drlEmitter) emitAction(n *gotreesitter.Node) string {
	actionName := ""
	if nameNode := e.childByField(n, "action_name"); nameNode != nil {
		raw := e.text(nameNode)
		// lowercase first letter for Drools method style
		if len(raw) > 0 {
			actionName = strings.ToLower(raw[:1]) + raw[1:]
		}
	}

	var params []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if e.nodeType(c) == "param_assignment" {
			value := e.emitValue(e.childByField(c, "value"))
			params = append(params, value)
		}
	}

	return actionName + "(" + strings.Join(params, ", ") + ");"
}

// toCamelCase converts snake_case to camelCase.
func toCamelCase(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// parseInt is a local helper matching the one in transpile.go.
func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
