package dataplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/odvcencio/arbiter/overrides"
)

// BundleLocator identifies one bundle by name or explicit ID.
type BundleLocator struct {
	Name     string
	BundleID string
}

// WatchRequest configures a bundle watch stream.
type WatchRequest struct {
	Name       string
	BundleID   string
	ActiveOnly bool
}

// OverrideLocator identifies one bundle override set by name or explicit ID.
type OverrideLocator struct {
	Name     string
	BundleID string
}

// Bundle is the source payload and metadata received from control plane sync.
type Bundle struct {
	ID          string
	Name        string
	Checksum    string
	Source      []byte
	PublishedAt time.Time
	Active      bool
}

// BundleEventType describes why a bundle update was emitted.
type BundleEventType string

const (
	BundleEventSnapshot  BundleEventType = "snapshot"
	BundleEventPublished BundleEventType = "published"
	BundleEventActivated BundleEventType = "activated"
	BundleEventRolled    BundleEventType = "rolled_back"
	BundleEventUnknown   BundleEventType = "unknown"
)

// BundleEvent is one update from the control-plane watch stream.
type BundleEvent struct {
	Type   BundleEventType
	Bundle Bundle
}

// BundleStream is a live watch stream of bundle events.
type BundleStream interface {
	Recv() (*BundleEvent, error)
	Close() error
}

// OverrideEventType describes why an override snapshot was emitted.
type OverrideEventType string

const (
	OverrideEventSnapshot OverrideEventType = "snapshot"
	OverrideEventMutation OverrideEventType = "mutation"
	OverrideEventUnknown  OverrideEventType = "unknown"
)

// OverrideEvent is one update from the override watch stream.
type OverrideEvent struct {
	Type     OverrideEventType
	BundleID string
	Snapshot overrides.Snapshot
}

// OverrideStream is a live watch stream of override snapshots.
type OverrideStream interface {
	Recv() (*OverrideEvent, error)
	Close() error
}

// ControlPlane describes the sync contract the local agent consumes.
type ControlPlane interface {
	GetBundle(context.Context, BundleLocator) (*Bundle, error)
	WatchBundles(context.Context, WatchRequest) (BundleStream, error)
}

// OverrideControlPlane describes the override sync contract the local agent consumes.
type OverrideControlPlane interface {
	GetOverrides(context.Context, OverrideLocator) (*overrides.Snapshot, error)
	WatchOverrides(context.Context, OverrideLocator) (OverrideStream, error)
}

func bundleIdentity(name string, source []byte) string {
	sum := sha256.Sum256(append(append([]byte(name), 0), source...))
	return hex.EncodeToString(sum[:])[:16]
}

func sourceChecksum(source []byte) string {
	sum := sha256.Sum256(source)
	return hex.EncodeToString(sum[:])
}
