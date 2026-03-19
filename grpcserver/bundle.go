package grpcserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/flags"
)

// Bundle is a published governed artifact available over gRPC.
type Bundle struct {
	ID              string
	Name            string
	Checksum        string
	Source          []byte
	Published       time.Time
	Compiled        *arbiter.CompileResult
	Expert          *expert.Program
	Flags           *flags.Flags
	RuleCount       int
	ExpertRuleCount int
	FlagCount       int
}

type bundleRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Checksum  string    `json:"checksum"`
	Source    []byte    `json:"source"`
	Published time.Time `json:"published"`
}

type registrySnapshot struct {
	Bundles []bundleRecord      `json:"bundles,omitempty"`
	History map[string][]string `json:"history,omitempty"`
	Active  map[string]string   `json:"active,omitempty"`
}

// Registry stores published bundles and optional active versions per bundle name.
type Registry struct {
	mu      sync.RWMutex
	bundles map[string]*Bundle
	history map[string][]string
	active  map[string]string
	path    string
}

// NewRegistry creates an empty in-memory bundle registry.
func NewRegistry() *Registry {
	return &Registry{
		bundles: make(map[string]*Bundle),
		history: make(map[string][]string),
		active:  make(map[string]string),
	}
}

// NewFileRegistry loads and persists bundle state to one JSON file.
func NewFileRegistry(path string) (*Registry, error) {
	reg := NewRegistry()
	if err := reg.UseFile(path); err != nil {
		return nil, err
	}
	return reg, nil
}

// UseFile enables file-backed persistence for bundle state.
func (r *Registry) UseFile(path string) error {
	if path == "" {
		r.mu.Lock()
		r.path = ""
		r.mu.Unlock()
		return nil
	}
	if err := r.loadFileIfExists(path); err != nil {
		return err
	}
	r.mu.Lock()
	r.path = path
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	return saveRegistrySnapshot(path, snapshot)
}

// Publish compiles and stores a governed bundle. The newest published version
// becomes the active version for its name.
func (r *Registry) Publish(name string, source []byte) (*Bundle, error) {
	record := bundleRecord{
		ID:        bundleIdentity(name, source),
		Name:      name,
		Checksum:  sourceChecksum(source),
		Source:    append([]byte(nil), source...),
		Published: time.Now().UTC(),
	}

	r.mu.RLock()
	if existing, ok := r.bundles[record.ID]; ok {
		r.mu.RUnlock()
		return existing, nil
	}
	r.mu.RUnlock()

	bundle, err := compileBundleRecord(record)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if existing, ok := r.bundles[bundle.ID]; ok {
		r.mu.Unlock()
		return existing, nil
	}
	r.bundles[bundle.ID] = bundle
	if !slices.Contains(r.history[bundle.Name], bundle.ID) {
		r.history[bundle.Name] = append(r.history[bundle.Name], bundle.ID)
	}
	r.active[bundle.Name] = bundle.ID
	snapshot := r.snapshotLocked()
	r.mu.Unlock()

	if err := r.persistSnapshot(snapshot); err != nil {
		return nil, err
	}
	return bundle, nil
}

// Get returns a previously published bundle by ID.
func (r *Registry) Get(id string) (*Bundle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	bundle, ok := r.bundles[id]
	return bundle, ok
}

// GetActive returns the active bundle for one bundle name.
func (r *Registry) GetActive(name string) (*Bundle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.active[name]
	if !ok {
		return nil, false
	}
	bundle, ok := r.bundles[id]
	return bundle, ok
}

// Resolve returns a bundle by explicit ID or active bundle name.
func (r *Registry) Resolve(id, name string) (*Bundle, error) {
	if id != "" {
		if bundle, ok := r.Get(id); ok {
			return bundle, nil
		}
		return nil, fmt.Errorf("bundle %q not found", id)
	}
	if name != "" {
		if bundle, ok := r.GetActive(name); ok {
			return bundle, nil
		}
		return nil, fmt.Errorf("active bundle %q not found", name)
	}
	return nil, fmt.Errorf("bundle_id or bundle_name is required")
}

// List returns all bundles, optionally filtered by name, newest first.
func (r *Registry) List(name string) []*Bundle {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []*Bundle
	if name != "" {
		for _, id := range r.history[name] {
			if bundle, ok := r.bundles[id]; ok {
				out = append(out, bundle)
			}
		}
	} else {
		out = make([]*Bundle, 0, len(r.bundles))
		for _, bundle := range r.bundles {
			out = append(out, bundle)
		}
	}
	slices.SortFunc(out, func(a, b *Bundle) int {
		switch {
		case a.Published.After(b.Published):
			return -1
		case a.Published.Before(b.Published):
			return 1
		default:
			return 0
		}
	})
	return out
}

// Activate switches the active bundle for one bundle name.
func (r *Registry) Activate(name, id string) (*Bundle, error) {
	r.mu.Lock()
	bundle, ok := r.bundles[id]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("bundle %q not found", id)
	}
	if bundle.Name != name {
		r.mu.Unlock()
		return nil, fmt.Errorf("bundle %q belongs to %q, not %q", id, bundle.Name, name)
	}
	r.active[name] = id
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	if err := r.persistSnapshot(snapshot); err != nil {
		return nil, err
	}
	return bundle, nil
}

// Rollback switches the active bundle to the previous published version.
func (r *Registry) Rollback(name string) (*Bundle, *Bundle, error) {
	r.mu.Lock()
	history := r.history[name]
	if len(history) < 2 {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("bundle %q has no previous version", name)
	}
	currentID := r.active[name]
	currentPos := -1
	for i, id := range history {
		if id == currentID {
			currentPos = i
			break
		}
	}
	if currentPos <= 0 {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("bundle %q has no previous version", name)
	}
	previousID := history[currentPos-1]
	current := r.bundles[currentID]
	previous := r.bundles[previousID]
	if previous == nil {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("previous bundle %q not found", previousID)
	}
	r.active[name] = previousID
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	if err := r.persistSnapshot(snapshot); err != nil {
		return nil, nil, err
	}
	return previous, current, nil
}

func (r *Registry) persistSnapshot(snapshot registrySnapshot) error {
	if r == nil || r.path == "" {
		return nil
	}
	return saveRegistrySnapshot(r.path, snapshot)
}

func (r *Registry) snapshotLocked() registrySnapshot {
	out := registrySnapshot{
		Bundles: make([]bundleRecord, 0, len(r.bundles)),
		History: make(map[string][]string, len(r.history)),
		Active:  make(map[string]string, len(r.active)),
	}
	for _, bundle := range r.bundles {
		out.Bundles = append(out.Bundles, bundleRecord{
			ID:        bundle.ID,
			Name:      bundle.Name,
			Checksum:  bundle.Checksum,
			Source:    append([]byte(nil), bundle.Source...),
			Published: bundle.Published,
		})
	}
	for name, ids := range r.history {
		out.History[name] = append([]string(nil), ids...)
	}
	for name, id := range r.active {
		out.Active[name] = id
	}
	return out
}

func (r *Registry) loadFileIfExists(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var snapshot registrySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	bundles := make(map[string]*Bundle, len(snapshot.Bundles))
	for _, record := range snapshot.Bundles {
		bundle, err := compileBundleRecord(record)
		if err != nil {
			return fmt.Errorf("load bundle %s: %w", record.ID, err)
		}
		bundles[bundle.ID] = bundle
	}
	r.mu.Lock()
	r.bundles = bundles
	r.history = make(map[string][]string, len(snapshot.History))
	for name, ids := range snapshot.History {
		r.history[name] = append([]string(nil), ids...)
	}
	r.active = make(map[string]string, len(snapshot.Active))
	for name, id := range snapshot.Active {
		r.active[name] = id
	}
	r.mu.Unlock()
	return nil
}

func compileBundleRecord(record bundleRecord) (*Bundle, error) {
	parsed, err := arbiter.ParseSource(record.Source)
	if err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}
	compiled, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, fmt.Errorf("compile rules: %w", err)
	}
	flagSet, err := flags.LoadParsed(parsed, compiled)
	if err != nil {
		return nil, fmt.Errorf("compile flags: %w", err)
	}
	expertProgram, err := expert.CompileParsed(parsed, compiled)
	if err != nil {
		return nil, fmt.Errorf("compile expert rules: %w", err)
	}
	return &Bundle{
		ID:              record.ID,
		Name:            record.Name,
		Checksum:        record.Checksum,
		Source:          append([]byte(nil), record.Source...),
		Published:       record.Published,
		Compiled:        compiled,
		Expert:          expertProgram,
		Flags:           flagSet,
		RuleCount:       len(compiled.Ruleset.Rules),
		ExpertRuleCount: len(expertProgram.Rules()),
		FlagCount:       flagSet.Count(),
	}, nil
}

func bundleIdentity(name string, source []byte) string {
	sum := sha256.Sum256(append(append([]byte(name), 0), source...))
	return hex.EncodeToString(sum[:])[:16]
}

func sourceChecksum(source []byte) string {
	sum := sha256.Sum256(source)
	return hex.EncodeToString(sum[:])
}

func saveRegistrySnapshot(path string, snapshot registrySnapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
