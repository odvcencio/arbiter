package factsource

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func init() {
	Register(".csv", LoaderFunc(loadCSV))
}

// loadCSV reads a CSV file where:
//   - First row is headers
//   - Required columns: "type" and "key"
//   - All other columns become Fields
//   - Lines starting with # are skipped (comments)
//   - Numeric strings are auto-converted to float64
//   - "true"/"false" are auto-converted to bool
//   - Empty cells are omitted from Fields
func loadCSV(path string) ([]Fact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.Comment = '#'
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv parse: %w", err)
	}
	if len(records) < 2 {
		return nil, nil // header only or empty
	}

	headers := records[0]
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
		return nil, fmt.Errorf("csv: missing required 'type' column")
	}
	if keyCol < 0 {
		return nil, fmt.Errorf("csv: missing required 'key' column")
	}

	var facts []Fact
	for _, row := range records[1:] {
		if len(row) <= typeCol || len(row) <= keyCol {
			continue
		}
		typ := strings.TrimSpace(row[typeCol])
		key := strings.TrimSpace(row[keyCol])
		if typ == "" || key == "" {
			continue
		}

		fields := make(map[string]any)
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

// coerce converts string values to typed Go values.
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
