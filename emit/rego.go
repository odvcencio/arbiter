package emit

import (
	"strings"

	"github.com/odvcencio/arbiter/ir"
)

// ToRego converts .arb source to Rego (OPA) policy text.
func ToRego(source []byte) (string, error) {
	program, err := lowerProgram(source)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	buf.WriteString("package rules\n\nimport rego.v1\n")
	for i := range program.Rules {
		buf.WriteString("\n")
		buf.WriteString(emitRegoRule(program, &program.Rules[i]))
	}
	return buf.String(), nil
}

func emitRegoRule(program *ir.Program, rule *ir.Rule) string {
	exprIDs := splitRegoOr(program, effectiveRuleCondition(program, rule))
	name := toSnakeCase(rule.Name)

	var buf strings.Builder
	for _, exprID := range exprIDs {
		buf.WriteString(name + " if {\n")
		for _, line := range emitRegoConditionLines(program, exprID, localBindings(rule)) {
			buf.WriteString("    " + line + "\n")
		}
		buf.WriteString("}\n\n")
	}
	return buf.String()
}

func effectiveRuleCondition(program *ir.Program, rule *ir.Rule) ir.ExprID {
	if rule.Segment == "" {
		return rule.Condition
	}
	segment, ok := program.SegmentByName(rule.Segment)
	if !ok {
		return rule.Condition
	}
	if !rule.HasCondition {
		return segment.Condition
	}
	return appendExpr(program, ir.Expr{
		Kind:     ir.ExprBinary,
		BinaryOp: ir.BinaryAnd,
		Left:     segment.Condition,
		Right:    rule.Condition,
	})
}

func splitRegoOr(program *ir.Program, exprID ir.ExprID) []ir.ExprID {
	expr := program.Expr(exprID)
	if expr == nil {
		return nil
	}
	if expr.Kind == ir.ExprBinary && expr.BinaryOp == ir.BinaryAnd {
		if lhs := program.Expr(expr.Left); lhs != nil && lhs.Kind == ir.ExprBinary && lhs.BinaryOp == ir.BinaryOr {
			return flattenOr(program, expr.Left, expr.Right)
		}
	}
	if expr.Kind == ir.ExprBinary && expr.BinaryOp == ir.BinaryOr {
		return flattenSimpleOr(program, exprID)
	}
	return []ir.ExprID{exprID}
}

func flattenOr(program *ir.Program, orID, tailID ir.ExprID) []ir.ExprID {
	branches := flattenSimpleOr(program, orID)
	out := make([]ir.ExprID, 0, len(branches))
	for _, branch := range branches {
		expr := ir.Expr{
			Kind:     ir.ExprBinary,
			BinaryOp: ir.BinaryAnd,
			Left:     branch,
			Right:    tailID,
		}
		out = append(out, appendExpr(program, expr))
	}
	return out
}

func flattenSimpleOr(program *ir.Program, exprID ir.ExprID) []ir.ExprID {
	expr := program.Expr(exprID)
	if expr == nil || expr.Kind != ir.ExprBinary || expr.BinaryOp != ir.BinaryOr {
		return []ir.ExprID{exprID}
	}
	out := flattenSimpleOr(program, expr.Left)
	out = append(out, flattenSimpleOr(program, expr.Right)...)
	return out
}

func appendExpr(program *ir.Program, expr ir.Expr) ir.ExprID {
	id := ir.ExprID(len(program.Exprs))
	program.Exprs = append(program.Exprs, expr)
	return id
}

func emitRegoConditionLines(program *ir.Program, exprID ir.ExprID, locals map[string]ir.ExprID) []string {
	expr := program.Expr(exprID)
	if expr == nil {
		return nil
	}
	if expr.Kind == ir.ExprBinary && expr.BinaryOp == ir.BinaryAnd {
		lines := emitRegoConditionLines(program, expr.Left, locals)
		lines = append(lines, emitRegoConditionLines(program, expr.Right, locals)...)
		return lines
	}
	return []string{emitRegoExpr(program, exprID, locals)}
}

func emitRegoExpr(program *ir.Program, exprID ir.ExprID, locals map[string]ir.ExprID) string {
	expr := program.Expr(exprID)
	if expr == nil {
		return ""
	}
	switch expr.Kind {
	case ir.ExprStringLit:
		return `"` + expr.String + `"`
	case ir.ExprNumberLit:
		return formatNumber(expr.Number)
	case ir.ExprBoolLit:
		if expr.Bool {
			return "true"
		}
		return "false"
	case ir.ExprNullLit:
		return "null"
	case ir.ExprListLit:
		items := make([]string, 0, len(expr.Elems))
		for _, elem := range expr.Elems {
			items = append(items, emitRegoExpr(program, elem, locals))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case ir.ExprVarRef:
		return "input." + expr.Path
	case ir.ExprLocalRef:
		if id, ok := locals[expr.Name]; ok {
			return emitRegoExpr(program, id, locals)
		}
		return "input." + expr.Name
	case ir.ExprConstRef:
		if decl, ok := program.ConstByName(expr.Name); ok {
			return emitRegoExpr(program, decl.Value, locals)
		}
		return expr.Name
	case ir.ExprSecretRef:
		return `"` + expr.Path + `"`
	case ir.ExprUnary:
		switch expr.UnaryOp {
		case ir.UnaryNot:
			return "not " + emitRegoExpr(program, expr.Operand, locals)
		case ir.UnaryIsNull:
			return emitRegoExpr(program, expr.Operand, locals) + " == null"
		case ir.UnaryIsNotNull:
			return emitRegoExpr(program, expr.Operand, locals) + " != null"
		}
	case ir.ExprBetween:
		left := emitRegoExpr(program, expr.Value, locals)
		low := emitRegoExpr(program, expr.Low, locals)
		high := emitRegoExpr(program, expr.High, locals)
		lowOp, highOp := ">", "<"
		switch expr.BetweenKind {
		case ir.BetweenClosedClosed:
			lowOp, highOp = ">=", "<="
		case ir.BetweenClosedOpen:
			lowOp, highOp = ">=", "<"
		case ir.BetweenOpenClosed:
			lowOp, highOp = ">", "<="
		}
		return left + " " + lowOp + " " + low + "; " + left + " " + highOp + " " + high
	case ir.ExprBinary:
		left := emitRegoExpr(program, expr.Left, locals)
		right := emitRegoExpr(program, expr.Right, locals)
		switch expr.BinaryOp {
		case ir.BinaryOr:
			return left + " # or " + right
		case ir.BinaryAnd:
			return left + "; " + right
		case ir.BinaryIn:
			return left + " in " + right
		case ir.BinaryNotIn:
			return "not " + left + " in " + right
		case ir.BinaryStartsWith:
			return "startswith(" + left + ", " + right + ")"
		case ir.BinaryEndsWith:
			return "endswith(" + left + ", " + right + ")"
		case ir.BinaryMatches:
			return "regex.match(" + right + ", " + left + ")"
		case ir.BinaryContains:
			return right + " in " + left
		case ir.BinaryEq, ir.BinaryNeq, ir.BinaryGt, ir.BinaryGte, ir.BinaryLt, ir.BinaryLte,
			ir.BinaryAdd, ir.BinarySub, ir.BinaryMul, ir.BinaryDiv, ir.BinaryMod:
			return left + " " + string(expr.BinaryOp) + " " + right
		default:
			return ir.RenderExpr(program, exprID)
		}
	default:
		return ir.RenderExpr(program, exprID)
	}
	return ir.RenderExpr(program, exprID)
}
