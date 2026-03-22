package expert

import (
	"encoding/json"
	"fmt"
	"time"

	dec "github.com/odvcencio/arbiter/decimal"
	"github.com/odvcencio/arbiter/ir"
	"github.com/odvcencio/arbiter/units"
)

func (p *Program) factSchema(name string) (*ir.FactSchema, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	schema, ok := p.factSchemas[name]
	return schema, ok
}

func (p *Program) outcomeSchema(name string) (*ir.OutcomeSchema, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	schema, ok := p.outcomeSchemas[name]
	return schema, ok
}

func (p *Program) validateFactFields(factType string, fields map[string]any, requireAll bool) (map[string]any, error) {
	if p == nil {
		normalized := cloneMap(fields)
		delete(normalized, "key")
		return normalized, nil
	}
	schema, ok := p.factSchema(factType)
	if !ok || schema == nil {
		normalized := cloneMap(fields)
		delete(normalized, "key")
		return normalized, nil
	}
	return normalizeFactFields(schema, fields, requireAll)
}

func (p *Program) validateOutcomeFields(outcomeType string, fields map[string]any) (map[string]any, error) {
	if p == nil {
		return cloneMap(fields), nil
	}
	schema, ok := p.outcomeSchema(outcomeType)
	if !ok || schema == nil {
		return cloneMap(fields), nil
	}
	return normalizeOutcomeFields(schema, fields)
}

func normalizeFactFields(schema *ir.FactSchema, fields map[string]any, requireAll bool) (map[string]any, error) {
	if schema == nil {
		return cloneMap(fields), nil
	}
	normalized := make(map[string]any, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for name, value := range fields {
		if name == "key" {
			continue
		}
		field, ok := runtimeFactField(schema, name)
		if !ok {
			return nil, fmt.Errorf("fact %s: unknown field %q", schema.Name, name)
		}
		next, err := normalizeSchemaValue(value, field)
		if err != nil {
			return nil, fmt.Errorf("fact %s field %q: %w", schema.Name, name, err)
		}
		normalized[name] = next
		seen[name] = struct{}{}
	}
	if requireAll {
		for _, field := range schema.Fields {
			if field.Name == "key" || !field.Required {
				continue
			}
			if _, ok := seen[field.Name]; !ok {
				return nil, fmt.Errorf("fact %s: missing required field %q", schema.Name, field.Name)
			}
		}
	}
	return normalized, nil
}

func normalizeOutcomeFields(schema *ir.OutcomeSchema, fields map[string]any) (map[string]any, error) {
	if schema == nil {
		return cloneMap(fields), nil
	}
	normalized := make(map[string]any, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for name, value := range fields {
		field, ok := runtimeOutcomeField(schema, name)
		if !ok {
			return nil, fmt.Errorf("outcome %s: unknown field %q", schema.Name, name)
		}
		next, err := normalizeSchemaValue(value, field)
		if err != nil {
			return nil, fmt.Errorf("outcome %s field %q: %w", schema.Name, name, err)
		}
		normalized[name] = next
		seen[name] = struct{}{}
	}
	for _, field := range schema.Fields {
		if !field.Required {
			continue
		}
		if _, ok := seen[field.Name]; !ok {
			return nil, fmt.Errorf("outcome %s: missing required field %q", schema.Name, field.Name)
		}
	}
	return normalized, nil
}

func runtimeFactField(schema *ir.FactSchema, name string) (ir.SchemaField, bool) {
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

func runtimeOutcomeField(schema *ir.OutcomeSchema, name string) (ir.SchemaField, bool) {
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

func normalizeSchemaValue(value any, field ir.SchemaField) (any, error) {
	if value == nil {
		if field.Required {
			return nil, fmt.Errorf("expected %s, got null", field.Type.Base)
		}
		return nil, nil
	}
	switch field.Type.Base {
	case "string":
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", value)
		}
		return s, nil
	case "boolean":
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("expected boolean, got %T", value)
		}
		return b, nil
	case "number":
		if field.Type.Dimension != "" {
			return normalizeQuantityValue(value, field)
		}
		n, ok := numericValue(value)
		if !ok {
			return nil, fmt.Errorf("expected number, got %T", value)
		}
		return n, nil
	case "decimal":
		return normalizeDecimalValue(value, field)
	case "timestamp":
		ts, ok := timestampValue(value)
		if !ok {
			return nil, fmt.Errorf("expected timestamp, got %T", value)
		}
		return ts, nil
	default:
		return nil, fmt.Errorf("unsupported field type %q", field.Type.Base)
	}
}

func normalizeQuantityValue(value any, field ir.SchemaField) (any, error) {
	switch v := value.(type) {
	case units.Quantity:
		n, entry, err := units.Normalize(v.Value, v.Unit)
		if err != nil {
			return nil, err
		}
		if entry.Dimension != field.Type.Dimension {
			return nil, fmt.Errorf("expected number<%s>, got %s", field.Type.Dimension, entry.Dimension)
		}
		return n, nil
	default:
		n, ok := numericValue(value)
		if !ok {
			return nil, fmt.Errorf("expected number<%s>, got %T", field.Type.Dimension, value)
		}
		return n, nil
	}
}

func normalizeDecimalValue(value any, field ir.SchemaField) (any, error) {
	var decimalValue dec.Value
	switch v := value.(type) {
	case dec.Value:
		decimalValue = v
	case json.Number:
		parsed, err := dec.Parse(v.String(), "")
		if err != nil {
			return nil, err
		}
		decimalValue = parsed
	case string:
		parsed, err := dec.Parse(v, "")
		if err != nil {
			return nil, err
		}
		decimalValue = parsed
	default:
		return nil, fmt.Errorf("expected decimal, got %T", value)
	}

	if field.Type.Dimension == "" {
		if decimalValue.Unit() != "" {
			return nil, fmt.Errorf("expected decimal, got %s", decimalValue.String())
		}
		return decimalValue, nil
	}
	if decimalValue.Unit() == "" {
		return nil, fmt.Errorf("expected decimal<%s>, got decimal", field.Type.Dimension)
	}
	entry, ok := units.Lookup(decimalValue.Unit())
	if !ok {
		return nil, fmt.Errorf("unknown unit %q", decimalValue.Unit())
	}
	if entry.Dimension != field.Type.Dimension {
		return nil, fmt.Errorf("expected decimal<%s>, got %s", field.Type.Dimension, entry.Dimension)
	}
	return decimalValue, nil
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func timestampValue(value any) (int64, bool) {
	switch v := value.(type) {
	case time.Time:
		return v.UTC().Unix(), true
	case string:
		ts, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return 0, false
		}
		return ts.UTC().Unix(), true
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		return int64(v), true
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		return int64(v), true
	case float64:
		return int64(v), true
	case float32:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}
