package govern

import "strings"

// NestDottedKeys converts a flat map with dotted keys into nested maps.
func NestDottedKeys(flat map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range flat {
		parts := strings.Split(k, ".")
		if len(parts) == 1 {
			result[k] = v
			continue
		}
		current := result
		for i, part := range parts {
			if i == len(parts)-1 {
				current[part] = v
				continue
			}
			next, ok := current[part].(map[string]any)
			if !ok {
				next = make(map[string]any)
				current[part] = next
			}
			current = next
		}
	}
	return result
}
