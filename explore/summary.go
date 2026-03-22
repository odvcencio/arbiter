package explore

import (
	"fmt"
	"sort"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/ir"
	"github.com/odvcencio/arbiter/units"
)

// Summary is a bundle-level semantic summary for inspection surfaces.
type Summary struct {
	Source         string              `json:"source,omitempty"`
	FactSchemas    []SchemaSummary     `json:"fact_schemas,omitempty"`
	OutcomeSchemas []SchemaSummary     `json:"outcome_schemas,omitempty"`
	Constants      []ConstantSummary   `json:"constants,omitempty"`
	Rules          []RuleSummary       `json:"rules,omitempty"`
	ExpertRules    []ExpertRuleSummary `json:"expert_rules,omitempty"`
	UsedUnits      []DimensionUnits    `json:"used_units,omitempty"`
}

type SchemaSummary struct {
	Name   string         `json:"name"`
	Fields []FieldSummary `json:"fields,omitempty"`
}

type FieldSummary struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type ConstantSummary struct {
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
	Raw   string `json:"raw"`
}

type RuleSummary struct {
	Name       string `json:"name"`
	Priority   int    `json:"priority"`
	Segment    string `json:"segment,omitempty"`
	KillSwitch bool   `json:"kill_switch,omitempty"`
	Action     string `json:"action"`
}

type ExpertRuleSummary struct {
	Name            string `json:"name"`
	Priority        int    `json:"priority"`
	Kind            string `json:"kind"`
	Target          string `json:"target"`
	PerFact         bool   `json:"per_fact,omitempty"`
	NoLoop          bool   `json:"no_loop,omitempty"`
	Stable          bool   `json:"stable,omitempty"`
	ActivationGroup string `json:"activation_group,omitempty"`
	For             string `json:"for,omitempty"`
	Within          string `json:"within,omitempty"`
	StableFor       string `json:"stable_for,omitempty"`
	Cooldown        string `json:"cooldown,omitempty"`
	Debounce        string `json:"debounce,omitempty"`
}

type DimensionUnits struct {
	Dimension string   `json:"dimension"`
	Symbols   []string `json:"symbols,omitempty"`
}

// BuildSummaryFile compiles one bundle file and returns its semantic summary.
func BuildSummaryFile(path string) (*Summary, error) {
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return nil, err
	}
	if full == nil || full.Program == nil {
		return nil, fmt.Errorf("nil compiled program")
	}
	summary := BuildSummary(full.Program)
	summary.Source = path
	return summary, nil
}

// BuildSummary creates an inspection summary from a lowered program.
func BuildSummary(program *ir.Program) *Summary {
	if program == nil {
		return &Summary{}
	}
	summary := &Summary{}
	for _, schema := range program.FactSchemas {
		summary.FactSchemas = append(summary.FactSchemas, summarizeFactSchema(schema))
	}
	for _, schema := range program.OutcomeSchemas {
		summary.OutcomeSchemas = append(summary.OutcomeSchemas, summarizeOutcomeSchema(schema))
	}
	for _, decl := range program.Consts {
		value, ok := ir.LiteralValue(program, decl.Value)
		item := ConstantSummary{
			Name: decl.Name,
			Raw:  ir.RenderExpr(program, decl.Value),
		}
		if ok {
			item.Value = value
		}
		summary.Constants = append(summary.Constants, item)
	}
	for _, rule := range program.Rules {
		summary.Rules = append(summary.Rules, RuleSummary{
			Name:       rule.Name,
			Priority:   int(rule.Priority),
			Segment:    rule.Segment,
			KillSwitch: rule.KillSwitch,
			Action:     rule.Action.Name,
		})
	}
	for _, rule := range program.Expert {
		item := ExpertRuleSummary{
			Name:            rule.Name,
			Priority:        int(rule.Priority),
			Kind:            string(rule.ActionKind),
			Target:          rule.Target,
			PerFact:         rule.PerFact,
			NoLoop:          rule.NoLoop,
			Stable:          rule.Stable,
			ActivationGroup: rule.ActivationGroup,
		}
		if rule.ForDuration != nil {
			item.For = formatDuration(rule.ForDuration)
		}
		if rule.WithinDuration != nil {
			item.Within = formatDuration(rule.WithinDuration)
		}
		if rule.HasStableCycles {
			item.StableFor = fmt.Sprintf("%d cycles", rule.StableCycles)
		}
		if rule.CooldownDuration != nil {
			item.Cooldown = formatDuration(rule.CooldownDuration)
		}
		if rule.DebounceDuration != nil {
			item.Debounce = formatDuration(rule.DebounceDuration)
		}
		summary.ExpertRules = append(summary.ExpertRules, item)
	}
	summary.UsedUnits = collectUsedUnits(program)
	return summary
}

func summarizeFactSchema(schema ir.FactSchema) SchemaSummary {
	out := SchemaSummary{
		Name: schema.Name,
	}
	for _, field := range schema.Fields {
		out.Fields = append(out.Fields, FieldSummary{
			Name:     field.Name,
			Type:     fieldTypeString(field.Type, field.Required),
			Required: field.Required,
		})
	}
	return out
}

func summarizeOutcomeSchema(schema ir.OutcomeSchema) SchemaSummary {
	out := SchemaSummary{
		Name: schema.Name,
	}
	for _, field := range schema.Fields {
		out.Fields = append(out.Fields, FieldSummary{
			Name:     field.Name,
			Type:     fieldTypeString(field.Type, field.Required),
			Required: field.Required,
		})
	}
	return out
}

func fieldTypeString(fieldType ir.FieldType, required bool) string {
	base := fieldType.Base
	if fieldType.Dimension != "" {
		base += "<" + fieldType.Dimension + ">"
	}
	if !required {
		return base + "?"
	}
	return base
}

func formatDuration(duration *ir.Duration) string {
	if duration == nil {
		return ""
	}
	return fmt.Sprintf("%g%s", duration.Value, duration.Unit)
}

func collectUsedUnits(program *ir.Program) []DimensionUnits {
	dimensions := make(map[string]map[string]struct{})
	add := func(dimension string, symbols []string) {
		if dimension == "" {
			return
		}
		bySymbol := dimensions[dimension]
		if bySymbol == nil {
			bySymbol = make(map[string]struct{})
			dimensions[dimension] = bySymbol
		}
		for _, symbol := range symbols {
			bySymbol[symbol] = struct{}{}
		}
	}

	for _, schema := range program.FactSchemas {
		for _, field := range schema.Fields {
			add(field.Type.Dimension, units.SymbolsForDimension(field.Type.Dimension))
		}
	}
	for _, schema := range program.OutcomeSchemas {
		for _, field := range schema.Fields {
			add(field.Type.Dimension, units.SymbolsForDimension(field.Type.Dimension))
		}
	}
	for _, expr := range program.Exprs {
		switch expr.Kind {
		case ir.ExprQuantityLit, ir.ExprDecimalLit:
			entry, ok := units.Lookup(expr.Unit)
			if !ok {
				continue
			}
			add(entry.Dimension, []string{entry.Symbol})
		}
	}

	if len(dimensions) == 0 {
		return nil
	}
	keys := make([]string, 0, len(dimensions))
	for dimension := range dimensions {
		keys = append(keys, dimension)
	}
	sort.Strings(keys)

	out := make([]DimensionUnits, 0, len(keys))
	for _, dimension := range keys {
		symbols := make([]string, 0, len(dimensions[dimension]))
		for symbol := range dimensions[dimension] {
			symbols = append(symbols, symbol)
		}
		sort.Strings(symbols)
		out = append(out, DimensionUnits{
			Dimension: dimension,
			Symbols:   symbols,
		})
	}
	return out
}
