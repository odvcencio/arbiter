package dataplane

import "time"

// BundleSyncStatus describes the last known local sync state for one bundle name.
type BundleSyncStatus struct {
	Name                string    `json:"name"`
	BundleID            string    `json:"bundle_id,omitempty"`
	Checksum            string    `json:"checksum,omitempty"`
	LoadedAt            time.Time `json:"loaded_at,omitempty"`
	BundleSyncedAt      time.Time `json:"bundle_synced_at,omitempty"`
	OverrideSyncedAt    time.Time `json:"override_synced_at,omitempty"`
	StalenessMs         int64     `json:"staleness_ms,omitempty"`
	OverrideStalenessMs int64     `json:"override_staleness_ms,omitempty"`
	BundleErrorsTotal   int64     `json:"bundle_errors_total"`
	OverrideErrorsTotal int64     `json:"override_errors_total"`
	BundleReconnects    int64     `json:"bundle_reconnects"`
	OverrideReconnects  int64     `json:"override_reconnects"`
	LastBundleError     string    `json:"last_bundle_error,omitempty"`
	LastBundleErrorAt   time.Time `json:"last_bundle_error_at,omitempty"`
	LastOverrideError   string    `json:"last_override_error,omitempty"`
	LastOverrideErrorAt time.Time `json:"last_override_error_at,omitempty"`
}

// AgentStatus describes the current data-plane sync state across tracked bundles.
type AgentStatus struct {
	Ready                   bool               `json:"ready"`
	PrimaryName             string             `json:"primary_name,omitempty"`
	BundleErrorsTotal       int64              `json:"bundle_errors_total"`
	OverrideErrorsTotal     int64              `json:"override_errors_total"`
	BundleReconnectsTotal   int64              `json:"bundle_reconnects_total"`
	OverrideReconnectsTotal int64              `json:"override_reconnects_total"`
	LastUpstreamError       string             `json:"last_upstream_error,omitempty"`
	LastUpstreamErrorAt     time.Time          `json:"last_upstream_error_at,omitempty"`
	Bundles                 []BundleSyncStatus `json:"bundles,omitempty"`
}
