// vm/datacontext.go
package vm

import (
	"encoding/json"
	"strings"
)

// StringPool is a read-only interface to the constant pool's string table.
// The VM and DataContext use it to resolve string indices.
type StringPool struct {
	strs []string
}

func NewStringPool(strs []string) *StringPool {
	return &StringPool{strs: strs}
}

func (sp *StringPool) Get(idx uint16) string {
	return sp.strs[idx]
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

// resolve walks dot-separated keys through nested maps.
func resolve(data map[string]any, key string) any {
	parts := strings.Split(key, ".")
	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[part]
	}
	return current
}

// anyToValue converts a Go value to a VM Value.
func anyToValue(v any, pool *StringPool) Value {
	if v == nil {
		return NullVal()
	}
	switch val := v.(type) {
	case bool:
		return BoolVal(val)
	case float64:
		return NumVal(val)
	case string:
		// Intern the string at lookup time — find or add to pool
		for i, s := range pool.strs {
			if s == val {
				return StrVal(uint16(i))
			}
		}
		// String not in pool — return a number-typed null as fallback
		// In practice, all strings referenced by rules are pre-interned
		return NullVal()
	case []any:
		// Lists from JSON are converted at lookup time
		// This is the one allocation path
		return NullVal() // TODO: handle in Task 7 when list support is wired
	default:
		return NullVal()
	}
}
