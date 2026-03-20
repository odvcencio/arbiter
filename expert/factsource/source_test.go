package factsource

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestGoogleSheet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/sheet123/values/Leads" {
			t.Fatalf("path = %q", got)
		}
		if got := r.URL.Query().Get("majorDimension"); got != "ROWS" {
			t.Fatalf("majorDimension = %q", got)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Fatalf("key query = %q", got)
		}
		_, _ = io.WriteString(w, `{"values":[["type","key","score","active"],["Lead","a@b.com",95,true],["Lead","b@c.com",80,false]]}`)
	}))
	defer server.Close()

	origBaseURL := googleSheetsAPIBaseURL
	origClient := googleSheetsHTTPClient
	googleSheetsAPIBaseURL = server.URL
	googleSheetsHTTPClient = server.Client()
	t.Cleanup(func() {
		googleSheetsAPIBaseURL = origBaseURL
		googleSheetsHTTPClient = origClient
	})
	t.Setenv("ARBITER_GSHEETS_API_KEY", "test-key")

	facts, err := Load("gsheet://sheet123/Leads")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d, want 2", len(facts))
	}
	if facts[0].Fields["score"] != float64(95) {
		t.Errorf("score = %v", facts[0].Fields["score"])
	}
	if facts[0].Fields["active"] != true {
		t.Errorf("active = %v", facts[0].Fields["active"])
	}
	if facts[1].Fields["key"] != "b@c.com" {
		t.Errorf("key = %v", facts[1].Fields["key"])
	}
}

func TestGoogleSheetAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"values":[["type","key"],["Lead","a@b.com"]]}`)
	}))
	defer server.Close()

	origBaseURL := googleSheetsAPIBaseURL
	origClient := googleSheetsHTTPClient
	googleSheetsAPIBaseURL = server.URL
	googleSheetsHTTPClient = server.Client()
	t.Cleanup(func() {
		googleSheetsAPIBaseURL = origBaseURL
		googleSheetsHTTPClient = origClient
	})
	t.Setenv("ARBITER_GSHEETS_ACCESS_TOKEN", "secret-token")

	facts, err := Load("gsheet://sheet123/Leads")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("got %d, want 1", len(facts))
	}
}

func TestUnknownExtension(t *testing.T) {
	_, err := Load("data.xlsx")
	if err == nil {
		t.Fatal("expected error for unregistered extension")
	}
}

func TestSaveCSVRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.csv")
	input := []Fact{
		{Type: "Lead", Key: "b", Fields: map[string]any{"company": "Beta", "score": float64(80)}},
		{Type: "Lead", Key: "a", Fields: map[string]any{"company": "Alpha", "active": true}},
	}

	if err := Save(path, input); err != nil {
		t.Fatalf("Save: %v", err)
	}
	facts, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	if facts[0].Key != "a" || facts[1].Key != "b" {
		t.Fatalf("facts not saved deterministically: %+v", facts)
	}
}

func TestSaveJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.json")
	input := []Fact{
		{Type: "Lead", Key: "a", Fields: map[string]any{"score": float64(95), "key": "a"}},
	}
	if err := Save(path, input); err != nil {
		t.Fatalf("Save: %v", err)
	}
	facts, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(facts) != 1 || facts[0].Fields["score"] != float64(95) {
		t.Fatalf("unexpected round-trip facts: %+v", facts)
	}
}

func TestSaveJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.jsonl")
	input := []Fact{
		{Type: "Lead", Key: "a", Fields: map[string]any{"name": "Alice"}},
		{Type: "Lead", Key: "b", Fields: map[string]any{"name": "Bob"}},
	}
	if err := Save(path, input); err != nil {
		t.Fatalf("Save: %v", err)
	}
	facts, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(facts) != 2 || facts[1].Fields["name"] != "Bob" {
		t.Fatalf("unexpected round-trip facts: %+v", facts)
	}
}

func TestSaveGoogleSheet(t *testing.T) {
	var clearCalls, updateCalls int
	var authHeader string
	var update googleSheetsWriteRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/values/Leads:clear"):
			clearCalls++
			authHeader = r.Header.Get("Authorization")
			if r.Method != http.MethodPost {
				t.Fatalf("clear method = %s", r.Method)
			}
			_, _ = io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/values/Leads"):
			updateCalls++
			authHeader = r.Header.Get("Authorization")
			if r.Method != http.MethodPut {
				t.Fatalf("update method = %s", r.Method)
			}
			if got := r.URL.Query().Get("valueInputOption"); got != "RAW" {
				t.Fatalf("valueInputOption = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				t.Fatalf("decode update: %v", err)
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	origBaseURL := googleSheetsAPIBaseURL
	origClient := googleSheetsHTTPClient
	googleSheetsAPIBaseURL = server.URL
	googleSheetsHTTPClient = server.Client()
	t.Cleanup(func() {
		googleSheetsAPIBaseURL = origBaseURL
		googleSheetsHTTPClient = origClient
	})
	t.Setenv("ARBITER_GSHEETS_ACCESS_TOKEN", "write-token")

	err := Save("gsheet://sheet123/Leads", []Fact{
		{Type: "Lead", Key: "a", Fields: map[string]any{"score": float64(95)}},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if clearCalls != 1 || updateCalls != 1 {
		t.Fatalf("expected clear+update, got clear=%d update=%d", clearCalls, updateCalls)
	}
	if authHeader != "Bearer write-token" {
		t.Fatalf("auth = %q", authHeader)
	}
	if update.MajorDimension != "ROWS" {
		t.Fatalf("majorDimension = %q", update.MajorDimension)
	}
	if len(update.Values) != 2 || update.Values[1][0] != "Lead" || update.Values[1][1] != "a" {
		t.Fatalf("unexpected values payload: %+v", update.Values)
	}
}

func TestSaveGoogleSheetRejectsAPIKeyAuth(t *testing.T) {
	origBaseURL := googleSheetsAPIBaseURL
	origClient := googleSheetsHTTPClient
	googleSheetsAPIBaseURL = "https://example.test"
	googleSheetsHTTPClient = &http.Client{}
	t.Cleanup(func() {
		googleSheetsAPIBaseURL = origBaseURL
		googleSheetsHTTPClient = origClient
	})
	t.Setenv("ARBITER_GSHEETS_API_KEY", "read-only-key")
	t.Setenv("ARBITER_GSHEETS_ACCESS_TOKEN", "")
	t.Setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "")
	t.Setenv("ARBITER_GSHEETS_SERVICE_ACCOUNT_JSON", "")
	t.Setenv("GOOGLE_SERVICE_ACCOUNT_JSON", "")
	t.Setenv("ARBITER_GSHEETS_SERVICE_ACCOUNT_FILE", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	err := Save("gsheet://sheet123/Leads", []Fact{{Type: "Lead", Key: "a"}})
	if err == nil || !strings.Contains(err.Error(), "API key is read-only for Sheets writes") {
		t.Fatalf("expected write auth error, got %v", err)
	}
}

func newFactSourceTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}
