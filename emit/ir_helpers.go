package emit

import (
	"fmt"
	"strings"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/ir"
)

func lowerProgram(source []byte) (*ir.Program, error) {
	parsed, err := arbiter.ParseSource(source)
	if err != nil {
		return nil, err
	}
	return ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
}

func formatNumber(n float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", n), "0"), ".")
}

// toCamelCase converts snake_case to camelCase.
func toCamelCase(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// toSnakeCase converts PascalCase/camelCase to snake_case.
func toSnakeCase(s string) string {
	var buf strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				buf.WriteByte('_')
			}
			buf.WriteByte(byte(r - 'A' + 'a'))
		} else {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}
