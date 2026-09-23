package compiler

import (
	"fmt"

	"m31labs.dev/arbiter/ir"
)

// MaxValueStackDepth bounds the VM's fixed-size value stack (see vm.maxStack,
// which is defined as this constant, so the two can never drift apart).
//
// Without a compile-time check, a deeply nested expression — for example
// 300 chained `+` operators — compiles without error and only fails later,
// at eval time, with a VM "stack overflow" error whose trigger point depends
// on runtime data (see TestCompileRejectsExcessiveStackDepth). Rejecting it
// here instead gives a clear, deterministic diagnostic at compile time.
const MaxValueStackDepth = 256

// maxConstDepthGuard bounds recursion into const_ref chains while computing
// stack depth. Constant cycles are already rejected by validateProgram and
// bounded chains by maxConstRefChain before compilation runs, so this is a
// defense-in-depth backstop, not the primary guard.
const maxConstDepthGuard = 4096

// exprStackDepth returns the maximum concurrent VM value-stack depth needed
// to evaluate exprID, mirroring compileExpr's actual left-to-right,
// sequential-push-then-combine emission order (not an optimal reordering).
//
// This is a conservative approximation for constructs whose exact runtime
// stack-reset behavior is not modeled precisely here — OpLookup's where/else
// sub-ranges compile into separate instruction ranges, and quantifier/
// aggregate bodies run after OpIterBegin consumes the collection value — so
// they are treated as depth-1 leaves rather than recursed into. That can
// under-count a pathologically deep lookup-else or quantifier/aggregate
// nesting, but those require far more verbose syntax per level than plain
// operator nesting; the binary/unary/between/builtin-call chain this models
// precisely is the reported failure mode (deeply nested arithmetic/boolean
// operators), and is unbounded with only a few characters per level.
func exprStackDepth(program *ir.Program, exprID ir.ExprID) int {
	return exprStackDepthGuarded(program, exprID, 0)
}

func exprStackDepthGuarded(program *ir.Program, exprID ir.ExprID, guard int) int {
	if guard > maxConstDepthGuard {
		return 1
	}
	expr := program.Expr(exprID)
	if expr == nil {
		return 1
	}
	switch expr.Kind {
	case ir.ExprConstRef:
		decl, ok := program.ConstByName(expr.Name)
		if !ok {
			return 1
		}
		return exprStackDepthGuarded(program, decl.Value, guard+1)
	case ir.ExprUnary:
		// Pop 1, push 1: same depth as the operand.
		return exprStackDepthGuarded(program, expr.Operand, guard+1)
	case ir.ExprBinary:
		return sequentialStackDepth(program, []ir.ExprID{expr.Left, expr.Right}, guard)
	case ir.ExprBetween:
		return sequentialStackDepth(program, []ir.ExprID{expr.Value, expr.Low, expr.High}, guard)
	case ir.ExprBuiltinCall:
		return sequentialStackDepth(program, expr.Args, guard)
	case ir.ExprQuantifier:
		// OpIterBegin consumes the collection value before the body runs;
		// they don't accumulate on the stack together.
		return maxInt(
			exprStackDepthGuarded(program, expr.Collection, guard+1),
			exprStackDepthGuarded(program, expr.Body, guard+1),
		)
	case ir.ExprAggregate:
		d := exprStackDepthGuarded(program, expr.Collection, guard+1)
		if expr.HasValueExpr {
			d = maxInt(d, exprStackDepthGuarded(program, expr.ValueExpr, guard+1))
		}
		return d
	default:
		// Literals, var/local/secret refs, list literals (folded to a pool
		// constant at compile time — see exprToPoolValue), and lookups (the
		// OpLookup result itself; its where/else ranges are separate) all
		// push exactly one value.
		return 1
	}
}

// sequentialStackDepth models compileExpr's fixed left-to-right emission for
// N children that are pushed in order and combined by one trailing op: while
// evaluating child i (0-indexed), i prior results are already pending on the
// stack, so the peak depth contributed by child i is i + depth(child_i).
func sequentialStackDepth(program *ir.Program, children []ir.ExprID, guard int) int {
	depth := 0
	for i, id := range children {
		if d := i + exprStackDepthGuarded(program, id, guard+1); d > depth {
			depth = d
		}
	}
	if depth == 0 {
		return 1
	}
	return depth
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// checkExprStackDepth validates that evaluating exprID — optionally preceded
// by let bindings, each of which resets to a clean stack via OpSetLocal —
// will not exceed MaxValueStackDepth. label identifies the expression in the
// returned error (for example "rule R condition" or "rule R action A param
// key").
func checkExprStackDepth(program *ir.Program, label string, lets []ir.LetBinding, exprID ir.ExprID) error {
	depth := exprStackDepth(program, exprID)
	for _, binding := range lets {
		if d := exprStackDepth(program, binding.Value); d > depth {
			depth = d
		}
	}
	if depth > MaxValueStackDepth {
		return fmt.Errorf("%s: expression requires stack depth %d, which exceeds the %d-deep evaluation limit — rewrite it with fewer nested operators", label, depth, MaxValueStackDepth)
	}
	return nil
}
