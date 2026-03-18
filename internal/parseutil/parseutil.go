package parseutil

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
	negative := false
	if len(s) > 0 && s[0] == '-' {
		negative = true
		s = s[1:]
	}

	var result float64
	decimal := false
	divisor := 1.0
	for _, ch := range s {
		if ch == '.' {
			decimal = true
			continue
		}
		if ch >= '0' && ch <= '9' {
			if decimal {
				divisor *= 10
				result += float64(ch-'0') / divisor
			} else {
				result = result*10 + float64(ch-'0')
			}
		}
	}
	if negative {
		result = -result
	}
	return result
}
