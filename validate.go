package arbiter

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/units"
)

const (
	schemaBaseUnknown   = ""
	schemaBaseNull      = "null"
	schemaBaseString    = "string"
	schemaBaseNumber    = "number"
	schemaBaseDecimal   = "decimal"
	schemaBaseBoolean   = "boolean"
	schemaBaseTimestamp = "timestamp"
	schemaBaseObject    = "object"
)

func validateProgram(program *ir.Program) ([]Diagnostic, error) {
	if program == nil {
		return nil, nil
	}
	validator := &programValidator{program: program}
	if err := validator.normalizeSchemas(); err != nil {
		return nil, err
	}
	if err := validator.validate(); err != nil {
		return nil, err
	}
	return validator.warnings, nil
}

// maxConstRefChain bounds how many const declarations one reference can chain
// through. It catches pathological (but acyclic) chains without rejecting any
// realistic program; it is not user-configurable.
const maxConstRefChain = 256

type programValidator struct {
	program  *ir.Program
	warnings []Diagnostic

	// constRefStack tracks the names of const declarations currently being
	// resolved, so a self- or mutually-referential const chain is reported as
	// a diagnostic instead of recursing until the goroutine stack overflows.
	constRefStack []string
}

func (v *programValidator) warn(span ir.Span, msg string) {
	v.warnings = append(v.warnings, Diagnostic{
		Severity: DiagWarning,
		Message:  msg,
		Line:     int(span.StartRow) + 1,
		Col:      int(span.StartCol) + 1,
	})
}

type exprType struct {
	Base      string
	Dimension string
	Unit      string
	Optional  bool
	Schema    *ir.FactSchema
}

type bindingInfo struct {
	typ exprType
}

type validationEnv struct {
	bindings map[string]bindingInfo
}

func newValidationEnv() *validationEnv {
	return &validationEnv{bindings: make(map[string]bindingInfo)}
}

func (e *validationEnv) clone() *validationEnv {
	if e == nil {
		return newValidationEnv()
	}
	out := newValidationEnv()
	for name, binding := range e.bindings {
		out.bindings[name] = binding
	}
	return out
}

func (e *validationEnv) bind(name string, binding bindingInfo) {
	if e == nil || name == "" {
		return
	}
	e.bindings[name] = binding
}

func (e *validationEnv) lookup(name string) (bindingInfo, bool) {
	if e == nil || name == "" {
		return bindingInfo{}, false
	}
	binding, ok := e.bindings[name]
	return binding, ok
}

func (v *programValidator) normalizeSchemas() error {
	for i := range v.program.FactSchemas {
		schema := &v.program.FactSchemas[i]
		fields, err := v.normalizeFactSchema(schema)
		if err != nil {
			return err
		}
		schema.Fields = fields
	}
	for i := range v.program.OutcomeSchemas {
		schema := &v.program.OutcomeSchemas[i]
		fields, err := v.normalizeOutcomeSchema(schema)
		if err != nil {
			return err
		}
		schema.Fields = fields
	}
	v.program.RebuildIndexes()
	return nil
}

func (v *programValidator) normalizeFactSchema(schema *ir.FactSchema) ([]ir.SchemaField, error) {
	if schema == nil {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(schema.Fields)+1)
	fields := make([]ir.SchemaField, 0, len(schema.Fields)+1)
	keyField := ir.SchemaField{
		Name:     "key",
		Type:     ir.FieldType{Base: schemaBaseString},
		Required: true,
		Span:     schema.Span,
	}
	seen["key"] = struct{}{}
	fields = append(fields, keyField)

	for _, field := range schema.Fields {
		if field.Name == "" {
			return nil, spanError(field.Span, "fact schema %s: field name is required", schema.Name)
		}
		if _, ok := seen[field.Name]; ok {
			if field.Name == "key" {
				if field.Type.Base != schemaBaseString {
					return nil, spanError(field.Span, "fact schema %s: key must have type string", schema.Name)
				}
				if !field.Required {
					return nil, spanError(field.Span, "fact schema %s: key cannot be optional", schema.Name)
				}
				continue
			}
			return nil, spanError(field.Span, "fact schema %s: duplicate field %q", schema.Name, field.Name)
		}
		if err := validateFieldType(field.Type, field.Span, "fact schema "+schema.Name); err != nil {
			return nil, err
		}
		seen[field.Name] = struct{}{}
		fields = append(fields, field)
	}
	return fields, nil
}

func (v *programValidator) normalizeOutcomeSchema(schema *ir.OutcomeSchema) ([]ir.SchemaField, error) {
	if schema == nil {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(schema.Fields))
	fields := make([]ir.SchemaField, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.Name == "" {
			return nil, spanError(field.Span, "outcome schema %s: field name is required", schema.Name)
		}
		if _, ok := seen[field.Name]; ok {
			return nil, spanError(field.Span, "outcome schema %s: duplicate field %q", schema.Name, field.Name)
		}
		if err := validateFieldType(field.Type, field.Span, "outcome schema "+schema.Name); err != nil {
			return nil, err
		}
		seen[field.Name] = struct{}{}
		fields = append(fields, field)
	}
	return fields, nil
}

func validateFieldType(fieldType ir.FieldType, span ir.Span, context string) error {
	switch fieldType.Base {
	case schemaBaseString, schemaBaseBoolean, schemaBaseTimestamp:
		if fieldType.Dimension != "" {
			return spanError(span, "%s: type %q cannot declare a dimension", context, fieldType.Base)
		}
		return nil
	case schemaBaseNumber:
		if fieldType.Dimension != "" && !units.KnownDimension(fieldType.Dimension) {
			return spanError(span, "%s: unknown dimension %q", context, fieldType.Dimension)
		}
		return nil
	case schemaBaseDecimal:
		if fieldType.Dimension != "" && !units.KnownDimension(fieldType.Dimension) {
			return spanError(span, "%s: unknown dimension %q", context, fieldType.Dimension)
		}
		return nil
	default:
		return spanError(span, "%s: unsupported field type %q", context, fieldType.Base)
	}
}

func (v *programValidator) validate() error {
	var errs []error
	env := newValidationEnv()
	if err := v.validateTagUsage(); err != nil {
		errs = append(errs, err)
	}
	for i := range v.program.Tables {
		if err := v.validateTable(&v.program.Tables[i]); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Consts {
		if _, err := v.validateExpr(v.program.Consts[i].Value, env); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Segments {
		if err := v.validateCondition(v.program.Segments[i].Condition, env, "segment "+v.program.Segments[i].Name); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Rules {
		if err := v.validateRule(&v.program.Rules[i]); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Strategies {
		if err := v.validateStrategy(&v.program.Strategies[i]); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Workers {
		if err := v.validateWorker(&v.program.Workers[i]); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Flags {
		if err := v.validateFlag(&v.program.Flags[i]); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Expert {
		if err := v.validateExpertRule(&v.program.Expert[i]); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range v.program.Arbiters {
		if err := v.validateArbiter(&v.program.Arbiters[i]); err != nil {
			errs = append(errs, err)
		}
	}
	if err := v.validateRolloutNamespaces(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (v *programValidator) validateTagUsage() error {
	if v.program == nil {
		return nil
	}
	declared := make(map[string]ir.TagDeclaration, len(v.program.Tags))
	for _, decl := range v.program.Tags {
		if decl.Name == "" {
			continue
		}
		if _, ok := declared[decl.Name]; ok {
			continue
		}
		declared[decl.Name] = decl
	}
	used := make(map[string]struct{})
	var errs []error
	check := func(scope string, span ir.Span, tags []string) {
		for _, tag := range tags {
			if tag == "" {
				continue
			}
			if _, ok := declared[tag]; ok {
				used[tag] = struct{}{}
				continue
			}
			msg := fmt.Sprintf(`%s: unknown tag %q`, scope, tag)
			if suggestion, ok := nearestTag(tag, declared); ok {
				msg += fmt.Sprintf(` (did you mean %q?)`, suggestion)
			}
			errs = append(errs, spanError(span, "%s", msg))
		}
	}
	for _, rule := range v.program.Rules {
		check("rule "+rule.Name, rule.Span, rule.Tags)
	}
	for _, flag := range v.program.Flags {
		check("flag "+flag.Name, flag.Span, flag.Tags)
	}
	for _, rule := range v.program.Expert {
		check("expert rule "+rule.Name, rule.Span, rule.Tags)
	}
	for _, decl := range v.program.Tags {
		if _, ok := used[decl.Name]; ok {
			continue
		}
		v.warn(decl.Span, fmt.Sprintf("tag %q declared but not used", decl.Name))
	}
	return errors.Join(errs...)
}

func nearestTag(name string, declared map[string]ir.TagDeclaration) (string, bool) {
	best := ""
	bestDistance := 3
	for candidate := range declared {
		distance := editDistance(name, candidate)
		if distance > 2 || distance >= bestDistance {
			continue
		}
		bestDistance = distance
		best = candidate
	}
	return best, best != ""
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			cur[j] = min3(
				prev[j]+1,
				cur[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a > b {
		a = b
	}
	if a > c {
		a = c
	}
	return a
}

func (v *programValidator) validateRolloutNamespaces() error {
	type nsOrigin struct {
		scope string
		span  ir.Span
	}
	seen := make(map[string]nsOrigin)

	checkRollout := func(rollout *ir.Rollout, scope string) error {
		if rollout == nil || !rollout.HasBps {
			return nil
		}
		ns := rollout.Namespace
		if !rollout.HasNamespace {
			ns = "arbiter:" + scope
		}
		if prev, ok := seen[ns]; ok && prev.scope != scope {
			return spanError(rollout.Span,
				"rollout namespace %q collides with %s — rollouts will be correlated; use explicit namespace to separate them",
				ns, prev.scope)
		}
		seen[ns] = nsOrigin{scope: scope, span: rollout.Span}
		return nil
	}

	for i := range v.program.Rules {
		rule := &v.program.Rules[i]
		if err := checkRollout(rule.Rollout, "rule:"+rule.Name); err != nil {
			return err
		}
	}
	for i := range v.program.Strategies {
		strat := &v.program.Strategies[i]
		for j := range strat.Candidates {
			c := &strat.Candidates[j]
			if err := checkRollout(c.Rollout, "strategy:"+strat.Name+":candidate:"+c.Label); err != nil {
				return err
			}
		}
	}
	for i := range v.program.Flags {
		flag := &v.program.Flags[i]
		for j := range flag.Rules {
			fr := &flag.Rules[j]
			if err := checkRollout(fr.Rollout, "flag:"+flag.Name+":rule:"+fmt.Sprintf("%d", j)); err != nil {
				return err
			}
		}
	}
	for i := range v.program.Expert {
		rule := &v.program.Expert[i]
		if err := checkRollout(rule.Rollout, "expert:"+rule.Name); err != nil {
			return err
		}
	}
	return nil
}

func (v *programValidator) validateRule(rule *ir.Rule) error {
	if rule == nil {
		return nil
	}
	if err := validateActiveWindow(rule.ActiveWindow, "rule "+rule.Name); err != nil {
		return err
	}
	env, err := v.validateLets(rule.Lets, newValidationEnv())
	if err != nil {
		return err
	}
	if rule.HasCondition {
		if err := v.validateCondition(rule.Condition, env, "rule "+rule.Name); err != nil {
			return err
		}
	}
	if err := v.validateRuleAction(rule, &rule.Action, env); err != nil {
		return err
	}
	if rule.Fallback != nil {
		if err := v.validateRuleAction(rule, rule.Fallback, env); err != nil {
			return err
		}
	}
	return nil
}

func (v *programValidator) validateStrategy(strategy *ir.Strategy) error {
	if strategy == nil {
		return nil
	}
	schema, ok := v.program.OutcomeSchemaByName(strategy.Returns)
	if !ok || schema == nil {
		return spanError(strategy.Span, "strategy %s returns %s: unknown outcome schema", strategy.Name, strategy.Returns)
	}
	if len(strategy.Candidates) == 0 {
		return spanError(strategy.Span, "strategy %s: at least one candidate is required", strategy.Name)
	}
	seenLabels := make(map[string]struct{}, len(strategy.Candidates))
	whenCount := 0
	sawElse := false
	for i := range strategy.Candidates {
		candidate := &strategy.Candidates[i]
		if candidate.Label == "" {
			return spanError(candidate.Span, "strategy %s: candidate label is required", strategy.Name)
		}
		if _, ok := seenLabels[candidate.Label]; ok {
			return spanError(candidate.Span, "strategy %s: duplicate candidate label %q", strategy.Name, candidate.Label)
		}
		seenLabels[candidate.Label] = struct{}{}
		if candidate.IsElse {
			if i != len(strategy.Candidates)-1 {
				return spanError(candidate.Span, "strategy %s: else arm must be last", strategy.Name)
			}
			if candidate.HasCondition || len(candidate.Lets) > 0 || candidate.Segment != "" || candidate.KillSwitch.IsSet() || candidate.ActiveWindow.Enabled() || candidate.Rollout != nil {
				return spanError(candidate.Span, "strategy %s candidate %s: else arm cannot declare conditions or governance", strategy.Name, candidate.Label)
			}
			sawElse = true
		} else {
			if sawElse {
				return spanError(candidate.Span, "strategy %s: when arms cannot appear after else", strategy.Name)
			}
			whenCount++
		}
		if err := validateActiveWindow(candidate.ActiveWindow, "strategy "+strategy.Name+" candidate "+candidate.Label); err != nil {
			return err
		}
		env, err := v.validateLets(candidate.Lets, newValidationEnv())
		if err != nil {
			return err
		}
		if candidate.HasCondition {
			if err := v.validateCondition(candidate.Condition, env, "strategy "+strategy.Name+" candidate "+candidate.Label); err != nil {
				return err
			}
		} else if !candidate.IsElse {
			return spanError(candidate.Span, "strategy %s candidate %s: when arm requires a condition", strategy.Name, candidate.Label)
		}
		if candidate.Segment != "" {
			if _, ok := v.program.SegmentByName(candidate.Segment); !ok {
				return spanError(candidate.Span, "strategy %s candidate %s: unknown segment %q", strategy.Name, candidate.Label, candidate.Segment)
			}
		}
		if err := v.validateStrategyOutcomeParams(strategy, candidate, schema, env); err != nil {
			return err
		}
	}
	if !sawElse {
		return spanError(strategy.Span, "strategy %s: else arm is required", strategy.Name)
	}
	if whenCount == 0 {
		return spanError(strategy.Span, "strategy %s: at least one when arm is required before else", strategy.Name)
	}
	return nil
}

func (v *programValidator) validateFlag(flag *ir.Flag) error {
	if flag == nil {
		return nil
	}
	for _, param := range flag.Defaults {
		if _, err := v.validateExpr(param.Value, newValidationEnv()); err != nil {
			return err
		}
	}
	for _, variant := range flag.Variants {
		for _, param := range variant.Params {
			if _, err := v.validateExpr(param.Value, newValidationEnv()); err != nil {
				return err
			}
		}
	}
	for i, rule := range flag.Rules {
		if err := validateActiveWindow(rule.ActiveWindow, fmt.Sprintf("flag %s rule %d", flag.Name, i)); err != nil {
			return err
		}
		if rule.HasCondition {
			if err := v.validateCondition(rule.Condition, newValidationEnv(), "flag "+flag.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *programValidator) validateExpertRule(rule *ir.ExpertRule) error {
	if rule == nil {
		return nil
	}
	if err := validateExpertTemporalRule(rule); err != nil {
		return err
	}
	env, err := v.validateLets(rule.Lets, newValidationEnv())
	if err != nil {
		return err
	}
	if rule.HasCondition {
		if err := v.validateCondition(rule.Condition, env, "expert rule "+rule.Name); err != nil {
			return err
		}
	}
	switch rule.ActionKind {
	case ir.ExpertAssert:
		if err := v.validateFactAction(rule, env, false); err != nil {
			return err
		}
	case ir.ExpertModify:
		if err := v.validateModifyAction(rule, env); err != nil {
			return err
		}
	case ir.ExpertRetract:
		if err := v.validateRetractAction(rule, env); err != nil {
			return err
		}
	case ir.ExpertEmit:
		if err := v.validateOutcomeAction(rule, env); err != nil {
			return err
		}
	}
	return nil
}

func validateExpertTemporalRule(rule *ir.ExpertRule) error {
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
		if rule.StableCycles <= 0 {
			return spanError(rule.Span, "expert rule %s: stable_for cycles must be greater than zero", rule.Name)
		}
		waiting = append(waiting, "stable_for")
	}
	if rule.PerFact && (rule.ForDuration != nil || rule.WithinDuration != nil || rule.HasStableCycles || rule.CooldownDuration != nil || rule.DebounceDuration != nil) {
		return spanError(rule.Span, "expert rule %s: temporal operators are not supported on per_fact rules", rule.Name)
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
	return spanError(rule.Span, `expert rule %s: cannot combine %q and %q - %s`, rule.Name, first, second, reason)
}

func validateActiveWindow(window ir.ActiveWindow, scope string) error {
	if !window.Enabled() {
		return nil
	}
	var (
		fromTime  time.Time
		untilTime time.Time
	)
	if window.HasFrom {
		parsed, err := time.Parse(time.RFC3339Nano, window.From)
		if err != nil {
			return spanError(window.FromSpan, "%s: invalid active_from %q", scope, window.From)
		}
		fromTime = parsed.UTC()
	}
	if window.HasUntil {
		parsed, err := time.Parse(time.RFC3339Nano, window.Until)
		if err != nil {
			return spanError(window.UntilSpan, "%s: invalid active_until %q", scope, window.Until)
		}
		untilTime = parsed.UTC()
	}
	if window.HasFrom && window.HasUntil && !fromTime.Before(untilTime) {
		return spanError(window.UntilSpan, "%s: active_from must be earlier than active_until", scope)
	}
	return nil
}

func (v *programValidator) validateArbiter(arb *ir.Arbiter) error {
	if arb == nil {
		return nil
	}
	for _, clause := range arb.Clauses {
		if clause.HasFilter {
			if err := v.validateCondition(clause.Filter, newValidationEnv(), "arbiter "+arb.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *programValidator) validateTable(table *ir.Table) error {
	if table == nil {
		return nil
	}
	// Warn if any column name shadows a root input field.
	if v.program.Input != nil {
		inputRoots := make(map[string]struct{}, len(v.program.Input.Fields))
		for _, f := range v.program.Input.Fields {
			inputRoots[f.Name] = struct{}{}
		}
		for _, col := range table.Columns {
			if _, ok := inputRoots[col.Name]; ok {
				v.warn(table.Span, fmt.Sprintf("table column %q shadows input field %q", col.Name, col.Name))
			}
		}
	}
	// Validate each row: correct number of values and matching column types.
	for _, row := range table.Rows {
		if len(row.Values) != len(table.Columns) {
			return spanError(row.Span, "table %s: row has %d values but table has %d columns",
				table.Name, len(row.Values), len(table.Columns))
		}
		for i, exprID := range row.Values {
			col := table.Columns[i]
			valueType, err := v.validateExpr(exprID, newValidationEnv())
			if err != nil {
				return err
			}
			if !valueType.isKnown() {
				continue
			}
			field := ir.SchemaField{
				Name:     col.Name,
				Type:     col.Type,
				Required: true,
			}
			if !assignableToField(valueType, field) {
				expr := v.program.Expr(exprID)
				span := table.Span
				if expr != nil {
					span = expr.Span
				}
				return spanError(span, "table %s: column %q expects %s, got %s",
					table.Name, col.Name, col.Type.Base, valueType.String())
			}
		}
	}
	return nil
}

func (v *programValidator) validateWorker(worker *ir.Worker) error {
	if worker == nil {
		return nil
	}
	if worker.Name == "" {
		return spanError(worker.Span, "worker declaration missing name")
	}
	if worker.Input == "" {
		return spanError(worker.Span, "worker %s: input is required", worker.Name)
	}
	if _, ok := v.program.OutcomeSchemaByName(worker.Input); !ok {
		return spanError(worker.Span, "worker %s: input %s must reference an outcome schema", worker.Name, worker.Input)
	}
	if worker.Output == "" {
		return spanError(worker.Span, "worker %s: output is required", worker.Name)
	}
	if _, ok := v.program.OutcomeSchemaByName(worker.Output); !ok {
		if _, ok := v.program.FactSchemaByName(worker.Output); !ok {
			return spanError(worker.Span, "worker %s: output %s must reference a fact or outcome schema", worker.Name, worker.Output)
		}
	}

	kind := ArbiterHandlerKind(worker.Kind)
	if !workerRuntimeKindAllowed(kind) {
		return spanError(worker.Span, "worker %s: unsupported runtime kind %s", worker.Name, worker.Kind)
	}
	if kind != ArbiterHandlerStdout && worker.Target == "" {
		return spanError(worker.Span, "worker %s: runtime %s requires a target", worker.Name, kind)
	}
	if kind == ArbiterHandlerStdout && worker.Target != "" {
		return spanError(worker.Span, "worker %s: runtime %s does not take a target", worker.Name, kind)
	}
	return nil
}

func (v *programValidator) validateLets(lets []ir.LetBinding, env *validationEnv) (*validationEnv, error) {
	next := env.clone()
	for _, binding := range lets {
		typ, err := v.validateExpr(binding.Value, next)
		if err != nil {
			return nil, err
		}
		next.bind(binding.Name, bindingInfo{typ: typ})
	}
	return next, nil
}

func (v *programValidator) validateParams(params []ir.ActionParam, env *validationEnv) error {
	for _, param := range params {
		if _, err := v.validateExpr(param.Value, env); err != nil {
			return err
		}
	}
	return nil
}

func (v *programValidator) validateFactAction(rule *ir.ExpertRule, env *validationEnv, partial bool) error {
	if err := v.validateParams(rule.Params, env); err != nil {
		return err
	}
	schema, ok := v.program.FactSchemaByName(rule.Target)
	if !ok || schema == nil {
		return nil
	}
	required := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.Required {
			required[field.Name] = struct{}{}
		}
	}
	for _, param := range rule.Params {
		field, ok := factSchemaField(schema, param.Key)
		if !ok {
			return spanError(param.Span, "expert rule %s %s %s: unknown field %q", rule.Name, rule.ActionKind, rule.Target, param.Key)
		}
		if err := v.validateAssignedType(param.Value, field, env, rule.Name, string(rule.ActionKind), rule.Target); err != nil {
			return err
		}
		delete(required, param.Key)
	}
	if partial {
		return nil
	}
	delete(required, "key")
	for _, field := range schema.Fields {
		if _, ok := required[field.Name]; ok {
			return spanError(rule.Span, "expert rule %s %s %s: missing required field %q", rule.Name, rule.ActionKind, rule.Target, field.Name)
		}
	}
	return nil
}

func (v *programValidator) validateModifyAction(rule *ir.ExpertRule, env *validationEnv) error {
	if err := v.validateParams(rule.Params, env); err != nil {
		return err
	}
	if len(rule.Params) != 1 || rule.Params[0].Key != "key" {
		return spanError(rule.Span, "expert rule %s modify %s: only key is allowed before set", rule.Name, rule.Target)
	}
	schema, ok := v.program.FactSchemaByName(rule.Target)
	if !ok || schema == nil {
		return v.validateParams(rule.SetParams, env)
	}
	for _, param := range rule.SetParams {
		if param.Key == "key" {
			return spanError(param.Span, "expert rule %s modify %s: key cannot be updated in set", rule.Name, rule.Target)
		}
		field, ok := factSchemaField(schema, param.Key)
		if !ok {
			return spanError(param.Span, "expert rule %s modify %s: unknown field %q", rule.Name, rule.Target, param.Key)
		}
		if err := v.validateAssignedType(param.Value, field, env, rule.Name, "modify", rule.Target); err != nil {
			return err
		}
	}
	return nil
}

func (v *programValidator) validateRetractAction(rule *ir.ExpertRule, env *validationEnv) error {
	if err := v.validateParams(rule.Params, env); err != nil {
		return err
	}
	if len(rule.Params) != 1 || rule.Params[0].Key != "key" {
		return spanError(rule.Span, "expert rule %s retract %s: only key is allowed", rule.Name, rule.Target)
	}
	return nil
}

func (v *programValidator) validateOutcomeAction(rule *ir.ExpertRule, env *validationEnv) error {
	if err := v.validateParams(rule.Params, env); err != nil {
		return err
	}
	schema, ok := v.program.OutcomeSchemaByName(rule.Target)
	if !ok || schema == nil {
		return nil
	}
	required := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.Required {
			required[field.Name] = struct{}{}
		}
	}
	for _, param := range rule.Params {
		field, ok := outcomeSchemaField(schema, param.Key)
		if !ok {
			return spanError(param.Span, "expert rule %s emit %s: unknown field %q", rule.Name, rule.Target, param.Key)
		}
		if err := v.validateAssignedType(param.Value, field, env, rule.Name, "emit", rule.Target); err != nil {
			return err
		}
		delete(required, param.Key)
	}
	for _, field := range schema.Fields {
		if _, ok := required[field.Name]; ok {
			return spanError(rule.Span, "expert rule %s emit %s: missing required field %q", rule.Name, rule.Target, field.Name)
		}
	}
	return nil
}

func (v *programValidator) validateStrategyOutcomeParams(strategy *ir.Strategy, candidate *ir.StrategyCandidate, schema *ir.OutcomeSchema, env *validationEnv) error {
	if strategy == nil || candidate == nil {
		return nil
	}
	if err := v.validateParams(candidate.Params, env); err != nil {
		return err
	}
	required := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.Required {
			required[field.Name] = struct{}{}
		}
	}
	for _, param := range candidate.Params {
		field, ok := outcomeSchemaField(schema, param.Key)
		if !ok {
			return spanError(param.Span, "strategy %s candidate %s: unknown field %q on %s", strategy.Name, candidate.Label, param.Key, strategy.Returns)
		}
		if err := v.validateStrategyAssignedType(strategy, candidate, param.Value, field, env); err != nil {
			return err
		}
		delete(required, param.Key)
	}
	for _, field := range schema.Fields {
		if _, ok := required[field.Name]; ok {
			return spanError(candidate.Span, "strategy %s candidate %s: missing required field %q for %s", strategy.Name, candidate.Label, field.Name, strategy.Returns)
		}
	}
	return nil
}

func (v *programValidator) validateRuleAction(rule *ir.Rule, action *ir.Action, env *validationEnv) error {
	// Validate action-level let bindings and extend the environment with them.
	actionEnv, err := v.validateLets(action.Lets, env)
	if err != nil {
		return err
	}
	if err := v.validateParams(action.Params, actionEnv); err != nil {
		return err
	}
	schema, ok := v.program.OutcomeSchemaByName(action.Name)
	if !ok || schema == nil {
		return nil
	}
	required := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.Required {
			required[field.Name] = struct{}{}
		}
	}
	for _, param := range action.Params {
		field, ok := outcomeSchemaField(schema, param.Key)
		if !ok {
			return spanError(param.Span, "rule %s action %s: unknown field %q", rule.Name, action.Name, param.Key)
		}
		if err := v.validateAssignedType(param.Value, field, actionEnv, rule.Name, "action", action.Name); err != nil {
			return err
		}
		delete(required, param.Key)
	}
	for _, field := range schema.Fields {
		if _, ok := required[field.Name]; ok {
			return spanError(action.Span, "rule %s action %s: missing required field %q", rule.Name, action.Name, field.Name)
		}
	}
	return nil
}

func (v *programValidator) validateStrategyAssignedType(strategy *ir.Strategy, candidate *ir.StrategyCandidate, exprID ir.ExprID, field ir.SchemaField, env *validationEnv) error {
	valueType, err := v.validateExpr(exprID, env)
	if err != nil {
		return err
	}
	if !assignableToField(valueType, field) {
		return spanError(v.program.Expr(exprID).Span, "strategy %s candidate %s: field %q expects %s, got %s", strategy.Name, candidate.Label, field.Name, fieldTypeString(field), valueType.String())
	}
	return nil
}

func (v *programValidator) validateAssignedType(exprID ir.ExprID, field ir.SchemaField, env *validationEnv, ruleName, action, target string) error {
	valueType, err := v.validateExpr(exprID, env)
	if err != nil {
		return err
	}
	if !assignableToField(valueType, field) {
		return spanError(v.program.Expr(exprID).Span, "expert rule %s %s %s: field %q expects %s, got %s", ruleName, action, target, field.Name, fieldTypeString(field), valueType.String())
	}
	return nil
}

func (v *programValidator) validateCondition(exprID ir.ExprID, env *validationEnv, context string) error {
	typ, err := v.validateExpr(exprID, env)
	if err != nil {
		return err
	}
	if typ.isKnown() && typ.Base != schemaBaseBoolean {
		return spanError(v.program.Expr(exprID).Span, "%s: condition must evaluate to boolean, got %s", context, typ.String())
	}
	return nil
}

func (v *programValidator) validateExpr(exprID ir.ExprID, env *validationEnv) (exprType, error) {
	expr := v.program.Expr(exprID)
	if expr == nil {
		return exprType{}, nil
	}

	switch expr.Kind {
	case ir.ExprStringLit:
		return exprType{Base: schemaBaseString}, nil
	case ir.ExprNumberLit:
		return exprType{Base: schemaBaseNumber}, nil
	case ir.ExprDecimalLit:
		if _, err := dec.Parse(expr.String, expr.Unit); err != nil {
			return exprType{}, spanError(expr.Span, "invalid decimal literal %q", ir.RenderExpr(v.program, exprID))
		}
		if expr.Unit == "" {
			return exprType{Base: schemaBaseDecimal}, nil
		}
		entry, ok := units.Lookup(expr.Unit)
		if !ok {
			return exprType{}, spanError(expr.Span, "unknown unit %q", expr.Unit)
		}
		return exprType{Base: schemaBaseDecimal, Dimension: entry.Dimension, Unit: entry.Symbol}, nil
	case ir.ExprQuantityLit:
		_, entry, err := units.Normalize(expr.Number, expr.Unit)
		if err != nil {
			return exprType{}, spanError(expr.Span, "%s", err.Error())
		}
		return exprType{Base: schemaBaseNumber, Dimension: entry.Dimension}, nil
	case ir.ExprTimestampLit:
		if _, err := time.Parse(time.RFC3339Nano, expr.String); err != nil {
			return exprType{}, spanError(expr.Span, "invalid timestamp literal %q", expr.String)
		}
		return exprType{Base: schemaBaseTimestamp}, nil
	case ir.ExprBoolLit:
		return exprType{Base: schemaBaseBoolean}, nil
	case ir.ExprNullLit:
		return exprType{Base: schemaBaseNull}, nil
	case ir.ExprConstRef:
		decl, ok := v.program.ConstByName(expr.Name)
		if !ok {
			return exprType{}, nil
		}
		for _, seen := range v.constRefStack {
			if seen == expr.Name {
				chain := append(append([]string{}, v.constRefStack...), expr.Name)
				return exprType{}, spanError(expr.Span, "constant cycle: %s", strings.Join(chain, " -> "))
			}
		}
		if len(v.constRefStack) >= maxConstRefChain {
			return exprType{}, spanError(expr.Span, "constant reference chain exceeds maximum depth (%d) starting at %q", maxConstRefChain, v.constRefStack[0])
		}
		v.constRefStack = append(v.constRefStack, expr.Name)
		result, err := v.validateExpr(decl.Value, env)
		v.constRefStack = v.constRefStack[:len(v.constRefStack)-1]
		return result, err
	case ir.ExprLocalRef:
		if binding, ok := env.lookup(expr.Name); ok {
			return binding.typ, nil
		}
		return exprType{}, nil
	case ir.ExprVarRef:
		return v.validateVarRef(expr, env)
	case ir.ExprListLit:
		for _, elem := range expr.Elems {
			if _, err := v.validateExpr(elem, env); err != nil {
				return exprType{}, err
			}
		}
		return exprType{}, nil
	case ir.ExprUnary:
		operandType, err := v.validateExpr(expr.Operand, env)
		if err != nil {
			return exprType{}, err
		}
		switch expr.UnaryOp {
		case ir.UnaryNot:
			if operandType.isKnown() && operandType.Base != schemaBaseBoolean {
				return exprType{}, spanError(expr.Span, "operator not expects boolean, got %s", operandType.String())
			}
			return exprType{Base: schemaBaseBoolean}, nil
		default:
			return exprType{Base: schemaBaseBoolean}, nil
		}
	case ir.ExprBinary:
		return v.validateBinary(expr, env)
	case ir.ExprBetween:
		valueType, err := v.validateExpr(expr.Value, env)
		if err != nil {
			return exprType{}, err
		}
		lowType, err := v.validateExpr(expr.Low, env)
		if err != nil {
			return exprType{}, err
		}
		highType, err := v.validateExpr(expr.High, env)
		if err != nil {
			return exprType{}, err
		}
		if incompatibleTypes(valueType, lowType) || incompatibleTypes(valueType, highType) {
			return exprType{}, spanError(expr.Span, "between bounds must share a compatible type")
		}
		return exprType{Base: schemaBaseBoolean}, nil
	case ir.ExprQuantifier:
		return v.validateQuantifier(expr, env)
	case ir.ExprAggregate:
		return v.validateAggregate(expr, env)
	case ir.ExprBuiltinCall:
		return v.validateBuiltinCall(expr, env)
	case ir.ExprLookup:
		return v.validateLookup(expr, env)
	default:
		return exprType{}, nil
	}
}

func (v *programValidator) validateVarRef(expr *ir.Expr, env *validationEnv) (exprType, error) {
	if expr == nil {
		return exprType{}, nil
	}
	parts := strings.Split(expr.Path, ".")
	if len(parts) == 0 {
		return exprType{}, nil
	}
	switch parts[0] {
	case "current_round":
		return exprType{Base: schemaBaseNumber}, nil
	case "__now":
		return exprType{Base: schemaBaseTimestamp}, nil
	}
	if parts[0] == "facts" {
		if len(parts) == 2 {
			return exprType{}, nil
		}
		if len(parts) >= 3 {
			if _, ok := v.program.FactSchemaByName(parts[1]); ok {
				return exprType{}, spanError(expr.Span, "facts.%s is a collection; bind an item before accessing %q", parts[1], strings.Join(parts[2:], "."))
			}
		}
		return exprType{}, nil
	}

	// Input schema validation (only when an input block is declared).
	// Bindings from any/all/bind take precedence over input fields, so check
	// the binding environment first.
	if _, boundInEnv := env.lookup(parts[0]); !boundInEnv && v.program.Input != nil {
		resolved, err := resolveInputPath(v.program.Input, expr.Path)
		if err != nil {
			return exprType{}, spanError(expr.Span, "%s", err.Error())
		}
		if resolved != nil {
			// Path resolved against input schema — return its type.
			return exprType{
				Base:      resolved.typ.Base,
				Dimension: resolved.typ.Dimension,
				Optional:  resolved.optional,
			}, nil
		}
		// Path doesn't match any input root field — fall through.
	}

	binding, ok := env.lookup(parts[0])
	if !ok {
		return exprType{}, nil
	}
	if len(parts) == 1 {
		return binding.typ, nil
	}
	if binding.typ.Schema == nil {
		// If the binding type is unknown (e.g. a lookup result), field access is
		// allowed and resolves to unknown rather than a hard error.
		if !binding.typ.isKnown() {
			return exprType{}, nil
		}
		return exprType{}, spanError(expr.Span, "cannot access field %q on %s", expr.Path, binding.typ.String())
	}
	field, ok := factSchemaField(binding.typ.Schema, parts[1])
	if !ok {
		return exprType{}, spanError(expr.Span, "fact %s has no field %q", binding.typ.Schema.Name, parts[1])
	}
	if len(parts) > 2 {
		return exprType{}, spanError(expr.Span, "field %q on fact %s is scalar and cannot be dereferenced", parts[1], binding.typ.Schema.Name)
	}
	return exprType{Base: field.Type.Base, Dimension: field.Type.Dimension, Optional: !field.Required}, nil
}

func (v *programValidator) validateBinary(expr *ir.Expr, env *validationEnv) (exprType, error) {
	leftType, err := v.validateExpr(expr.Left, env)
	if err != nil {
		return exprType{}, err
	}
	rightType, err := v.validateExpr(expr.Right, env)
	if err != nil {
		return exprType{}, err
	}
	switch expr.BinaryOp {
	case ir.BinaryAnd, ir.BinaryOr:
		if leftType.isKnown() && leftType.Base != schemaBaseBoolean {
			return exprType{}, spanError(expr.Span, "logical operator %s expects boolean operands", expr.BinaryOp)
		}
		if rightType.isKnown() && rightType.Base != schemaBaseBoolean {
			return exprType{}, spanError(expr.Span, "logical operator %s expects boolean operands", expr.BinaryOp)
		}
		return exprType{Base: schemaBaseBoolean}, nil
	case ir.BinaryEq, ir.BinaryNeq:
		if incompatibleTypes(leftType, rightType) {
			return exprType{}, spanError(expr.Span, "type mismatch: %s %s %s", leftType.String(), expr.BinaryOp, rightType.String())
		}
		return exprType{Base: schemaBaseBoolean}, nil
	case ir.BinaryGt, ir.BinaryGte, ir.BinaryLt, ir.BinaryLte:
		if incompatibleTypes(leftType, rightType) {
			return exprType{}, spanError(expr.Span, "type mismatch: %s %s %s", leftType.String(), expr.BinaryOp, rightType.String())
		}
		if (leftType.isKnown() && leftType.Base == schemaBaseBoolean) || (rightType.isKnown() && rightType.Base == schemaBaseBoolean) {
			return exprType{}, spanError(expr.Span, "relational operator %s does not support boolean operands", expr.BinaryOp)
		}
		return exprType{Base: schemaBaseBoolean}, nil
	case ir.BinaryAdd:
		switch {
		case isTimestampWithTimeDuration(leftType, rightType):
			return exprType{Base: schemaBaseTimestamp}, nil
		case isTimestampWithTimeDuration(rightType, leftType):
			return exprType{Base: schemaBaseTimestamp}, nil
		case !leftType.isKnown() || !rightType.isKnown():
			return exprType{}, nil
		case leftType.Base == schemaBaseString && rightType.Base == schemaBaseString:
			return exprType{Base: schemaBaseString}, nil
		case leftType.Base == schemaBaseDecimal && rightType.Base == schemaBaseDecimal && compatibleDecimalTypes(leftType, rightType):
			return mergeDecimalTypes(leftType, rightType), nil
		case leftType.Base == schemaBaseNumber && rightType.Base == schemaBaseNumber && compatibleNumberDimensions(leftType, rightType):
			return mergeNumberTypes(leftType, rightType), nil
		default:
			return exprType{}, spanError(expr.Span, "type mismatch: %s %s %s", leftType.String(), expr.BinaryOp, rightType.String())
		}
	case ir.BinarySub:
		if leftType.Base == schemaBaseTimestamp && rightType.Base == schemaBaseTimestamp {
			return exprType{Base: schemaBaseNumber, Dimension: "time"}, nil
		}
		if isTimestampWithTimeDuration(leftType, rightType) {
			return exprType{Base: schemaBaseTimestamp}, nil
		}
		if leftType.Base == schemaBaseDecimal && rightType.Base == schemaBaseDecimal {
			if !compatibleDecimalTypes(leftType, rightType) {
				return exprType{}, spanError(expr.Span, "type mismatch: %s %s %s", leftType.String(), expr.BinaryOp, rightType.String())
			}
			return mergeDecimalTypes(leftType, rightType), nil
		}
		if leftType.isKnown() && leftType.Base != schemaBaseNumber {
			return exprType{}, spanError(expr.Span, "operator %s expects numeric operands, got %s", expr.BinaryOp, leftType.String())
		}
		if rightType.isKnown() && rightType.Base != schemaBaseNumber {
			return exprType{}, spanError(expr.Span, "operator %s expects numeric operands, got %s", expr.BinaryOp, rightType.String())
		}
		if !compatibleNumberDimensions(leftType, rightType) {
			return exprType{}, spanError(expr.Span, "type mismatch: %s %s %s", leftType.String(), expr.BinaryOp, rightType.String())
		}
		return mergeNumberTypes(leftType, rightType), nil
	case ir.BinaryMul, ir.BinaryDiv, ir.BinaryMod:
		if leftType.Base == schemaBaseDecimal || rightType.Base == schemaBaseDecimal {
			return exprType{}, spanError(expr.Span, "operator %s does not support decimal operands", expr.BinaryOp)
		}
		if leftType.isKnown() && leftType.Base != schemaBaseNumber {
			return exprType{}, spanError(expr.Span, "operator %s expects numeric operands, got %s", expr.BinaryOp, leftType.String())
		}
		if rightType.isKnown() && rightType.Base != schemaBaseNumber {
			return exprType{}, spanError(expr.Span, "operator %s expects numeric operands, got %s", expr.BinaryOp, rightType.String())
		}
		switch expr.BinaryOp {
		case ir.BinaryMul:
			if leftType.Dimension != "" && rightType.Dimension != "" {
				return exprType{}, spanError(expr.Span, "cross-dimension multiply is not supported: %s * %s", leftType.String(), rightType.String())
			}
		case ir.BinaryDiv, ir.BinaryMod:
			if rightType.Dimension != "" {
				return exprType{}, spanError(expr.Span, "operator %s requires a dimensionless right operand, got %s", expr.BinaryOp, rightType.String())
			}
		}
		if leftType.Dimension != "" {
			return exprType{Base: schemaBaseNumber, Dimension: leftType.Dimension}, nil
		}
		if rightType.Dimension != "" && expr.BinaryOp == ir.BinaryMul {
			return exprType{Base: schemaBaseNumber, Dimension: rightType.Dimension}, nil
		}
		return exprType{Base: schemaBaseNumber}, nil
	case ir.BinaryMatches:
		// When the right operand is a string literal, validate and pre-compile
		// the regex at compile time so invalid patterns are caught early.
		right := v.program.Expr(expr.Right)
		if right != nil && right.Kind == ir.ExprStringLit {
			re, err := regexp.Compile(right.String)
			if err != nil {
				return exprType{}, spanError(right.Span, "invalid regex pattern %q: %s", right.String, err.Error())
			}
			if v.program.ValidatedRegexes == nil {
				v.program.ValidatedRegexes = make(map[string]*regexp.Regexp)
			}
			v.program.ValidatedRegexes[right.String] = re
		}
		return exprType{Base: schemaBaseBoolean}, nil
	default:
		return exprType{Base: schemaBaseBoolean}, nil
	}
}

func (v *programValidator) validateQuantifier(expr *ir.Expr, env *validationEnv) (exprType, error) {
	if _, err := v.validateExpr(expr.Collection, env); err != nil {
		return exprType{}, err
	}
	bodyEnv := env.clone()
	if schema := v.factSchemaForCollection(expr.Collection); schema != nil {
		bodyEnv.bind(expr.VarName, bindingInfo{typ: exprType{Base: schemaBaseObject, Schema: schema}})
	}
	if _, err := v.validateExpr(expr.Body, bodyEnv); err != nil {
		return exprType{}, err
	}
	return exprType{Base: schemaBaseBoolean}, nil
}

func (v *programValidator) validateAggregate(expr *ir.Expr, env *validationEnv) (exprType, error) {
	if _, err := v.validateExpr(expr.Collection, env); err != nil {
		return exprType{}, err
	}
	bodyEnv := env.clone()
	if schema := v.factSchemaForCollection(expr.Collection); schema != nil {
		bodyEnv.bind(expr.VarName, bindingInfo{typ: exprType{Base: schemaBaseObject, Schema: schema}})
	}
	if expr.HasValueExpr {
		valueType, err := v.validateExpr(expr.ValueExpr, bodyEnv)
		if err != nil {
			return exprType{}, err
		}
		if valueType.isKnown() && valueType.Base != schemaBaseNumber {
			return exprType{}, spanError(expr.Span, "aggregate %s expects numeric values, got %s", expr.AggregateKind, valueType.String())
		}
	}
	return exprType{Base: schemaBaseNumber}, nil
}

func (v *programValidator) validateBuiltinCall(expr *ir.Expr, env *validationEnv) (exprType, error) {
	if expr == nil {
		return exprType{}, nil
	}
	switch expr.FuncName {
	case "now":
		if len(expr.Args) != 0 {
			return exprType{}, spanError(expr.Span, "builtin now expects 0 arguments")
		}
		return exprType{Base: schemaBaseTimestamp}, nil
	case "abs", "round", "floor", "ceil":
		if len(expr.Args) != 1 {
			return exprType{}, spanError(expr.Span, "builtin %s expects 1 argument", expr.FuncName)
		}
		argType, err := v.validateExpr(expr.Args[0], env)
		if err != nil {
			return exprType{}, err
		}
		if argType.isKnown() && argType.Base != schemaBaseNumber && !(expr.FuncName == "abs" && argType.Base == schemaBaseDecimal) {
			return exprType{}, spanError(expr.Span, "builtin %s expects a number, got %s", expr.FuncName, argType.String())
		}
		return argType, nil
	case "min", "max":
		if len(expr.Args) != 2 {
			return exprType{}, spanError(expr.Span, "builtin %s expects 2 arguments", expr.FuncName)
		}
		leftType, err := v.validateExpr(expr.Args[0], env)
		if err != nil {
			return exprType{}, err
		}
		rightType, err := v.validateExpr(expr.Args[1], env)
		if err != nil {
			return exprType{}, err
		}
		if leftType.isKnown() && leftType.Base != schemaBaseNumber && leftType.Base != schemaBaseDecimal {
			return exprType{}, spanError(expr.Span, "builtin %s expects numeric arguments, got %s", expr.FuncName, leftType.String())
		}
		if rightType.isKnown() && rightType.Base != schemaBaseNumber && rightType.Base != schemaBaseDecimal {
			return exprType{}, spanError(expr.Span, "builtin %s expects numeric arguments, got %s", expr.FuncName, rightType.String())
		}
		if incompatibleTypes(leftType, rightType) {
			return exprType{}, spanError(expr.Span, "builtin %s arguments must share a compatible type", expr.FuncName)
		}
		if leftType.Base == schemaBaseDecimal || rightType.Base == schemaBaseDecimal {
			return mergeDecimalTypes(leftType, rightType), nil
		}
		return mergeNumberTypes(leftType, rightType), nil
	default:
		return exprType{}, spanError(expr.Span, "unknown builtin %q", expr.FuncName)
	}
}

func (v *programValidator) validateLookup(expr *ir.Expr, env *validationEnv) (exprType, error) {
	if expr == nil {
		return exprType{}, nil
	}
	// Verify the referenced table exists.
	table, ok := v.program.TableByName(expr.TableName)
	if !ok {
		return exprType{}, spanError(expr.Span, "lookup references unknown table %q", expr.TableName)
	}
	// Build a set of valid column names for subsequent checks.
	colSet := make(map[string]struct{}, len(table.Columns))
	for _, col := range table.Columns {
		colSet[col.Name] = struct{}{}
	}
	// Validate the where clause — must be boolean.
	if expr.Where != 0 {
		if err := v.validateCondition(expr.Where, env, "lookup "+expr.TableName); err != nil {
			return exprType{}, err
		}
	}
	// Validate the sort column.
	if expr.SortCol != "" {
		if _, ok := colSet[expr.SortCol]; !ok {
			return exprType{}, spanError(expr.Span, "lookup %s: sort column %q is not a valid column name",
				expr.TableName, expr.SortCol)
		}
	}
	// Validate the else keys.
	for _, key := range expr.ElseKeys {
		if _, ok := colSet[key]; !ok {
			return exprType{}, spanError(expr.Span, "lookup %s: else key %q is not a valid column name",
				expr.TableName, key)
		}
	}
	// Warn if no else is provided.
	if len(expr.ElseKeys) == 0 {
		v.warn(expr.Span, "lookup without else may return null")
	}
	// The result is an opaque object (table row or null); callers use field access on it.
	return exprType{}, nil
}

func (v *programValidator) factSchemaForCollection(exprID ir.ExprID) *ir.FactSchema {
	expr := v.program.Expr(exprID)
	if expr == nil || expr.Kind != ir.ExprVarRef {
		return nil
	}
	parts := strings.Split(expr.Path, ".")
	if len(parts) != 2 || parts[0] != "facts" {
		return nil
	}
	schema, _ := v.program.FactSchemaByName(parts[1])
	return schema
}

func factSchemaField(schema *ir.FactSchema, name string) (ir.SchemaField, bool) {
	if schema == nil {
		return ir.SchemaField{}, false
	}
	switch name {
	case "type":
		return ir.SchemaField{Name: "type", Type: ir.FieldType{Base: schemaBaseString}, Required: true}, true
	case "__asserted_at":
		return ir.SchemaField{Name: "__asserted_at", Type: ir.FieldType{Base: schemaBaseTimestamp}, Required: true}, true
	case "__age_seconds":
		return ir.SchemaField{Name: "__age_seconds", Type: ir.FieldType{Base: schemaBaseNumber, Dimension: "time"}, Required: true}, true
	}
	for _, field := range schema.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return ir.SchemaField{}, false
}

func outcomeSchemaField(schema *ir.OutcomeSchema, name string) (ir.SchemaField, bool) {
	if schema == nil {
		return ir.SchemaField{}, false
	}
	for _, field := range schema.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return ir.SchemaField{}, false
}

func assignableToField(valueType exprType, field ir.SchemaField) bool {
	if !valueType.isKnown() {
		return true
	}
	if valueType.Base == schemaBaseNull {
		return !field.Required
	}
	if valueType.Base != field.Type.Base {
		return false
	}
	if field.Type.Base == schemaBaseNumber || field.Type.Base == schemaBaseDecimal {
		if field.Type.Dimension == "" {
			return valueType.Dimension == ""
		}
		return valueType.Dimension == field.Type.Dimension
	}
	return true
}

func incompatibleTypes(left, right exprType) bool {
	if !left.isKnown() || !right.isKnown() {
		return false
	}
	if left.Base == schemaBaseNull || right.Base == schemaBaseNull {
		return false
	}
	if left.Base != right.Base {
		return true
	}
	if left.Base == schemaBaseNumber {
		return !compatibleNumberDimensions(left, right)
	}
	if left.Base == schemaBaseDecimal {
		return !compatibleDecimalTypes(left, right)
	}
	return false
}

func fieldTypeString(field ir.SchemaField) string {
	base := field.Type.Base
	if base == "" {
		base = schemaBaseUnknown
	}
	if field.Type.Dimension != "" {
		base += "<" + field.Type.Dimension + ">"
	}
	if !field.Required {
		return base + "?"
	}
	return base
}

func (t exprType) isKnown() bool {
	return t.Base != schemaBaseUnknown || t.Schema != nil
}

func (t exprType) String() string {
	switch {
	case t.Schema != nil:
		return "fact " + t.Schema.Name
	case t.Base == "":
		return "unknown"
	case t.Base == schemaBaseNumber && t.Dimension != "":
		if t.Optional {
			return "number<" + t.Dimension + ">?"
		}
		return "number<" + t.Dimension + ">"
	case t.Base == schemaBaseDecimal:
		var base string
		if t.Dimension != "" {
			base = "decimal<" + t.Dimension + ">"
		} else {
			base = "decimal"
		}
		if t.Unit != "" {
			base += "[" + t.Unit + "]"
		}
		if t.Optional {
			return base + "?"
		}
		return base
	case t.Optional && t.Base != schemaBaseNull:
		return t.Base + "?"
	default:
		return t.Base
	}
}

func compatibleNumberDimensions(left, right exprType) bool {
	if left.Base != schemaBaseNumber || right.Base != schemaBaseNumber {
		return true
	}
	if left.Dimension == "" || right.Dimension == "" {
		return left.Dimension == right.Dimension
	}
	return left.Dimension == right.Dimension
}

func compatibleDecimalTypes(left, right exprType) bool {
	if left.Base != schemaBaseDecimal || right.Base != schemaBaseDecimal {
		return true
	}
	if left.Dimension == "" || right.Dimension == "" {
		if left.Dimension != right.Dimension {
			return false
		}
		return left.Unit == "" || right.Unit == "" || left.Unit == right.Unit
	}
	if left.Dimension != right.Dimension {
		return false
	}
	return left.Unit == "" || right.Unit == "" || left.Unit == right.Unit
}

func isTimestampWithTimeDuration(left, right exprType) bool {
	return left.Base == schemaBaseTimestamp &&
		right.Base == schemaBaseNumber &&
		right.Dimension == "time"
}

func mergeNumberTypes(left, right exprType) exprType {
	if left.Dimension != "" {
		return exprType{Base: schemaBaseNumber, Dimension: left.Dimension}
	}
	if right.Dimension != "" {
		return exprType{Base: schemaBaseNumber, Dimension: right.Dimension}
	}
	return exprType{Base: schemaBaseNumber}
}

func mergeDecimalTypes(left, right exprType) exprType {
	out := exprType{Base: schemaBaseDecimal}
	if left.Dimension != "" {
		out.Dimension = left.Dimension
	} else {
		out.Dimension = right.Dimension
	}
	if left.Unit != "" && (right.Unit == "" || left.Unit == right.Unit) {
		out.Unit = left.Unit
	} else if right.Unit != "" {
		out.Unit = right.Unit
	}
	return out
}

func spanError(span ir.Span, format string, args ...any) error {
	return &positionedError{
		Line:    int(span.StartRow) + 1,
		Column:  int(span.StartCol) + 1,
		Message: fmt.Sprintf(format, args...),
		Err:     fmt.Errorf(format, args...),
	}
}
