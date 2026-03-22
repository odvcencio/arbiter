package expert

import (
	"fmt"
	"strings"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/ir"
)

// ActionKind is the kind of expert action a rule performs.
type ActionKind string

const (
	ActionAssert  ActionKind = "assert"
	ActionEmit    ActionKind = "emit"
	ActionRetract ActionKind = "retract"
	ActionModify  ActionKind = "modify"
)

const modifySetPrefix = "__expert_set__"

// Rule describes one compiled expert rule.
type Rule struct {
	Name     string
	Priority int
	Kind     ActionKind
	Target   string
	Prereqs  []string
	Excludes []string
	FactDeps []string
	NoLoop   bool
	Stable   bool
	PerFact  bool
	Group    string
}

// Program is a compiled expert-rules program.
type Program struct {
	ruleset    *compiler.CompiledRuleset
	segments   *govern.SegmentSet
	rules      []Rule
	ruleByName map[string]Rule
}

// Compile parses .arb source and extracts only expert rules into an expert program.
func Compile(source []byte) (*Program, error) {
	parsed, err := arbiter.ParseSource(source)
	if err != nil {
		return nil, err
	}
	base, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, err
	}
	return CompileParsed(parsed, base)
}

// CompileParsed extracts expert rules from a previously parsed source.
func CompileParsed(parsed *arbiter.ParsedSource, base *arbiter.CompileResult) (*Program, error) {
	if parsed == nil {
		return nil, fmt.Errorf("nil parsed source")
	}
	if base == nil {
		var err error
		base, err = arbiter.CompileFullParsed(parsed)
		if err != nil {
			return nil, err
		}
	}
	if base.Program == nil {
		return nil, fmt.Errorf("nil lowered program")
	}

	segmentDeps := make(map[string][]string, len(base.Program.Segments))
	for i := range base.Program.Segments {
		segment := &base.Program.Segments[i]
		segmentDeps[segment.Name] = ir.FactDeps(base.Program, segment.Condition)
	}

	rules := make([]Rule, 0, len(base.Program.Expert))
	byName := make(map[string]Rule, len(base.Program.Expert))
	syntheticRules := make([]ir.Rule, 0, len(base.Program.Expert))

	for i := range base.Program.Expert {
		expertRule := &base.Program.Expert[i]
		rule, synthetic, err := lowerExpertRule(base.Program, expertRule, segmentDeps)
		if err != nil {
			return nil, err
		}
		if _, exists := byName[rule.Name]; exists {
			return nil, fmt.Errorf("duplicate expert rule %q", rule.Name)
		}
		byName[rule.Name] = rule
		rules = append(rules, rule)
		syntheticRules = append(syntheticRules, synthetic)
	}

	p := &Program{
		segments:   base.Segments,
		rules:      rules,
		ruleByName: byName,
	}
	if len(syntheticRules) == 0 {
		return p, nil
	}

	syntheticProgram := &ir.Program{
		Consts: base.Program.Consts,
		Exprs:  base.Program.Exprs,
		Rules:  syntheticRules,
	}
	syntheticProgram.RebuildIndexes()

	compiled, err := compiler.CompileIR(syntheticProgram)
	if err != nil {
		return nil, fmt.Errorf("compile expert program: %w", err)
	}
	p.ruleset = compiled
	return p, nil
}

// CompileFile resolves includes and compiles expert rules from a root .arb file.
func CompileFile(path string) (*Program, error) {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return nil, err
	}
	base, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	program, err := CompileParsed(parsed, base)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	return program, nil
}

// Rules returns the compiled expert rules in source order.
func (p *Program) Rules() []Rule {
	if p == nil || len(p.rules) == 0 {
		return nil
	}
	out := make([]Rule, len(p.rules))
	copy(out, p.rules)
	return out
}

func (p *Program) lookupRule(name string) (Rule, bool) {
	if p == nil {
		return Rule{}, false
	}
	rule, ok := p.ruleByName[name]
	return rule, ok
}

func lowerExpertRule(program *ir.Program, expertRule *ir.ExpertRule, segmentDeps map[string][]string) (Rule, ir.Rule, error) {
	if expertRule == nil {
		return Rule{}, ir.Rule{}, fmt.Errorf("nil expert rule")
	}
	rule := Rule{
		Name:     expertRule.Name,
		Priority: int(expertRule.Priority),
		Kind:     ActionKind(expertRule.ActionKind),
		Target:   expertRule.Target,
		Prereqs:  append([]string(nil), expertRule.Prereqs...),
		Excludes: append([]string(nil), expertRule.Excludes...),
		NoLoop:   expertRule.NoLoop,
		Stable:   expertRule.Stable,
		PerFact:  expertRule.PerFact,
		Group:    expertRule.ActivationGroup,
	}

	deps := make([]string, 0)
	if expertRule.Segment != "" {
		deps = append(deps, segmentDeps[expertRule.Segment]...)
	}
	for _, binding := range expertRule.Lets {
		deps = append(deps, ir.FactDeps(program, binding.Value)...)
	}
	if expertRule.HasCondition {
		deps = append(deps, ir.FactDeps(program, expertRule.Condition)...)
	}
	for _, param := range expertRule.Params {
		if strings.HasPrefix(param.Key, modifySetPrefix) {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s uses reserved param prefix %q", rule.Name, modifySetPrefix)
		}
		deps = append(deps, ir.FactDeps(program, param.Value)...)
	}
	for _, param := range expertRule.SetParams {
		if strings.HasPrefix(param.Key, modifySetPrefix) {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s uses reserved set-field prefix %q", rule.Name, modifySetPrefix)
		}
		deps = append(deps, ir.FactDeps(program, param.Value)...)
	}
	rule.FactDeps = uniqueStrings(deps)

	hasKey := false
	for _, param := range expertRule.Params {
		if param.Key == "key" {
			hasKey = true
			break
		}
	}
	switch rule.Kind {
	case ActionRetract:
		if !hasKey {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s retract %s: key is required", rule.Name, rule.Target)
		}
		if len(expertRule.SetParams) > 0 {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s retract %s: set block is not allowed", rule.Name, rule.Target)
		}
	case ActionModify:
		if !hasKey {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s modify %s: key is required", rule.Name, rule.Target)
		}
		if len(expertRule.SetParams) == 0 {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s modify %s: non-empty set block is required", rule.Name, rule.Target)
		}
	default:
		if len(expertRule.SetParams) > 0 {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s %s %s: set block is only valid for modify", rule.Name, rule.Kind, rule.Target)
		}
	}

	action := ir.Action{
		Name:   rule.Target,
		Params: append([]ir.ActionParam(nil), expertRule.Params...),
	}
	if len(expertRule.SetParams) > 0 {
		for _, param := range expertRule.SetParams {
			action.Params = append(action.Params, ir.ActionParam{
				Key:   modifySetPrefix + param.Key,
				Span:  param.Span,
				Value: param.Value,
			})
		}
	}

	synthetic := ir.Rule{
		Name:         expertRule.Name,
		Span:         expertRule.Span,
		Priority:     expertRule.Priority,
		KillSwitch:   expertRule.KillSwitch,
		Prereqs:      append([]string(nil), expertRule.Prereqs...),
		Excludes:     append([]string(nil), expertRule.Excludes...),
		Segment:      expertRule.Segment,
		Lets:         append([]ir.LetBinding(nil), expertRule.Lets...),
		Condition:    expertRule.Condition,
		HasCondition: expertRule.HasCondition,
		Action:       action,
		Rollout:      expertRule.Rollout,
	}

	return rule, synthetic, nil
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
