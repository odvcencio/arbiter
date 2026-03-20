package factsource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

const (
	googleSheetsReadonlyScope  = "https://www.googleapis.com/auth/spreadsheets.readonly"
	googleSheetsReadWriteScope = "https://www.googleapis.com/auth/spreadsheets"
)

var (
	googleSheetsAPIBaseURL = "https://sheets.googleapis.com/v4/spreadsheets"
	googleSheetsHTTPClient = &http.Client{Timeout: 30 * time.Second}
)

func init() {
	Register("gsheet://", LoaderFunc(loadGoogleSheet))
	RegisterSaver("gsheet://", SaverFunc(saveGoogleSheet))
}

type googleSheetsValuesResponse struct {
	Values [][]any `json:"values"`
}

type googleSheetTarget struct {
	SpreadsheetID string
	SheetRange    string
}

type googleSheetsWriteRequest struct {
	MajorDimension string     `json:"majorDimension"`
	Values         [][]string `json:"values"`
}

// loadGoogleSheet reads rows from the Google Sheets Values API.
// URI format: gsheet://SPREADSHEET_ID/SheetName or gsheet://SPREADSHEET_ID/Sheet1!A:Z
func loadGoogleSheet(uri string) ([]Fact, error) {
	target, err := parseGoogleSheetTarget(uri)
	if err != nil {
		return nil, err
	}
	sheetURL, err := googleSheetValuesURL(target, true)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sheetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("gsheet request: %w", err)
	}
	if err := applyGoogleSheetAuth(req, googleSheetsReadonlyScope); err != nil {
		return nil, err
	}

	resp, err := googleSheetsHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gsheet: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("gsheet: %s returned %d: %s", uri, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload googleSheetsValuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("gsheet decode: %w", err)
	}
	return factsFromRows("gsheet", googleSheetRows(payload.Values))
}

func saveGoogleSheet(uri string, facts []Fact) error {
	target, err := parseGoogleSheetTarget(uri)
	if err != nil {
		return err
	}
	clearURL, err := googleSheetClearURL(target)
	if err != nil {
		return err
	}
	valuesURL, err := googleSheetValuesURL(target, false)
	if err != nil {
		return err
	}

	if err := googleSheetClear(clearURL); err != nil {
		return err
	}
	if err := googleSheetUpdate(valuesURL, factsToRows(facts)); err != nil {
		return err
	}
	return nil
}

func parseGoogleSheetTarget(uri string) (googleSheetTarget, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return googleSheetTarget{}, fmt.Errorf("gsheet parse: %w", err)
	}
	if parsed.Scheme != "gsheet" {
		return googleSheetTarget{}, fmt.Errorf("gsheet parse: unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return googleSheetTarget{}, fmt.Errorf("gsheet parse: spreadsheet id is required")
	}
	sheetRange := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if sheetRange == "" {
		return googleSheetTarget{}, fmt.Errorf("gsheet parse: sheet name or range is required")
	}
	return googleSheetTarget{
		SpreadsheetID: parsed.Host,
		SheetRange:    sheetRange,
	}, nil
}

func googleSheetValuesURL(target googleSheetTarget, includeMajorDimension bool) (string, error) {
	base := strings.TrimRight(googleSheetsAPIBaseURL, "/")
	sheetURL := fmt.Sprintf("%s/%s/values/%s", base, url.PathEscape(target.SpreadsheetID), target.SheetRange)
	valuesURL, err := url.Parse(sheetURL)
	if err != nil {
		return "", fmt.Errorf("gsheet parse: %w", err)
	}
	q := valuesURL.Query()
	if includeMajorDimension {
		q.Set("majorDimension", "ROWS")
	}
	valuesURL.RawQuery = q.Encode()
	return valuesURL.String(), nil
}

func googleSheetClearURL(target googleSheetTarget) (string, error) {
	valuesURL, err := googleSheetValuesURL(target, false)
	if err != nil {
		return "", err
	}
	return valuesURL + ":clear", nil
}

func googleSheetClear(clearURL string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, clearURL, strings.NewReader(`{}`))
	if err != nil {
		return fmt.Errorf("gsheet clear request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := applyGoogleSheetAuth(req, googleSheetsReadWriteScope); err != nil {
		return err
	}
	resp, err := googleSheetsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("gsheet clear: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("gsheet clear: returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func googleSheetUpdate(valuesURL string, rows [][]string) error {
	payload, err := json.Marshal(googleSheetsWriteRequest{
		MajorDimension: "ROWS",
		Values:         rows,
	})
	if err != nil {
		return fmt.Errorf("gsheet encode: %w", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, valuesURL, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("gsheet update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := applyGoogleSheetAuth(req, googleSheetsReadWriteScope); err != nil {
		return err
	}
	q := req.URL.Query()
	q.Set("valueInputOption", "RAW")
	req.URL.RawQuery = q.Encode()
	resp, err := googleSheetsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("gsheet update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("gsheet update: returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func applyGoogleSheetAuth(req *http.Request, scopes ...string) error {
	if req == nil {
		return nil
	}
	if token := firstNonEmptyEnv("ARBITER_GSHEETS_ACCESS_TOKEN", "GOOGLE_OAUTH_ACCESS_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
	if creds, err := googleSheetServiceAccountJSON(); err != nil {
		return err
	} else if len(creds) > 0 {
		config, err := google.JWTConfigFromJSON(creds, scopes...)
		if err != nil {
			return fmt.Errorf("gsheet auth: %w", err)
		}
		token, err := config.TokenSource(context.Background()).Token()
		if err != nil {
			return fmt.Errorf("gsheet auth: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		return nil
	}
	if apiKey := firstNonEmptyEnv("ARBITER_GSHEETS_API_KEY", "GOOGLE_API_KEY"); apiKey != "" {
		if requiresWriteScope(scopes) {
			return fmt.Errorf("gsheet auth: API key is read-only for Sheets writes; use OAuth access token or service account")
		}
		q := req.URL.Query()
		q.Set("key", apiKey)
		req.URL.RawQuery = q.Encode()
	}
	return nil
}

func requiresWriteScope(scopes []string) bool {
	for _, scope := range scopes {
		if scope == googleSheetsReadWriteScope {
			return true
		}
	}
	return false
}

func googleSheetServiceAccountJSON() ([]byte, error) {
	if inline := firstNonEmptyEnv("ARBITER_GSHEETS_SERVICE_ACCOUNT_JSON", "GOOGLE_SERVICE_ACCOUNT_JSON"); inline != "" {
		return []byte(inline), nil
	}
	if path := firstNonEmptyEnv("ARBITER_GSHEETS_SERVICE_ACCOUNT_FILE", "GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("gsheet auth: read service account file: %w", err)
		}
		return data, nil
	}
	return nil, nil
}

func googleSheetRows(values [][]any) [][]string {
	rows := make([][]string, 0, len(values))
	for _, row := range values {
		next := make([]string, 0, len(row))
		for _, cell := range row {
			next = append(next, factCellString(cell))
		}
		rows = append(rows, next)
	}
	return rows
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
