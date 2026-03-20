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
	Type    string
	Key     string
	Fields  map[string]any
	Version int64
}

// Loader loads facts from a source path.
type Loader interface {
	LoadFacts(path string) ([]Fact, error)
}

// Saver writes a complete fact set back to a source path.
type Saver interface {
	SaveFacts(path string, facts []Fact) error
}

// LoaderFunc adapts a function to the Loader interface.
type LoaderFunc func(path string) ([]Fact, error)

func (f LoaderFunc) LoadFacts(path string) ([]Fact, error) { return f(path) }

// SaverFunc adapts a function to the Saver interface.
type SaverFunc func(path string, facts []Fact) error

func (f SaverFunc) SaveFacts(path string, facts []Fact) error { return f(path, facts) }

var (
	mu      sync.RWMutex
	loaders = map[string]Loader{} // extension or scheme → loader
	savers  = map[string]Saver{}  // extension or scheme → saver
)

// Register adds a loader for a file extension (e.g. ".csv") or URI scheme (e.g. "postgres://").
func Register(scheme string, loader Loader) {
	mu.Lock()
	defer mu.Unlock()
	loaders[scheme] = loader
}

// RegisterSaver adds a saver for a file extension (e.g. ".csv") or URI scheme (e.g. "gsheet://").
func RegisterSaver(scheme string, saver Saver) {
	mu.Lock()
	defer mu.Unlock()
	savers[scheme] = saver
}

// Load detects the source type and loads facts.
// Checks URI schemes first (postgres://, http://, s3://), then file extension.
func Load(path string) ([]Fact, error) {
	mu.RLock()
	defer mu.RUnlock()

	loader, key, err := resolveLoader(path)
	if err != nil {
		return nil, err
	}
	if loader == nil {
		return nil, fmt.Errorf("no fact source loader registered for %q", key)
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

// Save writes facts back to a supported source path using the backend's native
// sync semantics. File-backed sources replace the full target, while mutable
// stores may expose more specific behavior through URI options.
func Save(path string, facts []Fact) error {
	mu.RLock()
	defer mu.RUnlock()

	saver, key, err := resolveSaver(path)
	if err != nil {
		return err
	}
	if saver == nil {
		return fmt.Errorf("no fact source saver registered for %q", key)
	}
	return saver.SaveFacts(path, facts)
}

// WritableSchemes returns all registered saver schemes/extensions.
func WritableSchemes() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(savers))
	for k := range savers {
		out = append(out, k)
	}
	return out
}

func resolveLoader(path string) (Loader, string, error) {
	for scheme, loader := range loaders {
		if strings.Contains(scheme, "://") && strings.HasPrefix(path, scheme) {
			return loader, scheme, nil
		}
	}
	ext := fileExt(path)
	if ext == "" {
		return nil, "", fmt.Errorf("cannot determine fact source type for %q", path)
	}
	return loaders[ext], ext, nil
}

func resolveSaver(path string) (Saver, string, error) {
	for scheme, saver := range savers {
		if strings.Contains(scheme, "://") && strings.HasPrefix(path, scheme) {
			return saver, scheme, nil
		}
	}
	ext := fileExt(path)
	if ext == "" {
		return nil, "", fmt.Errorf("cannot determine fact source type for %q", path)
	}
	return savers[ext], ext, nil
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
