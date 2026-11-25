// vm/datacontext.go
package vm

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/units"
)

const (
	maxStringPoolIndex   = 1<<16 - 1
	maxStringPoolEntries = maxStringPoolIndex + 1
)

// StringPool holds compiled string constants and optionally supports
// additional caller-managed interning. All methods are safe for concurrent use.
type StringPool struct {
	mu    sync.Mutex
	strs  []string
	view  atomic.Value
	index map[string]uint16
	err   error
}

func NewStringPool(strs []string) *StringPool {
	// Copy the input slice so the pool owns its backing array.
	// The caller may pass a shared slice (e.g. intern.Pool.Strings()).
	owned := make([]string, len(strs))
	copy(owned, strs)
	var poolErr error
	if len(owned) > maxStringPoolEntries {
		poolErr = fmt.Errorf("string pool overflow: maximum unique strings is %d", maxStringPoolEntries)
		owned = owned[:maxStringPoolEntries]
	}
	idx := make(map[string]uint16, len(owned))
	for i, s := range owned {
		idx[s] = uint16(i)
	}
	sp := &StringPool{strs: owned, index: idx, err: poolErr}
	sp.view.Store(owned)
	return sp
}

// snapshot returns the strings interned so far. Indices below its length
// never change, so a VM may read compile-time constants from it directly.
func (sp *StringPool) snapshot() []string {
	if sp == nil {
		return nil
	}
	strs, _ := sp.view.Load().([]string)
	return strs
}

func (sp *StringPool) Get(idx uint16) string {
	if sp == nil {
		return ""
	}
	strs, _ := sp.view.Load().([]string)
	if int(idx) >= len(strs) {
		return ""
	}
	return strs[idx]
}

// Intern returns the index for a string, adding it to the pool if not present.
func (sp *StringPool) Intern(s string) uint16 {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if idx, ok := sp.index[s]; ok {
		return idx
	}
	if len(sp.strs) > maxStringPoolIndex {
		if sp.err == nil {
			sp.err = fmt.Errorf("string pool overflow: maximum unique strings is %d", maxStringPoolEntries)
		}
		return 0
	}
	idx := uint16(len(sp.strs))
	sp.strs = append(sp.strs, s)
	sp.index[s] = idx
	sp.view.Store(sp.strs)
	return idx
}

// Err reports any overflow detected while constructing or extending the pool.
func (sp *StringPool) Err() error {
	if sp == nil {
		return nil
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.err
}

// DataContext provides variable lookup for the VM.
//
// A context passed to a PreparedEvaluator must behave as a snapshot for the
// length of one Eval or EvalInto call:
//
//   - Get must return equal values for the same key. With IndexedDataContext,
//     GetIndexed must return equal values for the same key too.
//   - A returned Value must not change until the call returns. That includes
//     any slice or map the Value references.
//
// A prepared evaluator relies on both rules. It may read a fact once, keep
// the Value and use it for every rule in that call, so it holds the Value
// across later Get calls for other keys. A context that computes a new value
// on every read (a counter, a clock, a random draw) breaks the first rule:
// every rule in the call then sees the first value read. A context that
// returns lists backed by one reused buffer breaks the second: a later Get
// refills the buffer under a Value the evaluator still holds. Between calls
// a context may change freely.
//
// The other evaluation entry points read a fact each time a rule tests it.
// That is how they work today and not a promise; a context that meets the
// two rules works with every entry point.
type DataContext interface {
	Get(key string) Value
}

// IndexedDataContext is an optional DataContext extension for contexts that
// resolve variable names ahead of time. The VM calls GetIndexed instead of Get,
// passing the name's index in the program's string pool along with the name,
// so a context bound to one program can find a field by index instead of by
// hashing the name. GetIndexed must return what Get(key) would return, and it
// is bound by the same contract as Get: see DataContext.
type IndexedDataContext interface {
	DataContext
	GetIndexed(idx uint16, key string) Value
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
	if val, ok := mc.data[key]; ok {
		return anyToValue(val, mc.pool)
	}
	val := resolve(mc.data, key)
	return anyToValue(val, mc.pool)
}

// resolve walks dot-separated keys through nested values.
func resolve(current any, key string) any {
	for len(key) > 0 {
		part := key
		if dot := strings.IndexByte(key, '.'); dot >= 0 {
			part = key[:dot]
			key = key[dot+1:]
		} else {
			key = ""
		}
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
		return Value{Typ: TypeString, Any: val}
	case dec.Value:
		return DecimalVal(val)
	case units.Quantity:
		n, _, err := units.Normalize(val.Value, val.Unit)
		if err != nil {
			return NullVal()
		}
		return NumVal(n)
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
