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
		return StrVal(pool.Intern(val))
	case []any:
		// Lists from JSON are converted at lookup time
		// This is the one allocation path
		return NullVal() // TODO: handle in Task 7 when list support is wired
	default:
		return NullVal()
	}
}
