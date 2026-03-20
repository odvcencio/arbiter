package factsource

import (
	"fmt"
	"sort"
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

func factsToRows(facts []Fact) [][]string {
	headers := []string{"type", "key"}
	fieldSet := make(map[string]struct{})
	sortedFacts := sortFacts(facts)
	for _, fact := range sortedFacts {
		for key := range fact.Fields {
			name := strings.TrimSpace(key)
			if name == "" || strings.EqualFold(name, "type") || strings.EqualFold(name, "key") {
				continue
			}
			fieldSet[name] = struct{}{}
		}
	}

	fieldNames := make([]string, 0, len(fieldSet))
	for key := range fieldSet {
		fieldNames = append(fieldNames, key)
	}
	sort.Strings(fieldNames)
	headers = append(headers, fieldNames...)

	rows := make([][]string, 0, len(sortedFacts)+1)
	rows = append(rows, headers)
	for _, fact := range sortedFacts {
		row := make([]string, len(headers))
		row[0] = fact.Type
		row[1] = fact.Key
		for i, header := range fieldNames {
			if value, ok := fact.Fields[header]; ok {
				row[i+2] = factCellString(value)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func sortFacts(facts []Fact) []Fact {
	out := make([]Fact, len(facts))
	copy(out, facts)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func factCellString(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(value), 'f', -1, 32)
	case int:
		return strconv.Itoa(value)
	case int8:
		return strconv.FormatInt(int64(value), 10)
	case int16:
		return strconv.FormatInt(int64(value), 10)
	case int32:
		return strconv.FormatInt(int64(value), 10)
	case int64:
		return strconv.FormatInt(value, 10)
	case uint:
		return strconv.FormatUint(uint64(value), 10)
	case uint8:
		return strconv.FormatUint(uint64(value), 10)
	case uint16:
		return strconv.FormatUint(uint64(value), 10)
	case uint32:
		return strconv.FormatUint(uint64(value), 10)
	case uint64:
		return strconv.FormatUint(value, 10)
	default:
		return fmt.Sprint(value)
	}
}
