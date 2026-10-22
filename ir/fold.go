package ir

// FoldConstants performs a single pass over all expressions in the program,
// replacing constant binary/unary operations with their result.
//
// Foldable operations:
//   - number OP number (arithmetic: +, -, *, /, %)
//   - bool AND/OR bool (short-circuit with known literal)
//   - NOT bool
//   - const_ref → inline the literal value
func FoldConstants(program *Program) {
	if program == nil || len(program.Exprs) == 0 {
		return
	}

	// Inline const refs to their literal values.
	for i := range program.Exprs {
		expr := &program.Exprs[i]
		if expr.Kind != ExprConstRef {
			continue
		}
		decl, ok := program.ConstByName(expr.Name)
		if !ok {
			continue
		}
		target := program.Expr(decl.Value)
		if target == nil {
			continue
		}
		switch target.Kind {
		case ExprNumberLit, ExprStringLit, ExprBoolLit:
			// Replace const ref with the literal.
			program.Exprs[i] = *target
			program.Exprs[i].Span = expr.Span
		}
	}

	// Fold binary/unary operations on literals.
	// Iterate in order so that inner expressions are folded before outer.
	for i := range program.Exprs {
		expr := &program.Exprs[i]
		switch expr.Kind {
		case ExprBinary:
			foldBinary(program, expr)
		case ExprUnary:
			foldUnary(program, expr)
		}
	}
}

func foldBinary(program *Program, expr *Expr) {
	left := program.Expr(expr.Left)
	right := program.Expr(expr.Right)
	if left == nil || right == nil {
		return
	}

	// Numeric arithmetic.
	if left.Kind == ExprNumberLit && right.Kind == ExprNumberLit {
		var result float64
		var ok bool
		switch expr.BinaryOp {
		case BinaryAdd:
			result, ok = left.Number+right.Number, true
		case BinarySub:
			result, ok = left.Number-right.Number, true
		case BinaryMul:
			// float64(...) forbids fusing with a later operation (FMA), so the
			// fold gives the same bits on every target.
			result, ok = float64(left.Number*right.Number), true
		case BinaryDiv:
			if right.Number != 0 {
				result, ok = float64(left.Number/right.Number), true
			}
		case BinaryMod:
			if right.Number != 0 {
				result = float64(int64(left.Number) % int64(right.Number))
				ok = true
			}
		}
		if ok {
			span := expr.Span
			*expr = Expr{Kind: ExprNumberLit, Number: result, Span: span}
			return
		}
	}

	// Boolean short-circuit folding.
	if expr.BinaryOp == BinaryAnd {
		if left.Kind == ExprBoolLit && !left.Bool {
			// false and X → false
			span := expr.Span
			*expr = Expr{Kind: ExprBoolLit, Bool: false, Span: span}
			return
		}
		if left.Kind == ExprBoolLit && left.Bool {
			// true and X → X
			*expr = *right
			return
		}
		if right.Kind == ExprBoolLit && !right.Bool {
			// X and false → false
			span := expr.Span
			*expr = Expr{Kind: ExprBoolLit, Bool: false, Span: span}
			return
		}
		if right.Kind == ExprBoolLit && right.Bool {
			// X and true → X
			*expr = *left
			return
		}
	}

	if expr.BinaryOp == BinaryOr {
		if left.Kind == ExprBoolLit && left.Bool {
			// true or X → true
			span := expr.Span
			*expr = Expr{Kind: ExprBoolLit, Bool: true, Span: span}
			return
		}
		if left.Kind == ExprBoolLit && !left.Bool {
			// false or X → X
			*expr = *right
			return
		}
		if right.Kind == ExprBoolLit && right.Bool {
			// X or true → true
			span := expr.Span
			*expr = Expr{Kind: ExprBoolLit, Bool: true, Span: span}
			return
		}
		if right.Kind == ExprBoolLit && !right.Bool {
			// X or false → X
			*expr = *left
			return
		}
	}

	// Numeric comparisons on literals.
	if left.Kind == ExprNumberLit && right.Kind == ExprNumberLit {
		var result bool
		var ok bool
		switch expr.BinaryOp {
		case BinaryEq:
			result, ok = left.Number == right.Number, true
		case BinaryNeq:
			result, ok = left.Number != right.Number, true
		case BinaryGt:
			result, ok = left.Number > right.Number, true
		case BinaryGte:
			result, ok = left.Number >= right.Number, true
		case BinaryLt:
			result, ok = left.Number < right.Number, true
		case BinaryLte:
			result, ok = left.Number <= right.Number, true
		}
		if ok {
			span := expr.Span
			*expr = Expr{Kind: ExprBoolLit, Bool: result, Span: span}
			return
		}
	}

	// String equality on literals.
	if left.Kind == ExprStringLit && right.Kind == ExprStringLit {
		switch expr.BinaryOp {
		case BinaryEq:
			span := expr.Span
			*expr = Expr{Kind: ExprBoolLit, Bool: left.String == right.String, Span: span}
		case BinaryNeq:
			span := expr.Span
			*expr = Expr{Kind: ExprBoolLit, Bool: left.String != right.String, Span: span}
		}
	}
}

func foldUnary(program *Program, expr *Expr) {
	operand := program.Expr(expr.Operand)
	if operand == nil {
		return
	}

	if expr.UnaryOp == UnaryNot && operand.Kind == ExprBoolLit {
		span := expr.Span
		*expr = Expr{Kind: ExprBoolLit, Bool: !operand.Bool, Span: span}
	}
}
