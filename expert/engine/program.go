package engine

import (
	"fmt"
	"strings"
	"time"

	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/ir"
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
	Name            string             `json:"name"`
	Tags            []string           `json:"tags"`
	Priority        int                `json:"priority"`
	Kind            ActionKind         `json:"kind"`
	Target          string             `json:"target"`
	Segment         string             `json:"segment"`
	Condition       string             `json:"condition"`
	KillSwitch      ir.KillSwitchState `json:"kill_switch"`
	Prereqs         []string           `json:"prereqs"`
	Excludes        []string           `json:"excludes"`
	FactDeps        []string           `json:"fact_deps"`
	NoLoop          bool               `json:"no_loop"`
	Stable          bool               `json:"stable"`
	PerFact         bool               `json:"per_fact"`
	Group           string             `json:"group"`
	ForDuration     time.Duration      `json:"for_duration"`
	HasFor          bool               `json:"has_for"`
	WithinDuration  time.Duration      `json:"within_duration"`
	HasWithin       bool               `json:"has_within"`
	StableCycles    int                `json:"stable_cycles"`
	HasStableCycles bool               `json:"has_stable_cycles"`
	Cooldown        time.Duration      `json:"cooldown"`
	HasCooldown     bool               `json:"has_cooldown"`
	Debounce        time.Duration      `json:"debounce"`
	HasDebounce     bool               `json:"has_debounce"`
}

func (r Rule) hasTemporal() bool {
	return r.HasFor || r.HasWithin || r.HasStableCycles || r.HasCooldown || r.HasDebounce
}

// Program is a compiled expert-rules program.
type Program struct {
	ruleset        *compiler.CompiledRuleset
	segments       *govern.SegmentSet
	rules          []Rule
	ruleByName     map[string]Rule
	factSchemas    map[string]*ir.FactSchema
	outcomeSchemas map[string]*ir.OutcomeSchema
}

// NewProgram lowers the expert rules of a lowered IR program into a runnable
// expert program. segments may be nil when the source declares none.
func NewProgram(prog *ir.Program, segments *govern.SegmentSet) (*Program, error) {
	if prog == nil {
		return nil, fmt.Errorf("nil lowered program")
	}
	segmentDeps := make(map[string][]string, len(prog.Segments))
	for i := range prog.Segments {
		segment := &prog.Segments[i]
		segmentDeps[segment.Name] = ir.FactDeps(prog, segment.Condition)
	}

	rules := make([]Rule, 0, len(prog.Expert))
	byName := make(map[string]Rule, len(prog.Expert))
	syntheticRules := make([]ir.Rule, 0, len(prog.Expert))

	for i := range prog.Expert {
		expertRule := &prog.Expert[i]
		rule, synthetic, err := lowerExpertRule(prog, expertRule, segmentDeps)
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
		segments:       segments,
		rules:          rules,
		ruleByName:     byName,
		factSchemas:    make(map[string]*ir.FactSchema, len(prog.FactSchemas)),
		outcomeSchemas: make(map[string]*ir.OutcomeSchema, len(prog.OutcomeSchemas)),
	}
	for i := range prog.FactSchemas {
		schema := &prog.FactSchemas[i]
		p.factSchemas[schema.Name] = schema
	}
	for i := range prog.OutcomeSchemas {
		schema := &prog.OutcomeSchemas[i]
		p.outcomeSchemas[schema.Name] = schema
	}
	if len(syntheticRules) == 0 {
		return p, nil
	}

	syntheticProgram := &ir.Program{
		Consts: prog.Consts,
		Exprs:  prog.Exprs,
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
		Name:       expertRule.Name,
		Tags:       append([]string(nil), expertRule.Tags...),
		Priority:   int(expertRule.Priority),
		Kind:       ActionKind(expertRule.ActionKind),
		Target:     expertRule.Target,
		Segment:    expertRule.Segment,
		KillSwitch: expertRule.KillSwitch,
		Prereqs:    append([]string(nil), expertRule.Prereqs...),
		Excludes:   append([]string(nil), expertRule.Excludes...),
		NoLoop:     expertRule.NoLoop,
		Stable:     expertRule.Stable,
		PerFact:    expertRule.PerFact,
		Group:      expertRule.ActivationGroup,
	}
	if expertRule.HasCondition {
		rule.Condition = ir.RenderExpr(program, expertRule.Condition)
	}
	if expertRule.PerFact && hasTemporalRule(expertRule) {
		return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s: temporal operators are not supported on per_fact rules", rule.Name)
	}
	if err := validateTemporalModifiers(rule.Name, expertRule); err != nil {
		return Rule{}, ir.Rule{}, err
	}
	if expertRule.ForDuration != nil {
		duration, err := compileTemporalDuration(expertRule.ForDuration)
		if err != nil {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s: %w", rule.Name, err)
		}
		rule.ForDuration = duration
		rule.HasFor = true
	}
	if expertRule.WithinDuration != nil {
		duration, err := compileTemporalDuration(expertRule.WithinDuration)
		if err != nil {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s: %w", rule.Name, err)
		}
		rule.WithinDuration = duration
		rule.HasWithin = true
	}
	if expertRule.HasStableCycles {
		if expertRule.StableCycles <= 0 {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s: stable_for cycles must be greater than zero", rule.Name)
		}
		rule.StableCycles = expertRule.StableCycles
		rule.HasStableCycles = true
	}
	if expertRule.CooldownDuration != nil {
		duration, err := compileTemporalDuration(expertRule.CooldownDuration)
		if err != nil {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s: %w", rule.Name, err)
		}
		rule.Cooldown = duration
		rule.HasCooldown = true
	}
	if expertRule.DebounceDuration != nil {
		duration, err := compileTemporalDuration(expertRule.DebounceDuration)
		if err != nil {
			return Rule{}, ir.Rule{}, fmt.Errorf("expert rule %s: %w", rule.Name, err)
		}
		rule.Debounce = duration
		rule.HasDebounce = true
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
		Tags:         append([]string(nil), expertRule.Tags...),
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

func hasTemporalRule(rule *ir.ExpertRule) bool {
	if rule == nil {
		return false
	}
	return rule.ForDuration != nil ||
		rule.WithinDuration != nil ||
		rule.HasStableCycles ||
		rule.CooldownDuration != nil ||
		rule.DebounceDuration != nil
}

func validateTemporalModifiers(ruleName string, rule *ir.ExpertRule) error {
	if rule == nil {
		return nil
	}
	waiting := make([]string, 0, 4)
	if rule.ForDuration != nil {
		waiting = append(waiting, "for")
	}
	if rule.WithinDuration != nil {
		waiting = append(waiting, "within")
	}
	if rule.DebounceDuration != nil {
		waiting = append(waiting, "debounce")
	}
	if rule.HasStableCycles {
		waiting = append(waiting, "stable_for")
	}
	if len(waiting) <= 1 {
		return nil
	}
	first := waiting[0]
	second := waiting[1]
	reason := `both impose timing constraints on when the rule fires`
	switch {
	case (first == "for" && second == "stable_for") || (first == "stable_for" && second == "for"):
		reason = `"for" is time-based, "stable_for" is cycle-based`
	case (first == "within" && second == "stable_for") || (first == "stable_for" && second == "within"):
		reason = `"within" is time-based, "stable_for" is cycle-based`
	case (first == "debounce" && second == "stable_for") || (first == "stable_for" && second == "debounce"):
		reason = `"debounce" is time-based, "stable_for" is cycle-based`
	case (first == "for" && second == "debounce") || (first == "debounce" && second == "for"):
		reason = `both define overlapping wait periods`
	case (first == "within" && second == "debounce") || (first == "debounce" && second == "within"):
		reason = `both define conflicting deadlines`
	}
	return fmt.Errorf(`expert rule %s: cannot combine %q and %q - %s`, ruleName, first, second, reason)
}

func compileTemporalDuration(duration *ir.Duration) (time.Duration, error) {
	if duration == nil {
		return 0, fmt.Errorf("missing duration")
	}
	if duration.Value <= 0 {
		return 0, fmt.Errorf("duration must be greater than zero")
	}
	var scale time.Duration
	switch duration.Unit {
	case "ms":
		scale = time.Millisecond
	case "s":
		scale = time.Second
	case "m":
		scale = time.Minute
	case "hr":
		scale = time.Hour
	case "d":
		scale = 24 * time.Hour
	default:
		return 0, fmt.Errorf("unsupported duration unit %q", duration.Unit)
	}
	return time.Duration(duration.Value * float64(scale)), nil
}
