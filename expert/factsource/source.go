// Package factsource provides pluggable fact loading for expert sessions.
// Register loaders by scheme (file extension or URI prefix) and load facts
// from any supported source with a single call.
package factsource

import (
	"fmt"
	"strings"
	"sync"
)

// Fact is a single fact to feed into an expert session.
type Fact struct {
	Type   string
	Key    string
	Fields map[string]any
}

// Loader loads facts from a source path.
type Loader interface {
	LoadFacts(path string) ([]Fact, error)
}

// LoaderFunc adapts a function to the Loader interface.
type LoaderFunc func(path string) ([]Fact, error)

func (f LoaderFunc) LoadFacts(path string) ([]Fact, error) { return f(path) }

var (
	mu      sync.RWMutex
	loaders = map[string]Loader{} // extension or scheme → loader
)

// Register adds a loader for a file extension (e.g. ".csv") or URI scheme (e.g. "postgres://").
func Register(scheme string, loader Loader) {
	mu.Lock()
	defer mu.Unlock()
	loaders[scheme] = loader
}

// Load detects the source type and loads facts.
// Checks URI schemes first (postgres://, http://, s3://), then file extension.
func Load(path string) ([]Fact, error) {
	mu.RLock()
	defer mu.RUnlock()

	// Check URI schemes
	for scheme, loader := range loaders {
		if strings.Contains(scheme, "://") && strings.HasPrefix(path, scheme) {
			return loader.LoadFacts(path)
		}
	}

	// Check file extension
	ext := fileExt(path)
	if ext == "" {
		return nil, fmt.Errorf("cannot determine fact source type for %q", path)
	}
	loader, ok := loaders[ext]
	if !ok {
		return nil, fmt.Errorf("no fact source loader registered for %q", ext)
	}
	return loader.LoadFacts(path)
}

// Schemes returns all registered schemes/extensions.
func Schemes() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(loaders))
	for k := range loaders {
		out = append(out, k)
	}
	return out
}

func fileExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' || path[i] == '\\' {
			break
		}
	}
	return ""
}
