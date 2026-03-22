package flags

import (
	"fmt"
	"time"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/ir"
)

func (f *Flags) parseParsed(parsed *arbiter.ParsedSource, full *arbiter.CompileResult) error {
	if parsed == nil {
		return fmt.Errorf("nil parsed source")
	}
	if full == nil {
		return fmt.Errorf("nil compiled ruleset")
	}
	if full.Program == nil {
		return fmt.Errorf("nil lowered program")
	}

	f.defs = make(map[string]*FlagDef)
	f.segments = full.Segments
	f.source = parsed.Source

	for i := range full.Program.Flags {
		def, err := lowerFlagDef(full.Program, &full.Program.Flags[i])
		if err != nil {
			return fmt.Errorf("parse flag: %w", err)
		}
		f.defs[def.Key] = def
	}

	return nil
}

func lowerFlagDef(program *ir.Program, flag *ir.Flag) (*FlagDef, error) {
	if flag == nil {
		return nil, fmt.Errorf("nil flag declaration")
	}

	def := &FlagDef{
		Key:           flag.Name,
		Default:       flag.Default,
		KillSwitch:    flag.KillSwitch,
		Prerequisites: append([]string(nil), flag.Requires...),
	}
	switch flag.Type {
	case ir.FlagMultivariate:
		def.Type = FlagMultivariate
	default:
		def.Type = FlagBoolean
	}

	for _, meta := range flag.Metadata {
		switch meta.Key {
		case "owner":
			def.Metadata.Owner = meta.Value
		case "ticket":
			def.Metadata.Ticket = meta.Value
		case "rationale":
			def.Metadata.Rationale = meta.Value
		case "expires":
			if t, err := time.Parse("2006-01-02", meta.Value); err == nil {
				def.Metadata.Expires = &t
			}
		}
	}

	if len(flag.Defaults) > 0 {
		def.DefaultValues = lowerParamValues(program, flag.Defaults)
	}
	if len(flag.Variants) > 0 {
		def.Variants = make(map[string]*VariantDef, len(flag.Variants))
		for _, variant := range flag.Variants {
			def.Variants[variant.Name] = &VariantDef{
				Name:   variant.Name,
				Values: lowerParamValues(program, variant.Params),
			}
		}
	}

	for _, ruleIR := range flag.Rules {
		rule, err := lowerFlagRule(program, &ruleIR)
		if err != nil {
			return nil, err
		}
		def.Rules = append(def.Rules, rule)
	}

	if err := validateVariantSchema(def); err != nil {
		return nil, err
	}
	if err := validateRuleVariants(def); err != nil {
		return nil, err
	}
	return def, nil
}

func lowerFlagRule(program *ir.Program, ruleIR *ir.FlagRule) (FlagRule, error) {
	rule := FlagRule{
		SegmentName: ruleIR.Segment,
	}

	if ruleIR.HasCondition {
		rule.InlineExpr = ir.RenderExpr(program, ruleIR.Condition)
		compiled, err := compileInlineExpr(program, ruleIR.Condition)
		if err != nil {
			return rule, err
		}
		rule.CompiledInline = compiled
	}

	if ruleIR.Rollout != nil {
		rule.HasRollout = true
		if ruleIR.Rollout.HasBps {
			rule.RolloutBps = ruleIR.Rollout.Bps
		}
		if ruleIR.Rollout.HasSubject {
			rule.RolloutSubject = ruleIR.Rollout.Subject
		}
		if ruleIR.Rollout.HasNamespace {
			rule.RolloutNamespace = ruleIR.Rollout.Namespace
		}
	}

	rule.Variant = ruleIR.Variant
	if ruleIR.Split != nil {
		if ruleIR.Split.HasSubject {
			rule.SplitSubject = ruleIR.Split.Subject
		}
		if ruleIR.Split.HasNamespace {
			rule.SplitNamespace = ruleIR.Split.Namespace
		}
		for _, weight := range ruleIR.Split.Weights {
			rule.Split = append(rule.Split, SplitBand{
				Variant:   weight.Variant,
				WeightBps: weight.Weight,
			})
		}
		if err := validateSplitWeights(rule.Split); err != nil {
			return rule, err
		}
	}

	return rule, nil
}

func lowerParamValues(program *ir.Program, params []ir.ActionParam) map[string]any {
	values := make(map[string]any, len(params))
	for _, param := range params {
		values[param.Key] = lowerValue(program, param.Value)
	}
	return values
}

func lowerValue(program *ir.Program, exprID ir.ExprID) any {
	expr := program.Expr(exprID)
	if expr == nil {
		return nil
	}
	switch expr.Kind {
	case ir.ExprSecretRef:
		return SecretValue{Ref: expr.Path}
	case ir.ExprConstRef:
		decl, ok := program.ConstByName(expr.Name)
		if !ok {
			return nil
		}
		return lowerValue(program, decl.Value)
	case ir.ExprListLit:
		out := make([]any, 0, len(expr.Elems))
		for _, elem := range expr.Elems {
			out = append(out, lowerValue(program, elem))
		}
		return out
	default:
		value, ok := ir.LiteralValue(program, exprID)
		if !ok {
			return ir.RenderExpr(program, exprID)
		}
		return value
	}
}

func inferType(v any) ValueType {
	switch v.(type) {
	case string:
		return ValueString
	case float64:
		return ValueNumber
	case bool:
		return ValueBool
	case SecretValue:
		return ValueSecret
	default:
		return ValueUnknown
	}
}

func typeName(vt ValueType) string {
	switch vt {
	case ValueString:
		return "string"
	case ValueNumber:
		return "number"
	case ValueBool:
		return "bool"
	case ValueSecret:
		return "secret"
	default:
		return "unknown"
	}
}

func variantNames(variants map[string]*VariantDef) []string {
	names := make([]string, 0, len(variants))
	for key := range variants {
		names = append(names, key)
	}
	return names
}

func validateVariantSchema(def *FlagDef) error {
	if len(def.Variants) == 0 {
		return nil
	}

	def.Schema = make(map[string]ValueType)
	for key, value := range def.DefaultValues {
		def.Schema[key] = inferType(value)
	}

	for variantName, vd := range def.Variants {
		for key, value := range vd.Values {
			valueType := inferType(value)
			if existing, ok := def.Schema[key]; ok {
				if existing != valueType {
					return fmt.Errorf(
						"flag %s: variant %q field %q has type %s, expected %s (from prior declaration)",
						def.Key,
						variantName,
						key,
						typeName(valueType),
						typeName(existing),
					)
				}
				continue
			}
			def.Schema[key] = valueType
		}
	}

	return nil
}

func validateRuleVariants(def *FlagDef) error {
	if len(def.Variants) == 0 {
		return nil
	}

	for _, rule := range def.Rules {
		if len(rule.Split) > 0 {
			for _, band := range rule.Split {
				if _, ok := def.Variants[band.Variant]; !ok {
					return fmt.Errorf(
						"flag %s: split references undeclared variant %q (declared: %v)",
						def.Key,
						band.Variant,
						variantNames(def.Variants),
					)
				}
			}
			continue
		}
		if _, ok := def.Variants[rule.Variant]; !ok {
			return fmt.Errorf(
				"flag %s: rule references undeclared variant %q (declared: %v)",
				def.Key,
				rule.Variant,
				variantNames(def.Variants),
			)
		}
	}

	if _, ok := def.Variants[def.Default]; !ok {
		def.Variants[def.Default] = &VariantDef{Name: def.Default}
	}

	return nil
}

func validateSplitWeights(split []SplitBand) error {
	total := 0
	for _, band := range split {
		total += int(band.WeightBps)
	}
	if total != int(govern.RolloutResolution) {
		return fmt.Errorf("split weights must sum to %d", govern.RolloutResolution)
	}
	return nil
}

func compileInlineExpr(program *ir.Program, exprID ir.ExprID) (*govern.CompiledSegment, error) {
	synthetic := &ir.Program{
		Consts: program.Consts,
		Exprs:  program.Exprs,
		Rules: []ir.Rule{
			{
				Name:         "__inline",
				HasCondition: true,
				Condition:    exprID,
				Action:       ir.Action{Name: "Match"},
			},
		},
	}
	synthetic.RebuildIndexes()
	rs, err := compiler.CompileIR(synthetic)
	if err != nil {
		return nil, fmt.Errorf("compile inline condition: %w", err)
	}
	return &govern.CompiledSegment{
		Name:    "inline",
		Source:  ir.RenderExpr(program, exprID),
		Ruleset: rs,
	}, nil
}
