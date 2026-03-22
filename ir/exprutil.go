package ir

import (
	"fmt"
	"strings"
)

// RenderExpr renders an expression back into Arbiter-like source.
func RenderExpr(program *Program, exprID ExprID) string {
	expr := program.Expr(exprID)
	if expr == nil {
		return "null"
	}

	switch expr.Kind {
	case ExprStringLit:
		return fmt.Sprintf("%q", expr.String)
	case ExprNumberLit:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", expr.Number), "0"), ".")
	case ExprBoolLit:
		if expr.Bool {
			return "true"
		}
		return "false"
	case ExprNullLit:
		return "null"
	case ExprListLit:
		items := make([]string, 0, len(expr.Elems))
		for _, elem := range expr.Elems {
			items = append(items, RenderExpr(program, elem))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case ExprVarRef:
		return expr.Path
	case ExprLocalRef:
		return expr.Name
	case ExprConstRef:
		return expr.Name
	case ExprSecretRef:
		return fmt.Sprintf("secret(%q)", expr.Path)
	case ExprBinary:
		return renderBinary(program, expr)
	case ExprUnary:
		return renderUnary(program, expr)
	case ExprBetween:
		return fmt.Sprintf(
			"%s between %s%s, %s%s",
			RenderExpr(program, expr.Value),
			string(expr.BetweenKind)[0:1],
			RenderExpr(program, expr.Low),
			RenderExpr(program, expr.High),
			string(expr.BetweenKind)[1:2],
		)
	case ExprQuantifier:
		return fmt.Sprintf(
			"%s %s in %s { %s }",
			expr.QuantifierKind,
			expr.VarName,
			RenderExpr(program, expr.Collection),
			RenderExpr(program, expr.Body),
		)
	case ExprAggregate:
		if expr.HasValueExpr {
			return fmt.Sprintf(
				"%s(%s for %s in %s)",
				expr.AggregateKind,
				RenderExpr(program, expr.ValueExpr),
				expr.VarName,
				RenderExpr(program, expr.Collection),
			)
		}
		return fmt.Sprintf(
			"%s(%s in %s)",
			expr.AggregateKind,
			expr.VarName,
			RenderExpr(program, expr.Collection),
		)
	default:
		return "null"
	}
}

func renderBinary(program *Program, expr *Expr) string {
	left := RenderExpr(program, expr.Left)
	right := RenderExpr(program, expr.Right)
	switch expr.BinaryOp {
	case BinaryAnd:
		return left + " and " + right
	case BinaryOr:
		return left + " or " + right
	case BinaryNotIn:
		return left + " not in " + right
	case BinaryNotContains:
		return left + " not contains " + right
	case BinaryNotRetains:
		return left + " not retains " + right
	default:
		return left + " " + string(expr.BinaryOp) + " " + right
	}
}

func renderUnary(program *Program, expr *Expr) string {
	operand := RenderExpr(program, expr.Operand)
	switch expr.UnaryOp {
	case UnaryNot:
		return "not " + operand
	case UnaryIsNull:
		return operand + " is null"
	case UnaryIsNotNull:
		return operand + " is not null"
	default:
		return operand
	}
}

// LiteralValue resolves one expression to a Go literal value if possible.
func LiteralValue(program *Program, exprID ExprID) (any, bool) {
	expr := program.Expr(exprID)
	if expr == nil {
		return nil, false
	}

	switch expr.Kind {
	case ExprStringLit:
		return expr.String, true
	case ExprNumberLit:
		return expr.Number, true
	case ExprBoolLit:
		return expr.Bool, true
	case ExprNullLit:
		return nil, true
	case ExprSecretRef:
		return expr.Path, true
	case ExprConstRef:
		decl, ok := program.ConstByName(expr.Name)
		if !ok {
			return nil, false
		}
		return LiteralValue(program, decl.Value)
	case ExprListLit:
		values := make([]any, 0, len(expr.Elems))
		for _, elem := range expr.Elems {
			value, ok := LiteralValue(program, elem)
			if !ok {
				return nil, false
			}
			values = append(values, value)
		}
		return values, true
	default:
		return nil, false
	}
}

// FactDeps returns referenced `facts.<Type>` dependencies in first-seen order.
func FactDeps(program *Program, exprID ExprID) []string {
	seen := make(map[string]struct{})
	var deps []string

	var walk func(ExprID)
	walk = func(id ExprID) {
		expr := program.Expr(id)
		if expr == nil {
			return
		}
		switch expr.Kind {
		case ExprVarRef:
			if strings.HasPrefix(expr.Path, "facts.") {
				parts := strings.Split(expr.Path, ".")
				if len(parts) >= 2 && parts[1] != "" {
					if _, ok := seen[parts[1]]; !ok {
						seen[parts[1]] = struct{}{}
						deps = append(deps, parts[1])
					}
				}
			}
		case ExprListLit:
			for _, elem := range expr.Elems {
				walk(elem)
			}
		case ExprBinary:
			walk(expr.Left)
			walk(expr.Right)
		case ExprUnary:
			walk(expr.Operand)
		case ExprBetween:
			walk(expr.Value)
			walk(expr.Low)
			walk(expr.High)
		case ExprQuantifier:
			walk(expr.Collection)
			walk(expr.Body)
		case ExprAggregate:
			walk(expr.Collection)
			if expr.HasValueExpr {
				walk(expr.ValueExpr)
			}
		}
	}

	walk(exprID)
	return deps
}
