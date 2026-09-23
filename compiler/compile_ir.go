package compiler

import (
	"fmt"
	"regexp"
	"time"

	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/intern"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/units"
)

// CompileIR emits a CompiledRuleset from a lowered Arbiter IR program.
func CompileIR(program *ir.Program) (*CompiledRuleset, error) {
	if program == nil {
		return nil, fmt.Errorf("nil program")
	}

	c := &irCompiler{
		program: program,
		pool:    intern.NewPool(),
	}
	return c.compile()
}

type irCompiler struct {
	program *ir.Program
	pool    *intern.Pool
	err     error
	rs      *CompiledRuleset

	constStack map[string]bool
}

func (c *irCompiler) compile() (*CompiledRuleset, error) {
	rs := &CompiledRuleset{
		Constants: c.pool,
	}
	c.rs = rs

	// Compile table declarations first so lookups can reference them.
	for i := range c.program.Tables {
		ct := c.compileTable(&c.program.Tables[i])
		if err := c.pool.Err(); err != nil {
			return nil, err
		}
		rs.Tables = append(rs.Tables, ct)
	}

	for i := range c.program.Rules {
		rule := &c.program.Rules[i]
		rh, err := c.compileRule(rule, rs)
		if err != nil {
			return nil, err
		}
		if c.err != nil {
			return nil, c.err
		}
		if err := c.pool.Err(); err != nil {
			return nil, err
		}
		rs.Rules = append(rs.Rules, rh)
	}
	if err := c.pool.Err(); err != nil {
		return nil, err
	}

	return rs, nil
}

func (c *irCompiler) compileTable(table *ir.Table) CompiledTable {
	ct := CompiledTable{
		Name:    table.Name,
		Columns: make([]string, len(table.Columns)),
	}
	for i, col := range table.Columns {
		ct.Columns[i] = col.Name
	}
	for _, row := range table.Rows {
		vals := make([]intern.PoolValue, len(row.Values))
		for j, exprID := range row.Values {
			vals[j] = c.exprToPoolValue(exprID)
		}
		ct.Rows = append(ct.Rows, vals)
	}
	return ct
}

func (c *irCompiler) findTableIndex(name string) (uint16, bool) {
	for i, t := range c.rs.Tables {
		if t.Name == name {
			return uint16(i), true
		}
	}
	return 0, false
}

func (c *irCompiler) compileRule(rule *ir.Rule, rs *CompiledRuleset) (RuleHeader, error) {
	rh := RuleHeader{
		NameIdx:    c.pool.String(rule.Name),
		Priority:   rule.Priority,
		KillSwitch: rule.KillSwitch,
	}
	if rule.ActiveWindow.HasFrom {
		fromUnixNano, err := compileTimestampLiteralUnixNano(rule.ActiveWindow.From)
		if err != nil {
			return RuleHeader{}, err
		}
		rh.HasActiveFrom = true
		rh.ActiveFromUnixNano = fromUnixNano
	}
	if rule.ActiveWindow.HasUntil {
		untilUnixNano, err := compileTimestampLiteralUnixNano(rule.ActiveWindow.Until)
		if err != nil {
			return RuleHeader{}, err
		}
		rh.HasActiveUntil = true
		rh.ActiveUntilUnixNano = untilUnixNano
	}

	for _, prereq := range rule.Prereqs {
		if rh.PrereqLen == 0 {
			rh.PrereqOff = uint16(len(rs.Prereqs))
		}
		rs.Prereqs = append(rs.Prereqs, c.pool.String(prereq))
		rh.PrereqLen++
	}

	for _, tag := range rule.Tags {
		if rh.TagLen == 0 {
			rh.TagOff = uint16(len(rs.Tags))
		}
		rs.Tags = append(rs.Tags, c.pool.String(tag))
		rh.TagLen++
	}

	for _, exclude := range rule.Excludes {
		if rh.ExcludeLen == 0 {
			rh.ExcludeOff = uint16(len(rs.Excludes))
		}
		rs.Excludes = append(rs.Excludes, c.pool.String(exclude))
		rh.ExcludeLen++
	}

	if rule.Segment != "" {
		rh.SegmentNameIdx = c.pool.String(rule.Segment)
		rh.HasSegment = true
	}

	if rule.HasCondition {
		if err := checkExprStackDepth(c.program, fmt.Sprintf("rule %s condition", rule.Name), rule.Lets, rule.Condition); err != nil {
			return RuleHeader{}, err
		}
		condOff := uint32(len(rs.Instructions))
		var code []byte
		for _, binding := range rule.Lets {
			code = c.compileLet(code, binding)
		}
		code = c.compileExpr(code, rule.Condition)
		rs.Instructions = append(rs.Instructions, code...)
		rh.ConditionOff = condOff
		rh.ConditionLen = uint32(len(code))
	}

	actionIdx := uint16(len(rs.Actions))
	rs.Actions = append(rs.Actions, c.compileAction(rule.Action, rs))
	rh.ActionIdx = actionIdx

	if rule.Fallback != nil {
		fallbackIdx := uint16(len(rs.Actions))
		rs.Actions = append(rs.Actions, c.compileAction(*rule.Fallback, rs))
		rh.FallbackIdx = fallbackIdx
	}

	if rule.Rollout != nil {
		rh.HasRollout = true
		if rule.Rollout.HasBps {
			rh.RolloutBps = rule.Rollout.Bps
		}
		if rule.Rollout.HasSubject {
			rh.RolloutSubjectIdx = c.pool.String(rule.Rollout.Subject)
			rh.HasRolloutSubject = true
		}
		if rule.Rollout.HasNamespace {
			rh.RolloutNamespaceIdx = c.pool.String(rule.Rollout.Namespace)
			rh.HasRolloutNamespace = true
		}
	}

	return rh, nil
}

func (c *irCompiler) compileAction(action ir.Action, rs *CompiledRuleset) ActionEntry {
	entry := ActionEntry{
		NameIdx: c.pool.String(action.Name),
	}

	// Compile let bindings in action blocks. These are prepended to
	// the first param's bytecode so they execute before param evaluation.
	var letCode []byte
	for _, binding := range action.Lets {
		letCode = c.compileLet(letCode, binding)
	}

	for i, param := range action.Params {
		paramLets := action.Lets
		if i != 0 {
			paramLets = nil // only prepended to the first param, see below
		}
		if err := checkExprStackDepth(c.program, fmt.Sprintf("rule action %s param %s", action.Name, param.Key), paramLets, param.Value); err != nil {
			if c.err == nil {
				c.err = err
			}
			return entry
		}
		paramOff := uint32(len(rs.Instructions))
		var code []byte
		// Prepend let bindings to the first param so locals are set up.
		if i == 0 && len(letCode) > 0 {
			code = append(code, letCode...)
		}
		code = c.compileExpr(code, param.Value)
		rs.Instructions = append(rs.Instructions, code...)
		entry.Params = append(entry.Params, ActionParam{
			KeyIdx:   c.pool.String(param.Key),
			ValueOff: paramOff,
			ValueLen: uint32(len(code)),
		})
	}

	return entry
}

func (c *irCompiler) compileLet(code []byte, binding ir.LetBinding) []byte {
	code = c.compileExpr(code, binding.Value)
	return Emit(code, OpSetLocal, 0, c.pool.String(binding.Name))
}

func (c *irCompiler) compileExpr(code []byte, exprID ir.ExprID) []byte {
	expr := c.program.Expr(exprID)
	if expr == nil {
		return Emit(code, OpLoadNull, 0, 0)
	}

	switch expr.Kind {
	case ir.ExprStringLit:
		return Emit(code, OpLoadStr, 0, c.pool.String(expr.String))
	case ir.ExprNumberLit:
		return Emit(code, OpLoadNum, 0, c.pool.Number(expr.Number))
	case ir.ExprDecimalLit:
		value, err := dec.Parse(expr.String, expr.Unit)
		if err != nil {
			if c.err == nil {
				c.err = err
			}
			return Emit(code, OpLoadNull, 0, 0)
		}
		return Emit(code, OpLoadDec, 0, c.pool.Decimal(value))
	case ir.ExprQuantityLit:
		n, _, err := units.Normalize(expr.Number, expr.Unit)
		if err != nil {
			if c.err == nil {
				c.err = err
			}
			return Emit(code, OpLoadNull, 0, 0)
		}
		return Emit(code, OpLoadNum, 0, c.pool.Number(n))
	case ir.ExprTimestampLit:
		n, err := compileTimestampLiteral(expr.String)
		if err != nil {
			if c.err == nil {
				c.err = err
			}
			return Emit(code, OpLoadNull, 0, 0)
		}
		return Emit(code, OpLoadNum, 0, c.pool.Number(n))
	case ir.ExprBoolLit:
		arg := uint16(0)
		if expr.Bool {
			arg = 1
		}
		return Emit(code, OpLoadBool, 0, arg)
	case ir.ExprNullLit:
		return Emit(code, OpLoadNull, 0, 0)
	case ir.ExprListLit:
		return c.compileList(code, expr)
	case ir.ExprVarRef:
		return Emit(code, OpLoadVar, 0, c.pool.String(expr.Path))
	case ir.ExprLocalRef:
		return Emit(code, OpLoadVar, 0, c.pool.String(expr.Name))
	case ir.ExprConstRef:
		return c.compileConstRef(code, expr.Name)
	case ir.ExprSecretRef:
		return Emit(code, OpLoadStr, 0, c.pool.String(expr.Path))
	case ir.ExprBinary:
		return c.compileBinary(code, expr)
	case ir.ExprUnary:
		return c.compileUnary(code, expr)
	case ir.ExprBetween:
		return c.compileBetween(code, expr)
	case ir.ExprQuantifier:
		return c.compileQuantifier(code, expr)
	case ir.ExprAggregate:
		return c.compileAggregate(code, expr)
	case ir.ExprBuiltinCall:
		return c.compileBuiltin(code, expr)
	case ir.ExprLookup:
		return c.compileLookup(code, expr)
	default:
		return Emit(code, OpLoadNull, 0, 0)
	}
}

func (c *irCompiler) compileConstRef(code []byte, name string) []byte {
	if c.constStack == nil {
		c.constStack = make(map[string]bool)
	}
	if c.constStack[name] {
		if c.err == nil {
			c.err = fmt.Errorf("circular const reference %q", name)
		}
		return Emit(code, OpLoadNull, 0, 0)
	}
	decl, ok := c.program.ConstByName(name)
	if !ok {
		return Emit(code, OpLoadNull, 0, 0)
	}
	c.constStack[name] = true
	code = c.compileExpr(code, decl.Value)
	delete(c.constStack, name)
	return code
}

func (c *irCompiler) compileList(code []byte, expr *ir.Expr) []byte {
	items := make([]intern.PoolValue, 0, len(expr.Elems))
	for _, elem := range expr.Elems {
		items = append(items, c.exprToPoolValue(elem))
	}
	listIdx, listLen := c.pool.List(items)
	code = Emit(code, OpLoadNull, intern.TypeList, listIdx)
	return Emit(code, OpLoadNull, 0xFF, listLen)
}

func (c *irCompiler) exprToPoolValue(exprID ir.ExprID) intern.PoolValue {
	expr := c.program.Expr(exprID)
	if expr == nil {
		return intern.PoolValue{Typ: intern.TypeNull}
	}

	switch expr.Kind {
	case ir.ExprNumberLit:
		return intern.PoolValue{Typ: intern.TypeNumber, Num: expr.Number}
	case ir.ExprDecimalLit:
		value, err := dec.Parse(expr.String, expr.Unit)
		if err != nil {
			return intern.PoolValue{Typ: intern.TypeNull}
		}
		return intern.PoolValue{Typ: intern.TypeDecimal, Dec: c.pool.Decimal(value)}
	case ir.ExprQuantityLit:
		n, _, err := units.Normalize(expr.Number, expr.Unit)
		if err != nil {
			return intern.PoolValue{Typ: intern.TypeNull}
		}
		return intern.PoolValue{Typ: intern.TypeNumber, Num: n}
	case ir.ExprTimestampLit:
		n, err := compileTimestampLiteral(expr.String)
		if err != nil {
			return intern.PoolValue{Typ: intern.TypeNull}
		}
		return intern.PoolValue{Typ: intern.TypeNumber, Num: n}
	case ir.ExprStringLit:
		return intern.PoolValue{Typ: intern.TypeString, Str: c.pool.String(expr.String)}
	case ir.ExprBoolLit:
		return intern.PoolValue{Typ: intern.TypeBool, Bool: expr.Bool}
	case ir.ExprNullLit:
		return intern.PoolValue{Typ: intern.TypeNull}
	case ir.ExprSecretRef:
		return intern.PoolValue{Typ: intern.TypeString, Str: c.pool.String(expr.Path)}
	case ir.ExprConstRef:
		decl, ok := c.program.ConstByName(expr.Name)
		if !ok {
			return intern.PoolValue{Typ: intern.TypeNull}
		}
		return c.exprToPoolValue(decl.Value)
	case ir.ExprListLit:
		items := make([]intern.PoolValue, 0, len(expr.Elems))
		for _, elem := range expr.Elems {
			items = append(items, c.exprToPoolValue(elem))
		}
		listIdx, listLen := c.pool.List(items)
		return intern.PoolValue{
			Typ:     intern.TypeList,
			ListIdx: listIdx,
			ListLen: listLen,
		}
	default:
		return intern.PoolValue{Typ: intern.TypeNull}
	}
}

func (c *irCompiler) compileBuiltin(code []byte, expr *ir.Expr) []byte {
	switch expr.FuncName {
	case "now":
		return Emit(code, OpLoadVar, 0, c.pool.String("__now"))
	case "abs":
		if len(expr.Args) != 1 {
			return Emit(code, OpLoadNull, 0, 0)
		}
		code = c.compileExpr(code, expr.Args[0])
		return Emit(code, OpAbs, 0, 0)
	case "min":
		if len(expr.Args) != 2 {
			return Emit(code, OpLoadNull, 0, 0)
		}
		code = c.compileExpr(code, expr.Args[0])
		code = c.compileExpr(code, expr.Args[1])
		return Emit(code, OpMin, 0, 0)
	case "max":
		if len(expr.Args) != 2 {
			return Emit(code, OpLoadNull, 0, 0)
		}
		code = c.compileExpr(code, expr.Args[0])
		code = c.compileExpr(code, expr.Args[1])
		return Emit(code, OpMax, 0, 0)
	case "round":
		if len(expr.Args) != 1 {
			return Emit(code, OpLoadNull, 0, 0)
		}
		code = c.compileExpr(code, expr.Args[0])
		return Emit(code, OpRound, 0, 0)
	case "floor":
		if len(expr.Args) != 1 {
			return Emit(code, OpLoadNull, 0, 0)
		}
		code = c.compileExpr(code, expr.Args[0])
		return Emit(code, OpFloor, 0, 0)
	case "ceil":
		if len(expr.Args) != 1 {
			return Emit(code, OpLoadNull, 0, 0)
		}
		code = c.compileExpr(code, expr.Args[0])
		return Emit(code, OpCeil, 0, 0)
	default:
		return Emit(code, OpLoadNull, 0, 0)
	}
}

func (c *irCompiler) compileLookup(code []byte, expr *ir.Expr) []byte {
	tableIdx, ok := c.findTableIndex(expr.TableName)
	if !ok {
		if c.err == nil {
			c.err = fmt.Errorf("unknown table %q", expr.TableName)
		}
		return Emit(code, OpLoadNull, 0, 0)
	}

	meta := LookupMeta{
		TableIdx: tableIdx,
		SortCol:  expr.SortCol,
		SortDesc: expr.SortDesc,
	}

	// Compile the where clause into the instruction stream.
	if expr.Where != 0 {
		whereOff := uint32(len(c.rs.Instructions))
		var whereCode []byte
		whereCode = c.compileExpr(whereCode, expr.Where)
		c.rs.Instructions = append(c.rs.Instructions, whereCode...)
		meta.WhereOff = whereOff
		meta.WhereLen = uint32(len(whereCode))
	}

	// Compile else values. Each else key/value pair is compiled so the VM
	// can build a row map from the evaluated results.
	if len(expr.ElseKeys) > 0 {
		meta.ElseKeys = make([]string, len(expr.ElseKeys))
		copy(meta.ElseKeys, expr.ElseKeys)
		elseOff := uint32(len(c.rs.Instructions))
		var elseCode []byte
		for _, valExpr := range expr.ElseVals {
			elseCode = c.compileExpr(elseCode, valExpr)
		}
		c.rs.Instructions = append(c.rs.Instructions, elseCode...)
		meta.ElseOff = elseOff
		meta.ElseLen = uint32(len(elseCode))
	}

	metaIdx := uint16(len(c.rs.LookupMetas))
	c.rs.LookupMetas = append(c.rs.LookupMetas, meta)

	return Emit(code, OpLookup, 0, metaIdx)
}

func compileTimestampLiteral(text string) (float64, error) {
	unixNano, err := compileTimestampLiteralUnixNano(text)
	if err != nil {
		return 0, err
	}
	return float64(unixNano) / float64(time.Second), nil
}

func compileTimestampLiteralUnixNano(text string) (int64, error) {
	ts, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp literal %q", text)
	}
	return ts.UTC().UnixNano(), nil
}

func (c *irCompiler) compileBinary(code []byte, expr *ir.Expr) []byte {
	switch expr.BinaryOp {
	case ir.BinaryAnd:
		code = c.compileExpr(code, expr.Left)
		jumpPos := len(code)
		code = Emit(code, OpJumpIfFalse, 0, 0)
		code = c.compileExpr(code, expr.Right)
		code = Emit(code, OpAnd, 0, 0)
		dist := uint16(len(code) - jumpPos)
		code[jumpPos+2] = byte(dist)
		code[jumpPos+3] = byte(dist >> 8)
		return code
	case ir.BinaryOr:
		code = c.compileExpr(code, expr.Left)
		jumpPos := len(code)
		code = Emit(code, OpJumpIfTrue, 0, 0)
		code = c.compileExpr(code, expr.Right)
		code = Emit(code, OpOr, 0, 0)
		dist := uint16(len(code) - jumpPos)
		code[jumpPos+2] = byte(dist)
		code[jumpPos+3] = byte(dist >> 8)
		return code
	case ir.BinaryMatches:
		code = c.compileExpr(code, expr.Left)
		rightExpr := c.program.Expr(expr.Right)
		code = c.compileExpr(code, expr.Right)
		// If the right operand is a literal string with a pre-validated regex,
		// record it in the ruleset for O(1) lookup at eval time.
		if rightExpr != nil && rightExpr.Kind == ir.ExprStringLit {
			if re, ok := c.program.ValidatedRegexes[rightExpr.String]; ok {
				poolIdx := c.pool.String(rightExpr.String)
				if c.rs.Regexes == nil {
					c.rs.Regexes = make(map[uint16]*regexp.Regexp)
				}
				c.rs.Regexes[poolIdx] = re
			}
		}
		return Emit(code, OpMatches, 0, 0)
	default:
		code = c.compileExpr(code, expr.Left)
		code = c.compileExpr(code, expr.Right)
		return Emit(code, mapBinaryOpcode(expr.BinaryOp), 0, 0)
	}
}

func (c *irCompiler) compileUnary(code []byte, expr *ir.Expr) []byte {
	code = c.compileExpr(code, expr.Operand)
	switch expr.UnaryOp {
	case ir.UnaryNot:
		return Emit(code, OpNot, 0, 0)
	case ir.UnaryIsNull:
		return Emit(code, OpIsNull, 0, 0)
	case ir.UnaryIsNotNull:
		return Emit(code, OpIsNotNull, 0, 0)
	default:
		return Emit(code, OpLoadNull, 0, 0)
	}
}

func (c *irCompiler) compileBetween(code []byte, expr *ir.Expr) []byte {
	code = c.compileExpr(code, expr.Value)
	code = c.compileExpr(code, expr.Low)
	code = c.compileExpr(code, expr.High)
	return Emit(code, mapBetweenOpcode(expr.BetweenKind), 0, 0)
}

func (c *irCompiler) compileQuantifier(code []byte, expr *ir.Expr) []byte {
	code = c.compileExpr(code, expr.Collection)
	code = Emit(code, OpIterBegin, mapQuantifierFlag(expr.QuantifierKind), c.pool.String(expr.VarName))
	bodyStart := len(code)
	code = c.compileExpr(code, expr.Body)
	bodyLen := uint16(len(code) - bodyStart)
	code = Emit(code, OpIterNext, mapQuantifierFlag(expr.QuantifierKind), bodyLen)
	return Emit(code, OpIterEnd, mapQuantifierFlag(expr.QuantifierKind), 0)
}

func (c *irCompiler) compileAggregate(code []byte, expr *ir.Expr) []byte {
	flag, ok := mapAggregateFlag(expr.AggregateKind)
	if !ok {
		if c.err == nil {
			c.err = fmt.Errorf("unsupported aggregate function %q", expr.AggregateKind)
		}
		return Emit(code, OpLoadNull, 0, 0)
	}

	code = c.compileExpr(code, expr.Collection)
	code = Emit(code, OpAggBegin, flag, c.pool.String(expr.VarName))
	bodyStart := len(code)
	if expr.HasValueExpr {
		code = c.compileExpr(code, expr.ValueExpr)
	} else {
		code = Emit(code, OpLoadNum, 0, c.pool.Number(1))
	}
	bodyLen := uint16(len(code) - bodyStart)
	code = Emit(code, OpAggAccum, flag, bodyLen)
	return Emit(code, OpAggEnd, flag, 0)
}

func mapBinaryOpcode(op ir.BinaryOpKind) OpCode {
	switch op {
	case ir.BinaryEq:
		return OpEq
	case ir.BinaryNeq:
		return OpNeq
	case ir.BinaryGt:
		return OpGt
	case ir.BinaryGte:
		return OpGte
	case ir.BinaryLt:
		return OpLt
	case ir.BinaryLte:
		return OpLte
	case ir.BinaryIn:
		return OpIn
	case ir.BinaryNotIn:
		return OpNotIn
	case ir.BinaryContains:
		return OpContains
	case ir.BinaryNotContains:
		return OpNotContains
	case ir.BinaryRetains:
		return OpRetains
	case ir.BinaryNotRetains:
		return OpNotRetains
	case ir.BinarySubsetOf:
		return OpSubsetOf
	case ir.BinarySupersetOf:
		return OpSupersetOf
	case ir.BinaryVagueContains:
		return OpVagueContains
	case ir.BinaryStartsWith:
		return OpStartsWith
	case ir.BinaryEndsWith:
		return OpEndsWith
	case ir.BinaryMatches:
		return OpMatches
	case ir.BinaryAdd:
		return OpAdd
	case ir.BinarySub:
		return OpSub
	case ir.BinaryMul:
		return OpMul
	case ir.BinaryDiv:
		return OpDiv
	case ir.BinaryMod:
		return OpMod
	default:
		return OpLoadNull
	}
}

func mapBetweenOpcode(kind ir.BetweenKind) OpCode {
	switch kind {
	case ir.BetweenOpenOpen:
		return OpBetweenOO
	case ir.BetweenClosedOpen:
		return OpBetweenCO
	case ir.BetweenOpenClosed:
		return OpBetweenOC
	default:
		return OpBetweenCC
	}
}

func mapQuantifierFlag(kind ir.QuantifierKind) uint8 {
	switch kind {
	case ir.QuantifierAny:
		return FlagAny
	case ir.QuantifierNone:
		return FlagNone
	default:
		return FlagAll
	}
}

func mapAggregateFlag(kind ir.AggregateKind) (uint8, bool) {
	switch kind {
	case ir.AggregateSum:
		return FlagSum, true
	case ir.AggregateCount:
		return FlagCount, true
	case ir.AggregateAvg:
		return FlagAvg, true
	default:
		return 0, false
	}
}
