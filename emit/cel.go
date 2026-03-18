package emit

import (
	"fmt"
	"strings"

	"github.com/odvcencio/arbiter"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// ToCEL converts .arb source to CEL expressions (one per rule).
func ToCEL(source []byte) (string, error) {
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

	e := &celEmitter{
		src:    source,
		lang:   lang,
		consts: make(map[string]string),
	}

	return e.emitSourceFile(root), nil
}

type celEmitter struct {
	src    []byte
	lang   *gotreesitter.Language
	consts map[string]string
}

func (e *celEmitter) text(n *gotreesitter.Node) string {
	return string(e.src[n.StartByte():n.EndByte()])
}

func (e *celEmitter) nodeType(n *gotreesitter.Node) string {
	return n.Type(e.lang)
}

func (e *celEmitter) childByField(n *gotreesitter.Node, field string) *gotreesitter.Node {
	return n.ChildByFieldName(field, e.lang)
}

func (e *celEmitter) emitSourceFile(n *gotreesitter.Node) string {
	// First pass: collect consts
	e.collectConsts(n)

	var rules []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if e.nodeType(c) == "rule_declaration" {
			rules = append(rules, e.emitRule(c))
		}
	}

	return strings.Join(rules, "\n")
}

func (e *celEmitter) collectConsts(root *gotreesitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if e.nodeType(c) == "const_declaration" {
			name := e.text(e.childByField(c, "name"))
			value := e.emitValue(e.childByField(c, "value"))
			e.consts[name] = value
		}
	}
}

func (e *celEmitter) emitRule(n *gotreesitter.Node) string {
	name := ""
	if nameNode := e.childByField(n, "name"); nameNode != nil {
		name = e.text(nameNode)
	}

	whenNode := e.childByField(n, "condition")
	if whenNode == nil {
		return ""
	}
	exprNode := e.childByField(whenNode, "expr")
	if exprNode == nil {
		return ""
	}

	expr := e.emitExpr(exprNode)
	return "// " + name + "\n" + expr
}

func (e *celEmitter) emitExpr(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}

	switch e.nodeType(n) {
	case "or_expr":
		left := e.emitExpr(e.childByField(n, "left"))
		right := e.emitExpr(e.childByField(n, "right"))
		return left + " || " + right

	case "and_expr":
		left := e.emitExpr(e.childByField(n, "left"))
		right := e.emitExpr(e.childByField(n, "right"))
		return left + " && " + right

	case "not_expr":
		operand := e.emitExpr(e.childByField(n, "operand"))
		return "!" + operand

	case "comparison_expr":
		return e.emitComparison(n)

	case "in_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + " in " + right

	case "not_in_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return "!(" + left + " in " + right + ")"

	case "starts_with_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + ".startsWith(" + right + ")"

	case "ends_with_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + ".endsWith(" + right + ")"

	case "matches_expr":
		left := e.emitValue(e.childByField(n, "left"))
		right := e.emitValue(e.childByField(n, "right"))
		return left + ".matches(" + right + ")"

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
		return left + " " + lowOp + " " + low + " && " + left + " " + highOp + " " + high

	case "paren_expr":
		return "(" + e.emitExpr(e.childByField(n, "expr")) + ")"

	default:
		return e.emitValue(n)
	}
}

func (e *celEmitter) emitComparison(n *gotreesitter.Node) string {
	left := e.emitValue(e.childByField(n, "left"))
	right := e.emitValue(e.childByField(n, "right"))
	op := e.extractComparisonOp(n)
	return left + " " + op + " " + right
}

func (e *celEmitter) extractComparisonOp(n *gotreesitter.Node) string {
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

func (e *celEmitter) emitValue(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}

	switch e.nodeType(n) {
	case "member_expr":
		return e.text(n)
	case "identifier":
		name := e.text(n)
		if val, ok := e.consts[name]; ok {
			return val
		}
		if name == "true" || name == "false" || name == "null" {
			return name
		}
		return name
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

func (e *celEmitter) emitList(n *gotreesitter.Node) string {
	var items []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		items = append(items, e.emitValue(n.NamedChild(i)))
	}
	return "[" + strings.Join(items, ", ") + "]"
}
