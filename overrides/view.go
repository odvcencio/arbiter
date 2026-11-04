package overrides

// FreezeBundle captures one bundle under the store's read lock. Its getters
// return copies so callers cannot change the captured values.
func (s *Store) FreezeBundle(bundleID string) View {
	if s == nil {
		return nil
	}
	return bundleView{s.SnapshotForBundle(bundleID)}
}

type bundleView struct{ snapshot BundleSnapshot }

func (v bundleView) Rule(bundleID, ruleName string) (RuleOverride, bool) {
	if bundleID != v.snapshot.BundleID {
		return RuleOverride{}, false
	}
	ov, ok := v.snapshot.Rules[ruleName]
	return cloneRuleOverride(ov), ok
}

func (v bundleView) Flag(bundleID, flagKey string) (FlagOverride, bool) {
	if bundleID != v.snapshot.BundleID {
		return FlagOverride{}, false
	}
	ov, ok := v.snapshot.Flags[flagKey]
	return cloneFlagOverride(ov), ok
}

func (v bundleView) FlagRule(bundleID, flagKey string, ruleIndex int) (FlagRuleOverride, bool) {
	if bundleID != v.snapshot.BundleID {
		return FlagRuleOverride{}, false
	}
	ov, ok := v.snapshot.FlagRules[flagKey][ruleIndex]
	return cloneFlagRuleOverride(ov), ok
}

func (v bundleView) Strategy(bundleID, strategyName, candidateLabel string) (StrategyOverride, bool) {
	if bundleID != v.snapshot.BundleID {
		return StrategyOverride{}, false
	}
	ov, ok := v.snapshot.Strategies[strategyName][candidateLabel]
	return cloneStrategyOverride(ov), ok
}
