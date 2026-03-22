package emit

import (
	"fmt"
	"strings"

	"github.com/odvcencio/arbiter/ir"
)

// ToDRL converts .arb source to Drools Rule Language (DRL).
func ToDRL(source []byte) (string, error) {
	program, err := lowerProgram(source)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	first := true
	for i := range program.Rules {
		if !first {
			buf.WriteString("\n")
		}
		buf.WriteString(emitDRLRule(program, &program.Rules[i]))
		first = false
	}
	return buf.String(), nil
}

func emitDRLRule(program *ir.Program, rule *ir.Rule) string {
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("rule \"%s\"\n", rule.Name))
	if rule.Priority > 0 {
		buf.WriteString(fmt.Sprintf("    salience %d\n", rule.Priority))
	}
	buf.WriteString("    when\n")

	if constraints := emitDRLRuleCondition(program, rule); constraints != "" {
		buf.WriteString("        $data : DataContext(" + constraints + ")\n")
	}

	buf.WriteString("    then\n")
	buf.WriteString("        " + emitDRLAction(program, rule.Action, localBindings(rule)) + "\n")
	buf.WriteString("end\n")
	return buf.String()
}

func emitDRLRuleCondition(program *ir.Program, rule *ir.Rule) string {
	locals := localBindings(rule)
	var parts []string
	if rule.Segment != "" {
		if segment, ok := program.SegmentByName(rule.Segment); ok {
			parts = append(parts, emitDRLConstraints(program, segment.Condition, nil))
		}
	}
	if rule.HasCondition {
		parts = append(parts, emitDRLConstraints(program, rule.Condition, locals))
	}
	return strings.Join(parts, ", ")
}

func emitDRLConstraints(program *ir.Program, exprID ir.ExprID, locals map[string]ir.ExprID) string {
	expr := program.Expr(exprID)
	if expr == nil {
		return ""
	}
	if expr.Kind == ir.ExprBinary && expr.BinaryOp == ir.BinaryAnd {
		left := emitDRLConstraints(program, expr.Left, locals)
		right := emitDRLConstraints(program, expr.Right, locals)
		if left == "" {
			return right
		}
		if right == "" {
			return left
		}
		return left + ", " + right
	}
	if expr.Kind == ir.ExprBinary && expr.BinaryOp == ir.BinaryOr {
		return emitDRLExpr(program, expr.Left, locals) + " || " + emitDRLExpr(program, expr.Right, locals)
	}
	return emitDRLExpr(program, exprID, locals)
}

func emitDRLExpr(program *ir.Program, exprID ir.ExprID, locals map[string]ir.ExprID) string {
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
			items = append(items, emitDRLExpr(program, elem, locals))
		}
		return "(" + strings.Join(items, ", ") + ")"
	case ir.ExprVarRef:
		return droolsField(expr.Path)
	case ir.ExprLocalRef:
		if id, ok := locals[expr.Name]; ok {
			return emitDRLExpr(program, id, locals)
		}
		return toCamelCase(expr.Name)
	case ir.ExprConstRef:
		if decl, ok := program.ConstByName(expr.Name); ok {
			return emitDRLExpr(program, decl.Value, locals)
		}
		return expr.Name
	case ir.ExprSecretRef:
		return `"` + expr.Path + `"`
	case ir.ExprUnary:
		switch expr.UnaryOp {
		case ir.UnaryNot:
			return "not (" + emitDRLExpr(program, expr.Operand, locals) + ")"
		case ir.UnaryIsNull:
			return emitDRLExpr(program, expr.Operand, locals) + " == null"
		case ir.UnaryIsNotNull:
			return emitDRLExpr(program, expr.Operand, locals) + " != null"
		}
	case ir.ExprBetween:
		left := emitDRLExpr(program, expr.Value, locals)
		low := emitDRLExpr(program, expr.Low, locals)
		high := emitDRLExpr(program, expr.High, locals)
		lowOp, highOp := ">", "<"
		switch expr.BetweenKind {
		case ir.BetweenClosedClosed:
			lowOp, highOp = ">=", "<="
		case ir.BetweenClosedOpen:
			lowOp, highOp = ">=", "<"
		case ir.BetweenOpenClosed:
			lowOp, highOp = ">", "<="
		}
		return left + " " + lowOp + " " + low + ", " + left + " " + highOp + " " + high
	case ir.ExprBinary:
		left := emitDRLExpr(program, expr.Left, locals)
		right := emitDRLExpr(program, expr.Right, locals)
		switch expr.BinaryOp {
		case ir.BinaryAnd:
			return left + ", " + right
		case ir.BinaryOr:
			return left + " || " + right
		case ir.BinaryIn:
			return left + " in " + right
		case ir.BinaryNotIn:
			return left + " not in " + right
		case ir.BinaryStartsWith:
			return left + ".startsWith(" + right + ")"
		case ir.BinaryEndsWith:
			return left + ".endsWith(" + right + ")"
		case ir.BinaryMatches:
			return left + " matches " + right
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

func emitDRLAction(program *ir.Program, action ir.Action, locals map[string]ir.ExprID) string {
	actionName := ""
	if action.Name != "" {
		actionName = strings.ToLower(action.Name[:1]) + action.Name[1:]
	}
	var params []string
	for _, param := range action.Params {
		params = append(params, emitDRLExpr(program, param.Value, locals))
	}
	return actionName + "(" + strings.Join(params, ", ") + ");"
}

func droolsField(path string) string {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 2 {
		return toCamelCase(parts[1])
	}
	return toCamelCase(path)
}
