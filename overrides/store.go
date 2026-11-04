package overrides

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"m31labs.dev/arbiter/ir"
)

// RuleOverride overlays rule-level governance fields at runtime.
type RuleOverride struct {
	KillSwitch *bool
	Rollout    *uint16
}

// KillSwitchState reports the override's explicit kill-switch mode.
func (o RuleOverride) KillSwitchState() ir.KillSwitchState {
	return killSwitchState(o.KillSwitch)
}

// FlagOverride overlays flag-level governance fields at runtime.
type FlagOverride struct {
	KillSwitch *bool
}

// KillSwitchState reports the override's explicit kill-switch mode.
func (o FlagOverride) KillSwitchState() ir.KillSwitchState {
	return killSwitchState(o.KillSwitch)
}

// FlagRuleOverride overlays flag rule targeting fields at runtime.
type FlagRuleOverride struct {
	Rollout *uint16
}

// StrategyOverride overlays strategy-candidate governance fields at runtime.
type StrategyOverride struct {
	KillSwitch *bool
	Rollout    *uint16
}

// KillSwitchState reports the override's explicit kill-switch mode.
func (o StrategyOverride) KillSwitchState() ir.KillSwitchState {
	return killSwitchState(o.KillSwitch)
}

func killSwitchState(enabled *bool) ir.KillSwitchState {
	if enabled == nil {
		return ir.KillSwitchUnset
	}
	if *enabled {
		return ir.KillSwitchOn
	}
	return ir.KillSwitchOff
}

// View exposes read-only access to runtime overrides.
type View interface {
	Rule(bundleID, ruleName string) (RuleOverride, bool)
	Flag(bundleID, flagKey string) (FlagOverride, bool)
	FlagRule(bundleID, flagKey string, ruleIndex int) (FlagRuleOverride, bool)
	Strategy(bundleID, strategyName, candidateLabel string) (StrategyOverride, bool)
}

// BundleSnapshot is a serializable copy of one bundle's runtime overrides.
type BundleSnapshot struct {
	BundleID   string
	Rules      map[string]RuleOverride
	Flags      map[string]FlagOverride
	FlagRules  map[string]map[int]FlagRuleOverride
	Strategies map[string]map[string]StrategyOverride
}

// OverrideEventType describes why an override update was emitted.
type OverrideEventType string

const (
	OverrideEventSnapshot OverrideEventType = "snapshot"
	OverrideEventRule     OverrideEventType = "rule"
	OverrideEventFlag     OverrideEventType = "flag"
	OverrideEventFlagRule OverrideEventType = "flag_rule"
	OverrideEventStrategy OverrideEventType = "strategy"
)

// OverrideEvent is one update from the control-plane override watch stream.
type OverrideEvent struct {
	Type           OverrideEventType
	BundleID       string
	RuleName       string
	FlagKey        string
	RuleIndex      int
	Rule           RuleOverride
	Flag           FlagOverride
	FlagRule       FlagRuleOverride
	StrategyName   string
	CandidateLabel string
	Strategy       StrategyOverride
	Snapshot       BundleSnapshot
}

// Store holds in-memory runtime overrides for governed bundles.
type Store struct {
	mu          sync.RWMutex
	rules       map[string]map[string]RuleOverride
	flags       map[string]map[string]FlagOverride
	flagRules   map[string]map[string]map[int]FlagRuleOverride
	strategies  map[string]map[string]map[string]StrategyOverride
	subscribers map[uint64]overrideSubscription
	nextSubID   uint64
	path        string
	persistence *persistenceTracker
}

// PersistenceStatus reports the health of file-backed override persistence.
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

type overrideSubscription struct {
	bundleID string
	ch       chan OverrideEvent
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

// Snapshot is a serializable copy of all stored overrides.
type Snapshot struct {
	Rules      map[string]map[string]RuleOverride                `json:"rules,omitempty"`
	Flags      map[string]map[string]FlagOverride                `json:"flags,omitempty"`
	FlagRules  map[string]map[string]map[int]FlagRuleOverride    `json:"flag_rules,omitempty"`
	Strategies map[string]map[string]map[string]StrategyOverride `json:"strategies,omitempty"`
}

// NewStore creates an empty override store.
func NewStore() *Store {
	return &Store{
		rules:       make(map[string]map[string]RuleOverride),
		flags:       make(map[string]map[string]FlagOverride),
		flagRules:   make(map[string]map[string]map[int]FlagRuleOverride),
		strategies:  make(map[string]map[string]map[string]StrategyOverride),
		subscribers: make(map[uint64]overrideSubscription),
		persistence: newPersistenceTracker(),
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
		s.persistence.Configure(false, false, "")
		return nil
	}
	s.persistence.Configure(true, true, path)
	if err := s.loadFileIfExists(path); err != nil {
		s.persistence.RecordError(time.Now().UTC(), err)
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
	rules[ruleName] = cloneRuleOverride(ov)
	return s.persistAndNotifyLocked(OverrideEvent{
		Type:     OverrideEventRule,
		BundleID: bundleID,
		RuleName: ruleName,
		Rule:     cloneRuleOverride(ov),
	}, true)
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
	flags[flagKey] = cloneFlagOverride(ov)
	return s.persistAndNotifyLocked(OverrideEvent{
		Type:     OverrideEventFlag,
		BundleID: bundleID,
		FlagKey:  flagKey,
		Flag:     cloneFlagOverride(ov),
	}, true)
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
	rules[ruleIndex] = cloneFlagRuleOverride(ov)
	return s.persistAndNotifyLocked(OverrideEvent{
		Type:      OverrideEventFlagRule,
		BundleID:  bundleID,
		FlagKey:   flagKey,
		RuleIndex: ruleIndex,
		FlagRule:  cloneFlagRuleOverride(ov),
	}, true)
}

// SetStrategy stores or clears a strategy-candidate override.
func (s *Store) SetStrategy(bundleID, strategyName, candidateLabel string, ov StrategyOverride) error {
	s.mu.Lock()
	changed := false
	if ov.KillSwitch == nil && ov.Rollout == nil {
		if strategies, ok := s.strategies[bundleID]; ok {
			if candidates, ok := strategies[strategyName]; ok {
				if _, ok := candidates[candidateLabel]; ok {
					delete(candidates, candidateLabel)
					changed = true
				}
				if len(candidates) == 0 {
					delete(strategies, strategyName)
				}
			}
			if len(strategies) == 0 {
				delete(s.strategies, bundleID)
			}
		}
		return s.persistAndNotifyLocked(OverrideEvent{
			Type:           OverrideEventStrategy,
			BundleID:       bundleID,
			StrategyName:   strategyName,
			CandidateLabel: candidateLabel,
			Strategy:       cloneStrategyOverride(ov),
		}, changed)
	}

	strategies := s.strategies[bundleID]
	if strategies == nil {
		strategies = make(map[string]map[string]StrategyOverride)
		s.strategies[bundleID] = strategies
	}
	candidates := strategies[strategyName]
	if candidates == nil {
		candidates = make(map[string]StrategyOverride)
		strategies[strategyName] = candidates
	}
	candidates[candidateLabel] = cloneStrategyOverride(ov)
	return s.persistAndNotifyLocked(OverrideEvent{
		Type:           OverrideEventStrategy,
		BundleID:       bundleID,
		StrategyName:   strategyName,
		CandidateLabel: candidateLabel,
		Strategy:       cloneStrategyOverride(ov),
	}, true)
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
	return cloneRuleOverride(ov), ok
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
	return cloneFlagOverride(ov), ok
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
	return cloneFlagRuleOverride(ov), ok
}

// Strategy returns a strategy-candidate override if present.
func (s *Store) Strategy(bundleID, strategyName, candidateLabel string) (StrategyOverride, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	strategies := s.strategies[bundleID]
	if strategies == nil {
		return StrategyOverride{}, false
	}
	candidates := strategies[strategyName]
	if candidates == nil {
		return StrategyOverride{}, false
	}
	ov, ok := candidates[candidateLabel]
	return cloneStrategyOverride(ov), ok
}

// Snapshot returns a deep copy of the current override store.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := Snapshot{
		Rules:      make(map[string]map[string]RuleOverride, len(s.rules)),
		Flags:      make(map[string]map[string]FlagOverride, len(s.flags)),
		FlagRules:  make(map[string]map[string]map[int]FlagRuleOverride, len(s.flagRules)),
		Strategies: make(map[string]map[string]map[string]StrategyOverride, len(s.strategies)),
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
	for bundleID, strategies := range s.strategies {
		clonedStrategies := make(map[string]map[string]StrategyOverride, len(strategies))
		for strategyName, candidates := range strategies {
			clonedCandidates := make(map[string]StrategyOverride, len(candidates))
			for candidateLabel, ov := range candidates {
				clonedCandidates[candidateLabel] = cloneStrategyOverride(ov)
			}
			clonedStrategies[strategyName] = clonedCandidates
		}
		out.Strategies[bundleID] = clonedStrategies
	}
	return out
}

// SnapshotForBundle returns a deep copy of one bundle's overrides.
func (s *Store) SnapshotForBundle(bundleID string) BundleSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return snapshotForBundleLocked(bundleID, s.rules, s.flags, s.flagRules, s.strategies)
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
	delete(s.strategies, bundleID)

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
	if strategies, ok := snapshot.Strategies[bundleID]; ok && len(strategies) > 0 {
		clonedStrategies := make(map[string]map[string]StrategyOverride, len(strategies))
		for strategyName, candidates := range strategies {
			clonedCandidates := make(map[string]StrategyOverride, len(candidates))
			for candidateLabel, ov := range candidates {
				clonedCandidates[candidateLabel] = cloneStrategyOverride(ov)
			}
			clonedStrategies[strategyName] = clonedCandidates
		}
		s.strategies[bundleID] = clonedStrategies
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
	snapshot := snapshotForBundleLocked(bundleID, s.rules, s.flags, s.flagRules, s.strategies)
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
	s.strategies = make(map[string]map[string]map[string]StrategyOverride, len(snapshot.Strategies))
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
	for bundleID, strategies := range snapshot.Strategies {
		clonedStrategies := make(map[string]map[string]StrategyOverride, len(strategies))
		for strategyName, candidates := range strategies {
			clonedCandidates := make(map[string]StrategyOverride, len(candidates))
			for candidateLabel, ov := range candidates {
				clonedCandidates[candidateLabel] = cloneStrategyOverride(ov)
			}
			clonedStrategies[strategyName] = clonedCandidates
		}
		s.strategies[bundleID] = clonedStrategies
	}
}

// SaveFile persists the current override snapshot to a JSON file.
func (s *Store) SaveFile(path string) error {
	snapshot := s.Snapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		s.persistence.RecordError(time.Now().UTC(), err)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.persistence.RecordError(time.Now().UTC(), err)
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		s.persistence.RecordError(time.Now().UTC(), err)
		return err
	}
	s.persistence.RecordSuccess(time.Now().UTC())
	return nil
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
		Rules:      make(map[string]map[string]RuleOverride, len(s.rules)),
		Flags:      make(map[string]map[string]FlagOverride, len(s.flags)),
		FlagRules:  make(map[string]map[string]map[int]FlagRuleOverride, len(s.flagRules)),
		Strategies: make(map[string]map[string]map[string]StrategyOverride, len(s.strategies)),
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
	for bundleID, strategies := range s.strategies {
		clonedStrategies := make(map[string]map[string]StrategyOverride, len(strategies))
		for strategyName, candidates := range strategies {
			clonedCandidates := make(map[string]StrategyOverride, len(candidates))
			for candidateLabel, ov := range candidates {
				clonedCandidates[candidateLabel] = cloneStrategyOverride(ov)
			}
			clonedStrategies[strategyName] = clonedCandidates
		}
		out.Strategies[bundleID] = clonedStrategies
	}
	return out
}

func (s *Store) persistAndNotifyLocked(event OverrideEvent, changed bool) error {
	path := s.path
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	if path != "" {
		if err := saveSnapshot(path, snapshot); err != nil {
			s.persistence.RecordError(time.Now().UTC(), err)
			return err
		}
		s.persistence.RecordSuccess(time.Now().UTC())
	}
	if changed {
		s.notify(event)
	}
	return nil
}

// PersistenceStatus reports the health of the store's file-backed persistence.
func (s *Store) PersistenceStatus() PersistenceStatus {
	if s == nil {
		return PersistenceStatus{Healthy: true}
	}
	return s.persistence.Snapshot()
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

func cloneStrategyOverride(ov StrategyOverride) StrategyOverride {
	out := StrategyOverride{}
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

func snapshotForBundleLocked(bundleID string, rules map[string]map[string]RuleOverride, flags map[string]map[string]FlagOverride, flagRules map[string]map[string]map[int]FlagRuleOverride, strategies map[string]map[string]map[string]StrategyOverride) BundleSnapshot {
	out := BundleSnapshot{
		BundleID:   bundleID,
		Rules:      make(map[string]RuleOverride),
		Flags:      make(map[string]FlagOverride),
		FlagRules:  make(map[string]map[int]FlagRuleOverride),
		Strategies: make(map[string]map[string]StrategyOverride),
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
	if strategySet, ok := strategies[bundleID]; ok {
		for strategyName, candidates := range strategySet {
			cloned := make(map[string]StrategyOverride, len(candidates))
			for candidateLabel, ov := range candidates {
				cloned[candidateLabel] = cloneStrategyOverride(ov)
			}
			out.Strategies[strategyName] = cloned
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
