package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/odvcencio/arbiter/dataplane"
)

type readinessPolicy struct {
	maxStaleness time.Duration
}

func newStatusHandler(syncer *dataplane.Agent, policy readinessPolicy) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, reason := readinessStatus(syncer, policy)
		if reason != "" {
			http.Error(w, reason, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		if syncer == nil {
			http.Error(w, "status unavailable", http.StatusServiceUnavailable)
			return
		}
		status := syncer.Status()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(status)
	})
	return mux
}

func readinessStatus(syncer *dataplane.Agent, policy readinessPolicy) (dataplane.AgentStatus, string) {
	if syncer == nil {
		return dataplane.AgentStatus{}, "status unavailable"
	}
	status := syncer.Status()
	if !status.Ready {
		return status, "initial sync incomplete"
	}
	if policy.maxStaleness <= 0 {
		return status, ""
	}

	limitMs := policy.maxStaleness.Milliseconds()
	for _, bundle := range status.Bundles {
		name := bundle.Name
		if name == "" {
			name = bundle.BundleID
		}
		if bundle.BundleSyncedAt.IsZero() {
			return status, fmt.Sprintf("bundle %s has never synced", name)
		}
		if bundle.StalenessMs > limitMs {
			return status, fmt.Sprintf("bundle %s stale (%dms > %dms)", name, bundle.StalenessMs, limitMs)
		}
		if !bundle.OverrideSyncedAt.IsZero() && bundle.OverrideStalenessMs > limitMs {
			return status, fmt.Sprintf("bundle %s overrides stale (%dms > %dms)", name, bundle.OverrideStalenessMs, limitMs)
		}
	}
	return status, ""
}
