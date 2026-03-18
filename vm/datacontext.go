// vm/datacontext.go
package vm

import (
	"encoding/json"
	"strings"
)

// StringPool holds interned strings. It is initialized from the constant pool
// but can grow at runtime when data values contain strings not seen at compile time.
type StringPool struct {
	strs  []string
	index map[string]uint16
}

func NewStringPool(strs []string) *StringPool {
	idx := make(map[string]uint16, len(strs))
	for i, s := range strs {
		idx[s] = uint16(i)
	}
	return &StringPool{strs: strs, index: idx}
}

func (sp *StringPool) Get(idx uint16) string {
	if int(idx) >= len(sp.strs) {
		return ""
	}
	return sp.strs[idx]
}

// Intern returns the index for a string, adding it to the pool if not present.
func (sp *StringPool) Intern(s string) uint16 {
	if idx, ok := sp.index[s]; ok {
		return idx
	}
	idx := uint16(len(sp.strs))
	sp.strs = append(sp.strs, s)
	sp.index[s] = idx
	return idx
}

// DataContext provides variable lookup for the VM.
type DataContext interface {
	Get(key string) Value
}

// mapContext wraps a map[string]any with dot-notation key traversal.
type mapContext struct {
	data map[string]any
	pool *StringPool
}

// DataFromMap creates a DataContext from a Go map.
func DataFromMap(m map[string]any, pool *StringPool) DataContext {
	return &mapContext{data: m, pool: pool}
}

// DataFromJSON parses JSON into a DataContext.
func DataFromJSON(jsonStr string, pool *StringPool) (DataContext, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return nil, err
	}
	return &mapContext{data: m, pool: pool}, nil
}

func (mc *mapContext) Get(key string) Value {
	val := resolve(mc.data, key)
	return anyToValue(val, mc.pool)
}

// resolve walks dot-separated keys through nested values.
func resolve(current any, key string) any {
	parts := strings.Split(key, ".")
	for _, part := range parts {
		current = resolvePart(current, part)
		if current == nil {
			return nil
		}
	}
	return current
}

func resolvePart(current any, part string) any {
	switch v := current.(type) {
	case map[string]any:
		return v[part]
	}

	return nil
}

// anyToValue converts a Go value to a VM Value.
func anyToValue(v any, pool *StringPool) Value {
	if v == nil {
		return NullVal()
	}
	switch val := v.(type) {
	case Value:
		return val
	case bool:
		return BoolVal(val)
	case float64:
		return NumVal(val)
	case float32:
		return NumVal(float64(val))
	case int:
		return NumVal(float64(val))
	case int8:
		return NumVal(float64(val))
	case int16:
		return NumVal(float64(val))
	case int32:
		return NumVal(float64(val))
	case int64:
		return NumVal(float64(val))
	case uint:
		return NumVal(float64(val))
	case uint8:
		return NumVal(float64(val))
	case uint16:
		return NumVal(float64(val))
	case uint32:
		return NumVal(float64(val))
	case uint64:
		return NumVal(float64(val))
	case string:
		return StrVal(pool.Intern(val))
	case json.Number:
		if n, err := val.Float64(); err == nil {
			return NumVal(n)
		}
		return NullVal()
	case []any:
		return DynListVal(val)
	case []string:
		return DynListVal(stringsToAny(val))
	case []float64:
		return DynListVal(float64sToAny(val))
	case []float32:
		return DynListVal(float32sToAny(val))
	case []int:
		return DynListVal(intsToAny(val))
	case []int64:
		return DynListVal(int64sToAny(val))
	case []bool:
		return DynListVal(boolsToAny(val))
	case []map[string]any:
		return DynListVal(mapsToAny(val))
	case map[string]any:
		return ObjectVal(val)
	default:
		return NullVal()
	}
}

func stringsToAny(src []string) []any {
	out := make([]any, len(src))
	for i, v := range src {
		out[i] = v
	}
	return out
}

func float64sToAny(src []float64) []any {
	out := make([]any, len(src))
	for i, v := range src {
		out[i] = v
	}
	return out
}

func float32sToAny(src []float32) []any {
	out := make([]any, len(src))
	for i, v := range src {
		out[i] = v
	}
	return out
}

func intsToAny(src []int) []any {
	out := make([]any, len(src))
	for i, v := range src {
		out[i] = v
	}
	return out
}

func int64sToAny(src []int64) []any {
	out := make([]any, len(src))
	for i, v := range src {
		out[i] = v
	}
	return out
}

func boolsToAny(src []bool) []any {
	out := make([]any, len(src))
	for i, v := range src {
		out[i] = v
	}
	return out
}

func mapsToAny(src []map[string]any) []any {
	out := make([]any, len(src))
	for i, v := range src {
		out[i] = v
	}
	return out
}
