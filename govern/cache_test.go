package govern

import "testing"

func TestRequestCacheRuleAndFlagPrerequisites(t *testing.T) {
	rc := NewRequestCache(nil, nil)
	rc.RecordRuleResult("risk_gate", true)
	rc.RecordFlagResult("checkout_v2", "treatment", "control")

	if !rc.PrerequisiteMet("risk_gate") {
		t.Fatal("expected rule prerequisite to be met")
	}
	if !rc.PrerequisiteMet("checkout_v2") {
		t.Fatal("expected flag prerequisite to be met")
	}
	if rc.PrerequisiteMet("missing") {
		t.Fatal("missing prerequisite should be false")
	}
}

func TestRequestCacheCycleMarkers(t *testing.T) {
	rc := NewRequestCache(nil, nil)
	if rc.HasCycle("checkout_v2") {
		t.Fatal("cycle marker should start empty")
	}
	rc.Enter("checkout_v2")
	if !rc.HasCycle("checkout_v2") {
		t.Fatal("cycle marker should be set")
	}
	rc.Leave("checkout_v2")
	if rc.HasCycle("checkout_v2") {
		t.Fatal("cycle marker should be cleared")
	}
}
