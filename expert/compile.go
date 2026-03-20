package expert

import (
	"fmt"
	"slices"
	"strings"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/internal/parseutil"
	gotreesitter "github.com/odvcencio/gotreesitter"
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
	source := parsed.Source
	lang := parsed.Lang
	root := parsed.Root
	var synthetic strings.Builder
	rules := make([]Rule, 0)
	byName := make(map[string]Rule)
	segmentDeps := make(map[string][]string)

	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		switch child.Type(lang) {
		case "const_declaration":
			synthetic.WriteString(nodeText(child, source))
			synthetic.WriteString("\n")
		case "segment_declaration":
			nameNode := child.ChildByFieldName("name", lang)
			condNode := child.ChildByFieldName("condition", lang)
			if nameNode != nil && condNode != nil {
				segmentDeps[nodeText(nameNode, source)] = collectFactDeps(condNode, source, lang)
			}
		}
	}

	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type(lang) != "expert_rule_declaration" {
			continue
		}

		rule, compiled, err := parseExpertRule(child, source, lang, segmentDeps)
		if err != nil {
			return nil, err
		}
		if _, exists := byName[rule.Name]; exists {
			return nil, fmt.Errorf("duplicate expert rule %q", rule.Name)
		}
		byName[rule.Name] = rule
		rules = append(rules, rule)
		synthetic.WriteString(compiled)
		synthetic.WriteString("\n")
	}

	p := &Program{
		segments:   base.Segments,
		rules:      rules,
		ruleByName: byName,
	}
	if len(rules) == 0 {
		return p, nil
	}

	compiled, err := arbiter.CompileFull([]byte(synthetic.String()))
	if err != nil {
		return nil, fmt.Errorf("compile expert program: %w", err)
	}
	p.ruleset = compiled.Ruleset
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

func parseExpertRule(n *gotreesitter.Node, source []byte, lang *gotreesitter.Language, segmentDeps map[string][]string) (Rule, string, error) {
	nameNode := n.ChildByFieldName("name", lang)
	whenNode := n.ChildByFieldName("condition", lang)
	actionNode := n.ChildByFieldName("action", lang)
	if nameNode == nil || whenNode == nil || actionNode == nil {
		return Rule{}, "", fmt.Errorf("expert rule missing name, condition, or action")
	}

	rule := Rule{
		Name: nodeText(nameNode, source),
	}
	if priNode := n.ChildByFieldName("priority", lang); priNode != nil {
		rule.Priority = parseutil.ParseInt(nodeText(priNode, source))
	}
	rule.NoLoop = n.ChildByFieldName("no_loop", lang) != nil
	rule.Stable = n.ChildByFieldName("stable", lang) != nil
	if groupNode := n.ChildByFieldName("activation_group", lang); groupNode != nil {
		if nameNode := groupNode.ChildByFieldName("name", lang); nameNode != nil {
			rule.Group = nodeText(nameNode, source)
		}
	}

	kindNode := actionNode.ChildByFieldName("kind", lang)
	targetNode := actionNode.ChildByFieldName("action_name", lang)
	if kindNode == nil || targetNode == nil {
		return Rule{}, "", fmt.Errorf("expert rule %s missing action kind or target", rule.Name)
	}
	switch nodeText(kindNode, source) {
	case string(ActionAssert):
		rule.Kind = ActionAssert
	case string(ActionEmit):
		rule.Kind = ActionEmit
	case string(ActionRetract):
		rule.Kind = ActionRetract
	case string(ActionModify):
		rule.Kind = ActionModify
	default:
		return Rule{}, "", fmt.Errorf("expert rule %s has unsupported action kind %q", rule.Name, nodeText(kindNode, source))
	}
	rule.Target = nodeText(targetNode, source)
	deps := make([]string, 0)
	if segNode := whenNode.ChildByFieldName("segment", lang); segNode != nil {
		deps = append(deps, segmentDeps[nodeText(segNode, source)]...)
	}
	whenSource, whenDeps, err := expertConditionSource(whenNode, source, lang)
	if err != nil {
		return Rule{}, "", fmt.Errorf("expert rule %s: %w", rule.Name, err)
	}
	deps = append(deps, whenDeps...)
	for i := 0; i < int(actionNode.NamedChildCount()); i++ {
		child := actionNode.NamedChild(i)
		switch child.Type(lang) {
		case "param_assignment":
			keyNode := child.ChildByFieldName("key", lang)
			if keyNode != nil && strings.HasPrefix(nodeText(keyNode, source), modifySetPrefix) {
				return Rule{}, "", fmt.Errorf("expert rule %s uses reserved param prefix %q", rule.Name, modifySetPrefix)
			}
			if valueNode := child.ChildByFieldName("value", lang); valueNode != nil {
				deps = append(deps, collectFactDeps(valueNode, source, lang)...)
			}
		case "expert_set_block":
			for j := 0; j < int(child.NamedChildCount()); j++ {
				setChild := child.NamedChild(j)
				if setChild.Type(lang) != "param_assignment" {
					continue
				}
				keyNode := setChild.ChildByFieldName("key", lang)
				if keyNode != nil && strings.HasPrefix(nodeText(keyNode, source), modifySetPrefix) {
					return Rule{}, "", fmt.Errorf("expert rule %s uses reserved set-field prefix %q", rule.Name, modifySetPrefix)
				}
				if valueNode := setChild.ChildByFieldName("value", lang); valueNode != nil {
					deps = append(deps, collectFactDeps(valueNode, source, lang)...)
				}
			}
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		switch child.Type(lang) {
		case "rule_requires":
			if prereqNode := child.ChildByFieldName("name", lang); prereqNode != nil {
				rule.Prereqs = append(rule.Prereqs, nodeText(prereqNode, source))
			}
		case "rule_excludes":
			if exclNode := child.ChildByFieldName("name", lang); exclNode != nil {
				rule.Excludes = append(rule.Excludes, nodeText(exclNode, source))
			}
		default:
			continue
		}
	}
	rule.FactDeps = uniqueStrings(deps)

	setBlock := actionNode.ChildByFieldName("set_block", lang)
	hasKey := false
	setCount := 0
	for i := 0; i < int(actionNode.NamedChildCount()); i++ {
		child := actionNode.NamedChild(i)
		if child.Type(lang) != "param_assignment" {
			continue
		}
		if keyNode := child.ChildByFieldName("key", lang); keyNode != nil && nodeText(keyNode, source) == "key" {
			hasKey = true
		}
	}
	if setBlock != nil {
		for i := 0; i < int(setBlock.NamedChildCount()); i++ {
			if setBlock.NamedChild(i).Type(lang) == "param_assignment" {
				setCount++
			}
		}
	}
	switch rule.Kind {
	case ActionRetract:
		if !hasKey {
			return Rule{}, "", fmt.Errorf("expert rule %s retract %s: key is required", rule.Name, rule.Target)
		}
		if setBlock != nil {
			return Rule{}, "", fmt.Errorf("expert rule %s retract %s: set block is not allowed", rule.Name, rule.Target)
		}
	case ActionModify:
		if !hasKey {
			return Rule{}, "", fmt.Errorf("expert rule %s modify %s: key is required", rule.Name, rule.Target)
		}
		if setBlock == nil || setCount == 0 {
			return Rule{}, "", fmt.Errorf("expert rule %s modify %s: non-empty set block is required", rule.Name, rule.Target)
		}
	default:
		if setBlock != nil {
			return Rule{}, "", fmt.Errorf("expert rule %s %s %s: set block is only valid for modify", rule.Name, rule.Kind, rule.Target)
		}
	}

	var synthetic strings.Builder
	synthetic.WriteString("rule ")
	synthetic.WriteString(rule.Name)
	if priNode := n.ChildByFieldName("priority", lang); priNode != nil {
		synthetic.WriteString(" priority ")
		synthetic.WriteString(nodeText(priNode, source))
	}
	synthetic.WriteString(" {\n")
	if ksNode := n.ChildByFieldName("kill_switch", lang); ksNode != nil {
		synthetic.WriteString(nodeText(ksNode, source))
		synthetic.WriteString("\n")
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(lang) != "rule_requires" {
			continue
		}
		synthetic.WriteString(nodeText(child, source))
		synthetic.WriteString("\n")
	}
	synthetic.WriteString(whenSource)
	synthetic.WriteString("\n")
	synthetic.WriteString("then ")
	synthetic.WriteString(rule.Target)
	synthetic.WriteString(" {\n")
	for i := 0; i < int(actionNode.NamedChildCount()); i++ {
		child := actionNode.NamedChild(i)
		switch child.Type(lang) {
		case "param_assignment":
			synthetic.WriteString(nodeText(child, source))
			synthetic.WriteString("\n")
		case "expert_set_block":
			for j := 0; j < int(child.NamedChildCount()); j++ {
				setChild := child.NamedChild(j)
				if setChild.Type(lang) != "param_assignment" {
					continue
				}
				keyNode := setChild.ChildByFieldName("key", lang)
				valueNode := setChild.ChildByFieldName("value", lang)
				if keyNode == nil || valueNode == nil {
					continue
				}
				synthetic.WriteString(modifySetPrefix)
				synthetic.WriteString(nodeText(keyNode, source))
				synthetic.WriteString(": ")
				synthetic.WriteString(nodeText(valueNode, source))
				synthetic.WriteString("\n")
			}
		}
	}
	synthetic.WriteString("}\n")
	if rolloutNode := n.ChildByFieldName("rollout", lang); rolloutNode != nil {
		synthetic.WriteString(nodeText(rolloutNode, source))
		synthetic.WriteString("\n")
	}
	synthetic.WriteString("}\n")

	return rule, synthetic.String(), nil
}

type bindingRef struct {
	Name   string
	Source string
}

func expertConditionSource(whenNode *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (string, []string, error) {
	if whenNode == nil {
		return "", nil, fmt.Errorf("missing expert when block")
	}
	letExprs, letDeps := collectLetBindings(whenNode, source, lang)
	if exprNode := whenNode.ChildByFieldName("expr", lang); exprNode != nil {
		deps := append(collectFactDeps(exprNode, source, lang), letDeps...)
		return nodeText(whenNode, source), uniqueStrings(deps), nil
	}

	bindingsNode := whenNode.ChildByFieldName("bindings", lang)
	if bindingsNode == nil {
		return "", nil, fmt.Errorf("expert binding clause is missing")
	}
	whereNode := bindingsNode.ChildByFieldName("where", lang)
	if whereNode == nil {
		return "", nil, fmt.Errorf("expert binding clause is missing where block")
	}
	bodyExpr := whereNode.ChildByFieldName("expr", lang)
	if bodyExpr == nil {
		return "", nil, fmt.Errorf("expert binding clause is missing where expression")
	}

	bindings := make([]bindingRef, 0)
	deps := append(collectFactDeps(bodyExpr, source, lang), letDeps...)
	for i := 0; i < int(bindingsNode.NamedChildCount()); i++ {
		child := bindingsNode.NamedChild(i)
		if child.Type(lang) != "expert_binding" {
			continue
		}
		nameNode := child.ChildByFieldName("name", lang)
		sourceNode := child.ChildByFieldName("source", lang)
		if nameNode == nil || sourceNode == nil {
			return "", nil, fmt.Errorf("expert binding is missing name or source")
		}
		bindings = append(bindings, bindingRef{
			Name:   nodeText(nameNode, source),
			Source: nodeText(sourceNode, source),
		})
		deps = append(deps, collectFactDeps(sourceNode, source, lang)...)
	}
	if len(bindings) == 0 {
		return "", nil, fmt.Errorf("expert binding clause requires at least one bind")
	}

	expr := nodeText(bodyExpr, source)
	for i := len(bindings) - 1; i >= 0; i-- {
		expr = fmt.Sprintf("any %s in %s { %s }", bindings[i].Name, bindings[i].Source, expr)
	}

	var out strings.Builder
	out.WriteString("when ")
	if segNode := whenNode.ChildByFieldName("segment", lang); segNode != nil {
		out.WriteString("segment ")
		out.WriteString(nodeText(segNode, source))
		out.WriteByte(' ')
	}
	out.WriteString("{ ")
	for _, letExpr := range letExprs {
		out.WriteString(letExpr)
		out.WriteByte(' ')
	}
	out.WriteString(expr)
	out.WriteString(" }")
	return out.String(), uniqueStrings(deps), nil
}

func collectLetBindings(whenNode *gotreesitter.Node, source []byte, lang *gotreesitter.Language) ([]string, []string) {
	if whenNode == nil {
		return nil, nil
	}
	letExprs := make([]string, 0)
	letDeps := make([]string, 0)
	for i := 0; i < int(whenNode.NamedChildCount()); i++ {
		child := whenNode.NamedChild(i)
		if child.Type(lang) != "let_binding" {
			continue
		}
		letExprs = append(letExprs, nodeText(child, source))
		if valueNode := child.ChildByFieldName("value", lang); valueNode != nil {
			letDeps = append(letDeps, collectFactDeps(valueNode, source, lang)...)
		}
	}
	return letExprs, letDeps
}

func nodeText(n *gotreesitter.Node, source []byte) string {
	return string(source[n.StartByte():n.EndByte()])
}

func collectFactDeps(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) []string {
	if root == nil {
		return nil
	}

	var deps []string
	var walk func(*gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "member_expr" {
			text := nodeText(n, source)
			if strings.HasPrefix(text, "facts.") {
				parts := strings.Split(text, ".")
				if len(parts) >= 2 && parts[1] != "" {
					deps = append(deps, parts[1])
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return uniqueStrings(deps)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || slices.Contains(out, value) {
			continue
		}
		out = append(out, value)
	}
	return out
}
