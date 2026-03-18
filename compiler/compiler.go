// compiler/compiler.go
package compiler

import (
	"fmt"
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/arbiter/intern"
)

// CompileCST walks a gotreesitter parse tree for an .arb source file and
// emits a CompiledRuleset containing bytecode, constant pool, and metadata.
func CompileCST(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (*CompiledRuleset, error) {
	c := &cstCompiler{
		src:    source,
		lang:   lang,
		pool:   intern.NewPool(),
		consts: make(map[string]constVal),
	}

	if root.HasError() {
		return nil, fmt.Errorf("parse errors in arbiter source")
	}

	return c.compileSourceFile(root)
}

// constVal stores a resolved constant value for inlining.
type constVal struct {
	kind      string // "number", "string", "bool", "list"
	num       float64
	str       string
	b         bool
	listItems []intern.PoolValue
}

type cstCompiler struct {
	src    []byte
	lang   *gotreesitter.Language
	pool   *intern.Pool
	consts map[string]constVal
}

func (c *cstCompiler) text(n *gotreesitter.Node) string {
	return string(c.src[n.StartByte():n.EndByte()])
}

func (c *cstCompiler) nodeType(n *gotreesitter.Node) string {
	return n.Type(c.lang)
}

func (c *cstCompiler) childByField(n *gotreesitter.Node, field string) *gotreesitter.Node {
	return n.ChildByFieldName(field, c.lang)
}

// compileSourceFile is the top-level walk.
func (c *cstCompiler) compileSourceFile(root *gotreesitter.Node) (*CompiledRuleset, error) {
	rs := &CompiledRuleset{
		Constants: c.pool,
	}

	// First pass: collect consts for inlining
	c.collectConsts(root)

	// Second pass: compile rules
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if c.nodeType(child) == "rule_declaration" {
			rh, err := c.compileRule(child, rs)
			if err != nil {
				return nil, err
			}
			rs.Rules = append(rs.Rules, rh)
		}
	}

	return rs, nil
}

// collectConsts gathers all const declarations for inlining.
func (c *cstCompiler) collectConsts(root *gotreesitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if c.nodeType(child) == "const_declaration" {
			nameNode := c.childByField(child, "name")
			valueNode := c.childByField(child, "value")
			if nameNode == nil || valueNode == nil {
				continue
			}
			name := c.text(nameNode)
			c.consts[name] = c.resolveConst(valueNode)
		}
	}
}

// resolveConst evaluates a constant expression at compile time.
func (c *cstCompiler) resolveConst(n *gotreesitter.Node) constVal {
	switch c.nodeType(n) {
	case "number_literal":
		return constVal{kind: "number", num: parseFloat(c.text(n))}
	case "string_literal":
		return constVal{kind: "string", str: stripQuotes(c.text(n))}
	case "bool_literal":
		return constVal{kind: "bool", b: c.text(n) == "true"}
	case "list_literal":
		var items []intern.PoolValue
		for i := 0; i < int(n.NamedChildCount()); i++ {
			items = append(items, c.exprToPoolValue(n.NamedChild(i)))
		}
		return constVal{kind: "list", listItems: items}
	default:
		if n.NamedChildCount() == 1 {
			return c.resolveConst(n.NamedChild(0))
		}
		return constVal{kind: "number", num: 0}
	}
}

// exprToPoolValue converts a literal expression node to a PoolValue for list storage.
func (c *cstCompiler) exprToPoolValue(n *gotreesitter.Node) intern.PoolValue {
	switch c.nodeType(n) {
	case "number_literal":
		return intern.PoolValue{Typ: intern.TypeNumber, Num: parseFloat(c.text(n))}
	case "string_literal":
		s := stripQuotes(c.text(n))
		idx := c.pool.String(s)
		return intern.PoolValue{Typ: intern.TypeString, Str: idx}
	case "bool_literal":
		return intern.PoolValue{Typ: intern.TypeBool, Bool: c.text(n) == "true"}
	default:
		if n.NamedChildCount() == 1 {
			return c.exprToPoolValue(n.NamedChild(0))
		}
		return intern.PoolValue{Typ: intern.TypeNull}
	}
}

// compileRule compiles one rule_declaration into a RuleHeader.
func (c *cstCompiler) compileRule(n *gotreesitter.Node, rs *CompiledRuleset) (RuleHeader, error) {
	rh := RuleHeader{}

	if nameNode := c.childByField(n, "name"); nameNode != nil {
		rh.NameIdx = c.pool.String(c.text(nameNode))
	}

	if priNode := c.childByField(n, "priority"); priNode != nil {
		rh.Priority = int32(parseInt(c.text(priNode)))
	}

	// Condition
	if whenNode := c.childByField(n, "condition"); whenNode != nil {
		if exprNode := c.childByField(whenNode, "expr"); exprNode != nil {
			condOff := uint32(len(rs.Instructions))
			var code []byte
			code = c.compileExpr(code, exprNode)
			rs.Instructions = append(rs.Instructions, code...)
			rh.ConditionOff = condOff
			rh.ConditionLen = uint32(len(code))
		}
	}

	// Action
	if thenNode := c.childByField(n, "action"); thenNode != nil {
		actionIdx := uint16(len(rs.Actions))
		rs.Actions = append(rs.Actions, c.compileAction(thenNode, rs))
		rh.ActionIdx = actionIdx
	}

	// Fallback
	if fallbackNode := c.childByField(n, "fallback"); fallbackNode != nil {
		fbIdx := uint16(len(rs.Actions))
		rs.Actions = append(rs.Actions, c.compileAction(fallbackNode, rs))
		rh.FallbackIdx = fbIdx
	}

	return rh, nil
}

// compileAction compiles a then_block or otherwise_block.
func (c *cstCompiler) compileAction(n *gotreesitter.Node, rs *CompiledRuleset) ActionEntry {
	ae := ActionEntry{}

	if nameNode := c.childByField(n, "action_name"); nameNode != nil {
		ae.NameIdx = c.pool.String(c.text(nameNode))
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if c.nodeType(child) == "param_assignment" {
			keyNode := c.childByField(child, "key")
			valueNode := c.childByField(child, "value")
			if keyNode == nil || valueNode == nil {
				continue
			}
			paramOff := uint32(len(rs.Instructions))
			var code []byte
			code = c.compileExpr(code, valueNode)
			rs.Instructions = append(rs.Instructions, code...)
			ae.Params = append(ae.Params, ActionParam{
				KeyIdx:   c.pool.String(c.text(keyNode)),
				ValueOff: paramOff,
				ValueLen: uint32(len(code)),
			})
		}
	}

	return ae
}

// compileExpr is the main CST walker. It mirrors transpile.go's emitExpr
// but emits bytecode instead of JSON.
func (c *cstCompiler) compileExpr(code []byte, n *gotreesitter.Node) []byte {
	if n == nil {
		return Emit(code, OpLoadNull, 0, 0)
	}

	switch c.nodeType(n) {
	// Logical
	case "and_expr":
		return c.compileAnd(code, n)
	case "or_expr":
		return c.compileOr(code, n)
	case "not_expr":
		code = c.compileExpr(code, c.childByField(n, "operand"))
		return Emit(code, OpNot, 0, 0)

	// Comparison
	case "comparison_expr":
		return c.compileComparison(code, n)

	// Collection operators
	case "in_expr":
		return c.compileBinaryOp(code, n, OpIn)
	case "not_in_expr":
		return c.compileBinaryOp(code, n, OpNotIn)
	case "contains_expr":
		return c.compileBinaryOp(code, n, OpContains)
	case "not_contains_expr":
		return c.compileBinaryOp(code, n, OpNotContains)
	case "retains_expr":
		return c.compileBinaryOp(code, n, OpRetains)
	case "not_retains_expr":
		return c.compileBinaryOp(code, n, OpNotRetains)
	case "subset_of_expr":
		return c.compileBinaryOp(code, n, OpSubsetOf)
	case "superset_of_expr":
		return c.compileBinaryOp(code, n, OpSupersetOf)
	case "vague_contains_expr":
		return c.compileBinaryOp(code, n, OpVagueContains)

	// String operators
	case "starts_with_expr":
		return c.compileBinaryOp(code, n, OpStartsWith)
	case "ends_with_expr":
		return c.compileBinaryOp(code, n, OpEndsWith)
	case "matches_expr":
		return c.compileBinaryOp(code, n, OpMatches)

	// Range
	case "between_expr":
		return c.compileBetween(code, n)

	// Null checks
	case "is_null_expr":
		code = c.compileExpr(code, c.childByField(n, "left"))
		return Emit(code, OpIsNull, 0, 0)
	case "is_not_null_expr":
		code = c.compileExpr(code, c.childByField(n, "left"))
		return Emit(code, OpIsNotNull, 0, 0)

	// Math
	case "math_expr":
		return c.compileMath(code, n)

	// Quantifiers
	case "quantifier_expr":
		return c.compileQuantifier(code, n)

	// Primaries
	case "member_expr":
		return c.compileMember(code, n)
	case "identifier":
		return c.compileIdentifier(code, n)
	case "number_literal":
		return c.compileNumber(code, n)
	case "string_literal":
		return c.compileString(code, n)
	case "bool_literal":
		return c.compileBool(code, n)
	case "list_literal":
		return c.compileList(code, n)
	case "paren_expr":
		return c.compileExpr(code, c.childByField(n, "expr"))

	default:
		// Recurse into single-child wrapper nodes
		if n.NamedChildCount() == 1 {
			return c.compileExpr(code, n.NamedChild(0))
		}
		return Emit(code, OpLoadNull, 0, 0)
	}
}

// compileAnd emits: left, right, OpAnd.
// The VM's JumpIfFalse pops unconditionally, so true short-circuit requires
// a plain Jump opcode (not yet available). We emit the correct non-short-circuit
// form and can add short-circuit as an optimization later.
func (c *cstCompiler) compileAnd(code []byte, n *gotreesitter.Node) []byte {
	code = c.compileExpr(code, c.childByField(n, "left"))
	code = c.compileExpr(code, c.childByField(n, "right"))
	return Emit(code, OpAnd, 0, 0)
}

// compileOr emits: left, right, OpOr.
func (c *cstCompiler) compileOr(code []byte, n *gotreesitter.Node) []byte {
	code = c.compileExpr(code, c.childByField(n, "left"))
	code = c.compileExpr(code, c.childByField(n, "right"))
	return Emit(code, OpOr, 0, 0)
}

// compileComparison emits: lhs, rhs, comparison_opcode.
func (c *cstCompiler) compileComparison(code []byte, n *gotreesitter.Node) []byte {
	code = c.compileExpr(code, c.childByField(n, "left"))
	code = c.compileExpr(code, c.childByField(n, "right"))

	opStr := ""
	if opNode := c.childByField(n, "op"); opNode != nil {
		opStr = c.text(opNode)
	} else {
		leftNode := c.childByField(n, "left")
		rightNode := c.childByField(n, "right")
		if leftNode != nil && rightNode != nil {
			opStr = strings.TrimSpace(string(c.src[leftNode.EndByte():rightNode.StartByte()]))
		}
		if opStr == "" {
			for i := 0; i < int(n.ChildCount()); i++ {
				ch := n.Child(i)
				txt := c.text(ch)
				if txt == "==" || txt == "!=" || txt == ">" || txt == "<" || txt == ">=" || txt == "<=" {
					opStr = txt
					break
				}
			}
		}
	}

	return Emit(code, mapComparisonOpcode(strings.TrimSpace(opStr)), 0, 0)
}

func mapComparisonOpcode(op string) OpCode {
	switch op {
	case "==":
		return OpEq
	case "!=":
		return OpNeq
	case ">":
		return OpGt
	case ">=":
		return OpGte
	case "<":
		return OpLt
	case "<=":
		return OpLte
	default:
		return OpEq
	}
}

// compileBinaryOp compiles a binary operator expression.
func (c *cstCompiler) compileBinaryOp(code []byte, n *gotreesitter.Node, op OpCode) []byte {
	code = c.compileExpr(code, c.childByField(n, "left"))
	code = c.compileExpr(code, c.childByField(n, "right"))
	return Emit(code, op, 0, 0)
}

// compileBetween emits: value, low, high, between_opcode.
func (c *cstCompiler) compileBetween(code []byte, n *gotreesitter.Node) []byte {
	code = c.compileExpr(code, c.childByField(n, "left"))
	code = c.compileExpr(code, c.childByField(n, "low"))
	code = c.compileExpr(code, c.childByField(n, "high"))

	open := c.text(c.childByField(n, "open"))
	close := c.text(c.childByField(n, "close"))

	var op OpCode
	switch {
	case open == "[" && close == "]":
		op = OpBetweenCC
	case open == "(" && close == ")":
		op = OpBetweenOO
	case open == "[" && close == ")":
		op = OpBetweenCO
	case open == "(" && close == "]":
		op = OpBetweenOC
	default:
		op = OpBetweenCC
	}

	return Emit(code, op, 0, 0)
}

// compileMath emits: left, right, math_opcode.
func (c *cstCompiler) compileMath(code []byte, n *gotreesitter.Node) []byte {
	code = c.compileExpr(code, c.childByField(n, "left"))
	code = c.compileExpr(code, c.childByField(n, "right"))

	switch c.text(c.childByField(n, "op")) {
	case "+":
		return Emit(code, OpAdd, 0, 0)
	case "-":
		return Emit(code, OpSub, 0, 0)
	case "*":
		return Emit(code, OpMul, 0, 0)
	case "/":
		return Emit(code, OpDiv, 0, 0)
	case "%":
		return Emit(code, OpMod, 0, 0)
	default:
		return Emit(code, OpAdd, 0, 0)
	}
}

// compileQuantifier emits iteration opcodes for any/all/none.
func (c *cstCompiler) compileQuantifier(code []byte, n *gotreesitter.Node) []byte {
	quantifier := c.text(c.childByField(n, "quantifier"))
	varName := c.text(c.childByField(n, "var"))

	// Compile collection expression
	code = c.compileExpr(code, c.childByField(n, "collection"))

	var flag uint8
	switch quantifier {
	case "any":
		flag = FlagAny
	case "all":
		flag = FlagAll
	case "none":
		flag = FlagNone
	default:
		flag = FlagAll
	}

	varIdx := c.pool.String(varName)

	// IterBegin: pop collection, start iteration
	code = Emit(code, OpIterBegin, flag, varIdx)

	// Compile body
	bodyStart := len(code)
	code = c.compileExpr(code, c.childByField(n, "body"))
	bodyLen := uint16(len(code) - bodyStart)

	// IterNext: arg = body length for loop control
	code = Emit(code, OpIterNext, flag, bodyLen)

	// IterEnd: push final result
	code = Emit(code, OpIterEnd, flag, 0)

	return code
}

// compileMember emits LoadVar with the full dotted path.
func (c *cstCompiler) compileMember(code []byte, n *gotreesitter.Node) []byte {
	path := c.text(n)
	idx := c.pool.String(path)
	return Emit(code, OpLoadVar, 0, idx)
}

// compileIdentifier handles identifiers: const references, bool literals, variables.
func (c *cstCompiler) compileIdentifier(code []byte, n *gotreesitter.Node) []byte {
	name := c.text(n)

	// Inline constant reference
	if cv, ok := c.consts[name]; ok {
		return c.emitConstVal(code, cv)
	}

	// Boolean/null literals that parse as identifiers
	switch name {
	case "true":
		return Emit(code, OpLoadBool, 0, 1)
	case "false":
		return Emit(code, OpLoadBool, 0, 0)
	case "null":
		return Emit(code, OpLoadNull, 0, 0)
	}

	// Variable reference
	idx := c.pool.String(name)
	return Emit(code, OpLoadVar, 0, idx)
}

// emitConstVal emits instructions to load a compile-time constant.
func (c *cstCompiler) emitConstVal(code []byte, cv constVal) []byte {
	switch cv.kind {
	case "number":
		return Emit(code, OpLoadNum, 0, c.pool.Number(cv.num))
	case "string":
		return Emit(code, OpLoadStr, 0, c.pool.String(cv.str))
	case "bool":
		arg := uint16(0)
		if cv.b {
			arg = 1
		}
		return Emit(code, OpLoadBool, 0, arg)
	case "list":
		listIdx, listLen := c.pool.List(cv.listItems)
		// Encode list: flags = listLen (uint8, max 255 elements), arg = listIdx.
		// The VM reconstructs ListVal from these fields.
		return Emit(code, OpLoadStr, uint8(listLen), listIdx)
	default:
		return Emit(code, OpLoadNull, 0, 0)
	}
}

func (c *cstCompiler) compileNumber(code []byte, n *gotreesitter.Node) []byte {
	return Emit(code, OpLoadNum, 0, c.pool.Number(parseFloat(c.text(n))))
}

func (c *cstCompiler) compileString(code []byte, n *gotreesitter.Node) []byte {
	return Emit(code, OpLoadStr, 0, c.pool.String(stripQuotes(c.text(n))))
}

func (c *cstCompiler) compileBool(code []byte, n *gotreesitter.Node) []byte {
	arg := uint16(0)
	if c.text(n) == "true" {
		arg = 1
	}
	return Emit(code, OpLoadBool, 0, arg)
}

func (c *cstCompiler) compileList(code []byte, n *gotreesitter.Node) []byte {
	var items []intern.PoolValue
	for i := 0; i < int(n.NamedChildCount()); i++ {
		items = append(items, c.exprToPoolValue(n.NamedChild(i)))
	}
	listIdx, listLen := c.pool.List(items)
	return Emit(code, OpLoadStr, uint8(listLen), listIdx)
}

// --- Helpers ---

func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func parseInt(s string) int {
	n := 0
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n
}

func parseFloat(s string) float64 {
	negative := false
	if len(s) > 0 && s[0] == '-' {
		negative = true
		s = s[1:]
	}
	var result float64
	decimal := false
	divisor := 1.0
	for _, ch := range s {
		if ch == '.' {
			decimal = true
			continue
		}
		if ch >= '0' && ch <= '9' {
			if decimal {
				divisor *= 10
				result += float64(ch-'0') / divisor
			} else {
				result = result*10 + float64(ch-'0')
			}
		}
	}
	if negative {
		result = -result
	}
	return result
}
