package overrides

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// RuleOverride overlays rule-level governance fields at runtime.
type RuleOverride struct {
	KillSwitch *bool
	Rollout    *uint16
}

// FlagOverride overlays flag-level governance fields at runtime.
type FlagOverride struct {
	KillSwitch *bool
}

// FlagRuleOverride overlays flag rule targeting fields at runtime.
type FlagRuleOverride struct {
	Rollout *uint16
}

// View exposes read-only access to runtime overrides.
type View interface {
	Rule(bundleID, ruleName string) (RuleOverride, bool)
	Flag(bundleID, flagKey string) (FlagOverride, bool)
	FlagRule(bundleID, flagKey string, ruleIndex int) (FlagRuleOverride, bool)
}

// BundleSnapshot is a serializable copy of one bundle's runtime overrides.
type BundleSnapshot struct {
	BundleID  string
	Rules     map[string]RuleOverride
	Flags     map[string]FlagOverride
	FlagRules map[string]map[int]FlagRuleOverride
}

// OverrideEventType describes why an override update was emitted.
type OverrideEventType string

const (
	OverrideEventSnapshot OverrideEventType = "snapshot"
	OverrideEventRule     OverrideEventType = "rule"
	OverrideEventFlag     OverrideEventType = "flag"
	OverrideEventFlagRule OverrideEventType = "flag_rule"
)

// OverrideEvent is one update from the control-plane override watch stream.
type OverrideEvent struct {
	Type      OverrideEventType
	BundleID  string
	RuleName  string
	FlagKey   string
	RuleIndex int
	Rule      RuleOverride
	Flag      FlagOverride
	FlagRule  FlagRuleOverride
	Snapshot  BundleSnapshot
}

// Store holds in-memory runtime overrides for governed bundles.
type Store struct {
	mu          sync.RWMutex
	rules       map[string]map[string]RuleOverride
	flags       map[string]map[string]FlagOverride
	flagRules   map[string]map[string]map[int]FlagRuleOverride
	subscribers map[uint64]overrideSubscription
	nextSubID   uint64
	path        string
}

type overrideSubscription struct {
	bundleID string
	ch       chan OverrideEvent
}

// Snapshot is a serializable copy of all stored overrides.
type Snapshot struct {
	Rules     map[string]map[string]RuleOverride             `json:"rules,omitempty"`
	Flags     map[string]map[string]FlagOverride             `json:"flags,omitempty"`
	FlagRules map[string]map[string]map[int]FlagRuleOverride `json:"flag_rules,omitempty"`
}

// NewStore creates an empty override store.
func NewStore() *Store {
	return &Store{
		rules:       make(map[string]map[string]RuleOverride),
		flags:       make(map[string]map[string]FlagOverride),
		flagRules:   make(map[string]map[string]map[int]FlagRuleOverride),
		subscribers: make(map[uint64]overrideSubscription),
	}
}

// NewFileStore loads and persists overrides to one JSON file.
func NewFileStore(path string) (*Store, error) {
	store := NewStore()
	if err := store.UseFile(path); err != nil {
		return nil, err
	}
	return store, nil
}

// UseFile enables file-backed persistence for the store.
func (s *Store) UseFile(path string) error {
	if path == "" {
		s.mu.Lock()
		s.path = ""
		s.mu.Unlock()
		return nil
	}
	if err := s.loadFileIfExists(path); err != nil {
		return err
	}
	s.mu.Lock()
	s.path = path
	s.mu.Unlock()
	return s.SaveFile(path)
}

// SetRule stores or clears a rule override.
func (s *Store) SetRule(bundleID, ruleName string, ov RuleOverride) error {
	s.mu.Lock()
	changed := false
	if ov.KillSwitch == nil && ov.Rollout == nil {
		if rules, ok := s.rules[bundleID]; ok {
			if _, ok := rules[ruleName]; ok {
				delete(rules, ruleName)
				changed = true
			}
			if len(rules) == 0 {
				delete(s.rules, bundleID)
			}
		}
		return s.persistAndNotifyLocked(OverrideEvent{
			Type:     OverrideEventRule,
			BundleID: bundleID,
			RuleName: ruleName,
			Rule:     cloneRuleOverride(ov),
		}, changed)
	}

	rules := s.rules[bundleID]
	if rules == nil {
		rules = make(map[string]RuleOverride)
		s.rules[bundleID] = rules
	}
	if current, ok := rules[ruleName]; !ok || current != ov {
		changed = true
	}
	rules[ruleName] = ov
	return s.persistAndNotifyLocked(OverrideEvent{
		Type:     OverrideEventRule,
		BundleID: bundleID,
		RuleName: ruleName,
		Rule:     cloneRuleOverride(ov),
	}, changed)
}

// SetFlag stores or clears a flag override.
func (s *Store) SetFlag(bundleID, flagKey string, ov FlagOverride) error {
	s.mu.Lock()
	changed := false
	if ov.KillSwitch == nil {
		if flags, ok := s.flags[bundleID]; ok {
			if _, ok := flags[flagKey]; ok {
				delete(flags, flagKey)
				changed = true
			}
			if len(flags) == 0 {
				delete(s.flags, bundleID)
			}
		}
		return s.persistAndNotifyLocked(OverrideEvent{
			Type:     OverrideEventFlag,
			BundleID: bundleID,
			FlagKey:  flagKey,
			Flag:     cloneFlagOverride(ov),
		}, changed)
	}

	flags := s.flags[bundleID]
	if flags == nil {
		flags = make(map[string]FlagOverride)
		s.flags[bundleID] = flags
	}
	if current, ok := flags[flagKey]; !ok || current != ov {
		changed = true
	}
	flags[flagKey] = ov
	return s.persistAndNotifyLocked(OverrideEvent{
		Type:     OverrideEventFlag,
		BundleID: bundleID,
		FlagKey:  flagKey,
		Flag:     cloneFlagOverride(ov),
	}, changed)
}

// SetFlagRule stores or clears a flag rule override.
func (s *Store) SetFlagRule(bundleID, flagKey string, ruleIndex int, ov FlagRuleOverride) error {
	s.mu.Lock()
	changed := false
	if ov.Rollout == nil {
		if flags, ok := s.flagRules[bundleID]; ok {
			if rules, ok := flags[flagKey]; ok {
				if _, ok := rules[ruleIndex]; ok {
					delete(rules, ruleIndex)
					changed = true
				}
				if len(rules) == 0 {
					delete(flags, flagKey)
				}
			}
			if len(flags) == 0 {
				delete(s.flagRules, bundleID)
			}
		}
		return s.persistAndNotifyLocked(OverrideEvent{
			Type:      OverrideEventFlagRule,
			BundleID:  bundleID,
			FlagKey:   flagKey,
			RuleIndex: ruleIndex,
			FlagRule:  cloneFlagRuleOverride(ov),
		}, changed)
	}

	flags := s.flagRules[bundleID]
	if flags == nil {
		flags = make(map[string]map[int]FlagRuleOverride)
		s.flagRules[bundleID] = flags
	}
	rules := flags[flagKey]
	if rules == nil {
		rules = make(map[int]FlagRuleOverride)
		flags[flagKey] = rules
	}
	if current, ok := rules[ruleIndex]; !ok || current != ov {
		changed = true
	}
	rules[ruleIndex] = ov
	return s.persistAndNotifyLocked(OverrideEvent{
		Type:      OverrideEventFlagRule,
		BundleID:  bundleID,
		FlagKey:   flagKey,
		RuleIndex: ruleIndex,
		FlagRule:  cloneFlagRuleOverride(ov),
	}, changed)
}

// Rule returns a rule override if present.
func (s *Store) Rule(bundleID, ruleName string) (RuleOverride, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rules := s.rules[bundleID]
	if rules == nil {
		return RuleOverride{}, false
	}
	ov, ok := rules[ruleName]
	return ov, ok
}

// Flag returns a flag override if present.
func (s *Store) Flag(bundleID, flagKey string) (FlagOverride, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	flags := s.flags[bundleID]
	if flags == nil {
		return FlagOverride{}, false
	}
	ov, ok := flags[flagKey]
	return ov, ok
}

// FlagRule returns a flag rule override if present.
func (s *Store) FlagRule(bundleID, flagKey string, ruleIndex int) (FlagRuleOverride, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	flags := s.flagRules[bundleID]
	if flags == nil {
		return FlagRuleOverride{}, false
	}
	rules := flags[flagKey]
	if rules == nil {
		return FlagRuleOverride{}, false
	}
	ov, ok := rules[ruleIndex]
	return ov, ok
}

// Snapshot returns a deep copy of the current override store.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := Snapshot{
		Rules:     make(map[string]map[string]RuleOverride, len(s.rules)),
		Flags:     make(map[string]map[string]FlagOverride, len(s.flags)),
		FlagRules: make(map[string]map[string]map[int]FlagRuleOverride, len(s.flagRules)),
	}
	for bundleID, rules := range s.rules {
		cloned := make(map[string]RuleOverride, len(rules))
		for name, ov := range rules {
			cloned[name] = cloneRuleOverride(ov)
		}
		out.Rules[bundleID] = cloned
	}
	for bundleID, flags := range s.flags {
		cloned := make(map[string]FlagOverride, len(flags))
		for key, ov := range flags {
			cloned[key] = cloneFlagOverride(ov)
		}
		out.Flags[bundleID] = cloned
	}
	for bundleID, flags := range s.flagRules {
		clonedFlags := make(map[string]map[int]FlagRuleOverride, len(flags))
		for key, rules := range flags {
			clonedRules := make(map[int]FlagRuleOverride, len(rules))
			for idx, ov := range rules {
				clonedRules[idx] = cloneFlagRuleOverride(ov)
			}
			clonedFlags[key] = clonedRules
		}
		out.FlagRules[bundleID] = clonedFlags
	}
	return out
}

// SnapshotForBundle returns a deep copy of one bundle's overrides.
func (s *Store) SnapshotForBundle(bundleID string) BundleSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return snapshotForBundleLocked(bundleID, s.rules, s.flags, s.flagRules)
}

// RestoreBundle replaces one bundle's overrides while leaving all others intact.
func (s *Store) RestoreBundle(bundleID string, snapshot Snapshot) {
	if bundleID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rules, bundleID)
	delete(s.flags, bundleID)
	delete(s.flagRules, bundleID)

	if rules, ok := snapshot.Rules[bundleID]; ok && len(rules) > 0 {
		cloned := make(map[string]RuleOverride, len(rules))
		for name, ov := range rules {
			cloned[name] = cloneRuleOverride(ov)
		}
		s.rules[bundleID] = cloned
	}
	if flags, ok := snapshot.Flags[bundleID]; ok && len(flags) > 0 {
		cloned := make(map[string]FlagOverride, len(flags))
		for key, ov := range flags {
			cloned[key] = cloneFlagOverride(ov)
		}
		s.flags[bundleID] = cloned
	}
	if flagRules, ok := snapshot.FlagRules[bundleID]; ok && len(flagRules) > 0 {
		clonedFlags := make(map[string]map[int]FlagRuleOverride, len(flagRules))
		for key, rules := range flagRules {
			clonedRules := make(map[int]FlagRuleOverride, len(rules))
			for idx, ov := range rules {
				clonedRules[idx] = cloneFlagRuleOverride(ov)
			}
			clonedFlags[key] = clonedRules
		}
		s.flagRules[bundleID] = clonedFlags
	}
}

// Subscribe registers for bundle-specific override change events.
func (s *Store) Subscribe(bundleID string) (BundleSnapshot, <-chan OverrideEvent, func()) {
	s.mu.Lock()
	if s.subscribers == nil {
		s.subscribers = make(map[uint64]overrideSubscription)
	}
	id := s.nextSubID
	s.nextSubID++
	ch := make(chan OverrideEvent, 4)
	s.subscribers[id] = overrideSubscription{bundleID: bundleID, ch: ch}
	snapshot := snapshotForBundleLocked(bundleID, s.rules, s.flags, s.flagRules)
	s.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subscribers, id)
			s.mu.Unlock()
		})
	}
	return snapshot, ch, cancel
}

// Restore replaces the store contents with a snapshot.
func (s *Store) Restore(snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = make(map[string]map[string]RuleOverride, len(snapshot.Rules))
	s.flags = make(map[string]map[string]FlagOverride, len(snapshot.Flags))
	s.flagRules = make(map[string]map[string]map[int]FlagRuleOverride, len(snapshot.FlagRules))
	for bundleID, rules := range snapshot.Rules {
		cloned := make(map[string]RuleOverride, len(rules))
		for name, ov := range rules {
			cloned[name] = cloneRuleOverride(ov)
		}
		s.rules[bundleID] = cloned
	}
	for bundleID, flags := range snapshot.Flags {
		cloned := make(map[string]FlagOverride, len(flags))
		for key, ov := range flags {
			cloned[key] = cloneFlagOverride(ov)
		}
		s.flags[bundleID] = cloned
	}
	for bundleID, flags := range snapshot.FlagRules {
		clonedFlags := make(map[string]map[int]FlagRuleOverride, len(flags))
		for key, rules := range flags {
			clonedRules := make(map[int]FlagRuleOverride, len(rules))
			for idx, ov := range rules {
				clonedRules[idx] = cloneFlagRuleOverride(ov)
			}
			clonedFlags[key] = clonedRules
		}
		s.flagRules[bundleID] = clonedFlags
	}
}

// SaveFile persists the current override snapshot to a JSON file.
func (s *Store) SaveFile(path string) error {
	snapshot := s.Snapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadFile restores a snapshot from a JSON file.
func (s *Store) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	s.Restore(snapshot)
	return nil
}

func (s *Store) snapshotLocked() Snapshot {
	out := Snapshot{
		Rules:     make(map[string]map[string]RuleOverride, len(s.rules)),
		Flags:     make(map[string]map[string]FlagOverride, len(s.flags)),
		FlagRules: make(map[string]map[string]map[int]FlagRuleOverride, len(s.flagRules)),
	}
	for bundleID, rules := range s.rules {
		cloned := make(map[string]RuleOverride, len(rules))
		for name, ov := range rules {
			cloned[name] = cloneRuleOverride(ov)
		}
		out.Rules[bundleID] = cloned
	}
	for bundleID, flags := range s.flags {
		cloned := make(map[string]FlagOverride, len(flags))
		for key, ov := range flags {
			cloned[key] = cloneFlagOverride(ov)
		}
		out.Flags[bundleID] = cloned
	}
	for bundleID, flags := range s.flagRules {
		clonedFlags := make(map[string]map[int]FlagRuleOverride, len(flags))
		for key, rules := range flags {
			clonedRules := make(map[int]FlagRuleOverride, len(rules))
			for idx, ov := range rules {
				clonedRules[idx] = cloneFlagRuleOverride(ov)
			}
			clonedFlags[key] = clonedRules
		}
		out.FlagRules[bundleID] = clonedFlags
	}
	return out
}

func (s *Store) persistAndNotifyLocked(event OverrideEvent, changed bool) error {
	path := s.path
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	if path != "" {
		if err := saveSnapshot(path, snapshot); err != nil {
			return err
		}
	}
	if changed {
		s.notify(event)
	}
	return nil
}

func (s *Store) loadFileIfExists(path string) error {
	if err := s.LoadFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func saveSnapshot(path string, snapshot Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func cloneRuleOverride(ov RuleOverride) RuleOverride {
	out := RuleOverride{}
	if ov.KillSwitch != nil {
		v := *ov.KillSwitch
		out.KillSwitch = &v
	}
	if ov.Rollout != nil {
		v := *ov.Rollout
		out.Rollout = &v
	}
	return out
}

func cloneFlagOverride(ov FlagOverride) FlagOverride {
	out := FlagOverride{}
	if ov.KillSwitch != nil {
		v := *ov.KillSwitch
		out.KillSwitch = &v
	}
	return out
}

func cloneFlagRuleOverride(ov FlagRuleOverride) FlagRuleOverride {
	out := FlagRuleOverride{}
	if ov.Rollout != nil {
		v := *ov.Rollout
		out.Rollout = &v
	}
	return out
}

func snapshotForBundleLocked(bundleID string, rules map[string]map[string]RuleOverride, flags map[string]map[string]FlagOverride, flagRules map[string]map[string]map[int]FlagRuleOverride) BundleSnapshot {
	out := BundleSnapshot{
		BundleID:  bundleID,
		Rules:     make(map[string]RuleOverride),
		Flags:     make(map[string]FlagOverride),
		FlagRules: make(map[string]map[int]FlagRuleOverride),
	}
	if ruleSet, ok := rules[bundleID]; ok {
		for name, ov := range ruleSet {
			out.Rules[name] = cloneRuleOverride(ov)
		}
	}
	if flagSet, ok := flags[bundleID]; ok {
		for key, ov := range flagSet {
			out.Flags[key] = cloneFlagOverride(ov)
		}
	}
	if flagRuleSet, ok := flagRules[bundleID]; ok {
		for key, ruleSet := range flagRuleSet {
			cloned := make(map[int]FlagRuleOverride, len(ruleSet))
			for idx, ov := range ruleSet {
				cloned[idx] = cloneFlagRuleOverride(ov)
			}
			out.FlagRules[key] = cloned
		}
	}
	return out
}

func (s *Store) notify(event OverrideEvent) {
	s.mu.RLock()
	subscribers := make([]overrideSubscription, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		if sub.bundleID == event.BundleID {
			subscribers = append(subscribers, sub)
		}
	}
	s.mu.RUnlock()

	for _, sub := range subscribers {
		select {
		case sub.ch <- event:
			continue
		default:
		}
		select {
		case <-sub.ch:
		default:
		}
		select {
		case sub.ch <- event:
		default:
		}
	}
}
