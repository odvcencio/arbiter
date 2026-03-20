package factsource

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func init() {
	Register("http://", LoaderFunc(loadHTTP))
	Register("https://", LoaderFunc(loadHTTP))
}

// loadHTTP fetches a JSON array of facts from an HTTP endpoint.
// Same schema as JSON loader: array of objects with "type" and "key".
// Timeout: 30 seconds.
func loadHTTP(url string) ([]Fact, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http: %s returned %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http read: %w", err)
	}

	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("http json: %w", err)
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
			if k != "type" && k != "key" {
				fields[k] = v
			}
		}
		facts = append(facts, Fact{Type: typ, Key: key, Fields: fields})
	}

	return facts, nil
}
