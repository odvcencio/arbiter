package govern

import (
	"crypto/sha256"
	"encoding/binary"
)

// Bucket returns a deterministic 0-99 bucket for a user ID.
func Bucket(userID string) int {
	h := sha256.Sum256([]byte(userID))
	n := binary.BigEndian.Uint32(h[:4])
	return int(n % 100)
}

// RolloutUserID extracts the identifier used for rollout bucketing.
func RolloutUserID(ctx map[string]any) string {
	if uid, ok := ctx["user.id"].(string); ok {
		return uid
	}
	if uid, ok := ctx["user_id"].(string); ok {
		return uid
	}
	return ""
}

// RolloutAllows checks whether a rollout percentage allows this request.
func RolloutAllows(rollout uint8, ctx map[string]any) bool {
	if rollout == 0 {
		return true
	}
	return Bucket(RolloutUserID(ctx)) < int(rollout)
}
