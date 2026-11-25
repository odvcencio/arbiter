package arbiter

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// Arbiter's documented operator precedence (docs/language/grammar.ebnf,
// README "Operators"): `not` binds tighter than `and`, `and` binds tighter
// than `or`, and `and` and `or` associate left to right. The tests in this
// file check conditions without parentheses against that rule, both through
// the public Compile and Eval path and through the shape of the parse tree.

// TestBooleanPrecedenceAuditRows covers three conditions that an earlier
// parse table grouped wrongly, so the rule fired when it should not, or the
// other way round.
func TestBooleanPrecedenceAuditRows(t *testing.T) {
	cases := []struct {
		cond string
		vars map[string]any
		want bool
	}{
		// (a and (not b)) and c: false because c is false.
		{"a and not b and c", map[string]any{"a": true, "b": false, "c": false}, false},
		// (a and b) or (c and d): true because c and d are true.
		{"a and b or c and d", map[string]any{"a": false, "b": false, "c": true, "d": true}, true},
		// (a or (not b)) or c: true because c is true.
		{"a or not b or c", map[string]any{"a": false, "b": true, "c": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.cond, func(t *testing.T) {
			src := "rule R { when { " + tc.cond + " } then A {} }"
			if got := evalRule(t, src, tc.vars); got != tc.want {
				t.Errorf("%q with %v: fired=%v, want %v (tree groups it as %s, want %s)",
					tc.cond, tc.vars, got, tc.want, parsedBoolGrouping(t, tc.cond), refBoolGrouping(t, tc.cond))
			}
		})
	}
}

// TestBooleanPrecedenceMatchesReference compiles every condition over the
// variables a, b, c and d that uses `and`, `or` and `not`, no parentheses,
// and at most four operators. It evaluates each one for all 16 assignments
// and compares the result with a reference evaluator that applies the
// documented precedence.
func TestBooleanPrecedenceMatchesReference(t *testing.T) {
	exprs := enumerateBoolExprs("abcd", 4)
	const chunk = 1000
	pairs, bad := 0, 0
	badExprs := map[string]bool{}
	var examples []string
	for start := 0; start < len(exprs); start += chunk {
		end := min(start+chunk, len(exprs))
		var src strings.Builder
		for i := start; i < end; i++ {
			fmt.Fprintf(&src, "rule R%d { when { %s } then A {} }\n", i, exprs[i])
		}
		prog, err := Compile([]byte(src.String()))
		if err != nil {
			t.Fatalf("compile expressions %d-%d: %v", start, end-1, err)
		}
		for mask := 0; mask < 16; mask++ {
			vars := map[string]any{}
			env := map[byte]bool{}
			for bit, name := range "abcd" {
				v := mask&(1<<bit) != 0
				vars[string(name)] = v
				env[byte(name)] = v
			}
			matched, err := Eval(prog, DataFromMap(vars, prog))
			if err != nil {
				t.Fatalf("eval expressions %d-%d with %v: %v", start, end-1, vars, err)
			}
			fired := make(map[string]bool, len(matched))
			for _, m := range matched {
				if !m.Fallback {
					fired[m.Name] = true
				}
			}
			for i := start; i < end; i++ {
				pairs++
				want := exprs[i].eval(env)
				if got := fired[fmt.Sprintf("R%d", i)]; got != want {
					bad++
					badExprs[exprs[i].String()] = true
					if len(examples) < 12 {
						examples = append(examples, fmt.Sprintf("%q with %s: fired=%v, want %v", exprs[i], fmtAssignment(env), got, want))
					}
				}
			}
		}
	}
	t.Logf("checked %d expressions, %d expression-assignment pairs", len(exprs), pairs)
	if bad > 0 {
		t.Errorf("%d of %d expression-assignment pairs disagree with the documented precedence (%d of %d expressions); first cases:\n  %s",
			bad, pairs, len(badExprs), len(exprs), strings.Join(examples, "\n  "))
	}
}

// TestBooleanGroupingMatchesReference checks the parse tree shape, which also
// covers associativity: `a and b and c` evaluates the same either way, but
// the grammar promises left association. One distinct variable per operand
// position keeps the shapes readable.
func TestBooleanGroupingMatchesReference(t *testing.T) {
	shapes := enumerateBoolShapes(4)
	var bad []string
	for _, e := range shapes {
		cond := e.String()
		got := parsedBoolGrouping(t, cond)
		want := e.grouping()
		if got != want {
			bad = append(bad, fmt.Sprintf("%q: tree %s, want %s", cond, got, want))
		}
	}
	t.Logf("checked %d operator shapes", len(shapes))
	if len(bad) > 0 {
		t.Errorf("%d of %d operator shapes group differently from the documented precedence:\n  %s",
			len(bad), len(shapes), strings.Join(bad, "\n  "))
	}
}

// boolOperand is one operand: a variable with zero or more `not` prefixes.
type boolOperand struct {
	nots int
	name byte
}

// boolExpr is a condition without parentheses: operands joined by `and` or
// `or`. ops[i] sits between operands[i] and operands[i+1].
type boolExpr struct {
	operands []boolOperand
	ops      []string
}

func (e boolExpr) String() string {
	var b strings.Builder
	for i, o := range e.operands {
		if i > 0 {
			b.WriteString(" " + e.ops[i-1] + " ")
		}
		b.WriteString(strings.Repeat("not ", o.nots))
		b.WriteByte(o.name)
	}
	return b.String()
}

// refNode is the reference parse of a boolExpr: `not` binds tighter than
// `and`, `and` tighter than `or`, and both binary operators associate left.
type refNode struct {
	op          string // "and", "or", "not", or "" for a variable
	left, right *refNode
	name        byte
}

func (e boolExpr) tree() *refNode {
	operand := func(i int) *refNode {
		n := &refNode{name: e.operands[i].name}
		for k := 0; k < e.operands[i].nots; k++ {
			n = &refNode{op: "not", left: n}
		}
		return n
	}
	// Fold `and` runs first, then fold the `or` terms from the left.
	var terms []*refNode
	cur := operand(0)
	for i, op := range e.ops {
		next := operand(i + 1)
		if op == "and" {
			cur = &refNode{op: "and", left: cur, right: next}
			continue
		}
		terms = append(terms, cur)
		cur = next
	}
	terms = append(terms, cur)
	root := terms[0]
	for _, t := range terms[1:] {
		root = &refNode{op: "or", left: root, right: t}
	}
	return root
}

func (n *refNode) eval(env map[byte]bool) bool {
	switch n.op {
	case "and":
		return n.left.eval(env) && n.right.eval(env)
	case "or":
		return n.left.eval(env) || n.right.eval(env)
	case "not":
		return !n.left.eval(env)
	}
	return env[n.name]
}

func (n *refNode) String() string {
	switch n.op {
	case "and", "or":
		return "(" + n.left.String() + " " + n.op + " " + n.right.String() + ")"
	case "not":
		return "(not " + n.left.String() + ")"
	}
	return string(n.name)
}

func (e boolExpr) eval(env map[byte]bool) bool { return e.tree().eval(env) }
func (e boolExpr) grouping() string            { return e.tree().String() }

// enumerateBoolExprs returns every condition with at most maxOps operators
// (binary operators plus `not` prefixes), each operand drawn from vars.
func enumerateBoolExprs(vars string, maxOps int) []boolExpr {
	var out []boolExpr
	for _, shape := range enumerateBoolShapes(maxOps) {
		n := len(shape.operands)
		total := 1
		for i := 0; i < n; i++ {
			total *= len(vars)
		}
		for code := 0; code < total; code++ {
			e := boolExpr{operands: make([]boolOperand, n), ops: shape.ops}
			c := code
			for i := 0; i < n; i++ {
				e.operands[i] = boolOperand{nots: shape.operands[i].nots, name: vars[c%len(vars)]}
				c /= len(vars)
			}
			out = append(out, e)
		}
	}
	return out
}

// enumerateBoolShapes returns every operator layout with at most maxOps
// operators. Operand i is named by the i-th letter, so each shape appears
// once.
func enumerateBoolShapes(maxOps int) []boolExpr {
	var out []boolExpr
	for binary := 0; binary <= maxOps; binary++ {
		operands := binary + 1
		for opBits := 0; opBits < 1<<binary; opBits++ {
			ops := make([]string, binary)
			for i := range ops {
				ops[i] = "and"
				if opBits&(1<<i) != 0 {
					ops[i] = "or"
				}
			}
			for _, nots := range distributeNots(maxOps-binary, operands) {
				e := boolExpr{operands: make([]boolOperand, operands), ops: ops}
				for i := range e.operands {
					e.operands[i] = boolOperand{nots: nots[i], name: byte('a' + i)}
				}
				out = append(out, e)
			}
		}
	}
	return out
}

// distributeNots returns every way to place at most budget `not` prefixes
// on slots operands.
func distributeNots(budget, slots int) [][]int {
	if slots == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for k := 0; k <= budget; k++ {
		for _, rest := range distributeNots(budget-k, slots-1) {
			out = append(out, append([]int{k}, rest...))
		}
	}
	return out
}

func fmtAssignment(env map[byte]bool) string {
	keys := make([]int, 0, len(env))
	for k := range env {
		keys = append(keys, int(k))
	}
	sort.Ints(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%c=%v", k, env[byte(k)])
	}
	return strings.Join(parts, " ")
}

// refBoolGrouping parses cond with the reference precedence. It only
// accepts the `and`, `or`, `not` and identifier tokens that the tests use.
func refBoolGrouping(t *testing.T, cond string) string {
	t.Helper()
	var e boolExpr
	nots := 0
	for _, tok := range strings.Fields(cond) {
		switch tok {
		case "not":
			nots++
		case "and", "or":
			e.ops = append(e.ops, tok)
		default:
			if len(tok) != 1 {
				t.Fatalf("refBoolGrouping: unsupported token %q", tok)
			}
			e.operands = append(e.operands, boolOperand{nots: nots, name: tok[0]})
			nots = 0
		}
	}
	return e.grouping()
}

// parsedBoolGrouping parses cond inside a rule with the embedded parse table
// and renders the condition's tree in the same form as refNode.String.
func parsedBoolGrouping(t *testing.T, cond string) string {
	t.Helper()
	lang, err := GetLanguage()
	if err != nil {
		t.Fatalf("GetLanguage: %v", err)
	}
	src := []byte("rule R { when { " + cond + " } then A {} }")
	tree, err := gotreesitter.NewParser(lang).Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", cond, err)
	}
	root := tree.RootNode()
	if root.HasError() {
		return "ERROR " + root.SExpr(lang)
	}
	rule := root.NamedChild(0)
	when := rule.ChildByFieldName("condition", lang)
	if when == nil {
		return "no condition: " + root.SExpr(lang)
	}
	return renderBoolTree(when.ChildByFieldName("expr", lang), src, lang)
}

func renderBoolTree(n *gotreesitter.Node, src []byte, lang *gotreesitter.Language) string {
	if n == nil {
		return "<nil>"
	}
	switch n.Type(lang) {
	case "and_expr", "or_expr":
		op := strings.TrimSuffix(n.Type(lang), "_expr")
		return "(" + renderBoolTree(n.ChildByFieldName("left", lang), src, lang) + " " + op + " " +
			renderBoolTree(n.ChildByFieldName("right", lang), src, lang) + ")"
	case "not_expr":
		return "(not " + renderBoolTree(n.ChildByFieldName("operand", lang), src, lang) + ")"
	case "identifier":
		return string(src[n.StartByte():n.EndByte()])
	}
	return "<" + n.Type(lang) + ">"
}
