// ci-gateway is a GitHub webhook receiver that evaluates Arbiter rules
// before allowing workflow runs to proceed. It aggregates billing usage
// from the GitHub API since the per-user billing endpoint was deprecated.
//
// Deploy alongside arbiter serve, or embed the Go library directly.
//
// Setup:
//   1. Create a GitHub webhook on your repos (or org-wide)
//      - Events: workflow_job, workflow_run
//      - URL: https://your-host/webhook
//      - Secret: set WEBHOOK_SECRET env var
//   2. Set GITHUB_TOKEN with repo + workflow scopes
//   3. Set ARBITER_ADDR to your gRPC server (or use embedded mode)
//   4. Point RULES_FILE at your .arb governance rules
//
// Usage:
//   GITHUB_TOKEN=ghp_... WEBHOOK_SECRET=... go run ./examples/ci-gateway

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/arbiter"
)

// Config from environment.
var (
	ghToken       = os.Getenv("GITHUB_TOKEN")
	webhookSecret = os.Getenv("WEBHOOK_SECRET")
	rulesFile     = envOr("RULES_FILE", "examples/ci_governance.arb")
	listenAddr    = envOr("LISTEN_ADDR", ":8090")
	monthlyBudget = 2000 // included minutes for your plan (GitHub Pro = 3000)
)

// UsageTracker aggregates billable minutes across repos for the current cycle.
type UsageTracker struct {
	mu            sync.RWMutex
	totalMinutes  float64
	lastRefreshed time.Time
	repos         []string // repos to scan
}

func (u *UsageTracker) UsedMinutesPct() float64 {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return (u.totalMinutes / float64(monthlyBudget)) * 100
}

func (u *UsageTracker) Refresh(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()

	// Only refresh every 5 minutes
	if time.Since(u.lastRefreshed) < 5*time.Minute {
		return
	}

	total := 0.0
	now := time.Now()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	for _, repo := range u.repos {
		minutes, err := fetchRepoBillableMinutes(ctx, repo, cycleStart)
		if err != nil {
			log.Printf("usage: error fetching %s: %v", repo, err)
			continue
		}
		total += minutes
	}

	u.totalMinutes = total
	u.lastRefreshed = now
	log.Printf("usage: %.1f minutes used (%.1f%% of %d budget)", total, (total/float64(monthlyBudget))*100, monthlyBudget)
}

func fetchRepoBillableMinutes(ctx context.Context, repo string, since time.Time) (float64, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs?created=>=%s&per_page=100",
		repo, since.Format("2006-01-02"))

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result struct {
		WorkflowRuns []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	total := 0.0
	for _, run := range result.WorkflowRuns {
		timingURL := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/timing", repo, run.ID)
		treq, _ := http.NewRequestWithContext(ctx, "GET", timingURL, nil)
		treq.Header.Set("Authorization", "Bearer "+ghToken)
		treq.Header.Set("Accept", "application/vnd.github+json")

		tresp, err := http.DefaultClient.Do(treq)
		if err != nil {
			continue
		}
		var timing struct {
			Billable map[string]struct {
				TotalMS int64 `json:"total_ms"`
			} `json:"billable"`
		}
		json.NewDecoder(tresp.Body).Decode(&timing)
		tresp.Body.Close()

		for _, runner := range timing.Billable {
			total += float64(runner.TotalMS) / 60000.0
		}
	}

	return total, nil
}

// RecentRunTracker tracks when workflows last ran for rate limiting.
type RecentRunTracker struct {
	mu   sync.RWMutex
	runs map[string]time.Time // "repo:workflow" -> last run time
}

func (r *RecentRunTracker) Record(repo, workflow string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[repo+":"+workflow] = time.Now()
}

func (r *RecentRunTracker) MinutesSinceLast(repo, workflow string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.runs[repo+":"+workflow]; ok {
		return time.Since(t).Minutes()
	}
	return 999 // never ran
}

// Webhook handler.
func main() {
	if ghToken == "" {
		log.Fatal("GITHUB_TOKEN is required")
	}

	// Load rules
	source, err := os.ReadFile(rulesFile)
	if err != nil {
		log.Fatalf("read rules: %v", err)
	}
	result, err := arbiter.CompileFullFile(rulesFile)
	if err != nil {
		log.Fatalf("compile rules: %v", err)
	}
	_ = source

	// Discover repos to track
	repos := discoverRepos()
	log.Printf("tracking %d repos: %v", len(repos), repos)

	usage := &UsageTracker{repos: repos}
	recentRuns := &RecentRunTracker{runs: make(map[string]time.Time)}

	// Initial usage refresh
	usage.Refresh(context.Background())

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		// Verify signature
		if webhookSecret != "" {
			sig := r.Header.Get("X-Hub-Signature-256")
			if !verifySignature(body, sig, webhookSecret) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}

		event := r.Header.Get("X-GitHub-Event")
		if event != "workflow_run" {
			w.WriteHeader(http.StatusOK)
			return
		}

		var payload struct {
			Action      string `json:"action"`
			WorkflowRun struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				HeadBranch string `json:"head_branch"`
			} `json:"workflow_run"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "parse payload", http.StatusBadRequest)
			return
		}

		if payload.Action != "requested" {
			w.WriteHeader(http.StatusOK)
			return
		}

		repo := payload.Repository.FullName
		branch := payload.WorkflowRun.HeadBranch
		workflow := payload.WorkflowRun.Name
		runID := payload.WorkflowRun.ID

		// Refresh usage in background
		go usage.Refresh(r.Context())

		// Build evaluation context
		now := time.Now()
		ctx := map[string]any{
			"workflow": map[string]any{
				"name":                   workflow,
				"branch":                 branch,
				"repo":                   repo,
				"estimated_minutes":      10, // could be derived from historical average
				"minutes_since_last_run": recentRuns.MinutesSinceLast(repo, workflow),
			},
			"billing": map[string]any{
				"used_minutes_pct": usage.UsedMinutesPct(),
			},
			"context": map[string]any{
				"hour":        now.Hour(),
				"day_of_week": int(now.Weekday()),
			},
		}

		dc := arbiter.DataFromMap(ctx, result.Ruleset)
		matched, trace, evalErr := arbiter.EvalGoverned(result.Ruleset, dc, result.Segments, ctx)
		if evalErr != nil {
			log.Printf("eval error: %v", evalErr)
			w.WriteHeader(http.StatusOK) // fail open
			return
		}

		// Check decision
		decision := "allow"
		reason := "default"
		for _, m := range matched {
			if m.Action == "Deny" {
				decision = "deny"
				if r, ok := m.Params["reason"].(string); ok {
					reason = r
				}
				break
			}
			if m.Action == "Allow" {
				if r, ok := m.Params["reason"].(string); ok {
					reason = r
				}
			}
		}

		traceLen := 0
		if trace != nil {
			traceLen = len(trace.Steps)
		}
		log.Printf("decision=%s repo=%s branch=%s workflow=%s reason=%q trace_steps=%d",
			decision, repo, branch, workflow, reason, traceLen)

		if decision == "deny" {
			// Cancel the run
			cancelURL := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/cancel", repo, runID)
			req, _ := http.NewRequestWithContext(r.Context(), "POST", cancelURL, nil)
			req.Header.Set("Authorization", "Bearer "+ghToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("cancel failed: %v", err)
			} else {
				resp.Body.Close()
				log.Printf("cancelled run %d: %s", runID, reason)
			}
		}

		recentRuns.Record(repo, workflow)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"decision": decision,
			"reason":   reason,
			"matched":  len(matched),
		})
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"ok","usage_pct":%.1f}`, usage.UsedMinutesPct())
	})

	log.Printf("ci-gateway listening on %s (rules: %s)", listenAddr, rulesFile)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func discoverRepos() []string {
	// Could be env var or GitHub API call
	if r := os.Getenv("REPOS"); r != "" {
		return strings.Split(r, ",")
	}
	return []string{
		"odvcencio/arbiter",
		"odvcencio/gotreesitter",
	}
}

func verifySignature(payload []byte, signature, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
