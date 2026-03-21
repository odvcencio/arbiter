package govern

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

const (
	// RolloutResolution is the basis-point resolution used for rollout decisions.
	RolloutResolution uint16 = 10000
	// DefaultRolloutSubject is the default sticky identity for rollout checks.
	DefaultRolloutSubject = "user.id"
)

// PercentRollout is a first-class percent rollout specification.
type PercentRollout struct {
	PercentBps uint16
	SubjectKey string
	Namespace  string
}

// RolloutDecision records how a rollout check resolved.
type RolloutDecision struct {
	Allowed       bool
	MissingSubject bool
	SubjectKey    string
	SubjectValue  string
	Namespace     string
	Bucket        uint16
	Threshold     uint16
	Resolution    uint16
}

// Bucket returns a deterministic legacy 0-99 bucket for a subject.
func Bucket(userID string) int {
	h := sha256.Sum256([]byte(userID))
	n := binary.BigEndian.Uint32(h[:4])
	return int(n % 100)
}

// RolloutBucket returns a deterministic 0..9999 bucket scoped by namespace.
func RolloutBucket(namespace, subject string) uint16 {
	h := sha256.Sum256([]byte(namespace + "\x00" + subject))
	n := binary.BigEndian.Uint32(h[:4])
	return uint16(n % uint32(RolloutResolution))
}

// RolloutUserID extracts the identifier used for rollout bucketing.
func RolloutUserID(ctx map[string]any) string {
	uid, _ := RolloutSubject(ctx, DefaultRolloutSubject)
	return uid
}

// AutoRolloutNamespace derives a stable namespace when source does not declare one.
func AutoRolloutNamespace(bundleID string, scope string) string {
	if bundleID == "" {
		return "arbiter:" + scope
	}
	return bundleID + ":" + scope
}

// RolloutSubject resolves the sticky subject value for a rollout key.
func RolloutSubject(ctx map[string]any, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = DefaultRolloutSubject
	}
	if ctx == nil {
		return "", false
	}
	if value, ok := stringifyRolloutValue(ctx[key]); ok {
		return value, true
	}
	if key == DefaultRolloutSubject {
		if value, ok := stringifyRolloutValue(ctx["user_id"]); ok {
			return value, true
		}
	}
	nested := NestDottedKeys(ctx)
	if value, ok := resolveRolloutValue(nested, key); ok {
		return value, true
	}
	if key == DefaultRolloutSubject {
		if value, ok := stringifyRolloutValue(nested["user_id"]); ok {
			return value, true
		}
	}
	return "", false
}

// DecidePercentRollout evaluates a percent rollout against request context.
func DecidePercentRollout(spec PercentRollout, ctx map[string]any) RolloutDecision {
	subjectKey := strings.TrimSpace(spec.SubjectKey)
	if subjectKey == "" {
		subjectKey = DefaultRolloutSubject
	}
	decision := RolloutDecision{
		SubjectKey: subjectKey,
		Namespace:  strings.TrimSpace(spec.Namespace),
		Threshold:  spec.PercentBps,
		Resolution: RolloutResolution,
	}
	subjectValue, ok := RolloutSubject(ctx, subjectKey)
	if !ok {
		decision.MissingSubject = true
		return decision
	}
	decision.SubjectValue = subjectValue
	decision.Bucket = RolloutBucket(decision.Namespace, subjectValue)
	decision.Allowed = decision.Bucket < spec.PercentBps
	return decision
}

// RolloutAllows checks whether a percent rollout allows this request.
func RolloutAllows(spec PercentRollout, ctx map[string]any) bool {
	return DecidePercentRollout(spec, ctx).Allowed
}

// CheckLabel formats the rollout check for traces and explain output.
func (spec PercentRollout) CheckLabel() string {
	subjectKey := strings.TrimSpace(spec.SubjectKey)
	if subjectKey == "" {
		subjectKey = DefaultRolloutSubject
	}
	return fmt.Sprintf(
		"rollout percent %s by %s namespace %q",
		formatPercentBps(spec.PercentBps),
		subjectKey,
		spec.Namespace,
	)
}

// Detail formats a rollout decision for traces.
func (d RolloutDecision) Detail() string {
	if d.MissingSubject {
		return fmt.Sprintf(
			"subject_key=%s missing, namespace=%q, threshold=%d, resolution=%d",
			d.SubjectKey,
			d.Namespace,
			d.Threshold,
			d.Resolution,
		)
	}
	return fmt.Sprintf(
		"subject_key=%s, subject=%q, namespace=%q, bucket=%d, threshold=%d, resolution=%d",
		d.SubjectKey,
		d.SubjectValue,
		d.Namespace,
		d.Bucket,
		d.Threshold,
		d.Resolution,
	)
}

func resolveRolloutValue(current any, key string) (string, bool) {
	parts := strings.Split(key, ".")
	for _, part := range parts {
		next, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = next[part]
		if !ok {
			return "", false
		}
	}
	return stringifyRolloutValue(current)
}

func stringifyRolloutValue(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return "", false
		}
		return v, true
	case fmt.Stringer:
		s := v.String()
		if s == "" {
			return "", false
		}
		return s, true
	case int:
		return strconv.Itoa(v), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}

func formatPercentBps(bps uint16) string {
	if bps%100 == 0 {
		return strconv.FormatUint(uint64(bps/100), 10)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", float64(bps)/100), "0"), ".")
}
