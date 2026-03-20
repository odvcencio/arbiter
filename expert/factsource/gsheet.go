package factsource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

const googleSheetsReadonlyScope = "https://www.googleapis.com/auth/spreadsheets.readonly"

var (
	googleSheetsAPIBaseURL = "https://sheets.googleapis.com/v4/spreadsheets"
	googleSheetsHTTPClient = &http.Client{Timeout: 30 * time.Second}
)

func init() {
	Register("gsheet://", LoaderFunc(loadGoogleSheet))
}

type googleSheetsValuesResponse struct {
	Values [][]any `json:"values"`
}

// loadGoogleSheet reads rows from the Google Sheets Values API.
// URI format: gsheet://SPREADSHEET_ID/SheetName or gsheet://SPREADSHEET_ID/Sheet1!A:Z
func loadGoogleSheet(uri string) ([]Fact, error) {
	sheetURL, err := parseGoogleSheetURI(uri)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sheetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("gsheet request: %w", err)
	}
	if err := applyGoogleSheetAuth(req); err != nil {
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

func parseGoogleSheetURI(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("gsheet parse: %w", err)
	}
	if parsed.Scheme != "gsheet" {
		return "", fmt.Errorf("gsheet parse: unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("gsheet parse: spreadsheet id is required")
	}
	sheetRange := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if sheetRange == "" {
		return "", fmt.Errorf("gsheet parse: sheet name or range is required")
	}
	base := strings.TrimRight(googleSheetsAPIBaseURL, "/")
	sheetURL := fmt.Sprintf("%s/%s/values/%s", base, url.PathEscape(parsed.Host), sheetRange)
	valuesURL, err := url.Parse(sheetURL)
	if err != nil {
		return "", fmt.Errorf("gsheet parse: %w", err)
	}
	q := valuesURL.Query()
	q.Set("majorDimension", "ROWS")
	valuesURL.RawQuery = q.Encode()
	return valuesURL.String(), nil
}

func applyGoogleSheetAuth(req *http.Request) error {
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
		config, err := google.JWTConfigFromJSON(creds, googleSheetsReadonlyScope)
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
		q := req.URL.Query()
		q.Set("key", apiKey)
		req.URL.RawQuery = q.Encode()
	}
	return nil
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
			next = append(next, googleSheetCellString(cell))
		}
		rows = append(rows, next)
	}
	return rows
}

func googleSheetCellString(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
