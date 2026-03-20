package factsource

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func init() {
	Register(".jsonl", LoaderFunc(loadJSONL))
}

// loadJSONL reads a JSON Lines file — one JSON object per line.
// Same schema as JSON: each line must have "type" and "key".
// Lines starting with # or // are skipped. Empty lines are skipped.
func loadJSONL(path string) ([]Fact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jsonl: %w", err)
	}
	defer f.Close()

	var facts []Fact
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			return nil, fmt.Errorf("jsonl line %d: %w", lineNum, err)
		}

		typ, _ := obj["type"].(string)
		key, _ := obj["key"].(string)
		if typ == "" || key == "" {
			continue
		}

		fields := make(map[string]any, len(obj)-2)
		for k, v := range obj {
			if k == "type" || k == "key" {
				continue
			}
			fields[k] = v
		}

		facts = append(facts, Fact{Type: typ, Key: key, Fields: fields})
	}

	return facts, nil
}
