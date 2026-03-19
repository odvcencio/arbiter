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
	Rollout    *uint8
}

// FlagOverride overlays flag-level governance fields at runtime.
type FlagOverride struct {
	KillSwitch *bool
}

// FlagRuleOverride overlays flag rule targeting fields at runtime.
type FlagRuleOverride struct {
	Rollout *uint8
}

// View exposes read-only access to runtime overrides.
type View interface {
	Rule(bundleID, ruleName string) (RuleOverride, bool)
	Flag(bundleID, flagKey string) (FlagOverride, bool)
	FlagRule(bundleID, flagKey string, ruleIndex int) (FlagRuleOverride, bool)
}

// Store holds in-memory runtime overrides for governed bundles.
type Store struct {
	mu        sync.RWMutex
	rules     map[string]map[string]RuleOverride
	flags     map[string]map[string]FlagOverride
	flagRules map[string]map[string]map[int]FlagRuleOverride
	path      string
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
		rules:     make(map[string]map[string]RuleOverride),
		flags:     make(map[string]map[string]FlagOverride),
		flagRules: make(map[string]map[string]map[int]FlagRuleOverride),
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

	if ov.KillSwitch == nil && ov.Rollout == nil {
		if rules, ok := s.rules[bundleID]; ok {
			delete(rules, ruleName)
			if len(rules) == 0 {
				delete(s.rules, bundleID)
			}
		}
		return s.persistLocked()
	}

	rules := s.rules[bundleID]
	if rules == nil {
		rules = make(map[string]RuleOverride)
		s.rules[bundleID] = rules
	}
	rules[ruleName] = ov
	return s.persistLocked()
}

// SetFlag stores or clears a flag override.
func (s *Store) SetFlag(bundleID, flagKey string, ov FlagOverride) error {
	s.mu.Lock()

	if ov.KillSwitch == nil {
		if flags, ok := s.flags[bundleID]; ok {
			delete(flags, flagKey)
			if len(flags) == 0 {
				delete(s.flags, bundleID)
			}
		}
		return s.persistLocked()
	}

	flags := s.flags[bundleID]
	if flags == nil {
		flags = make(map[string]FlagOverride)
		s.flags[bundleID] = flags
	}
	flags[flagKey] = ov
	return s.persistLocked()
}

// SetFlagRule stores or clears a flag rule override.
func (s *Store) SetFlagRule(bundleID, flagKey string, ruleIndex int, ov FlagRuleOverride) error {
	s.mu.Lock()

	if ov.Rollout == nil {
		if flags, ok := s.flagRules[bundleID]; ok {
			if rules, ok := flags[flagKey]; ok {
				delete(rules, ruleIndex)
				if len(rules) == 0 {
					delete(flags, flagKey)
				}
			}
			if len(flags) == 0 {
				delete(s.flagRules, bundleID)
			}
		}
		return s.persistLocked()
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
	rules[ruleIndex] = ov
	return s.persistLocked()
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

func (s *Store) persistLocked() error {
	path := s.path
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	return saveSnapshot(path, snapshot)
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
