package flags

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/vm"
)

// Flags is a compiled flag ruleset that evaluates flag state at 163ns/rule.
type Flags struct {
	mu      sync.RWMutex
	ruleset *compiler.CompiledRuleset
}

// Load compiles flag rules from .arb source.
func Load(source []byte) (*Flags, error) {
	rs, err := arbiter.Compile(source)
	if err != nil {
		return nil, fmt.Errorf("compile flags: %w", err)
	}
	return &Flags{ruleset: rs}, nil
}

// LoadFile loads and compiles flags from a file path.
func LoadFile(path string) (*Flags, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flags file: %w", err)
	}
	return Load(source)
}

// LoadEnv loads flags for a specific environment.
// Looks for flags/<env>.arb at the given base directory.
func LoadEnv(dir, env string) (*Flags, error) {
	path := dir + "/" + env + ".arb"
	return LoadFile(path)
}

// Reload atomically recompiles and swaps the ruleset from new source.
func (f *Flags) Reload(source []byte) error {
	rs, err := arbiter.Compile(source)
	if err != nil {
		return fmt.Errorf("compile flags: %w", err)
	}
	f.mu.Lock()
	f.ruleset = rs
	f.mu.Unlock()
	return nil
}

// ReloadFile atomically reloads from a file path.
func (f *Flags) ReloadFile(path string) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read flags file: %w", err)
	}
	return f.Reload(source)
}

// Enabled returns true if the flag is enabled for the given context.
// Context should include user attributes, environment, etc.
func (f *Flags) Enabled(flag string, ctx map[string]any) bool {
	return f.Variant(flag, ctx) != ""
}

// Variant returns the variant string for a flag ("treatment", "control", etc).
// Returns empty string if the flag is not enabled.
func (f *Flags) Variant(flag string, ctx map[string]any) string {
	f.mu.RLock()
	rs := f.ruleset
	f.mu.RUnlock()

	dc := vm.DataFromMap(ctx, vm.NewStringPool(rs.Constants.Strings()))
	matched, err := vm.Eval(rs, dc)
	if err != nil {
		return ""
	}

	for _, m := range matched {
		if flagVal, ok := m.Params["flag"]; ok {
			if flagStr, ok := flagVal.(string); ok && flagStr == flag {
				if variant, ok := m.Params["variant"]; ok {
					if vs, ok := variant.(string); ok {
						return vs
					}
				}
				return "enabled"
			}
		}
	}
	return ""
}

// AllFlags returns all flag states for the given context.
// Returns a map of flag name -> variant string.
func (f *Flags) AllFlags(ctx map[string]any) map[string]string {
	f.mu.RLock()
	rs := f.ruleset
	f.mu.RUnlock()

	dc := vm.DataFromMap(ctx, vm.NewStringPool(rs.Constants.Strings()))
	matched, err := vm.Eval(rs, dc)
	if err != nil {
		return nil
	}

	result := make(map[string]string)
	for _, m := range matched {
		if flagVal, ok := m.Params["flag"]; ok {
			if flagStr, ok := flagVal.(string); ok {
				variant := "enabled"
				if v, ok := m.Params["variant"]; ok {
					if vs, ok := v.(string); ok {
						variant = vs
					}
				}
				// First match wins per flag (priority ordering from VM)
				if _, exists := result[flagStr]; !exists {
					result[flagStr] = variant
				}
			}
		}
	}
	return result
}

// Bucket returns a deterministic 0-99 bucket for a user ID.
// Use this for percentage-based rollouts.
// The same user ID always gets the same bucket.
func Bucket(userID string) int {
	h := sha256.Sum256([]byte(userID))
	n := binary.BigEndian.Uint32(h[:4])
	return int(n % 100)
}

// Handler returns an HTTP handler that serves flag state as JSON.
// GET /flags?user_id=xxx&org=yyy&plan=zzz&env=production
// Response: {"new_dashboard": "treatment", "ai_features": "enabled"}
func (f *Flags) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := make(map[string]any)

		// Build context from query params
		for key, values := range r.URL.Query() {
			if len(values) > 0 {
				val := values[0]
				// Try to parse as number for numeric comparisons
				if n, err := strconv.ParseFloat(val, 64); err == nil {
					ctx[key] = n
				} else {
					ctx[key] = val
				}
			}
		}

		// Auto-inject percent_bucket if user_id is present
		if userID, ok := ctx["user_id"].(string); ok {
			ctx["percent_bucket"] = float64(Bucket(userID))
		}

		flags := f.AllFlags(ctx)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flags)
	})
}
