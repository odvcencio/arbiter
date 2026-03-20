package dataplane

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/grpcserver"
	"github.com/odvcencio/arbiter/overrides"
)

// Snapshot is the compiled local state for one active bundle.
type Snapshot struct {
	Bundle   Bundle
	Compiled *arbiter.CompileResult
	LoadedAt time.Time
}

type syncTarget struct {
	locator BundleLocator
	watch   WatchRequest
}

type overrideBinding struct {
	bundleID string
	cancel   context.CancelFunc
}

type preparedOverrideSync struct {
	locator     OverrideLocator
	snapshot    overrides.Snapshot
	watchCtx    context.Context
	cancel      context.CancelFunc
	oldBundleID string
	oldCancel   context.CancelFunc
}

// Agent bootstraps bundle state from a control plane and keeps it hot-reloaded.
type Agent struct {
	cp            ControlPlane
	overrideCP    OverrideControlPlane
	registry      *grpcserver.Registry
	overrideStore *overrides.Store

	current atomic.Pointer[Snapshot]

	snapshotsMu sync.RWMutex
	snapshots   map[string]Snapshot

	ready     chan struct{}
	readyOnce sync.Once
	updates   chan Snapshot

	trackingMu  sync.Mutex
	primaryName string
	readyTarget int
	readySeen   map[string]struct{}

	overrideMu       sync.Mutex
	overrideBindings map[string]overrideBinding

	statusMu sync.RWMutex
	statuses map[string]BundleSyncStatus
}

// New creates a local sync agent backed by one control-plane client.
func New(cp ControlPlane, overrideCP ...OverrideControlPlane) *Agent {
	var ov OverrideControlPlane
	if len(overrideCP) > 0 {
		ov = overrideCP[0]
	}
	return &Agent{
		cp:               cp,
		overrideCP:       ov,
		registry:         grpcserver.NewRegistry(),
		overrideStore:    overrides.NewStore(),
		snapshots:        make(map[string]Snapshot),
		ready:            make(chan struct{}),
		updates:          make(chan Snapshot, 8),
		readySeen:        make(map[string]struct{}),
		overrideBindings: make(map[string]overrideBinding),
		statuses:         make(map[string]BundleSyncStatus),
	}
}

// Registry returns the local compiled bundle registry kept hot by the dataplane.
func (a *Agent) Registry() *grpcserver.Registry {
	return a.registry
}

// Overrides returns the local runtime override store kept in sync by the dataplane.
func (a *Agent) Overrides() *overrides.Store {
	return a.overrideStore
}

// Ready is closed after the configured bundle set finishes initial sync.
func (a *Agent) Ready() <-chan struct{} {
	return a.ready
}

// Updates streams every successfully loaded bundle snapshot.
func (a *Agent) Updates() <-chan Snapshot {
	return a.updates
}

// Current returns the current snapshot for the primary tracked bundle.
func (a *Agent) Current() (Snapshot, bool) {
	a.trackingMu.Lock()
	primary := a.primaryName
	a.trackingMu.Unlock()
	if primary != "" {
		if snapshot, ok := a.CurrentBundle(primary); ok {
			return snapshot, true
		}
	}
	ptr := a.current.Load()
	if ptr == nil {
		return Snapshot{}, false
	}
	return cloneSnapshot(*ptr), true
}

// CurrentBundle returns the current snapshot for one tracked bundle name.
func (a *Agent) CurrentBundle(name string) (Snapshot, bool) {
	a.snapshotsMu.RLock()
	defer a.snapshotsMu.RUnlock()
	snapshot, ok := a.snapshots[name]
	if !ok {
		return Snapshot{}, false
	}
	return cloneSnapshot(snapshot), true
}

// Snapshots returns a deep copy of all current tracked bundle snapshots keyed by name.
func (a *Agent) Snapshots() map[string]Snapshot {
	a.snapshotsMu.RLock()
	defer a.snapshotsMu.RUnlock()
	out := make(map[string]Snapshot, len(a.snapshots))
	for name, snapshot := range a.snapshots {
		out[name] = cloneSnapshot(snapshot)
	}
	return out
}

// Status returns the current sync health across tracked bundles.
func (a *Agent) Status() AgentStatus {
	a.statusMu.RLock()
	statuses := make([]BundleSyncStatus, 0, len(a.statuses))
	var (
		bundleErrorsTotal       int64
		overrideErrorsTotal     int64
		bundleReconnectsTotal   int64
		overrideReconnectsTotal int64
		lastUpstreamError       string
		lastUpstreamErrorAt     time.Time
	)
	for _, status := range a.statuses {
		status.StalenessMs = stalenessMs(status.BundleSyncedAt)
		status.OverrideStalenessMs = stalenessMs(status.OverrideSyncedAt)
		bundleErrorsTotal += status.BundleErrorsTotal
		overrideErrorsTotal += status.OverrideErrorsTotal
		bundleReconnectsTotal += status.BundleReconnects
		overrideReconnectsTotal += status.OverrideReconnects
		if status.LastBundleError != "" && status.LastBundleErrorAt.After(lastUpstreamErrorAt) {
			lastUpstreamError = status.LastBundleError
			lastUpstreamErrorAt = status.LastBundleErrorAt
		}
		if status.LastOverrideError != "" && status.LastOverrideErrorAt.After(lastUpstreamErrorAt) {
			lastUpstreamError = status.LastOverrideError
			lastUpstreamErrorAt = status.LastOverrideErrorAt
		}
		statuses = append(statuses, status)
	}
	a.statusMu.RUnlock()

	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Name < statuses[j].Name
	})

	a.trackingMu.Lock()
	primary := a.primaryName
	readyTarget := a.readyTarget
	readySeen := len(a.readySeen)
	a.trackingMu.Unlock()

	return AgentStatus{
		Ready:                   readyTarget > 0 && readySeen >= readyTarget,
		PrimaryName:             primary,
		BundleErrorsTotal:       bundleErrorsTotal,
		OverrideErrorsTotal:     overrideErrorsTotal,
		BundleReconnectsTotal:   bundleReconnectsTotal,
		OverrideReconnectsTotal: overrideReconnectsTotal,
		LastUpstreamError:       lastUpstreamError,
		LastUpstreamErrorAt:     lastUpstreamErrorAt,
		Bundles:                 statuses,
	}
}

// Bootstrap loads one bundle, compiles it locally, and stores the snapshot.
func (a *Agent) Bootstrap(ctx context.Context, locator BundleLocator) error {
	bundle, err := a.cp.GetBundle(ctx, locator)
	if err != nil {
		err = fmt.Errorf("get bundle: %w", err)
		a.recordBundleError(firstNonEmpty(locator.Name, locator.BundleID), err)
		return err
	}
	if bundle != nil && locator.Name != "" && locator.BundleID == "" {
		bundle.Active = true
	}
	return a.applyBundle(ctx, *bundle)
}

// Run bootstraps one bundle, then watches for updates until ctx is canceled.
func (a *Agent) Run(ctx context.Context, locator BundleLocator, watch WatchRequest) error {
	return a.runAll(ctx, []syncTarget{{
		locator: locator,
		watch:   watch,
	}})
}

// RunMany bootstraps and watches multiple active bundles by name.
func (a *Agent) RunMany(ctx context.Context, bundleNames []string) error {
	targets := make([]syncTarget, 0, len(bundleNames))
	seen := make(map[string]struct{}, len(bundleNames))
	for _, name := range bundleNames {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		targets = append(targets, syncTarget{
			locator: BundleLocator{Name: name},
			watch: WatchRequest{
				Name:       name,
				ActiveOnly: true,
			},
		})
	}
	if len(targets) == 0 {
		return errors.New("at least one bundle name is required")
	}
	return a.runAll(ctx, targets)
}

func (a *Agent) runAll(ctx context.Context, targets []syncTarget) error {
	defer close(a.updates)

	a.initTracking(targets)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	done := make(chan struct{})

	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.runOne(runCtx, target); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	case <-done:
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		return nil
	case <-runCtx.Done():
		wg.Wait()
		return runCtx.Err()
	}
}

func (a *Agent) initTracking(targets []syncTarget) {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()

	a.primaryName = ""
	a.readyTarget = 0
	a.readySeen = make(map[string]struct{}, len(targets))

	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	for _, target := range targets {
		name := targetName(target)
		if name == "" {
			continue
		}
		if a.primaryName == "" {
			a.primaryName = name
		}
		a.readyTarget++
		status := a.statuses[name]
		status.Name = name
		a.statuses[name] = status
	}
}

func (a *Agent) runOne(ctx context.Context, target syncTarget) error {
	name := targetName(target)
	backoff := 100 * time.Millisecond
	for {
		if err := a.Bootstrap(ctx, target.locator); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !sleepContext(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff)
			continue
		}
		break
	}

	backoff = 100 * time.Millisecond
	connected := false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		stream, err := a.cp.WatchBundles(ctx, target.watch)
		if err != nil {
			a.recordBundleError(name, fmt.Errorf("watch bundles: %w", err))
			if !sleepContext(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if connected {
			a.recordBundleReconnect(name)
		}
		connected = true
		backoff = 100 * time.Millisecond
		skipBootstrapSnapshot := true

		for {
			event, err := stream.Recv()
			if err != nil {
				_ = stream.Close()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				a.recordBundleError(name, fmt.Errorf("watch bundles recv: %w", err))
				if !sleepContext(ctx, backoff) {
					return ctx.Err()
				}
				backoff = nextBackoff(backoff)
				break
			}
			if event == nil {
				continue
			}
			if skipBootstrapSnapshot && event.Type == BundleEventSnapshot && a.matchesCurrentForName(name, event.Bundle) {
				skipBootstrapSnapshot = false
				continue
			}
			skipBootstrapSnapshot = false
			if err := a.applyBundle(ctx, event.Bundle); err != nil {
				continue
			}
			backoff = 100 * time.Millisecond
		}
	}
}

func (a *Agent) applyBundle(ctx context.Context, bundle Bundle) error {
	if len(bundle.Source) == 0 {
		err := errors.New("bundle source is required")
		a.recordBundleError(bundle.Name, err)
		return err
	}

	local, err := grpcserver.BuildBundle(bundle.Name, bundle.Source, bundle.PublishedAt)
	if err != nil {
		err = fmt.Errorf("compile bundle: %w", err)
		a.recordBundleError(bundle.Name, err)
		return err
	}
	if bundle.ID != "" && local.ID != bundle.ID {
		err = fmt.Errorf("bundle identity mismatch: got %s want %s", local.ID, bundle.ID)
		a.recordBundleError(bundle.Name, err)
		return err
	}

	nextBundle := Bundle{
		ID:          local.ID,
		Name:        local.Name,
		Checksum:    local.Checksum,
		Source:      append([]byte(nil), bundle.Source...),
		PublishedAt: local.Published,
		Active:      bundle.Active,
	}
	preparedOverrides, err := a.prepareOverrideSync(ctx, nextBundle)
	if err != nil {
		return err
	}
	if preparedOverrides != nil {
		a.overrideStore.RestoreBundle(nextBundle.ID, preparedOverrides.snapshot)
	}

	installed, err := a.registry.Install(local, bundle.Active)
	if err != nil {
		if preparedOverrides != nil {
			preparedOverrides.cancel()
			a.overrideStore.RestoreBundle(nextBundle.ID, overrides.Snapshot{})
		}
		err = fmt.Errorf("install bundle: %w", err)
		a.recordBundleError(bundle.Name, err)
		return err
	}

	loadedAt := time.Now().UTC()
	snapshot := Snapshot{
		Bundle: Bundle{
			ID:          installed.ID,
			Name:        installed.Name,
			Checksum:    installed.Checksum,
			Source:      append([]byte(nil), bundle.Source...),
			PublishedAt: installed.Published,
			Active:      bundle.Active,
		},
		Compiled: installed.Compiled,
		LoadedAt: loadedAt,
	}
	if preparedOverrides != nil {
		a.activateOverrideSync(preparedOverrides)
	}

	a.current.Store(&snapshot)
	a.snapshotsMu.Lock()
	a.snapshots[snapshot.Bundle.Name] = cloneSnapshot(snapshot)
	a.snapshotsMu.Unlock()

	a.recordBundleStatus(snapshot.Bundle, loadedAt)
	a.markReady(snapshot.Bundle.Name)

	select {
	case a.updates <- cloneSnapshot(snapshot):
	default:
	}
	return nil
}

func (a *Agent) markReady(name string) {
	if name == "" {
		return
	}
	a.trackingMu.Lock()
	if a.readyTarget == 0 {
		a.readyTarget = 1
	}
	a.readySeen[name] = struct{}{}
	ready := len(a.readySeen) >= a.readyTarget
	a.trackingMu.Unlock()
	if ready {
		a.readyOnce.Do(func() { close(a.ready) })
	}
}

func (a *Agent) prepareOverrideSync(ctx context.Context, bundle Bundle) (*preparedOverrideSync, error) {
	if a.overrideCP == nil || bundle.ID == "" || bundle.Name == "" {
		return nil, nil
	}

	prepared := &preparedOverrideSync{
		locator: OverrideLocator{Name: bundle.Name, BundleID: bundle.ID},
	}
	a.overrideMu.Lock()
	if binding, ok := a.overrideBindings[bundle.Name]; ok {
		prepared.oldBundleID = binding.bundleID
		prepared.oldCancel = binding.cancel
	}
	a.overrideMu.Unlock()

	snapshot, err := a.overrideCP.GetOverrides(ctx, prepared.locator)
	if err != nil {
		err = fmt.Errorf("get overrides: %w", err)
		a.recordOverrideError(bundle.Name, bundle.ID, err)
		return nil, err
	}
	if snapshot == nil {
		snapshot = &overrides.Snapshot{}
	}
	prepared.snapshot = *snapshot
	prepared.watchCtx, prepared.cancel = context.WithCancel(ctx)
	return prepared, nil
}

func (a *Agent) activateOverrideSync(prepared *preparedOverrideSync) {
	if prepared == nil {
		return
	}
	a.overrideMu.Lock()
	a.overrideBindings[prepared.locator.Name] = overrideBinding{
		bundleID: prepared.locator.BundleID,
		cancel:   prepared.cancel,
	}
	a.overrideMu.Unlock()

	if prepared.oldCancel != nil {
		prepared.oldCancel()
	}
	if prepared.oldBundleID != "" && prepared.oldBundleID != prepared.locator.BundleID {
		a.overrideStore.RestoreBundle(prepared.oldBundleID, overrides.Snapshot{})
	}
	a.recordOverrideStatus(prepared.locator.Name, prepared.locator.BundleID, time.Now().UTC())
	go a.runOverrideWatch(prepared.watchCtx, prepared.locator)
}

func (a *Agent) runOverrideWatch(ctx context.Context, locator OverrideLocator) {
	backoff := 100 * time.Millisecond
	connected := false
	for {
		if ctx.Err() != nil {
			return
		}

		stream, err := a.overrideCP.WatchOverrides(ctx, locator)
		if err != nil {
			a.recordOverrideError(locator.Name, locator.BundleID, fmt.Errorf("watch overrides: %w", err))
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if connected {
			a.recordOverrideReconnect(locator.Name, locator.BundleID)
		}
		connected = true
		backoff = 100 * time.Millisecond

		for {
			event, err := stream.Recv()
			if err != nil {
				_ = stream.Close()
				if ctx.Err() != nil {
					return
				}
				a.recordOverrideError(locator.Name, locator.BundleID, fmt.Errorf("watch overrides recv: %w", err))
				if !sleepContext(ctx, backoff) {
					return
				}
				backoff = nextBackoff(backoff)
				break
			}
			if event == nil {
				continue
			}
			bundleID := firstNonEmpty(event.BundleID, locator.BundleID)
			if bundleID == "" || bundleID != locator.BundleID {
				continue
			}
			a.overrideStore.RestoreBundle(bundleID, event.Snapshot)
			a.recordOverrideStatus(locator.Name, bundleID, time.Now().UTC())
			backoff = 100 * time.Millisecond
		}
	}
}

func (a *Agent) recordBundleStatus(bundle Bundle, syncedAt time.Time) {
	if bundle.Name == "" {
		return
	}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	status := a.statuses[bundle.Name]
	status.Name = bundle.Name
	status.BundleID = bundle.ID
	status.Checksum = bundle.Checksum
	status.LoadedAt = syncedAt
	status.BundleSyncedAt = syncedAt
	a.statuses[bundle.Name] = status
}

func (a *Agent) recordOverrideStatus(name, bundleID string, syncedAt time.Time) {
	if name == "" {
		return
	}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	status := a.statuses[name]
	status.Name = name
	if bundleID != "" {
		status.BundleID = bundleID
	}
	status.OverrideSyncedAt = syncedAt
	a.statuses[name] = status
}

func (a *Agent) recordBundleReconnect(name string) {
	if name == "" {
		return
	}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	status := a.statuses[name]
	status.Name = name
	status.BundleReconnects++
	a.statuses[name] = status
}

func (a *Agent) recordBundleError(name string, err error) {
	if name == "" || err == nil {
		return
	}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	status := a.statuses[name]
	status.Name = name
	status.BundleErrorsTotal++
	status.LastBundleError = err.Error()
	status.LastBundleErrorAt = time.Now().UTC()
	a.statuses[name] = status
}

func (a *Agent) recordOverrideReconnect(name, bundleID string) {
	if name == "" {
		return
	}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	status := a.statuses[name]
	status.Name = name
	if bundleID != "" {
		status.BundleID = bundleID
	}
	status.OverrideReconnects++
	a.statuses[name] = status
}

func (a *Agent) recordOverrideError(name, bundleID string, err error) {
	if name == "" || err == nil {
		return
	}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	status := a.statuses[name]
	status.Name = name
	if bundleID != "" {
		status.BundleID = bundleID
	}
	status.OverrideErrorsTotal++
	status.LastOverrideError = err.Error()
	status.LastOverrideErrorAt = time.Now().UTC()
	a.statuses[name] = status
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Bundle = cloneBundle(snapshot.Bundle)
	return snapshot
}

func cloneBundle(bundle Bundle) Bundle {
	bundle.Source = append([]byte(nil), bundle.Source...)
	return bundle
}

func (a *Agent) matchesCurrentForName(name string, bundle Bundle) bool {
	if name != "" {
		if current, ok := a.CurrentBundle(name); ok {
			return sameBundle(current.Bundle, bundle)
		}
	}
	return a.matchesCurrent(bundle)
}

func (a *Agent) matchesCurrent(bundle Bundle) bool {
	current, ok := a.Current()
	if !ok {
		return false
	}
	return sameBundle(current.Bundle, bundle)
}

func sameBundle(left, right Bundle) bool {
	if left.ID != "" && right.ID != "" {
		return left.ID == right.ID
	}
	if left.Checksum != "" && right.Checksum != "" {
		return left.Checksum == right.Checksum
	}
	return false
}

func targetName(target syncTarget) string {
	return firstNonEmpty(target.watch.Name, target.locator.Name)
}

func stalenessMs(loadedAt time.Time) int64 {
	if loadedAt.IsZero() {
		return 0
	}
	return time.Since(loadedAt).Milliseconds()
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return 100 * time.Millisecond
	}
	next := current * 2
	if next > 5*time.Second {
		return 5 * time.Second
	}
	return next
}
