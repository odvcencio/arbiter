package overrides

import "sync"

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
}

// NewStore creates an empty override store.
func NewStore() *Store {
	return &Store{
		rules:     make(map[string]map[string]RuleOverride),
		flags:     make(map[string]map[string]FlagOverride),
		flagRules: make(map[string]map[string]map[int]FlagRuleOverride),
	}
}

// SetRule stores or clears a rule override.
func (s *Store) SetRule(bundleID, ruleName string, ov RuleOverride) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ov.KillSwitch == nil && ov.Rollout == nil {
		if rules, ok := s.rules[bundleID]; ok {
			delete(rules, ruleName)
			if len(rules) == 0 {
				delete(s.rules, bundleID)
			}
		}
		return
	}

	rules := s.rules[bundleID]
	if rules == nil {
		rules = make(map[string]RuleOverride)
		s.rules[bundleID] = rules
	}
	rules[ruleName] = ov
}

// SetFlag stores or clears a flag override.
func (s *Store) SetFlag(bundleID, flagKey string, ov FlagOverride) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ov.KillSwitch == nil {
		if flags, ok := s.flags[bundleID]; ok {
			delete(flags, flagKey)
			if len(flags) == 0 {
				delete(s.flags, bundleID)
			}
		}
		return
	}

	flags := s.flags[bundleID]
	if flags == nil {
		flags = make(map[string]FlagOverride)
		s.flags[bundleID] = flags
	}
	flags[flagKey] = ov
}

// SetFlagRule stores or clears a flag rule override.
func (s *Store) SetFlagRule(bundleID, flagKey string, ruleIndex int, ov FlagRuleOverride) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
		return
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
