package factsource

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func init() {
	Register("censys://", LoaderFunc(loadCensys))
}

const (
	censysBaseURL    = "https://search.censys.io/api/v2"
	censysEnvToken   = "CENSYS_API_TOKEN"
	censysEnvOrgID   = "CENSYS_ORG_ID"
	censysMaxResults = 100
)

// loadCensys fetches hosts, certificates, or services from the Censys Platform API.
//
// URI formats:
//
//	censys://hosts?q=services.port:22                         → search hosts
//	censys://hosts/8.8.8.8                                    → single host lookup
//	censys://hosts?q=services.tls.certificates.leaf_data.issuer.common_name:acme.com
//
// Auth via environment variables:
//
//	CENSYS_API_TOKEN  — Personal Access Token (required)
//	CENSYS_ORG_ID     — Organization ID (required for search; lookups work without it on free tier)
type censysAuth struct {
	token string
	orgID string
}

func resolveCensysAuth() (censysAuth, error) {
	token := os.Getenv(censysEnvToken)
	if token == "" {
		return censysAuth{}, fmt.Errorf("censys: %s environment variable is required", censysEnvToken)
	}
	return censysAuth{
		token: token,
		orgID: os.Getenv(censysEnvOrgID),
	}, nil
}

func loadCensys(uri string) ([]Fact, error) {
	auth, err := resolveCensysAuth()
	if err != nil {
		return nil, err
	}

	parsed, err := parseCensysURI(uri)
	if err != nil {
		return nil, err
	}

	switch parsed.resource {
	case "hosts":
		if parsed.id != "" {
			return fetchCensysHost(auth, parsed.id)
		}
		return searchCensysHosts(auth, parsed.query)
	default:
		return nil, fmt.Errorf("censys: unsupported resource %q (supported: hosts)", parsed.resource)
	}
}

type censysURI struct {
	resource string // "hosts"
	id       string // optional: specific IP or SHA
	query    string // search query
}

func parseCensysURI(uri string) (censysURI, error) {
	const prefix = "censys://"
	if !strings.HasPrefix(uri, prefix) {
		return censysURI{}, fmt.Errorf("censys: invalid URI %q", uri)
	}
	remainder := strings.TrimPrefix(uri, prefix)

	// Split resource from query params
	resource := remainder
	query := ""
	if i := strings.IndexByte(remainder, '?'); i >= 0 {
		resource = remainder[:i]
		params, err := url.ParseQuery(remainder[i+1:])
		if err != nil {
			return censysURI{}, fmt.Errorf("censys: parse query: %w", err)
		}
		query = params.Get("q")
	}

	// Split resource/id (e.g., "hosts/8.8.8.8")
	id := ""
	if i := strings.IndexByte(resource, '/'); i >= 0 {
		id = resource[i+1:]
		resource = resource[:i]
	}

	if resource == "" {
		return censysURI{}, fmt.Errorf("censys: resource is required (e.g., censys://hosts?q=...)")
	}

	return censysURI{resource: resource, id: id, query: query}, nil
}

func searchCensysHosts(auth censysAuth, query string) ([]Fact, error) {
	if query == "" {
		return nil, fmt.Errorf("censys: search query is required (censys://hosts?q=...)")
	}

	reqURL := fmt.Sprintf("%s/hosts/search?q=%s&per_page=%d",
		censysBaseURL, url.QueryEscape(query), censysMaxResults)

	body, err := censysRequest(auth, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result struct {
			Hits []json.RawMessage `json:"hits"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("censys: parse response: %w", err)
	}

	facts := make([]Fact, 0, len(resp.Result.Hits))
	for _, raw := range resp.Result.Hits {
		fact, err := censysHostToFact(raw)
		if err != nil {
			continue // skip malformed entries
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

func fetchCensysHost(auth censysAuth, ip string) ([]Fact, error) {
	reqURL := fmt.Sprintf("%s/hosts/%s", censysBaseURL, url.PathEscape(ip))

	body, err := censysRequest(auth, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("censys: parse response: %w", err)
	}

	fact, err := censysHostToFact(resp.Result)
	if err != nil {
		return nil, err
	}
	return []Fact{fact}, nil
}

func censysHostToFact(raw json.RawMessage) (Fact, error) {
	var host map[string]any
	if err := json.Unmarshal(raw, &host); err != nil {
		return Fact{}, fmt.Errorf("censys: parse host: %w", err)
	}

	ip, _ := host["ip"].(string)
	if ip == "" {
		return Fact{}, fmt.Errorf("censys: host missing ip field")
	}

	fields := make(map[string]any, len(host))
	fields["key"] = ip

	// Flatten top-level fields
	for k, v := range host {
		fields[k] = v
	}

	// Extract service summary for easy rule access
	if services, ok := host["services"].([]any); ok {
		fields["service_count"] = float64(len(services))
		ports := make([]any, 0, len(services))
		serviceNames := make([]any, 0, len(services))
		for _, svc := range services {
			svcMap, ok := svc.(map[string]any)
			if !ok {
				continue
			}
			if port, ok := svcMap["port"].(float64); ok {
				ports = append(ports, port)
			}
			if name, ok := svcMap["service_name"].(string); ok {
				serviceNames = append(serviceNames, name)
			}
		}
		fields["ports"] = ports
		fields["service_names"] = serviceNames
	}

	// Extract autonomous system for easy access
	if as, ok := host["autonomous_system"].(map[string]any); ok {
		if asn, ok := as["asn"].(float64); ok {
			fields["asn"] = asn
		}
		if name, ok := as["name"].(string); ok {
			fields["as_name"] = name
		}
		if bgp, ok := as["bgp_prefix"].(string); ok {
			fields["bgp_prefix"] = bgp
		}
	}

	// Extract location if present
	if loc, ok := host["location"].(map[string]any); ok {
		if country, ok := loc["country"].(string); ok {
			fields["country"] = country
		}
		if city, ok := loc["city"].(string); ok {
			fields["city"] = city
		}
	}

	return Fact{
		Type:   "Host",
		Key:    ip,
		Fields: fields,
	}, nil
}

func censysRequest(auth censysAuth, reqURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("censys: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.token)
	if auth.orgID != "" {
		req.Header.Set("Censys-Organization-ID", auth.orgID)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("censys: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("censys: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("censys: %s returned %d: %s", reqURL, resp.StatusCode, truncate(string(body), 200))
	}

	return body, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
