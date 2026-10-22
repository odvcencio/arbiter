package govern

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// RecordActiveWindow appends structured active window checks and reports whether
// evaluation may continue inside the current window.
func RecordActiveWindow(arbitrace *Arbitrace, evalTime time.Time, scope, subject, checkPrefix string, hasFrom bool, fromUnixNano int64, hasUntil bool, untilUnixNano int64) bool {
	if hasFrom {
		fromText := formatUnixNanoTimestamp(fromUnixNano)
		passed := !evalTime.Before(time.Unix(0, fromUnixNano).UTC())
		detail := fmt.Sprintf("eval_time=%s is before active_from=%s", formatUnixNanoTimestamp(evalTime.UTC().UnixNano()), fromText)
		if passed {
			detail = fmt.Sprintf("eval_time=%s satisfies active_from=%s", formatUnixNanoTimestamp(evalTime.UTC().UnixNano()), fromText)
		}
		arbitrace.AppendStep(NewScopedArbitraceStep(
			ArbitracePhaseGovernance,
			scope,
			subject,
			ArbitraceKindActiveFrom,
			fromText,
			checkPrefix+"active_from "+fromText,
			passed,
			detail,
		))
		if !passed {
			return false
		}
	}
	if hasUntil {
		untilText := formatUnixNanoTimestamp(untilUnixNano)
		passed := evalTime.Before(time.Unix(0, untilUnixNano).UTC())
		detail := fmt.Sprintf("eval_time=%s is on/after active_until=%s", formatUnixNanoTimestamp(evalTime.UTC().UnixNano()), untilText)
		if passed {
			detail = fmt.Sprintf("eval_time=%s satisfies active_until=%s", formatUnixNanoTimestamp(evalTime.UTC().UnixNano()), untilText)
		}
		arbitrace.AppendStep(NewScopedArbitraceStep(
			ArbitracePhaseGovernance,
			scope,
			subject,
			ArbitraceKindActiveUntil,
			untilText,
			checkPrefix+"active_until "+untilText,
			passed,
			detail,
		))
		if !passed {
			return false
		}
	}
	return true
}

func resolveEvalTime(ctx map[string]any) time.Time {
	if ctx != nil {
		if now, ok := parseEvalTime(ctx["__now"]); ok {
			return now
		}
	}
	return time.Now().UTC()
}

func parseEvalTime(raw any) (time.Time, bool) {
	switch value := raw.(type) {
	case time.Time:
		return value.UTC(), true
	case float64:
		// Reject values float64->int64 cannot convert the same way on every target
		// (the conversion is implementation-defined outside the int64 range).
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) >= 1<<62 {
			return time.Time{}, false
		}
		seconds, frac := math.Modf(value)
		return time.Unix(int64(seconds), int64(frac*float64(time.Second))).UTC(), true
	case float32:
		return parseEvalTime(float64(value))
	case int:
		return time.Unix(int64(value), 0).UTC(), true
	case int8:
		return time.Unix(int64(value), 0).UTC(), true
	case int16:
		return time.Unix(int64(value), 0).UTC(), true
	case int32:
		return time.Unix(int64(value), 0).UTC(), true
	case int64:
		return time.Unix(value, 0).UTC(), true
	case uint:
		return time.Unix(int64(value), 0).UTC(), true
	case uint8:
		return time.Unix(int64(value), 0).UTC(), true
	case uint16:
		return time.Unix(int64(value), 0).UTC(), true
	case uint32:
		return time.Unix(int64(value), 0).UTC(), true
	case uint64:
		if value > math.MaxInt64 {
			return time.Time{}, false
		}
		return time.Unix(int64(value), 0).UTC(), true
	case json.Number:
		if n, err := value.Float64(); err == nil {
			return parseEvalTime(n)
		}
		return time.Time{}, false
	case string:
		if value == "" {
			return time.Time{}, false
		}
		if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return ts.UTC(), true
		}
		if n, err := strconv.ParseFloat(value, 64); err == nil {
			return parseEvalTime(n)
		}
	}
	return time.Time{}, false
}

func formatUnixNanoTimestamp(unixNano int64) string {
	return time.Unix(0, unixNano).UTC().Format(time.RFC3339Nano)
}
