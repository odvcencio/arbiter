package factsource

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leads.csv")
	os.WriteFile(path, []byte(`type,key,title,company,days_since,active
# This is a comment
Lead,sarah@co.com,Head of Fraud,PayCo,,
Lead,mike@bank.com,VP Engineering,BigBank,,
DMSent,sarah@co.com,,PayCo,4,true
`), 0644)

	facts, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("got %d facts, want 3", len(facts))
	}

	// Check Lead
	if facts[0].Type != "Lead" || facts[0].Key != "sarah@co.com" {
		t.Errorf("fact[0] = %+v", facts[0])
	}
	if facts[0].Fields["title"] != "Head of Fraud" {
		t.Errorf("title = %v", facts[0].Fields["title"])
	}
	if facts[0].Fields["key"] != "sarah@co.com" {
		t.Errorf("key = %v", facts[0].Fields["key"])
	}

	// Check coercion
	dm := facts[2]
	if dm.Fields["days_since"] != float64(4) {
		t.Errorf("days_since = %v (%T), want float64(4)", dm.Fields["days_since"], dm.Fields["days_since"])
	}
	if dm.Fields["active"] != true {
		t.Errorf("active = %v (%T), want true", dm.Fields["active"], dm.Fields["active"])
	}

	// Empty fields should be omitted
	if _, ok := dm.Fields["title"]; ok {
		t.Error("empty title should be omitted")
	}
}

func TestJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.json")
	os.WriteFile(path, []byte(`[
		{"type": "Lead", "key": "a@b.com", "title": "CTO", "score": 95},
		{"type": "Lead", "key": "c@d.com", "title": "VP", "score": 80}
	]`), 0644)

	facts, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d, want 2", len(facts))
	}
	if facts[0].Fields["score"] != float64(95) {
		t.Errorf("score = %v", facts[0].Fields["score"])
	}
	if facts[0].Fields["key"] != "a@b.com" {
		t.Errorf("key = %v", facts[0].Fields["key"])
	}
}

func TestJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.jsonl")
	os.WriteFile(path, []byte(`# comment
{"type": "Lead", "key": "a@b.com", "name": "Alice"}
{"type": "Lead", "key": "c@d.com", "name": "Bob"}
`), 0644)

	facts, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d, want 2", len(facts))
	}
	if facts[1].Fields["name"] != "Bob" {
		t.Errorf("name = %v", facts[1].Fields["name"])
	}
	if facts[1].Fields["key"] != "c@d.com" {
		t.Errorf("key = %v", facts[1].Fields["key"])
	}
}

func TestHTTP(t *testing.T) {
	server := newFactSourceTestServer(t, `[{"type":"Lead","key":"a@b.com","name":"Alice"}]`)
	facts, err := Load(server.URL)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("got %d, want 1", len(facts))
	}
	if facts[0].Fields["key"] != "a@b.com" {
		t.Errorf("key = %v", facts[0].Fields["key"])
	}
}

func TestUnknownExtension(t *testing.T) {
	_, err := Load("data.xlsx")
	if err == nil {
		t.Fatal("expected error for unregistered extension")
	}
}

func newFactSourceTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}
