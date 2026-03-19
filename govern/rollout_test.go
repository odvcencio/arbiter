package govern

import "testing"

func TestBucketDeterministic(t *testing.T) {
	if Bucket("user_123") != Bucket("user_123") {
		t.Fatal("bucket should be deterministic")
	}
}

func TestRolloutAllowsUsesUserID(t *testing.T) {
	ctx := map[string]any{"user.id": "user_123"}
	allowed1 := RolloutAllows(25, ctx)
	allowed2 := RolloutAllows(25, ctx)
	if allowed1 != allowed2 {
		t.Fatal("rollout should be deterministic for the same user")
	}
}
