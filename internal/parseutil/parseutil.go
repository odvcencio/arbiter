package parseutil

import (
	"fmt"
	"math"
	"strconv"
)

// StripQuotes removes matching double quotes from the ends of a string.
func StripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// ParseInt extracts digits from a string and returns the resulting integer.
func ParseInt(s string) int {
	n := 0
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n
}

// ParseFloat parses a simple decimal number without allocations.
func ParseFloat(s string) float64 {
	result, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return result
}

// ParsePercentBps parses a percentage string into basis points (0..10000).
// The input is expressed in percent units, so 25 -> 2500 and 0.25 -> 25.
func ParsePercentBps(s string) (uint16, error) {
	value := ParseFloat(s)
	if value < 0 || value > 100 {
		return 0, fmt.Errorf("rollout must be between 0 and 100")
	}
	return uint16(math.Round(value * 100)), nil
}
