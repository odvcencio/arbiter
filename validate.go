package arbiter

import (
	"fmt"
	"strings"
	"time"

	dec "github.com/odvcencio/arbiter/decimal"
	"github.com/odvcencio/arbiter/ir"
	"github.com/odvcencio/arbiter/units"
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

func validateProgram(program *ir.Program) error {
	if program == nil {
		return nil
	}
	validator := &programValidator{program: program}
	if err := validator.normalizeSchemas(); err != nil {
		return err
	}
	return validator.validate()
}

type programValidator struct {
	program *ir.Program
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
	env := newValidationEnv()
	for i := range v.program.Consts {
		if _, err := v.validateExpr(v.program.Consts[i].Value, env); err != nil {
			return err
		}
	}
	for i := range v.program.Segments {
		if err := v.validateCondition(v.program.Segments[i].Condition, env, "segment "+v.program.Segments[i].Name); err != nil {
			return err
		}
	}
	for i := range v.program.Rules {
		if err := v.validateRule(&v.program.Rules[i]); err != nil {
			return err
		}
	}
	for i := range v.program.Flags {
		if err := v.validateFlag(&v.program.Flags[i]); err != nil {
			return err
		}
	}
	for i := range v.program.Expert {
		if err := v.validateExpertRule(&v.program.Expert[i]); err != nil {
			return err
		}
	}
	for i := range v.program.Arbiters {
		if err := v.validateArbiter(&v.program.Arbiters[i]); err != nil {
			return err
		}
	}
	return nil
}

func (v *programValidator) validateRule(rule *ir.Rule) error {
	if rule == nil {
		return nil
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
	if err := v.validateParams(rule.Action.Params, env); err != nil {
		return err
	}
	if rule.Fallback != nil {
		if err := v.validateParams(rule.Fallback.Params, env); err != nil {
			return err
		}
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
	for _, rule := range flag.Rules {
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
		return v.validateExpr(decl.Value, env)
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
	binding, ok := env.lookup(parts[0])
	if !ok {
		return exprType{}, nil
	}
	if len(parts) == 1 {
		return binding.typ, nil
	}
	if binding.typ.Schema == nil {
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
