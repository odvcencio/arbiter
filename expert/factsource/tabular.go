package factsource

import (
	"fmt"
	"strconv"
	"strings"
)

func factsFromRows(sourceName string, rows [][]string) ([]Fact, error) {
	if len(rows) < 2 {
		return nil, nil
	}

	headers := rows[0]
	typeCol := -1
	keyCol := -1
	for i, h := range headers {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "type":
			typeCol = i
		case "key":
			keyCol = i
		}
	}
	if typeCol < 0 {
		return nil, fmt.Errorf("%s: missing required 'type' column", sourceName)
	}
	if keyCol < 0 {
		return nil, fmt.Errorf("%s: missing required 'key' column", sourceName)
	}

	var facts []Fact
	for _, row := range rows[1:] {
		if len(row) == 0 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(row[0]), "#") {
			continue
		}
		if len(row) <= typeCol || len(row) <= keyCol {
			continue
		}

		typ := strings.TrimSpace(row[typeCol])
		key := strings.TrimSpace(row[keyCol])
		if typ == "" || key == "" {
			continue
		}

		fields := make(map[string]any)
		fields["key"] = key
		for i, val := range row {
			if i == typeCol || i == keyCol || i >= len(headers) {
				continue
			}
			val = strings.TrimSpace(val)
			if val == "" {
				continue
			}
			fields[strings.TrimSpace(headers[i])] = coerce(val)
		}

		facts = append(facts, Fact{Type: typ, Key: key, Fields: fields})
	}

	return facts, nil
}

func coerce(s string) any {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}
	return s
}
