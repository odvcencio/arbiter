package gameai

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"m31labs.dev/arbiter/bytecode"
	"m31labs.dev/arbiter/vm"
)

// Selection says how a decision turns its matched rules into one choice. The
// rules are the candidates: each rule that matches (or whose otherwise action
// runs) is one candidate, in declaration order.
type Selection struct {
	mode selMode
	key  string
}

type selMode uint8

const (
	selFirst selMode = iota
	selBest
	selWeighted
)

// First picks the first candidate in declaration order, like a strategy or a
// priority list.
func First() Selection { return Selection{mode: selFirst} }

// Best picks the candidate whose action parameter scoreKey is highest (utility
// scoring). Ties go to the candidate declared first. A candidate whose score
// is missing, not a number, or NaN is skipped. Scores may be negative.
func Best(scoreKey string) Selection { return Selection{mode: selBest, key: scoreKey} }

// Weighted draws one candidate with probability proportional to its action
// parameter weightKey, from the seed passed to Decide. Weights that are
// missing, not numbers, zero, negative, NaN or infinite take no part. The draw
// is u*total with u = Unit(seed), compared against the running sum in
// declaration order, so it is the same on every target.
func Weighted(weightKey string) Selection { return Selection{mode: selWeighted, key: weightKey} }

// String describes the selection, for example "best(score)".
func (s Selection) String() string {
	switch s.mode {
	case selBest:
		return "best(" + s.key + ")"
	case selWeighted:
		return "weighted(" + s.key + ")"
	}
	return "first"
}

// Decider evaluates one decision: the rules with one tag (or every rule, for
// the empty tag) in declaration order. Rules with `kill_switch on` are left
// out. A Decider reuses its buffers, so it is not safe for concurrent use and
// the Choice it returns is valid only until its next evaluation.
type Decider struct {
	prog  *Program
	tag   string
	ev    *vm.PreparedEvaluator
	out   vm.Matches
	reads []string
}

// NewDecider prepares the rules tagged tag. An empty tag selects every rule.
func NewDecider(p *Program, tag string) (*Decider, error) {
	if p == nil {
		return nil, fmt.Errorf("gameai: nil program")
	}
	rs := p.rs
	var tags []string
	if tag != "" {
		tags = []string{tag}
	}
	var indices []int
	for i, rule := range rs.Rules {
		if rule.KillSwitch.Enabled() || !rs.RuleMatchesTags(rule, tags) {
			continue
		}
		indices = append(indices, i)
	}
	if tag != "" && len(indices) == 0 {
		return nil, fmt.Errorf("gameai: no active rules tagged %q", tag)
	}
	ev, err := vm.NewPreparedEvaluatorRules(rs, p.pool, indices)
	if err != nil {
		return nil, fmt.Errorf("gameai: %w", err)
	}
	return &Decider{prog: p, tag: tag, ev: ev, reads: contextReads(rs, indices)}, nil
}

// Tag returns the tag the decider was prepared for.
func (d *Decider) Tag() string { return d.tag }

// Program returns the program the decider runs.
func (d *Decider) Program() *Program { return d.prog }

// Rules returns the indices in Program.Rules of the decider's rules.
func (d *Decider) Rules() []int { return d.ev.Rules() }

// Reads returns the variable names the decision's rules read from the data
// context (let bindings, quantifier variables and table columns excluded),
// sorted. A bound Struct must provide all of them.
func (d *Decider) Reads() []string { return d.reads }

// Eval evaluates the rules and returns every match in declaration order. The
// slice is valid until the decider's next evaluation. tr may be nil.
func (d *Decider) Eval(ctx vm.DataContext, tr *Trace) ([]vm.Match, error) {
	if err := d.eval(ctx, tr); err != nil {
		return nil, err
	}
	if tr != nil {
		tr.fill(d, First(), 0, -1)
	}
	return d.out.List, nil
}

// Decide evaluates the rules and selects one candidate. seed is used only by
// Weighted. tr, when non-nil, receives a trace; pass nil in normal play, which
// costs nothing. When no candidate qualifies the Choice is not OK and the
// error is nil.
func (d *Decider) Decide(ctx vm.DataContext, sel Selection, seed uint64, tr *Trace) (Choice, error) {
	if err := d.eval(ctx, tr); err != nil {
		return Choice{}, err
	}
	list := d.out.List
	chosen, score := -1, 0.0
	var total, draw float64
	switch sel.mode {
	case selFirst:
		if len(list) > 0 {
			chosen = 0
		}
	case selBest:
		for i := range list {
			s, ok := numberParam(&list[i], sel.key)
			if !ok || s != s {
				continue
			}
			if chosen < 0 || s > score {
				chosen, score = i, s
			}
		}
	case selWeighted:
		for i := range list {
			if w, ok := weightParam(&list[i], sel.key); ok {
				total = float64(total + w)
			}
		}
		if math.IsInf(total, 0) {
			return Choice{}, fmt.Errorf("gameai: decision %q: weights overflow float64", d.tag)
		}
		if total > 0 {
			// float64(...) keeps the product from fusing with anything; the
			// draw is then exact IEEE arithmetic on every target.
			draw = float64(Unit(seed) * total)
			var sum float64
			for i := range list {
				w, ok := weightParam(&list[i], sel.key)
				if !ok {
					continue
				}
				sum = float64(sum + w)
				chosen, score = i, w
				if draw < sum {
					break
				}
			}
		}
	}
	if tr != nil {
		tr.fill(d, sel, seed, chosen)
		tr.Total, tr.Draw = total, draw
	}
	if chosen < 0 {
		return Choice{}, nil
	}
	return Choice{m: &list[chosen], d: d, score: score}, nil
}

func (d *Decider) eval(ctx vm.DataContext, tr *Trace) error {
	if b, ok := ctx.(interface{ boundProgram() *Program }); ok && b.boundProgram() != d.prog {
		return fmt.Errorf("gameai: decision %q: context is bound to a different program", d.tag)
	}
	d.out.Trace = nil
	if tr != nil {
		tr.VM.Reset()
		d.out.Trace = &tr.VM
	}
	if err := d.ev.EvalInto(ctx, &d.out); err != nil {
		return fmt.Errorf("gameai: decision %q: %w", d.tag, err)
	}
	return nil
}

func numberParam(m *vm.Match, key string) (float64, bool) {
	v, ok := m.Param(key)
	if !ok || v.Typ != vm.TypeNumber {
		return 0, false
	}
	return v.Num, true
}

func weightParam(m *vm.Match, key string) (float64, bool) {
	w, ok := numberParam(m, key)
	if !ok || !(w > 0) || math.IsInf(w, 0) {
		return 0, false
	}
	return w, true
}

// Choice is the candidate a decision selected. It refers to the Decider's
// buffers and is valid until the Decider's next evaluation; copy out what you
// need (strings it returns stay valid).
type Choice struct {
	m     *vm.Match
	d     *Decider
	score float64
}

// OK reports whether a candidate was selected.
func (c Choice) OK() bool { return c.m != nil }

// Rule returns the selected rule's name.
func (c Choice) Rule() string {
	if c.m == nil {
		return ""
	}
	return c.m.Name
}

// RuleIndex returns the selected rule's index in Program.Rules, or -1.
func (c Choice) RuleIndex() int {
	if c.m == nil {
		return -1
	}
	return c.m.Rule
}

// Action returns the selected action's name, such as "Intent".
func (c Choice) Action() string {
	if c.m == nil {
		return ""
	}
	return c.m.Action
}

// Fallback reports whether the selected candidate is a rule's otherwise action.
func (c Choice) Fallback() bool { return c.m != nil && c.m.Fallback }

// Score returns the score (Best) or weight (Weighted) of the selected
// candidate, and 0 for First.
func (c Choice) Score() float64 { return c.score }

// Value returns the action parameter named key.
func (c Choice) Value(key string) (vm.Value, bool) {
	if c.m == nil {
		return vm.Value{}, false
	}
	return c.m.Param(key)
}

// Number returns the numeric action parameter named key, or 0.
func (c Choice) Number(key string) float64 {
	v, ok := c.Value(key)
	if !ok || v.Typ != vm.TypeNumber {
		return 0
	}
	return v.Num
}

// Text returns the string action parameter named key, or "".
func (c Choice) Text(key string) string {
	v, ok := c.Value(key)
	if !ok {
		return ""
	}
	return c.d.ev.String(v)
}

// Bool returns the boolean action parameter named key, or false.
func (c Choice) Bool(key string) bool {
	v, ok := c.Value(key)
	return ok && v.AsBool()
}

// Params returns the action parameters in declaration order. Resolve string
// values with Text or Program.Strings.
func (c Choice) Params() []vm.Param {
	if c.m == nil {
		return nil
	}
	return c.m.Params
}

// contextReads lists the variable names the given rules read from the data
// context: every OpLoadVar name in their conditions, actions and lookups,
// minus names the rules bind themselves.
func contextReads(rs *bytecode.CompiledRuleset, rules []int) []string {
	type span struct{ off, n uint32 }
	var spans []span
	for _, i := range rules {
		rule := rs.Rules[i]
		spans = append(spans, span{rule.ConditionOff, rule.ConditionLen})
		actions := []uint16{rule.ActionIdx}
		if rule.FallbackIdx != 0 {
			actions = append(actions, rule.FallbackIdx)
		}
		for _, a := range actions {
			if int(a) >= len(rs.Actions) {
				continue
			}
			for _, p := range rs.Actions[a].Params {
				spans = append(spans, span{p.ValueOff, p.ValueLen})
			}
		}
	}
	strs := rs.Constants.Strings()
	loads := map[string]bool{}
	locals := map[string]bool{}
	seenLookup := map[uint16]bool{}
	for len(spans) > 0 {
		sp := spans[len(spans)-1]
		spans = spans[:len(spans)-1]
		end := sp.off + sp.n
		if end > uint32(len(rs.Instructions)) {
			end = uint32(len(rs.Instructions))
		}
		for ip := sp.off; ip+bytecode.InstrSize <= end; ip += bytecode.InstrSize {
			var buf [bytecode.InstrSize]byte
			copy(buf[:], rs.Instructions[ip:ip+bytecode.InstrSize])
			op, _, arg := bytecode.DecodeInstr(buf)
			name := ""
			if int(arg) < len(strs) {
				name = strs[arg]
			}
			switch op {
			case bytecode.OpLoadVar:
				loads[name] = true
			case bytecode.OpSetLocal, bytecode.OpIterBegin, bytecode.OpAggBegin:
				locals[name] = true
			case bytecode.OpLookup:
				if seenLookup[arg] || int(arg) >= len(rs.LookupMetas) {
					continue
				}
				seenLookup[arg] = true
				meta := rs.LookupMetas[arg]
				spans = append(spans, span{meta.WhereOff, meta.WhereLen}, span{meta.ElseOff, meta.ElseLen})
				if int(meta.TableIdx) < len(rs.Tables) {
					for _, col := range rs.Tables[meta.TableIdx].Columns {
						locals[col] = true
					}
				}
			}
		}
	}
	var out []string
	for name := range loads {
		base, _, _ := strings.Cut(name, ".")
		if locals[name] || locals[base] || name == "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
