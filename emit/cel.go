package emit

import (
	"strings"

	"github.com/odvcencio/arbiter/ir"
)

// ToCEL converts .arb source to CEL expressions (one per rule).
func ToCEL(source []byte) (string, error) {
	program, err := lowerProgram(source)
	if err != nil {
		return "", err
	}
	var rules []string
	for i := range program.Rules {
		rules = append(rules, emitCELRule(program, &program.Rules[i]))
	}
	return strings.Join(rules, "\n"), nil
}

func emitCELRule(program *ir.Program, rule *ir.Rule) string {
	expr := emitCELRuleCondition(program, rule)
	if expr == "" {
		return ""
	}
	return "// " + rule.Name + "\n" + expr
}

func emitCELRuleCondition(program *ir.Program, rule *ir.Rule) string {
	locals := localBindings(rule)
	var parts []string
	if rule.Segment != "" {
		if segment, ok := program.SegmentByName(rule.Segment); ok {
			parts = append(parts, emitCELExpr(program, segment.Condition, nil))
		}
	}
	if rule.HasCondition {
		parts = append(parts, emitCELExpr(program, rule.Condition, locals))
	}
	return strings.Join(parts, " && ")
}

func emitCELExpr(program *ir.Program, exprID ir.ExprID, locals map[string]ir.ExprID) string {
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
			items = append(items, emitCELExpr(program, elem, locals))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case ir.ExprVarRef:
		return expr.Path
	case ir.ExprLocalRef:
		if id, ok := locals[expr.Name]; ok {
			return emitCELExpr(program, id, locals)
		}
		return expr.Name
	case ir.ExprConstRef:
		if decl, ok := program.ConstByName(expr.Name); ok {
			return emitCELExpr(program, decl.Value, locals)
		}
		return expr.Name
	case ir.ExprSecretRef:
		return `"` + expr.Path + `"`
	case ir.ExprUnary:
		switch expr.UnaryOp {
		case ir.UnaryNot:
			return "!" + emitCELExpr(program, expr.Operand, locals)
		case ir.UnaryIsNull:
			return emitCELExpr(program, expr.Operand, locals) + " == null"
		case ir.UnaryIsNotNull:
			return emitCELExpr(program, expr.Operand, locals) + " != null"
		}
	case ir.ExprBetween:
		left := emitCELExpr(program, expr.Value, locals)
		low := emitCELExpr(program, expr.Low, locals)
		high := emitCELExpr(program, expr.High, locals)
		lowOp, highOp := ">", "<"
		switch expr.BetweenKind {
		case ir.BetweenClosedClosed:
			lowOp, highOp = ">=", "<="
		case ir.BetweenClosedOpen:
			lowOp, highOp = ">=", "<"
		case ir.BetweenOpenClosed:
			lowOp, highOp = ">", "<="
		}
		return left + " " + lowOp + " " + low + " && " + left + " " + highOp + " " + high
	case ir.ExprBinary:
		left := emitCELExpr(program, expr.Left, locals)
		right := emitCELExpr(program, expr.Right, locals)
		switch expr.BinaryOp {
		case ir.BinaryAnd:
			return left + " && " + right
		case ir.BinaryOr:
			return left + " || " + right
		case ir.BinaryIn:
			return left + " in " + right
		case ir.BinaryNotIn:
			return "!(" + left + " in " + right + ")"
		case ir.BinaryContains:
			return right + " in " + left
		case ir.BinaryNotContains:
			return "!(" + right + " in " + left + ")"
		case ir.BinaryStartsWith:
			return left + ".startsWith(" + right + ")"
		case ir.BinaryEndsWith:
			return left + ".endsWith(" + right + ")"
		case ir.BinaryMatches:
			return left + ".matches(" + right + ")"
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

func localBindings(rule *ir.Rule) map[string]ir.ExprID {
	if len(rule.Lets) == 0 {
		return nil
	}
	locals := make(map[string]ir.ExprID, len(rule.Lets))
	for _, binding := range rule.Lets {
		locals[binding.Name] = binding.Value
	}
	return locals
}
