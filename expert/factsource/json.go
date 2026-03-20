package factsource

import (
	"encoding/json"
	"fmt"
	"os"
)

func init() {
	Register(".json", LoaderFunc(loadJSON))
	RegisterSaver(".json", SaverFunc(saveJSON))
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

		fields := make(map[string]any, len(obj)-1)
		fields["key"] = key // always include key in fields for rule access
		for k, v := range obj {
			if k == "type" {
				continue
			}
			fields[k] = v
		}

		facts = append(facts, Fact{Type: typ, Key: key, Fields: fields})
	}

	return facts, nil
}

func saveJSON(path string, facts []Fact) error {
	data, err := json.MarshalIndent(factsToObjects(facts), "", "  ")
	if err != nil {
		return fmt.Errorf("json encode: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	return nil
}

func factsToObjects(facts []Fact) []map[string]any {
	sortedFacts := sortFacts(facts)
	out := make([]map[string]any, 0, len(sortedFacts))
	for _, fact := range sortedFacts {
		obj := make(map[string]any, len(fact.Fields)+2)
		obj["type"] = fact.Type
		obj["key"] = fact.Key
		for key, value := range fact.Fields {
			switch key {
			case "type", "key":
				continue
			default:
				obj[key] = value
			}
		}
		out = append(out, obj)
	}
	return out
}
