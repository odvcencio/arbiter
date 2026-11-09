package gameai

import (
	"strconv"

	"m31labs.dev/arbiter/bytecode"
	"m31labs.dev/arbiter/intern"
	"m31labs.dev/arbiter/vm"
)

// Trace explains one decision for a debug overlay: which rules were tried,
// which conditions held, the candidates and their scores or weights, and the
// choice. Tracing is off unless a Trace is passed in, and then costs one
// pointer test per comparison when off. A Trace reuses its slices, so tracing
// one watched agent every tick does not allocate once they have grown.
type Trace struct {
	// Decision is the decision's name (set by Brain) or tag.
	Decision  string
	Selection Selection
	// Candidates are the matched rules, in declaration order.
	Candidates []Candidate
	// Chosen is the index in Candidates of the choice, or -1.
	Chosen int
	// Seed, Total and Draw describe a Weighted draw: Draw = Unit(Seed)*Total.
	Seed  uint64
	Total float64
	Draw  float64
	// VM holds every rule tried and the comparisons each one executed.
	VM vm.Trace

	d *Decider
}

// Candidate is one matched rule in a Trace.
type Candidate struct {
	Rule string
	// RuleIndex is the rule's index in Program.Rules.
	RuleIndex int
	Action    string
	Fallback  bool
	// Score is the candidate's score or weight; Scored is false when the
	// parameter was missing or unusable, so the candidate could not be chosen.
	Score  float64
	Scored bool
}

func (t *Trace) fill(d *Decider, sel Selection, seed uint64, chosen int) {
	t.Decision = d.tag
	t.d, t.Selection, t.Seed, t.Chosen = d, sel, seed, chosen
	t.Total, t.Draw = 0, 0
	clear(t.Candidates)
	t.Candidates = t.Candidates[:0]
	for i := range d.out.List {
		m := &d.out.List[i]
		c := Candidate{Rule: m.Name, RuleIndex: m.Rule, Action: m.Action, Fallback: m.Fallback}
		switch sel.mode {
		case selBest:
			c.Score, c.Scored = numberParam(m, sel.key)
			c.Scored = c.Scored && c.Score == c.Score
		case selWeighted:
			c.Score, c.Scored = weightParam(m, sel.key)
		}
		t.Candidates = append(t.Candidates, c)
	}
}

// String renders the trace as text; see AppendText.
func (t *Trace) String() string { return string(t.AppendText(nil)) }

// AppendText renders the trace as compact text, one line per rule tried:
//
//	intent best(score) -> Carry 80
//	> Carry 80: self.has_orb=true
//	+ Pass 77: self.has_orb=true self.pass_open=true
//	- Chase: self.has_orb=true orb.free=false
//
// The first line names the decision, the selection and the choice. Each rule
// line starts with ">" for the choice, "+" for another candidate, "~" for a
// candidate from an otherwise action, and "-" for a rule whose condition did
// not hold; then the rule, its score or weight, and the conditions it checked
// in order. A boolean input reads "name=value"; a comparison reads
// "lhs(value) op rhs(value)" and ends in "NO" when it was false.
func (t *Trace) AppendText(dst []byte) []byte {
	dst = append(dst, t.Decision...)
	dst = append(dst, ' ')
	dst = append(dst, t.Selection.String()...)
	dst = append(dst, " -> "...)
	if t.Chosen >= 0 && t.Chosen < len(t.Candidates) {
		c := t.Candidates[t.Chosen]
		dst = append(dst, c.Rule...)
		if t.Selection.mode != selFirst {
			dst = append(dst, ' ')
			dst = appendNumber(dst, c.Score)
		}
	} else {
		dst = append(dst, "none"...)
	}
	if t.Selection.mode == selWeighted {
		dst = append(dst, " (draw "...)
		dst = appendNumber(dst, t.Draw)
		dst = append(dst, " of "...)
		dst = appendNumber(dst, t.Total)
		dst = append(dst, ')')
	}
	dst = append(dst, '\n')

	cand := 0
	for _, r := range t.VM.Rules {
		mark := byte('-')
		var c *Candidate
		if cand < len(t.Candidates) && t.Candidates[cand].RuleIndex == r.Rule && (r.Held || r.Fallback) {
			c = &t.Candidates[cand]
			switch {
			case cand == t.Chosen:
				mark = '>'
			case c.Fallback:
				mark = '~'
			default:
				mark = '+'
			}
			cand++
		}
		dst = append(dst, mark, ' ')
		dst = append(dst, r.Name...)
		if c != nil && c.Scored {
			dst = append(dst, ' ')
			dst = appendNumber(dst, c.Score)
		}
		dst = append(dst, ':')
		for i := r.CheckStart; i < r.CheckEnd && i < len(t.VM.Checks); i++ {
			dst = append(dst, ' ')
			dst = t.appendCheck(dst, &t.VM.Checks[i])
		}
		dst = append(dst, '\n')
	}
	if t.VM.Truncated {
		dst = append(dst, "(trace truncated)\n"...)
	}
	return dst
}

var checkOps = map[bytecode.OpCode]string{
	bytecode.OpEq: "==", bytecode.OpNeq: "!=", bytecode.OpGt: ">", bytecode.OpGte: ">=",
	bytecode.OpLt: "<", bytecode.OpLte: "<=", bytecode.OpIn: "in", bytecode.OpNotIn: "not in",
	bytecode.OpContains: "contains", bytecode.OpNotContains: "not contains",
	bytecode.OpRetains: "retains", bytecode.OpNotRetains: "not retains",
	bytecode.OpVagueContains: "vague_contains", bytecode.OpSubsetOf: "subset_of",
	bytecode.OpSupersetOf: "superset_of", bytecode.OpStartsWith: "starts_with",
	bytecode.OpEndsWith: "ends_with", bytecode.OpMatches: "matches",
}

func (t *Trace) appendCheck(dst []byte, c *vm.Check) []byte {
	var rs *bytecode.CompiledRuleset
	if t.d != nil {
		rs = t.d.prog.rs
	}
	switch c.Op {
	case bytecode.OpLoadVar:
		// A boolean input may be negated by the condition, so its value is
		// shown without a pass or fail mark.
		return append(dst, t.operandText(rs, c.IP+bytecode.InstrSize, c.A)...)
	case bytecode.OpIsNull, bytecode.OpIsNotNull:
		a, _ := t.operand(rs, c.IP, c.A)
		dst = append(dst, a...)
		if c.Op == bytecode.OpIsNull {
			dst = append(dst, " is null"...)
		} else {
			dst = append(dst, " is not null"...)
		}
	case bytecode.OpBetweenCC, bytecode.OpBetweenOO, bytecode.OpBetweenCO, bytecode.OpBetweenOC:
		hi, start := t.operand(rs, c.IP, c.C)
		lo, start := t.operand(rs, start, c.B)
		val, _ := t.operand(rs, start, c.A)
		open, close := "[", "]"
		if c.Op == bytecode.OpBetweenOO || c.Op == bytecode.OpBetweenOC {
			open = "("
		}
		if c.Op == bytecode.OpBetweenOO || c.Op == bytecode.OpBetweenCO {
			close = ")"
		}
		dst = append(dst, val...)
		dst = append(dst, " between "...)
		dst = append(dst, open...)
		dst = append(dst, lo...)
		dst = append(dst, ", "...)
		dst = append(dst, hi...)
		dst = append(dst, close...)
	default:
		b, start := t.operand(rs, c.IP, c.B)
		a, _ := t.operand(rs, start, c.A)
		dst = append(dst, a...)
		dst = append(dst, ' ')
		dst = append(dst, checkOps[c.Op]...)
		dst = append(dst, ' ')
		dst = append(dst, b...)
	}
	if !c.Result {
		dst = append(dst, " NO"...)
	}
	return dst
}

// operand renders the operand whose code ends just before ip, with its value,
// and returns where that code starts (0 when it could not be decoded).
func (t *Trace) operand(rs *bytecode.CompiledRuleset, ip uint32, v vm.Value) (string, uint32) {
	text, start, ok := exprBefore(rs, ip, 4)
	if !ok {
		return "(" + t.valueText(v) + ")", 0
	}
	if (start+bytecode.InstrSize == ip && isLiteral(rs, start)) || isListLiteral(rs, start, ip) {
		return text, start
	}
	return group(text) + "(" + t.valueText(v) + ")", start
}

// operandText renders a boolean variable load as name=value.
func (t *Trace) operandText(rs *bytecode.CompiledRuleset, ip uint32, v vm.Value) string {
	text, _, ok := exprBefore(rs, ip, 1)
	if !ok {
		text = "?"
	}
	return text + "=" + t.valueText(v)
}

func (t *Trace) valueText(v vm.Value) string {
	switch v.Typ {
	case vm.TypeNull:
		return "null"
	case vm.TypeBool:
		return strconv.FormatBool(v.Bool)
	case vm.TypeNumber:
		return string(appendNumber(nil, v.Num))
	case vm.TypeString:
		if t.d != nil {
			return strconv.Quote(t.d.ev.String(v))
		}
		return `"?"`
	case vm.TypeList:
		return "[list]"
	case vm.TypeObject:
		return "{object}"
	case vm.TypeDecimal:
		return "decimal"
	}
	return "?"
}

func appendNumber(dst []byte, f float64) []byte {
	return strconv.AppendFloat(dst, f, 'g', -1, 64)
}

func instrAt(rs *bytecode.CompiledRuleset, ip uint32) (bytecode.OpCode, uint8, uint16, bool) {
	if rs == nil || ip+bytecode.InstrSize > uint32(len(rs.Instructions)) {
		return 0, 0, 0, false
	}
	var buf [bytecode.InstrSize]byte
	copy(buf[:], rs.Instructions[ip:ip+bytecode.InstrSize])
	op, flags, arg := bytecode.DecodeInstr(buf)
	return op, flags, arg, true
}

func isLiteral(rs *bytecode.CompiledRuleset, ip uint32) bool {
	op, flags, _, ok := instrAt(rs, ip)
	if !ok {
		return false
	}
	switch op {
	case bytecode.OpLoadNum, bytecode.OpLoadBool:
		return true
	case bytecode.OpLoadStr:
		return flags == 0
	}
	return false
}

// isListLiteral reports whether the code from start to ip is one list
// literal, which the compiler emits as a head and a length instruction.
func isListLiteral(rs *bytecode.CompiledRuleset, start, ip uint32) bool {
	op, flags, _, ok := instrAt(rs, start)
	return ok && op == bytecode.OpLoadNull && flags == intern.TypeList && start+2*bytecode.InstrSize == ip
}

var mathOps = map[bytecode.OpCode]string{
	bytecode.OpAdd: "+", bytecode.OpSub: "-", bytecode.OpMul: "*", bytecode.OpDiv: "/", bytecode.OpMod: "%",
}

var mathCalls = map[bytecode.OpCode]string{
	bytecode.OpAbs: "abs", bytecode.OpRound: "round", bytecode.OpFloor: "floor", bytecode.OpCeil: "ceil",
	bytecode.OpMin: "min", bytecode.OpMax: "max",
}

// exprBefore reconstructs source text for the expression whose last
// instruction is at ip-InstrSize. It understands loads, list literals and
// arithmetic, which covers what game rules usually compare, and gives up on
// anything else.
func exprBefore(rs *bytecode.CompiledRuleset, ip uint32, depth int) (string, uint32, bool) {
	if depth < 0 || ip < bytecode.InstrSize {
		return "", 0, false
	}
	at := ip - bytecode.InstrSize
	op, flags, arg, ok := instrAt(rs, at)
	if !ok {
		return "", 0, false
	}
	pool := rs.Constants
	switch op {
	case bytecode.OpLoadVar:
		return pool.GetString(arg), at, true
	case bytecode.OpLoadNum:
		return string(appendNumber(nil, pool.GetNumber(arg))), at, true
	case bytecode.OpLoadBool:
		return strconv.FormatBool(arg == 1), at, true
	case bytecode.OpLoadStr:
		if flags != 0 {
			return "[list]", at, true
		}
		return strconv.Quote(pool.GetString(arg)), at, true
	case bytecode.OpLoadNull:
		if flags != 0xFF || at < bytecode.InstrSize {
			return "null", at, flags == 0
		}
		head, hflags, idx, ok := instrAt(rs, at-bytecode.InstrSize)
		if !ok || head != bytecode.OpLoadNull || hflags != intern.TypeList {
			return "", 0, false
		}
		return listText(pool, idx, arg), at - bytecode.InstrSize, true
	case bytecode.OpAbs, bytecode.OpRound, bytecode.OpFloor, bytecode.OpCeil:
		inner, start, ok := exprBefore(rs, at, depth-1)
		if !ok {
			return "", 0, false
		}
		return mathCalls[op] + "(" + inner + ")", start, true
	case bytecode.OpMin, bytecode.OpMax:
		b, start, ok := exprBefore(rs, at, depth-1)
		if !ok {
			return "", 0, false
		}
		a, start, ok := exprBefore(rs, start, depth-1)
		if !ok {
			return "", 0, false
		}
		return mathCalls[op] + "(" + a + ", " + b + ")", start, true
	}
	if sym, isMath := mathOps[op]; isMath {
		b, start, ok := exprBefore(rs, at, depth-1)
		if !ok {
			return "", 0, false
		}
		a, start, ok := exprBefore(rs, start, depth-1)
		if !ok {
			return "", 0, false
		}
		return group(a) + " " + sym + " " + group(b), start, true
	}
	return "", 0, false
}

// group parenthesizes an operator expression so its operands read
// unambiguously. Calls, lists and quoted strings are left alone.
func group(s string) string {
	depth, quoted := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted:
			if c == '\\' {
				i++
			} else if c == '"' {
				quoted = false
			}
		case c == '"':
			quoted = true
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ' ' && depth == 0:
			return "(" + s + ")"
		}
	}
	return s
}

func listText(pool *intern.Pool, idx, n uint16) string {
	items := pool.GetList(idx, n)
	out := []byte{'['}
	for i, item := range items {
		if i > 0 {
			out = append(out, ", "...)
		}
		if i == 4 {
			out = append(out, "..."...)
			break
		}
		switch item.Typ {
		case intern.TypeNumber:
			out = appendNumber(out, item.Num)
		case intern.TypeString:
			out = strconv.AppendQuote(out, pool.GetString(item.Str))
		case intern.TypeBool:
			out = strconv.AppendBool(out, item.Bool)
		default:
			out = append(out, '?')
		}
	}
	return string(append(out, ']'))
}
