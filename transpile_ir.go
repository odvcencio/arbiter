package arbiter

import "github.com/odvcencio/arbiter/ir"

func emitIRProgram(program *ir.Program) *TranspileResult {
	result := &TranspileResult{
		Features: make(map[string]Feature),
		Consts:   make(map[string]any),
	}

	for _, feature := range program.Features {
		out := Feature{
			Name:   feature.Name,
			Source: feature.Source,
			Fields: make(map[string]string),
		}
		for _, field := range feature.Fields {
			out.Fields[field.Name] = field.Type
		}
		result.Features[out.Name] = out
	}

	for _, decl := range program.Consts {
		result.Consts[decl.Name] = emitIRExpr(program, decl.Value)
	}

	for i := range program.Rules {
		result.Rules = append(result.Rules, emitIRRule(program, &program.Rules[i]))
	}

	return result
}

func emitIRRule(program *ir.Program, rule *ir.Rule) RuleOutput {
	out := RuleOutput{
		Name:     rule.Name,
		Priority: int(rule.Priority),
		Action:   emitIRAction(program, rule.Action),
	}
	if rule.HasCondition || rule.Segment != "" {
		out.Condition = emitIRRuleCondition(program, rule)
	}
	if rule.Fallback != nil {
		out.Fallback = emitIRAction(program, *rule.Fallback)
	}
	return out
}

func emitIRRuleCondition(program *ir.Program, rule *ir.Rule) any {
	var condition any
	if rule.HasCondition {
		condition = emitIRExpr(program, rule.Condition)
	}
	if rule.Segment == "" {
		return condition
	}
	segment, ok := program.SegmentByName(rule.Segment)
	if !ok {
		return condition
	}
	segmentExpr := emitIRExpr(program, segment.Condition)
	if condition == nil {
		return segmentExpr
	}
	return map[string]any{
		"OpLogic":    "&&",
		"Conditions": []any{segmentExpr, condition},
	}
}

func emitIRAction(program *ir.Program, action ir.Action) map[string]any {
	out := map[string]any{
		"ActionName": action.Name,
	}
	if len(action.Params) == 0 {
		return out
	}
	params := make(map[string]any, len(action.Params))
	for _, param := range action.Params {
		params[param.Key] = emitIRExpr(program, param.Value)
	}
	out["ParamMap"] = params
	return out
}

func emitIRExpr(program *ir.Program, exprID ir.ExprID) any {
	expr := program.Expr(exprID)
	if expr == nil {
		return nil
	}

	switch expr.Kind {
	case ir.ExprStringLit:
		return map[string]any{"Const": map[string]any{"StrConst": expr.String}}
	case ir.ExprNumberLit:
		return map[string]any{"Const": map[string]any{"NumConst": expr.Number}}
	case ir.ExprBoolLit:
		return map[string]any{"Const": map[string]any{"BoolConst": expr.Bool}}
	case ir.ExprNullLit:
		return map[string]any{"Const": nil}
	case ir.ExprListLit:
		items := make([]any, 0, len(expr.Elems))
		for _, elem := range expr.Elems {
			items = append(items, emitIRExpr(program, elem))
		}
		return map[string]any{"ConstList": items}
	case ir.ExprVarRef:
		return map[string]any{"VarExpr": expr.Path}
	case ir.ExprLocalRef:
		return map[string]any{"VarExpr": expr.Name}
	case ir.ExprConstRef:
		decl, ok := program.ConstByName(expr.Name)
		if !ok {
			return map[string]any{"VarExpr": expr.Name}
		}
		return emitIRExpr(program, decl.Value)
	case ir.ExprSecretRef:
		return map[string]any{"Const": map[string]any{"StrConst": expr.Path}}
	case ir.ExprBinary:
		return emitIRBinaryExpr(program, expr)
	case ir.ExprUnary:
		return emitIRUnaryExpr(program, expr)
	case ir.ExprBetween:
		return map[string]any{
			"Operator": mapBetweenOperator(expr.BetweenKind),
			"Lhs":      emitIRExpr(program, expr.Value),
			"Rhs": map[string]any{
				"ConstList": []any{
					emitIRExpr(program, expr.Low),
					emitIRExpr(program, expr.High),
				},
			},
		}
	case ir.ExprQuantifier:
		return map[string]any{
			"ForeachOperator": "FOREACH",
			"ForeachLogic":    mapQuantifierLogic(expr.QuantifierKind),
			"ForeachVar":      expr.VarName,
			"Lhs":             emitIRExpr(program, expr.Collection),
			"Condition":       emitIRExpr(program, expr.Body),
		}
	case ir.ExprAggregate:
		return map[string]any{"_raw": ir.RenderExpr(program, exprID)}
	default:
		return map[string]any{"_raw": ir.RenderExpr(program, exprID)}
	}
}

func emitIRBinaryExpr(program *ir.Program, expr *ir.Expr) any {
	switch expr.BinaryOp {
	case ir.BinaryAnd:
		return map[string]any{
			"OpLogic":    "&&",
			"Conditions": flattenLogical(program, expr.BinaryOp, expr.Left, expr.Right),
		}
	case ir.BinaryOr:
		return map[string]any{
			"OpLogic":    "||",
			"Conditions": flattenLogical(program, expr.BinaryOp, expr.Left, expr.Right),
		}
	case ir.BinaryAdd, ir.BinarySub, ir.BinaryMul, ir.BinaryDiv, ir.BinaryMod:
		return map[string]any{
			"MathExpr": map[string]any{
				"Operator": string(expr.BinaryOp),
				"Lhs":      emitIRExpr(program, expr.Left),
				"Rhs":      emitIRExpr(program, expr.Right),
			},
		}
	default:
		return map[string]any{
			"Operator": mapBinaryOperator(expr.BinaryOp),
			"Lhs":      emitIRExpr(program, expr.Left),
			"Rhs":      emitIRExpr(program, expr.Right),
		}
	}
}

func emitIRUnaryExpr(program *ir.Program, expr *ir.Expr) any {
	switch expr.UnaryOp {
	case ir.UnaryNot:
		return map[string]any{
			"OpLogic":    "not",
			"Conditions": []any{emitIRExpr(program, expr.Operand)},
		}
	case ir.UnaryIsNull:
		return map[string]any{
			"Operator": "IS_NULL",
			"Lhs":      emitIRExpr(program, expr.Operand),
		}
	case ir.UnaryIsNotNull:
		return map[string]any{
			"Operator": "!IS_NULL",
			"Lhs":      emitIRExpr(program, expr.Operand),
		}
	default:
		return map[string]any{"_raw": ir.RenderExpr(program, expr.Operand)}
	}
}

func flattenLogical(program *ir.Program, op ir.BinaryOpKind, left, right ir.ExprID) []any {
	out := make([]any, 0, 2)
	out = appendFlattenedLogical(out, program, op, left)
	out = appendFlattenedLogical(out, program, op, right)
	return out
}

func appendFlattenedLogical(dst []any, program *ir.Program, op ir.BinaryOpKind, exprID ir.ExprID) []any {
	expr := program.Expr(exprID)
	if expr != nil && expr.Kind == ir.ExprBinary && expr.BinaryOp == op {
		dst = appendFlattenedLogical(dst, program, op, expr.Left)
		dst = appendFlattenedLogical(dst, program, op, expr.Right)
		return dst
	}
	return append(dst, emitIRExpr(program, exprID))
}

func mapBinaryOperator(op ir.BinaryOpKind) string {
	switch op {
	case ir.BinaryEq:
		return "=="
	case ir.BinaryNeq:
		return "!="
	case ir.BinaryGt:
		return ">"
	case ir.BinaryGte:
		return ">="
	case ir.BinaryLt:
		return "<"
	case ir.BinaryLte:
		return "<="
	case ir.BinaryIn:
		return "LIST_IN"
	case ir.BinaryNotIn:
		return "!LIST_IN"
	case ir.BinaryContains:
		return "LIST_CONTAINS"
	case ir.BinaryNotContains:
		return "!LIST_CONTAINS"
	case ir.BinaryRetains:
		return "LIST_RETAIN"
	case ir.BinaryNotRetains:
		return "!LIST_RETAIN"
	case ir.BinarySubsetOf:
		return "SUB_LIST_IN"
	case ir.BinarySupersetOf:
		return "SUB_LIST_CONTAINS"
	case ir.BinaryVagueContains:
		return "LIST_VAGUE_CONTAINS"
	case ir.BinaryStartsWith:
		return "STRING_START_WITH"
	case ir.BinaryEndsWith:
		return "STRING_END_WITH"
	case ir.BinaryMatches:
		return "CONTAIN_REGEXP"
	default:
		return string(op)
	}
}

func mapBetweenOperator(kind ir.BetweenKind) string {
	switch kind {
	case ir.BetweenOpenOpen:
		return "BETWEEN_ALL_OPEN"
	case ir.BetweenOpenClosed:
		return "BETWEEN_LEFT_OPEN_RIGHT_CLOSE"
	case ir.BetweenClosedOpen:
		return "BETWEEN_LEFT_CLOSE_RIGHT_OPEN"
	default:
		return "BETWEEN_ALL_CLOSE"
	}
}

func mapQuantifierLogic(kind ir.QuantifierKind) string {
	switch kind {
	case ir.QuantifierAny:
		return "||"
	case ir.QuantifierNone:
		return "!||"
	default:
		return "&&"
	}
}
