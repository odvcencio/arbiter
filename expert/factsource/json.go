package factsource

import (
	"encoding/json"
	"fmt"
	"os"
)

func init() {
	Register(".json", LoaderFunc(loadJSON))
}

// loadJSON reads a JSON file containing an array of fact objects.
// Each object must have "type" and "key" fields. All other fields
// are stored in Fields.
//
//	[
//	  {"type": "Lead", "key": "sarah@co.com", "title": "Head of Fraud", "company": "PayCo"},
//	  {"type": "DMSent", "key": "sarah@co.com", "status": "no_response", "days_since": 4}
//	]
func loadJSON(path string) ([]Fact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}

	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("json parse: %w", err)
	}

	var facts []Fact
	for _, obj := range raw {
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
