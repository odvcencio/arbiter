package govern

import "testing"

func TestBucketDeterministic(t *testing.T) {
	first := Bucket("user_123")
	second := Bucket("user_123")
	if first != second {
		t.Fatal("bucket should be deterministic")
	}
}

func TestRolloutAllowsUsesUserID(t *testing.T) {
	ctx := map[string]any{"user.id": "user_123"}
	spec := PercentRollout{PercentBps: 2500, SubjectKey: "user.id", Namespace: "checkout_v2"}
	allowed1 := RolloutAllows(spec, ctx)
	allowed2 := RolloutAllows(spec, ctx)
	if allowed1 != allowed2 {
		t.Fatal("rollout should be deterministic for the same user")
	}
}

func TestRolloutNamespaceChangesBucket(t *testing.T) {
	subject := "user_123"
	first := RolloutBucket("bundle_a:rule:checkout", subject)
	second := RolloutBucket("bundle_a:rule:search", subject)
	if first == second {
		t.Fatal("expected namespace to change rollout bucket")
	}
}

func TestRolloutMissingSubjectFailsClosed(t *testing.T) {
	decision := DecidePercentRollout(PercentRollout{
		PercentBps: 10000,
		SubjectKey: "account.id",
		Namespace:  "launch_wave_1",
	}, map[string]any{"user.id": "user_123"})
	if decision.Allowed {
		t.Fatal("expected missing subject rollout to fail closed")
	}
	if !decision.MissingSubject {
		t.Fatal("expected decision to record missing subject")
	}
}

func TestRolloutZeroPercentBlocksEveryone(t *testing.T) {
	decision := DecidePercentRollout(PercentRollout{
		PercentBps: 0,
		SubjectKey: "user.id",
		Namespace:  "zero_percent",
	}, map[string]any{"user.id": "user_123"})
	if decision.Allowed {
		t.Fatal("expected 0% rollout to reject every subject")
	}
}
