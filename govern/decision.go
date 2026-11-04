package govern

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"m31labs.dev/arbiter/overrides"
)

// DecisionSnapshot owns the inputs to a stateless policy decision. Each
// evaluation gets an independent fact context and the same captured overrides.
type DecisionSnapshot struct {
	facts    map[string]any
	at       time.Time
	bundleID string
	view     overrides.View
}

// NewDecisionSnapshot copies JSON-shaped facts and captures the overrides for
// bundleID. Callers must not mutate facts while the constructor copies them.
// Dotted top-level keys overlay nested facts. The reserved __now fact is
// replaced with at, supplied by the trusted host, in UTC RFC3339Nano form.
func NewDecisionSnapshot(facts map[string]any, at time.Time, bundleID string, store *overrides.Store) (DecisionSnapshot, error) {
	if at.IsZero() {
		return DecisionSnapshot{}, fmt.Errorf("decision requires an evaluation time")
	}
	cloned, err := copyDecisionFact(facts, 0)
	if err != nil {
		return DecisionSnapshot{}, err
	}
	at = at.UTC()
	normalized := NestDottedKeys(cloned.(map[string]any))
	normalized["__now"] = at.Format(time.RFC3339Nano)
	return DecisionSnapshot{facts: normalized, at: at, bundleID: bundleID, view: store.FreezeBundle(bundleID)}, nil
}

// Facts returns an independent copy of the effective facts, including __now.
func (d DecisionSnapshot) Facts() map[string]any {
	// The constructor validated the tree and owns every mutable element.
	facts, _ := copyDecisionFact(d.facts, 0)
	return facts.(map[string]any)
}

// EvalTime returns the effective evaluation time, or zero for an uninitialized snapshot.
func (d DecisionSnapshot) EvalTime() time.Time { return d.at }

// Overrides returns the bundle ID and its immutable override view.
func (d DecisionSnapshot) Overrides() (string, overrides.View) { return d.bundleID, d.view }

// RequestCache creates isolated decision-local state for segments and prerequisites.
func (d DecisionSnapshot) RequestCache(segments *SegmentSet) *RequestCache {
	cache := NewRequestCache(segments, d.Facts())
	cache.evalTime, cache.hasEvalTime = d.at, true
	return cache
}

func copyDecisionFact(value any, depth int) (any, error) {
	// Bound recursion for cycles and deeply nested application input.
	if depth > 256 {
		return nil, fmt.Errorf("decision facts exceed maximum depth")
	}
	switch val := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for key, child := range val {
			childDepth := depth + 1
			if depth == 0 {
				childDepth += strings.Count(key, ".")
			}
			copied, err := copyDecisionFact(child, childDepth)
			if err != nil {
				return nil, err
			}
			out[key] = copied
		}
		return out, nil
	case []any:
		out := make([]any, len(val))
		for index, child := range val {
			copied, err := copyDecisionFact(child, depth+1)
			if err != nil {
				return nil, err
			}
			out[index] = copied
		}
		return out, nil
	case []map[string]any:
		out := make([]map[string]any, len(val))
		for index, child := range val {
			copied, err := copyDecisionFact(child, depth+1)
			if err != nil {
				return nil, err
			}
			out[index] = copied.(map[string]any)
		}
		return out, nil
	case []string:
		return slices.Clone(val), nil
	case []bool:
		return slices.Clone(val), nil
	case []float32:
		return slices.Clone(val), nil
	case []float64:
		return slices.Clone(val), nil
	case []int:
		return slices.Clone(val), nil
	case []int64:
		return slices.Clone(val), nil
	case nil, bool, string, float32, float64, json.Number,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return val, nil
	default:
		return nil, fmt.Errorf("unsupported decision fact type %T", value)
	}
}
