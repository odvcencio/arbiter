package ir

import (
	"fmt"
	"strings"

	"github.com/odvcencio/arbiter/internal/parseutil"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// Lower converts a parsed Arbiter CST into the shared in-process IR.
func Lower(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (*Program, error) {
	if root == nil {
		return nil, fmt.Errorf("nil root node")
	}
	if lang == nil {
		return nil, fmt.Errorf("nil language")
	}
	if root.HasError() {
		return nil, fmt.Errorf("parse errors in arbiter source")
	}

	l := lowerer{
		source:     source,
		lang:       lang,
		program:    &Program{},
		constNames: make(map[string]struct{}),
	}

	l.collectConstNames(root)
	if err := l.lowerSourceFile(root); err != nil {
		return nil, err
	}
	l.program.rebuildIndexes()
	return l.program, nil
}

type lowerer struct {
	source []byte
	lang   *gotreesitter.Language

	program    *Program
	constNames map[string]struct{}
}

type scope struct {
	parent *scope
	names  map[string]struct{}
}

func newScope(parent *scope) *scope {
	return &scope{
		parent: parent,
		names:  make(map[string]struct{}),
	}
}

func (s *scope) define(name string) {
	if s == nil || name == "" {
		return
	}
	s.names[name] = struct{}{}
}

func (s *scope) contains(name string) bool {
	for cur := s; cur != nil; cur = cur.parent {
		if _, ok := cur.names[name]; ok {
			return true
		}
	}
	return false
}

func (l *lowerer) collectConstNames(root *gotreesitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type(l.lang) != "const_declaration" {
			continue
		}
		nameNode := child.ChildByFieldName("name", l.lang)
		if nameNode == nil {
			continue
		}
		l.constNames[l.text(nameNode)] = struct{}{}
	}
}

func (l *lowerer) lowerSourceFile(root *gotreesitter.Node) error {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		switch child.Type(l.lang) {
		case "const_declaration":
			l.program.Consts = append(l.program.Consts, l.lowerConst(child))
		case "feature_declaration":
			l.program.Features = append(l.program.Features, l.lowerFeature(child))
		case "fact_declaration":
			l.program.FactSchemas = append(l.program.FactSchemas, l.lowerFactSchema(child))
		case "outcome_declaration":
			l.program.OutcomeSchemas = append(l.program.OutcomeSchemas, l.lowerOutcomeSchema(child))
		case "segment_declaration":
			segment, err := l.lowerSegment(child)
			if err != nil {
				return err
			}
			l.program.Segments = append(l.program.Segments, segment)
		case "rule_declaration":
			rule, err := l.lowerRule(child)
			if err != nil {
				return err
			}
			l.program.Rules = append(l.program.Rules, rule)
		case "flag_declaration":
			flag, err := l.lowerFlag(child)
			if err != nil {
				return err
			}
			l.program.Flags = append(l.program.Flags, flag)
		case "expert_rule_declaration":
			rule, err := l.lowerExpertRule(child)
			if err != nil {
				return err
			}
			l.program.Expert = append(l.program.Expert, rule)
		case "arbiter_declaration":
			arbiter, err := l.lowerArbiter(child)
			if err != nil {
				return err
			}
			l.program.Arbiters = append(l.program.Arbiters, arbiter)
		}
	}
	return nil
}

func (l *lowerer) lowerConst(n *gotreesitter.Node) Const {
	nameNode := n.ChildByFieldName("name", l.lang)
	valueNode := n.ChildByFieldName("value", l.lang)
	valueID := l.lowerExpr(valueNode, nil)
	return Const{
		Name:  l.text(nameNode),
		Span:  spanForNode(n),
		Value: valueID,
	}
}

func (l *lowerer) lowerFeature(n *gotreesitter.Node) Feature {
	feature := Feature{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		feature.Name = l.text(nameNode)
	}
	if sourceNode := n.ChildByFieldName("source", l.lang); sourceNode != nil {
		feature.Source = parseutil.StripQuotes(l.text(sourceNode))
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "field_declaration" {
			continue
		}
		field := FeatureField{
			Span: spanForNode(child),
		}
		if nameNode := child.ChildByFieldName("name", l.lang); nameNode != nil {
			field.Name = l.text(nameNode)
		}
		if typeNode := child.ChildByFieldName("type", l.lang); typeNode != nil {
			field.Type = l.text(typeNode)
		}
		feature.Fields = append(feature.Fields, field)
	}
	return feature
}

func (l *lowerer) lowerFactSchema(n *gotreesitter.Node) FactSchema {
	schema := FactSchema{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		schema.Name = l.text(nameNode)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "schema_field_declaration" {
			continue
		}
		schema.Fields = append(schema.Fields, l.lowerSchemaField(child))
	}
	return schema
}

func (l *lowerer) lowerOutcomeSchema(n *gotreesitter.Node) OutcomeSchema {
	schema := OutcomeSchema{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		schema.Name = l.text(nameNode)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "schema_field_declaration" {
			continue
		}
		schema.Fields = append(schema.Fields, l.lowerSchemaField(child))
	}
	return schema
}

func (l *lowerer) lowerSchemaField(n *gotreesitter.Node) SchemaField {
	field := SchemaField{
		Span:     spanForNode(n),
		Required: n.ChildByFieldName("optional", l.lang) == nil,
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		field.Name = l.text(nameNode)
	}
	if typeNode := n.ChildByFieldName("type", l.lang); typeNode != nil {
		field.Type = parseSchemaFieldType(l.text(typeNode))
	}
	return field
}

func (l *lowerer) lowerSegment(n *gotreesitter.Node) (Segment, error) {
	nameNode := n.ChildByFieldName("name", l.lang)
	conditionNode := n.ChildByFieldName("condition", l.lang)
	if nameNode == nil || conditionNode == nil {
		return Segment{}, fmt.Errorf("segment missing name or condition")
	}
	return Segment{
		Name:      l.text(nameNode),
		Span:      spanForNode(n),
		Condition: l.lowerExpr(conditionNode, nil),
	}, nil
}

func (l *lowerer) lowerRule(n *gotreesitter.Node) (Rule, error) {
	rule := Rule{
		Span: spanForNode(n),
	}

	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		rule.Name = l.text(nameNode)
	}
	if priNode := n.ChildByFieldName("priority", l.lang); priNode != nil {
		rule.Priority = int32(parseutil.ParseInt(l.text(priNode)))
	}
	rule.KillSwitch = n.ChildByFieldName("kill_switch", l.lang) != nil

	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		switch child.Type(l.lang) {
		case "rule_requires":
			if refNode := child.ChildByFieldName("name", l.lang); refNode != nil {
				rule.Prereqs = append(rule.Prereqs, l.text(refNode))
			}
		case "rule_excludes":
			if refNode := child.ChildByFieldName("name", l.lang); refNode != nil {
				rule.Excludes = append(rule.Excludes, l.text(refNode))
			}
		}
	}

	whenNode := n.ChildByFieldName("condition", l.lang)
	if whenNode != nil {
		if segNode := whenNode.ChildByFieldName("segment", l.lang); segNode != nil {
			rule.Segment = l.text(segNode)
		}

		ruleScope := newScope(nil)
		for i := 0; i < int(whenNode.NamedChildCount()); i++ {
			child := whenNode.NamedChild(i)
			if child.Type(l.lang) != "let_binding" {
				continue
			}
			nameNode := child.ChildByFieldName("name", l.lang)
			valueNode := child.ChildByFieldName("value", l.lang)
			if nameNode == nil || valueNode == nil {
				continue
			}
			binding := LetBinding{
				Name:  l.text(nameNode),
				Span:  spanForNode(child),
				Value: l.lowerExpr(valueNode, ruleScope),
			}
			rule.Lets = append(rule.Lets, binding)
			ruleScope.define(binding.Name)
		}

		if exprNode := whenNode.ChildByFieldName("expr", l.lang); exprNode != nil {
			rule.Condition = l.lowerExpr(exprNode, ruleScope)
			rule.HasCondition = true
		}
	}

	if actionNode := n.ChildByFieldName("action", l.lang); actionNode != nil {
		actionScope := newScope(nil)
		for _, binding := range rule.Lets {
			actionScope.define(binding.Name)
		}
		rule.Action = l.lowerAction(actionNode, actionScope)
	}
	if fallbackNode := n.ChildByFieldName("fallback", l.lang); fallbackNode != nil {
		actionScope := newScope(nil)
		for _, binding := range rule.Lets {
			actionScope.define(binding.Name)
		}
		action := l.lowerAction(fallbackNode, actionScope)
		rule.Fallback = &action
	}
	if rolloutNode := n.ChildByFieldName("rollout", l.lang); rolloutNode != nil {
		rollout, err := l.lowerRollout(rolloutNode)
		if err != nil {
			return Rule{}, fmt.Errorf("rule %s: %w", rule.Name, err)
		}
		rule.Rollout = rollout
	}

	return rule, nil
}

func (l *lowerer) lowerFlag(n *gotreesitter.Node) (Flag, error) {
	flag := Flag{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		flag.Name = l.text(nameNode)
	}
	if typeNode := n.ChildByFieldName("flag_type", l.lang); typeNode != nil {
		switch l.text(typeNode) {
		case "multivariate":
			flag.Type = FlagMultivariate
		default:
			flag.Type = FlagBoolean
		}
	} else {
		flag.Type = FlagBoolean
	}
	if defaultNode := n.ChildByFieldName("default_value", l.lang); defaultNode != nil {
		flag.Default = normalizedPrimaryText(l.text(defaultNode))
	}
	flag.KillSwitch = n.ChildByFieldName("kill_switch", l.lang) != nil

	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		switch child.Type(l.lang) {
		case "flag_metadata":
			keyNode := child.ChildByFieldName("key", l.lang)
			valueNode := child.ChildByFieldName("value", l.lang)
			if keyNode != nil && valueNode != nil {
				flag.Metadata = append(flag.Metadata, MetadataEntry{
					Key:   l.text(keyNode),
					Value: parseutil.StripQuotes(l.text(valueNode)),
					Span:  spanForNode(child),
				})
			}
		case "flag_requires":
			if nameNode := child.ChildByFieldName("flag_name", l.lang); nameNode != nil {
				flag.Requires = append(flag.Requires, l.text(nameNode))
			}
		case "flag_rule":
			rule, err := l.lowerFlagRule(child)
			if err != nil {
				return Flag{}, err
			}
			flag.Rules = append(flag.Rules, rule)
		case "variant_declaration":
			flag.Variants = append(flag.Variants, l.lowerVariant(child))
		case "defaults_block":
			flag.Defaults = l.lowerParamAssignments(child, nil)
		}
	}
	return flag, nil
}

func (l *lowerer) lowerFlagRule(n *gotreesitter.Node) (FlagRule, error) {
	rule := FlagRule{
		Span:   spanForNode(n),
		IsElse: hasLeadingLiteral(n, l.source, "else"),
	}

	conditionNode := n.ChildByFieldName("condition", l.lang)
	if segmentNode := n.ChildByFieldName("segment", l.lang); segmentNode != nil {
		rule.Segment = l.text(segmentNode)
	}
	if conditionNode != nil && conditionNode.Type(l.lang) == "identifier" && rule.Segment == "" {
		rule.Segment = l.text(conditionNode)
	}
	if exprNode := n.ChildByFieldName("expr", l.lang); exprNode != nil {
		rule.Condition = l.lowerExpr(exprNode, nil)
		rule.HasCondition = true
	}
	if rolloutNode := n.ChildByFieldName("rollout", l.lang); rolloutNode != nil {
		rollout, err := l.lowerRollout(rolloutNode)
		if err != nil {
			return FlagRule{}, err
		}
		rule.Rollout = rollout
	}
	if variantNode := n.ChildByFieldName("variant", l.lang); variantNode != nil {
		rule.Variant = normalizedPrimaryText(l.text(variantNode))
	}
	if splitNode := n.ChildByFieldName("split", l.lang); splitNode != nil {
		rule.Split = l.lowerFlagSplit(splitNode)
	}

	return rule, nil
}

func (l *lowerer) lowerFlagSplit(n *gotreesitter.Node) *FlagSplit {
	split := &FlagSplit{}
	if subjectNode := n.ChildByFieldName("subject", l.lang); subjectNode != nil {
		split.Subject = l.text(subjectNode)
		split.HasSubject = true
	}
	if namespaceNode := n.ChildByFieldName("namespace", l.lang); namespaceNode != nil {
		split.Namespace = parseutil.StripQuotes(l.text(namespaceNode))
		split.HasNamespace = true
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "flag_split_weight" {
			continue
		}
		variantNode := child.ChildByFieldName("variant", l.lang)
		weightNode := child.ChildByFieldName("weight", l.lang)
		if variantNode == nil || weightNode == nil {
			continue
		}
		split.Weights = append(split.Weights, FlagSplitWeight{
			Variant: parseutil.StripQuotes(l.text(variantNode)),
			Weight:  uint16(parseutil.ParseInt(l.text(weightNode))),
			Span:    spanForNode(child),
		})
	}
	return split
}

func (l *lowerer) lowerVariant(n *gotreesitter.Node) Variant {
	variant := Variant{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		variant.Name = parseutil.StripQuotes(l.text(nameNode))
	}
	variant.Params = l.lowerParamAssignments(n, nil)
	return variant
}

func (l *lowerer) lowerExpertRule(n *gotreesitter.Node) (ExpertRule, error) {
	rule := ExpertRule{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		rule.Name = l.text(nameNode)
	}
	rule.KillSwitch = n.ChildByFieldName("kill_switch", l.lang) != nil
	rule.NoLoop = n.ChildByFieldName("no_loop", l.lang) != nil
	rule.Stable = n.ChildByFieldName("stable", l.lang) != nil
	if groupNode := n.ChildByFieldName("activation_group", l.lang); groupNode != nil {
		if nameNode := groupNode.ChildByFieldName("name", l.lang); nameNode != nil {
			rule.ActivationGroup = l.text(nameNode)
		}
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		switch child.Type(l.lang) {
		case "expert_rule_priority":
			if priNode := child.ChildByFieldName("priority", l.lang); priNode != nil {
				rule.Priority = int32(parseutil.ParseInt(l.text(priNode)))
			}
		case "per_fact":
			rule.PerFact = true
		case "expert_rule_cooldown":
			if durationNode := child.ChildByFieldName("duration", l.lang); durationNode != nil {
				rule.CooldownDuration = l.lowerTemporalDuration(durationNode)
			}
		case "expert_rule_debounce":
			if durationNode := child.ChildByFieldName("duration", l.lang); durationNode != nil {
				rule.DebounceDuration = l.lowerTemporalDuration(durationNode)
			}
		case "rule_requires":
			if refNode := child.ChildByFieldName("name", l.lang); refNode != nil {
				rule.Prereqs = append(rule.Prereqs, l.text(refNode))
			}
		case "rule_excludes":
			if refNode := child.ChildByFieldName("name", l.lang); refNode != nil {
				rule.Excludes = append(rule.Excludes, l.text(refNode))
			}
		}
	}

	whenNode := n.ChildByFieldName("condition", l.lang)
	if whenNode != nil {
		segment, lets, cond, hasCond, forDuration, withinDuration, stableCycles, hasStableCycles, err := l.lowerExpertWhen(whenNode)
		if err != nil {
			return ExpertRule{}, err
		}
		rule.Segment = segment
		rule.Lets = lets
		rule.Condition = cond
		rule.HasCondition = hasCond
		rule.ForDuration = forDuration
		rule.WithinDuration = withinDuration
		rule.StableCycles = stableCycles
		rule.HasStableCycles = hasStableCycles
	}

	actionNode := n.ChildByFieldName("action", l.lang)
	if actionNode == nil {
		return ExpertRule{}, fmt.Errorf("expert rule missing action")
	}
	if kindNode := actionNode.ChildByFieldName("kind", l.lang); kindNode != nil {
		rule.ActionKind = ExpertActionKind(l.text(kindNode))
	}
	if targetNode := actionNode.ChildByFieldName("action_name", l.lang); targetNode != nil {
		rule.Target = l.text(targetNode)
	}
	actionScope := newScope(nil)
	for _, binding := range rule.Lets {
		actionScope.define(binding.Name)
	}
	rule.Params = l.lowerParamAssignments(actionNode, actionScope)
	if setBlock := actionNode.ChildByFieldName("set_block", l.lang); setBlock != nil {
		rule.SetParams = l.lowerParamAssignments(setBlock, actionScope)
	}
	if rolloutNode := n.ChildByFieldName("rollout", l.lang); rolloutNode != nil {
		rollout, err := l.lowerRollout(rolloutNode)
		if err != nil {
			return ExpertRule{}, err
		}
		rule.Rollout = rollout
	}
	return rule, nil
}

func (l *lowerer) lowerExpertWhen(n *gotreesitter.Node) (string, []LetBinding, ExprID, bool, *Duration, *Duration, int, bool, error) {
	segment := ""
	if segNode := n.ChildByFieldName("segment", l.lang); segNode != nil {
		segment = l.text(segNode)
	}

	baseScope := newScope(nil)
	var lets []LetBinding
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "let_binding" {
			continue
		}
		nameNode := child.ChildByFieldName("name", l.lang)
		valueNode := child.ChildByFieldName("value", l.lang)
		if nameNode == nil || valueNode == nil {
			continue
		}
		binding := LetBinding{
			Name:  l.text(nameNode),
			Span:  spanForNode(child),
			Value: l.lowerExpr(valueNode, baseScope),
		}
		lets = append(lets, binding)
		baseScope.define(binding.Name)
	}

	if exprNode := n.ChildByFieldName("expr", l.lang); exprNode != nil {
		forDuration, withinDuration, stableCycles, hasStableCycles := l.lowerWhenTemporal(n)
		return segment, lets, l.lowerExpr(exprNode, baseScope), true, forDuration, withinDuration, stableCycles, hasStableCycles, nil
	}
	bindingsNode := n.ChildByFieldName("bindings", l.lang)
	if bindingsNode == nil {
		return segment, lets, 0, false, nil, nil, 0, false, nil
	}
	bodyID, err := l.lowerExpertBindingClause(bindingsNode, baseScope)
	if err != nil {
		return "", nil, 0, false, nil, nil, 0, false, err
	}
	forDuration, withinDuration, stableCycles, hasStableCycles := l.lowerWhenTemporal(n)
	return segment, lets, bodyID, true, forDuration, withinDuration, stableCycles, hasStableCycles, nil
}

func (l *lowerer) lowerWhenTemporal(n *gotreesitter.Node) (*Duration, *Duration, int, bool) {
	if n == nil {
		return nil, nil, 0, false
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		switch child.Type(l.lang) {
		case "expert_when_for":
			if durationNode := child.ChildByFieldName("duration", l.lang); durationNode != nil {
				return l.lowerTemporalDuration(durationNode), nil, 0, false
			}
		case "expert_when_within":
			if durationNode := child.ChildByFieldName("duration", l.lang); durationNode != nil {
				return nil, l.lowerTemporalDuration(durationNode), 0, false
			}
		case "expert_when_stable_for":
			if cyclesNode := child.ChildByFieldName("cycles", l.lang); cyclesNode != nil {
				return nil, nil, parseutil.ParseInt(l.text(cyclesNode)), true
			}
		}
	}
	return nil, nil, 0, false
}

type expertBinding struct {
	name   string
	source *gotreesitter.Node
}

func (l *lowerer) lowerExpertBindingClause(n *gotreesitter.Node, baseScope *scope) (ExprID, error) {
	whereNode := n.ChildByFieldName("where", l.lang)
	if whereNode == nil {
		return 0, fmt.Errorf("expert binding clause is missing where block")
	}
	bodyExprNode := whereNode.ChildByFieldName("expr", l.lang)
	if bodyExprNode == nil {
		return 0, fmt.Errorf("expert binding clause is missing where expression")
	}

	var bindings []expertBinding
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "expert_binding" {
			continue
		}
		nameNode := child.ChildByFieldName("name", l.lang)
		sourceNode := child.ChildByFieldName("source", l.lang)
		if nameNode == nil || sourceNode == nil {
			return 0, fmt.Errorf("expert binding is missing name or source")
		}
		bindings = append(bindings, expertBinding{
			name:   l.text(nameNode),
			source: sourceNode,
		})
	}
	if len(bindings) == 0 {
		return 0, fmt.Errorf("expert binding clause requires at least one bind")
	}

	bodyScope := cloneScope(baseScope)
	for _, binding := range bindings {
		bodyScope.define(binding.name)
	}
	bodyID := l.lowerExpr(bodyExprNode, bodyScope)

	for i := len(bindings) - 1; i >= 0; i-- {
		sourceScope := cloneScope(baseScope)
		for j := 0; j < i; j++ {
			sourceScope.define(bindings[j].name)
		}
		bodyID = l.addExpr(Expr{
			Kind:           ExprQuantifier,
			Span:           spanForNode(bindings[i].source),
			QuantifierKind: QuantifierAny,
			VarName:        bindings[i].name,
			Collection:     l.lowerExpr(bindings[i].source, sourceScope),
			Body:           bodyID,
		})
	}
	return bodyID, nil
}

func (l *lowerer) lowerArbiter(n *gotreesitter.Node) (Arbiter, error) {
	arbiter := Arbiter{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("name", l.lang); nameNode != nil {
		arbiter.Name = l.text(nameNode)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		clause := ArbiterClause{
			Span: spanForNode(child),
		}
		switch child.Type(l.lang) {
		case "arbiter_poll_clause":
			clause.Kind = ArbiterPollClause
			if intervalNode := child.ChildByFieldName("interval", l.lang); intervalNode != nil {
				clause.Interval = l.text(intervalNode)
			}
		case "arbiter_stream_clause":
			clause.Kind = ArbiterStreamClause
			if targetNode := child.ChildByFieldName("target", l.lang); targetNode != nil {
				clause.Target = arbiterTarget(l.text(targetNode))
			}
		case "arbiter_schedule_clause":
			clause.Kind = ArbiterScheduleClause
			if exprNode := child.ChildByFieldName("expr", l.lang); exprNode != nil {
				clause.Expr = parseutil.StripQuotes(l.text(exprNode))
			}
			if targetNode := child.ChildByFieldName("target", l.lang); targetNode != nil {
				clause.Target = arbiterTarget(l.text(targetNode))
			}
		case "arbiter_source_clause":
			clause.Kind = ArbiterSourceClause
			if targetNode := child.ChildByFieldName("target", l.lang); targetNode != nil {
				clause.Target = arbiterTarget(l.text(targetNode))
			}
		case "arbiter_checkpoint_clause":
			clause.Kind = ArbiterCheckpointClause
			if targetNode := child.ChildByFieldName("target", l.lang); targetNode != nil {
				clause.Target = arbiterTarget(l.text(targetNode))
			}
		case "arbiter_handler_clause":
			clause.Kind = ArbiterHandlerClause
			if outcomeNode := child.ChildByFieldName("outcome", l.lang); outcomeNode != nil {
				clause.Outcome = l.text(outcomeNode)
			}
			if kindNode := child.ChildByFieldName("kind", l.lang); kindNode != nil {
				clause.Handler = l.text(kindNode)
			}
			if filterNode := child.ChildByFieldName("filter", l.lang); filterNode != nil {
				if exprNode := filterNode.ChildByFieldName("expr", l.lang); exprNode != nil {
					clause.Filter = l.lowerExpr(exprNode, nil)
					clause.HasFilter = true
				}
			}
			if targetNode := child.ChildByFieldName("target", l.lang); targetNode != nil {
				clause.Target = arbiterTarget(l.text(targetNode))
			}
		default:
			continue
		}
		arbiter.Clauses = append(arbiter.Clauses, clause)
	}
	return arbiter, nil
}

func (l *lowerer) lowerAction(n *gotreesitter.Node, scope *scope) Action {
	action := Action{
		Span: spanForNode(n),
	}
	if nameNode := n.ChildByFieldName("action_name", l.lang); nameNode != nil {
		action.Name = l.text(nameNode)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "param_assignment" {
			continue
		}
		keyNode := child.ChildByFieldName("key", l.lang)
		valueNode := child.ChildByFieldName("value", l.lang)
		if keyNode == nil || valueNode == nil {
			continue
		}
		action.Params = append(action.Params, ActionParam{
			Key:   l.text(keyNode),
			Span:  spanForNode(child),
			Value: l.lowerExpr(valueNode, scope),
		})
	}
	return action
}

func (l *lowerer) lowerParamAssignments(n *gotreesitter.Node, scope *scope) []ActionParam {
	var params []ActionParam
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) != "param_assignment" {
			continue
		}
		keyNode := child.ChildByFieldName("key", l.lang)
		valueNode := child.ChildByFieldName("value", l.lang)
		if keyNode == nil || valueNode == nil {
			continue
		}
		params = append(params, ActionParam{
			Key:   l.text(keyNode),
			Span:  spanForNode(child),
			Value: l.lowerExpr(valueNode, scope),
		})
	}
	return params
}

func (l *lowerer) lowerRollout(n *gotreesitter.Node) (*Rollout, error) {
	rollout := &Rollout{
		Span: spanForNode(n),
	}
	if valueNode := n.ChildByFieldName("value", l.lang); valueNode != nil {
		bps, err := parseutil.ParsePercentBps(l.text(valueNode))
		if err != nil {
			return nil, err
		}
		rollout.Bps = bps
		rollout.HasBps = true
	}
	if subjectNode := n.ChildByFieldName("subject", l.lang); subjectNode != nil {
		rollout.Subject = l.text(subjectNode)
		rollout.HasSubject = true
	}
	if namespaceNode := n.ChildByFieldName("namespace", l.lang); namespaceNode != nil {
		rollout.Namespace = parseutil.StripQuotes(l.text(namespaceNode))
		rollout.HasNamespace = true
	}
	return rollout, nil
}

func (l *lowerer) lowerExpr(n *gotreesitter.Node, scope *scope) ExprID {
	if n == nil {
		return l.addExpr(Expr{
			Kind: ExprNullLit,
			Span: Span{},
		})
	}

	switch n.Type(l.lang) {
	case "and_expr":
		return l.lowerBinary(n, scope, BinaryAnd)
	case "or_expr":
		return l.lowerBinary(n, scope, BinaryOr)
	case "not_expr":
		return l.addExpr(Expr{
			Kind:    ExprUnary,
			Span:    spanForNode(n),
			UnaryOp: UnaryNot,
			Operand: l.lowerExpr(n.ChildByFieldName("operand", l.lang), scope),
		})
	case "comparison_expr":
		return l.lowerBinary(n, scope, comparisonOp(n, l.source, l.lang))
	case "in_expr":
		return l.lowerBinary(n, scope, BinaryIn)
	case "not_in_expr":
		return l.lowerBinary(n, scope, BinaryNotIn)
	case "contains_expr":
		return l.lowerBinary(n, scope, BinaryContains)
	case "not_contains_expr":
		return l.lowerBinary(n, scope, BinaryNotContains)
	case "retains_expr":
		return l.lowerBinary(n, scope, BinaryRetains)
	case "not_retains_expr":
		return l.lowerBinary(n, scope, BinaryNotRetains)
	case "subset_of_expr":
		return l.lowerBinary(n, scope, BinarySubsetOf)
	case "superset_of_expr":
		return l.lowerBinary(n, scope, BinarySupersetOf)
	case "vague_contains_expr":
		return l.lowerBinary(n, scope, BinaryVagueContains)
	case "starts_with_expr":
		return l.lowerBinary(n, scope, BinaryStartsWith)
	case "ends_with_expr":
		return l.lowerBinary(n, scope, BinaryEndsWith)
	case "matches_expr":
		return l.lowerBinary(n, scope, BinaryMatches)
	case "math_expr":
		return l.lowerBinary(n, scope, mathOp(n, l.source, l.lang))
	case "between_expr":
		return l.addExpr(Expr{
			Kind:        ExprBetween,
			Span:        spanForNode(n),
			BetweenKind: betweenKind(n, l.source, l.lang),
			Value:       l.lowerExpr(n.ChildByFieldName("left", l.lang), scope),
			Low:         l.lowerExpr(n.ChildByFieldName("low", l.lang), scope),
			High:        l.lowerExpr(n.ChildByFieldName("high", l.lang), scope),
		})
	case "is_null_expr":
		return l.addExpr(Expr{
			Kind:    ExprUnary,
			Span:    spanForNode(n),
			UnaryOp: UnaryIsNull,
			Operand: l.lowerExpr(n.ChildByFieldName("left", l.lang), scope),
		})
	case "is_not_null_expr":
		return l.addExpr(Expr{
			Kind:    ExprUnary,
			Span:    spanForNode(n),
			UnaryOp: UnaryIsNotNull,
			Operand: l.lowerExpr(n.ChildByFieldName("left", l.lang), scope),
		})
	case "quantifier_expr":
		return l.lowerQuantifier(n, scope)
	case "join_expr", "join_expr_full", "join_expr_shorthand":
		return l.lowerJoinExpr(n, scope)
	case "aggregate_expr":
		return l.lowerAggregate(n, scope)
	case "member_expr":
		return l.addExpr(Expr{
			Kind: ExprVarRef,
			Span: spanForNode(n),
			Path: l.text(n),
		})
	case "identifier":
		name := l.text(n)
		switch {
		case scope != nil && scope.contains(name):
			return l.addExpr(Expr{
				Kind: ExprLocalRef,
				Span: spanForNode(n),
				Name: name,
			})
		case l.isConst(name):
			return l.addExpr(Expr{
				Kind: ExprConstRef,
				Span: spanForNode(n),
				Name: name,
			})
		case name == "true":
			return l.addExpr(Expr{
				Kind: ExprBoolLit,
				Span: spanForNode(n),
				Bool: true,
			})
		case name == "false":
			return l.addExpr(Expr{
				Kind: ExprBoolLit,
				Span: spanForNode(n),
				Bool: false,
			})
		case name == "null":
			return l.addExpr(Expr{
				Kind: ExprNullLit,
				Span: spanForNode(n),
			})
		default:
			return l.addExpr(Expr{
				Kind: ExprVarRef,
				Span: spanForNode(n),
				Path: name,
			})
		}
	case "number_literal":
		return l.addExpr(Expr{
			Kind:   ExprNumberLit,
			Span:   spanForNode(n),
			Number: parseutil.ParseFloat(l.text(n)),
		})
	case "timestamp_literal":
		return l.addExpr(Expr{
			Kind:   ExprTimestampLit,
			Span:   spanForNode(n),
			String: l.text(n),
		})
	case "decimal_literal":
		value, unit := parseQuantityLiteral(l.text(n))
		return l.addExpr(Expr{
			Kind:   ExprDecimalLit,
			Span:   spanForNode(n),
			String: strings.TrimSpace(strings.TrimSuffix(l.text(n), unit)),
			Number: value,
			Unit:   unit,
		})
	case "temporal_duration_literal":
		value, unit := parseTemporalDurationExpr(l.text(n))
		return l.addExpr(Expr{
			Kind:   ExprQuantityLit,
			Span:   spanForNode(n),
			Number: value,
			Unit:   unit,
		})
	case "quantity_literal":
		value, unit := parseQuantityLiteral(l.text(n))
		return l.addExpr(Expr{
			Kind:   ExprQuantityLit,
			Span:   spanForNode(n),
			Number: value,
			Unit:   unit,
		})
	case "call_expr":
		return l.lowerCallExpr(n, scope)
	case "string_literal":
		return l.addExpr(Expr{
			Kind:   ExprStringLit,
			Span:   spanForNode(n),
			String: parseutil.StripQuotes(l.text(n)),
		})
	case "bool_literal":
		return l.addExpr(Expr{
			Kind: ExprBoolLit,
			Span: spanForNode(n),
			Bool: l.text(n) == "true",
		})
	case "list_literal":
		expr := Expr{
			Kind: ExprListLit,
			Span: spanForNode(n),
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			expr.Elems = append(expr.Elems, l.lowerExpr(n.NamedChild(i), scope))
		}
		return l.addExpr(expr)
	case "secret_ref":
		refNode := n.ChildByFieldName("ref", l.lang)
		path := ""
		if refNode != nil {
			path = parseutil.StripQuotes(l.text(refNode))
		}
		return l.addExpr(Expr{
			Kind: ExprSecretRef,
			Span: spanForNode(n),
			Path: path,
		})
	case "paren_expr":
		return l.lowerExpr(n.ChildByFieldName("expr", l.lang), scope)
	default:
		if n.NamedChildCount() == 1 {
			return l.lowerExpr(n.NamedChild(0), scope)
		}
		return l.addExpr(Expr{
			Kind: ExprNullLit,
			Span: spanForNode(n),
		})
	}
}

func (l *lowerer) lowerCallExpr(n *gotreesitter.Node, scope *scope) ExprID {
	expr := Expr{
		Kind: ExprBuiltinCall,
		Span: spanForNode(n),
	}
	if fnNode := n.ChildByFieldName("function", l.lang); fnNode != nil {
		expr.FuncName = l.text(fnNode)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type(l.lang) == "identifier" && l.text(child) == expr.FuncName {
			continue
		}
		expr.Args = append(expr.Args, l.lowerExpr(child, scope))
	}
	return l.addExpr(expr)
}

func (l *lowerer) lowerBinary(n *gotreesitter.Node, scope *scope, op BinaryOpKind) ExprID {
	return l.addExpr(Expr{
		Kind:     ExprBinary,
		Span:     spanForNode(n),
		BinaryOp: op,
		Left:     l.lowerExpr(n.ChildByFieldName("left", l.lang), scope),
		Right:    l.lowerExpr(n.ChildByFieldName("right", l.lang), scope),
	})
}

func (l *lowerer) lowerQuantifier(n *gotreesitter.Node, scope *scope) ExprID {
	varName := ""
	if varNode := n.ChildByFieldName("var", l.lang); varNode != nil {
		varName = l.text(varNode)
	}
	bodyScope := newScope(scope)
	bodyScope.define(varName)
	return l.addExpr(Expr{
		Kind:           ExprQuantifier,
		Span:           spanForNode(n),
		QuantifierKind: quantifierKind(n, l.source, l.lang),
		VarName:        varName,
		Collection:     l.lowerExpr(n.ChildByFieldName("collection", l.lang), scope),
		Body:           l.lowerExpr(n.ChildByFieldName("body", l.lang), bodyScope),
	})
}

type joinBinding struct {
	alias    string
	factType string
}

func (l *lowerer) lowerJoinExpr(n *gotreesitter.Node, scope *scope) ExprID {
	if n != nil && n.Type(l.lang) == "join_expr" && n.NamedChildCount() == 1 {
		n = n.NamedChild(0)
	}
	bindings, includingSelf := l.lowerJoinBindings(n)
	if len(bindings) < 2 {
		return l.addExpr(Expr{Kind: ExprNullLit, Span: spanForNode(n)})
	}

	bodyScope := cloneScope(scope)
	for _, binding := range bindings {
		bodyScope.define(binding.alias)
	}

	predicateID, hasPredicate := l.lowerJoinPredicate(n, bindings, bodyScope)
	bodyNode := n.ChildByFieldName("body", l.lang)
	bodyID := l.lowerExpr(bodyNode, bodyScope)
	combined := bodyID
	if hasPredicate {
		combined = l.combineJoinExprs(predicateID, bodyID)
	}

	if !includingSelf {
		for i := 0; i < len(bindings); i++ {
			for j := i + 1; j < len(bindings); j++ {
				if bindings[i].factType != bindings[j].factType {
					continue
				}
				leftKey := l.addExpr(Expr{
					Kind: ExprVarRef,
					Span: spanForNode(n),
					Path: bindings[i].alias + ".key",
				})
				rightKey := l.addExpr(Expr{
					Kind: ExprVarRef,
					Span: spanForNode(n),
					Path: bindings[j].alias + ".key",
				})
				exclusion := l.addExpr(Expr{
					Kind:     ExprBinary,
					Span:     spanForNode(n),
					BinaryOp: BinaryNeq,
					Left:     leftKey,
					Right:    rightKey,
				})
				combined = l.combineJoinExprs(exclusion, combined)
			}
		}
	}

	for i := len(bindings) - 1; i >= 0; i-- {
		collection := l.addExpr(Expr{
			Kind: ExprVarRef,
			Span: spanForNode(n),
			Path: "facts." + bindings[i].factType,
		})
		combined = l.addExpr(Expr{
			Kind:           ExprQuantifier,
			Span:           spanForNode(n),
			QuantifierKind: QuantifierAny,
			VarName:        bindings[i].alias,
			Collection:     collection,
			Body:           combined,
		})
	}
	return combined
}

func (l *lowerer) lowerJoinBindings(n *gotreesitter.Node) ([]joinBinding, bool) {
	var bindings []joinBinding
	includingSelf := false
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		switch child.Type(l.lang) {
		case "join_binding":
			aliasNode := child.ChildByFieldName("alias", l.lang)
			factTypeNode := child.ChildByFieldName("fact_type", l.lang)
			if aliasNode == nil || factTypeNode == nil {
				continue
			}
			bindings = append(bindings, joinBinding{
				alias:    l.text(aliasNode),
				factType: l.text(factTypeNode),
			})
		case "join_including_self":
			includingSelf = true
		}
	}
	return bindings, includingSelf
}

func (l *lowerer) lowerJoinPredicate(n *gotreesitter.Node, bindings []joinBinding, scope *scope) (ExprID, bool) {
	if fieldNode := n.ChildByFieldName("field", l.lang); fieldNode != nil && len(bindings) == 2 {
		field := l.text(fieldNode)
		left := l.addExpr(Expr{
			Kind: ExprVarRef,
			Span: spanForNode(fieldNode),
			Path: bindings[0].alias + "." + field,
		})
		right := l.addExpr(Expr{
			Kind: ExprVarRef,
			Span: spanForNode(fieldNode),
			Path: bindings[1].alias + "." + field,
		})
		return l.addExpr(Expr{
			Kind:     ExprBinary,
			Span:     spanForNode(n),
			BinaryOp: BinaryEq,
			Left:     left,
			Right:    right,
		}), true
	}
	predicateNode := n.ChildByFieldName("predicate", l.lang)
	if predicateNode == nil {
		return 0, false
	}
	return l.lowerExpr(predicateNode, scope), true
}

func (l *lowerer) combineJoinExprs(left, right ExprID) ExprID {
	return l.addExpr(Expr{
		Kind:     ExprBinary,
		Span:     l.program.Expr(left).Span,
		BinaryOp: BinaryAnd,
		Left:     left,
		Right:    right,
	})
}

func (l *lowerer) lowerAggregate(n *gotreesitter.Node, scope *scope) ExprID {
	varName := ""
	if varNode := n.ChildByFieldName("var", l.lang); varNode != nil {
		varName = l.text(varNode)
	}
	bodyScope := newScope(scope)
	bodyScope.define(varName)
	expr := Expr{
		Kind:          ExprAggregate,
		Span:          spanForNode(n),
		AggregateKind: aggregateKind(n, l.source, l.lang),
		VarName:       varName,
		Collection:    l.lowerExpr(n.ChildByFieldName("collection", l.lang), scope),
	}
	if valueExprNode := n.ChildByFieldName("value_expr", l.lang); valueExprNode != nil {
		expr.ValueExpr = l.lowerExpr(valueExprNode, bodyScope)
		expr.HasValueExpr = true
	}
	return l.addExpr(expr)
}

func (l *lowerer) addExpr(expr Expr) ExprID {
	id := ExprID(len(l.program.Exprs))
	l.program.Exprs = append(l.program.Exprs, expr)
	return id
}

func cloneScope(s *scope) *scope {
	if s == nil {
		return nil
	}
	out := cloneScope(s.parent)
	next := newScope(out)
	for name := range s.names {
		next.names[name] = struct{}{}
	}
	return next
}

func (l *lowerer) text(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}
	return string(l.source[n.StartByte():n.EndByte()])
}

func (l *lowerer) isConst(name string) bool {
	_, ok := l.constNames[name]
	return ok
}

func normalizeSchemaTypeName(name string) string {
	switch name {
	case "bool":
		return "boolean"
	default:
		return name
	}
}

func parseSchemaFieldType(text string) FieldType {
	text = normalizeSchemaTypeName(text)
	if strings.HasPrefix(text, "number<") && strings.HasSuffix(text, ">") {
		return FieldType{
			Base:      "number",
			Dimension: text[len("number<") : len(text)-1],
		}
	}
	if strings.HasPrefix(text, "decimal<") && strings.HasSuffix(text, ">") {
		return FieldType{
			Base:      "decimal",
			Dimension: text[len("decimal<") : len(text)-1],
		}
	}
	return FieldType{Base: text}
}

func parseQuantityLiteral(text string) (float64, string) {
	text = strings.TrimSpace(text)
	for i, r := range text {
		if r == ' ' || r == '\t' {
			value := parseutil.ParseFloat(strings.TrimSpace(text[:i]))
			unit := strings.TrimSpace(text[i+1:])
			return value, unit
		}
	}
	return parseutil.ParseFloat(text), ""
}

func parseTemporalDurationExpr(text string) (float64, string) {
	text = strings.TrimSpace(text)
	unitStart := len(text)
	for i, r := range text {
		if (r < '0' || r > '9') && r != '.' {
			unitStart = i
			break
		}
	}
	value := parseutil.ParseFloat(strings.TrimSpace(text[:unitStart]))
	switch strings.TrimSpace(text[unitStart:]) {
	case "m":
		return value, "min"
	default:
		return value, strings.TrimSpace(text[unitStart:])
	}
}

func (l *lowerer) lowerTemporalDuration(n *gotreesitter.Node) *Duration {
	if n == nil {
		return nil
	}
	text := strings.TrimSpace(l.text(n))
	unitStart := len(text)
	for i, r := range text {
		if (r < '0' || r > '9') && r != '.' {
			unitStart = i
			break
		}
	}
	return &Duration{
		Value: parseutil.ParseFloat(strings.TrimSpace(text[:unitStart])),
		Unit:  strings.TrimSpace(text[unitStart:]),
		Span:  spanForNode(n),
	}
}

func spanForNode(n *gotreesitter.Node) Span {
	if n == nil {
		return Span{}
	}
	start := n.StartPoint()
	end := n.EndPoint()
	return Span{
		StartByte: n.StartByte(),
		EndByte:   n.EndByte(),
		StartRow:  start.Row,
		StartCol:  start.Column,
		EndRow:    end.Row,
		EndCol:    end.Column,
	}
}

func comparisonOp(n *gotreesitter.Node, source []byte, lang *gotreesitter.Language) BinaryOpKind {
	if opNode := n.ChildByFieldName("op", lang); opNode != nil {
		switch strings.TrimSpace(string(source[opNode.StartByte():opNode.EndByte()])) {
		case "==":
			return BinaryEq
		case "!=":
			return BinaryNeq
		case ">":
			return BinaryGt
		case ">=":
			return BinaryGte
		case "<":
			return BinaryLt
		case "<=":
			return BinaryLte
		}
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		text := strings.TrimSpace(string(source[n.Child(i).StartByte():n.Child(i).EndByte()]))
		switch text {
		case "==":
			return BinaryEq
		case "!=":
			return BinaryNeq
		case ">":
			return BinaryGt
		case ">=":
			return BinaryGte
		case "<":
			return BinaryLt
		case "<=":
			return BinaryLte
		}
	}
	leftNode := n.ChildByFieldName("left", lang)
	rightNode := n.ChildByFieldName("right", lang)
	if leftNode != nil && rightNode != nil {
		switch strings.TrimSpace(string(source[leftNode.EndByte():rightNode.StartByte()])) {
		case "==":
			return BinaryEq
		case "!=":
			return BinaryNeq
		case ">":
			return BinaryGt
		case ">=":
			return BinaryGte
		case "<":
			return BinaryLt
		case "<=":
			return BinaryLte
		}
	}
	return BinaryEq
}

func mathOp(n *gotreesitter.Node, source []byte, lang *gotreesitter.Language) BinaryOpKind {
	if opNode := n.ChildByFieldName("op", lang); opNode != nil {
		switch strings.TrimSpace(string(source[opNode.StartByte():opNode.EndByte()])) {
		case "+":
			return BinaryAdd
		case "-":
			return BinarySub
		case "*":
			return BinaryMul
		case "/":
			return BinaryDiv
		case "%":
			return BinaryMod
		}
	}
	return BinaryAdd
}

func betweenKind(n *gotreesitter.Node, source []byte, lang *gotreesitter.Language) BetweenKind {
	open := ""
	close := ""
	if openNode := n.ChildByFieldName("open", lang); openNode != nil {
		open = strings.TrimSpace(string(source[openNode.StartByte():openNode.EndByte()]))
	}
	if closeNode := n.ChildByFieldName("close", lang); closeNode != nil {
		close = strings.TrimSpace(string(source[closeNode.StartByte():closeNode.EndByte()]))
	}
	switch open + close {
	case "[]":
		return BetweenClosedClosed
	case "()":
		return BetweenOpenOpen
	case "[)":
		return BetweenClosedOpen
	case "(]":
		return BetweenOpenClosed
	default:
		return BetweenClosedClosed
	}
}

func quantifierKind(n *gotreesitter.Node, source []byte, lang *gotreesitter.Language) QuantifierKind {
	if qNode := n.ChildByFieldName("quantifier", lang); qNode != nil {
		switch strings.TrimSpace(string(source[qNode.StartByte():qNode.EndByte()])) {
		case "any":
			return QuantifierAny
		case "none":
			return QuantifierNone
		default:
			return QuantifierAll
		}
	}
	return QuantifierAll
}

func aggregateKind(n *gotreesitter.Node, source []byte, lang *gotreesitter.Language) AggregateKind {
	if fnNode := n.ChildByFieldName("function", lang); fnNode != nil {
		switch strings.TrimSpace(string(source[fnNode.StartByte():fnNode.EndByte()])) {
		case "sum":
			return AggregateSum
		case "count":
			return AggregateCount
		case "avg":
			return AggregateAvg
		}
	}
	return AggregateSum
}

func normalizedPrimaryText(raw string) string {
	return parseutil.StripQuotes(raw)
}

func arbiterTarget(raw string) string {
	return parseutil.StripQuotes(raw)
}

func hasLeadingLiteral(n *gotreesitter.Node, source []byte, want string) bool {
	if n == nil {
		return false
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child.IsNamed() {
			continue
		}
		text := strings.TrimSpace(string(source[child.StartByte():child.EndByte()]))
		if text == "" {
			continue
		}
		return text == want
	}
	return false
}
