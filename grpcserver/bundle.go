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

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/flags"
)

type bundleEventType string

const (
	bundleEventPublished  bundleEventType = "published"
	bundleEventActivated  bundleEventType = "activated"
	bundleEventRolledBack bundleEventType = "rolled_back"
)

type bundleEvent struct {
	Type             bundleEventType
	Bundle           *Bundle
	PreviousBundleID string
}

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
	StrategyCount   int
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

// PersistenceStatus reports the health of file-backed bundle persistence.
type PersistenceStatus struct {
	Configured    bool
	Durable       bool
	File          string
	Healthy       bool
	WritesTotal   uint64
	ErrorsTotal   uint64
	LastSuccessAt time.Time
	LastError     string
	LastErrorAt   time.Time
}

// Registry stores published bundles and optional active versions per bundle name.
type Registry struct {
	mu          sync.RWMutex
	bundles     map[string]*Bundle
	history     map[string][]string
	active      map[string]string
	subscribers map[uint64]chan bundleEvent
	nextSubID   uint64
	path        string
	persistence *persistenceTracker
}

type persistenceTracker struct {
	mu sync.RWMutex

	configured bool
	durable    bool
	file       string

	writesTotal   uint64
	errorsTotal   uint64
	lastSuccessAt time.Time
	lastError     string
	lastErrorAt   time.Time
}

func newPersistenceTracker() *persistenceTracker {
	return &persistenceTracker{}
}

func (t *persistenceTracker) Configure(configured bool, durable bool, file string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.configured != configured || t.durable != durable || t.file != file {
		t.writesTotal = 0
		t.errorsTotal = 0
		t.lastSuccessAt = time.Time{}
		t.lastError = ""
		t.lastErrorAt = time.Time{}
	}
	t.configured = configured
	t.durable = durable
	t.file = file
}

func (t *persistenceTracker) Snapshot() PersistenceStatus {
	if t == nil {
		return PersistenceStatus{Healthy: true}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.configured {
		return PersistenceStatus{Healthy: true}
	}
	healthy := true
	if !t.lastErrorAt.IsZero() && (t.lastSuccessAt.IsZero() || t.lastErrorAt.After(t.lastSuccessAt)) {
		healthy = false
	}
	return PersistenceStatus{
		Configured:    true,
		Durable:       t.durable,
		File:          t.file,
		Healthy:       healthy,
		WritesTotal:   t.writesTotal,
		ErrorsTotal:   t.errorsTotal,
		LastSuccessAt: t.lastSuccessAt,
		LastError:     t.lastError,
		LastErrorAt:   t.lastErrorAt,
	}
}

func (t *persistenceTracker) RecordSuccess(at time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.configured {
		return
	}
	t.writesTotal++
	t.lastSuccessAt = at.UTC()
}

func (t *persistenceTracker) RecordError(at time.Time, err error) {
	if t == nil || err == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.configured {
		return
	}
	t.errorsTotal++
	t.lastError = err.Error()
	t.lastErrorAt = at.UTC()
}

// BuildBundle compiles one bundle payload without mutating a registry.
func BuildBundle(name string, source []byte, published time.Time) (*Bundle, error) {
	if published.IsZero() {
		published = time.Now().UTC()
	} else {
		published = published.UTC()
	}
	return compileBundleRecord(bundleRecord{
		ID:        bundleIdentity(name, source),
		Name:      name,
		Checksum:  sourceChecksum(source),
		Source:    append([]byte(nil), source...),
		Published: published,
	})
}

// NewRegistry creates an empty in-memory bundle registry.
func NewRegistry() *Registry {
	return &Registry{
		bundles:     make(map[string]*Bundle),
		history:     make(map[string][]string),
		active:      make(map[string]string),
		subscribers: make(map[uint64]chan bundleEvent),
		persistence: newPersistenceTracker(),
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if path == "" {
		r.path = ""
		r.persistence.Configure(false, false, "")
		return nil
	}
	if r.path == "" {
		r.persistence.Configure(true, true, path)
	}

	snapshot, bundles, err := readRegistrySnapshot(path)
	if err != nil {
		r.persistence.RecordError(time.Now().UTC(), err)
		return err
	}
	if snapshot == nil {
		current := r.snapshotLocked()
		snapshot = &current
	}
	if err := saveRegistrySnapshot(path, *snapshot); err != nil {
		r.persistence.RecordError(time.Now().UTC(), err)
		return err
	}
	if bundles != nil {
		r.bundles = bundles
		r.history = snapshot.History
		r.active = snapshot.Active
	}
	r.path = path
	r.persistence.Configure(true, true, path)
	r.persistence.RecordSuccess(time.Now().UTC())
	return nil
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
	defer r.mu.Unlock()
	if existing, ok := r.bundles[bundle.ID]; ok {
		return existing, nil
	}
	if _, _, _, err := r.installLocked(bundle, true); err != nil {
		return nil, err
	}
	r.notifyLocked(bundleEvent{
		Type:   bundleEventPublished,
		Bundle: bundle,
	})
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
	if name != "" {
		return listBundlesLocked(r.bundles, r.history, []string{name})
	}
	return listBundlesLocked(r.bundles, r.history, nil)
}

// ActiveBundles returns the active bundle for each requested name.
// If names is empty, it returns all active bundles.
func (r *Registry) ActiveBundles(names []string) []*Bundle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return activeBundlesLocked(r.bundles, r.active, names)
}

func activeBundlesLocked(bundles map[string]*Bundle, active map[string]string, names []string) []*Bundle {
	if len(names) == 0 {
		keys := make([]string, 0, len(active))
		for name := range active {
			keys = append(keys, name)
		}
		slices.Sort(keys)

		out := make([]*Bundle, 0, len(keys))
		for _, name := range keys {
			if id, ok := active[name]; ok {
				if bundle, ok := bundles[id]; ok {
					out = append(out, bundle)
				}
			}
		}
		return out
	}

	seen := make(map[string]struct{}, len(names))
	out := make([]*Bundle, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if id, ok := active[name]; ok {
			if bundle, ok := bundles[id]; ok {
				out = append(out, bundle)
			}
		}
	}
	return out
}

// Install stores a precompiled bundle and optionally activates it.
func (r *Registry) Install(bundle *Bundle, activate bool) (*Bundle, error) {
	if bundle == nil {
		return nil, errors.New("bundle is required")
	}
	if bundle.ID == "" || bundle.Name == "" {
		return nil, errors.New("bundle id and name are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	stored, published, activated, err := r.installLocked(bundle, activate)
	if err != nil {
		return nil, err
	}
	if published {
		r.notifyLocked(bundleEvent{Type: bundleEventPublished, Bundle: stored})
	}
	if activated {
		r.notifyLocked(bundleEvent{Type: bundleEventActivated, Bundle: stored})
	}
	return stored, nil
}

func (r *Registry) installLocked(bundle *Bundle, activate bool) (*Bundle, bool, bool, error) {
	stored, exists := r.bundles[bundle.ID]
	var added, active *Bundle
	if !exists {
		stored = bundle
		added = bundle
	}
	if activate && r.active[stored.Name] != stored.ID {
		active = stored
	}
	if err := r.persistChangeLocked(added, active); err != nil {
		return nil, false, false, err
	}
	if added != nil {
		r.bundles[added.ID] = added
		r.history[added.Name] = appendBundleID(r.history[added.Name], added.ID)
	}
	if active != nil {
		r.active[active.Name] = active.ID
	}
	return stored, added != nil, active != nil, nil
}

func appendBundleID(ids []string, id string) []string {
	if slices.Contains(ids, id) {
		return ids
	}
	return append(ids, id)
}

// Activate switches the active bundle for one bundle name.
func (r *Registry) Activate(name, id string) (*Bundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bundle, ok := r.bundles[id]
	if !ok {
		return nil, fmt.Errorf("bundle %q not found", id)
	}
	if bundle.Name != name {
		return nil, fmt.Errorf("bundle %q belongs to %q, not %q", id, bundle.Name, name)
	}
	if err := r.persistChangeLocked(nil, bundle); err != nil {
		return nil, err
	}
	r.active[name] = id
	r.notifyLocked(bundleEvent{
		Type:   bundleEventActivated,
		Bundle: bundle,
	})
	return bundle, nil
}

// Rollback switches the active bundle to the previous published version.
func (r *Registry) Rollback(name string) (*Bundle, *Bundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	history := r.history[name]
	currentID := r.active[name]
	currentPos := slices.Index(history, currentID)
	if currentPos <= 0 {
		return nil, nil, fmt.Errorf("bundle %q has no previous version", name)
	}
	previousID := history[currentPos-1]
	current := r.bundles[currentID]
	previous := r.bundles[previousID]
	if previous == nil {
		return nil, nil, fmt.Errorf("previous bundle %q not found", previousID)
	}
	if err := r.persistChangeLocked(nil, previous); err != nil {
		return nil, nil, err
	}
	r.active[name] = previousID
	r.notifyLocked(bundleEvent{
		Type:             bundleEventRolledBack,
		Bundle:           previous,
		PreviousBundleID: currentID,
	})
	return previous, current, nil
}

// Subscribe registers for bundle change events.
func (r *Registry) Subscribe() (<-chan bundleEvent, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subscribers == nil {
		r.subscribers = make(map[uint64]chan bundleEvent)
	}
	id := r.nextSubID
	r.nextSubID++
	ch := make(chan bundleEvent, 4)
	r.subscribers[id] = ch
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.subscribers, id)
			r.mu.Unlock()
		})
	}
	return ch, cancel
}

// SubscribeActive atomically captures the current active bundles and registers
// for future bundle change events.
func (r *Registry) SubscribeActive(names []string) ([]*Bundle, <-chan bundleEvent, func()) {
	return r.SubscribeBundles(names, true)
}

// SubscribeBundles atomically captures the current bundle snapshot and registers
// for future bundle change events.
func (r *Registry) SubscribeBundles(names []string, activeOnly bool) ([]*Bundle, <-chan bundleEvent, func()) {
	r.mu.Lock()
	if r.subscribers == nil {
		r.subscribers = make(map[uint64]chan bundleEvent)
	}
	id := r.nextSubID
	r.nextSubID++
	ch := make(chan bundleEvent, 4)
	r.subscribers[id] = ch
	initial := activeBundlesLocked(r.bundles, r.active, names)
	if !activeOnly {
		initial = listBundlesLocked(r.bundles, r.history, names)
	}
	r.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.subscribers, id)
			r.mu.Unlock()
		})
	}
	return initial, ch, cancel
}

func listBundlesLocked(bundles map[string]*Bundle, history map[string][]string, names []string) []*Bundle {
	var out []*Bundle
	if len(names) == 0 {
		out = make([]*Bundle, 0, len(bundles))
		for _, bundle := range bundles {
			out = append(out, bundle)
		}
	} else {
		seen := make(map[string]struct{}, len(names))
		for _, name := range names {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			for _, id := range history[name] {
				if bundle, ok := bundles[id]; ok {
					out = append(out, bundle)
				}
			}
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

// notifyLocked queues events in mutation order while holding the registry lock.
func (r *Registry) notifyLocked(event bundleEvent) {
	for _, ch := range r.subscribers {
		select {
		case ch <- event:
			continue
		default:
		}
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- event:
		default:
		}
	}
}

// persistChangeLocked writes the next snapshot before exposing it to readers.
func (r *Registry) persistChangeLocked(added, active *Bundle) error {
	if r.path == "" || (added == nil && active == nil) {
		return nil
	}
	snapshot := r.snapshotLocked()
	if added != nil {
		snapshot.Bundles = append(snapshot.Bundles, recordFromBundle(added))
		snapshot.History[added.Name] = appendBundleID(snapshot.History[added.Name], added.ID)
	}
	if active != nil {
		snapshot.Active[active.Name] = active.ID
	}
	err := saveRegistrySnapshot(r.path, snapshot)
	now := time.Now().UTC()
	if err != nil {
		r.persistence.RecordError(now, err)
		return err
	}
	r.persistence.RecordSuccess(now)
	return nil
}

// PersistenceStatus reports the health of the registry's file-backed persistence.
func (r *Registry) PersistenceStatus() PersistenceStatus {
	if r == nil {
		return PersistenceStatus{Healthy: true}
	}
	return r.persistence.Snapshot()
}

func (r *Registry) snapshotLocked() registrySnapshot {
	out := registrySnapshot{
		Bundles: make([]bundleRecord, 0, len(r.bundles)),
		History: make(map[string][]string, len(r.history)),
		Active:  make(map[string]string, len(r.active)),
	}
	for _, bundle := range r.bundles {
		out.Bundles = append(out.Bundles, recordFromBundle(bundle))
	}
	for name, ids := range r.history {
		out.History[name] = append([]string(nil), ids...)
	}
	for name, id := range r.active {
		out.Active[name] = id
	}
	return out
}

func recordFromBundle(bundle *Bundle) bundleRecord {
	return bundleRecord{
		ID:        bundle.ID,
		Name:      bundle.Name,
		Checksum:  bundle.Checksum,
		Source:    bundle.Source,
		Published: bundle.Published,
	}
}

func readRegistrySnapshot(path string) (*registrySnapshot, map[string]*Bundle, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var snapshot registrySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, nil, err
	}
	bundles := make(map[string]*Bundle, len(snapshot.Bundles))
	for _, record := range snapshot.Bundles {
		bundle, err := compileBundleRecord(record)
		if err != nil {
			return nil, nil, fmt.Errorf("load bundle %s: %w", record.ID, err)
		}
		bundles[bundle.ID] = bundle
	}
	if snapshot.History == nil {
		snapshot.History = make(map[string][]string)
	}
	if snapshot.Active == nil {
		snapshot.Active = make(map[string]string)
	}
	return &snapshot, bundles, nil
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
		StrategyCount:   compiled.Strategies.Count(),
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".registry-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		if err := file.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
