package dataplane

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/odvcencio/arbiter/overrides"
)

// FileOverrideControlPlane reads a persisted override snapshot from disk and
// emits watch events when the file changes.
type FileOverrideControlPlane struct {
	path string
}

// NewFileOverrideControlPlane creates a file-backed override sync source.
func NewFileOverrideControlPlane(path string) *FileOverrideControlPlane {
	return &FileOverrideControlPlane{path: path}
}

// GetOverrides reads the override snapshot for one bundle from disk.
func (f *FileOverrideControlPlane) GetOverrides(_ context.Context, locator OverrideLocator) (*overrides.Snapshot, error) {
	snapshot, err := f.load()
	if err != nil {
		return nil, err
	}
	return selectOverrideSnapshot(snapshot, locator), nil
}

// WatchOverrides watches the override file and emits the selected bundle snapshot on every write.
func (f *FileOverrideControlPlane) WatchOverrides(ctx context.Context, locator OverrideLocator) (OverrideStream, error) {
	target := filepath.Clean(f.path)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}
	dir := filepath.Dir(target)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watch directory %s: %w", dir, err)
	}

	events := make(chan *OverrideEvent, 4)
	errs := make(chan error, 1)
	done := make(chan struct{})
	stream := &fileOverrideStream{
		events:  events,
		errs:    errs,
		done:    done,
		watcher: watcher,
	}

	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()

	go func() {
		defer close(events)
		defer close(errs)
		defer watcher.Close()

		sendSnapshot := func() bool {
			snapshot, err := f.load()
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				return false
			}
			selected := selectOverrideSnapshot(snapshot, locator)
			ev := &OverrideEvent{
				Type:     OverrideEventMutation,
				BundleID: bundleIDForOverrideLocator(locator),
				Snapshot: *selected,
			}
			select {
			case events <- ev:
				return true
			case <-done:
				return false
			case <-ctx.Done():
				return false
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				if filepath.Clean(event.Name) != target {
					continue
				}
				sendSnapshot()
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()

	return stream, nil
}

func (f *FileOverrideControlPlane) load() (overrides.Snapshot, error) {
	store := overrides.NewStore()
	if f.path == "" {
		return store.Snapshot(), nil
	}
	if err := store.LoadFile(f.path); err != nil {
		if os.IsNotExist(err) {
			return overrides.Snapshot{}, nil
		}
		return overrides.Snapshot{}, err
	}
	return store.Snapshot(), nil
}

func selectOverrideSnapshot(snapshot overrides.Snapshot, locator OverrideLocator) *overrides.Snapshot {
	bundleID := bundleIDForOverrideLocator(locator)
	selected := overrides.Snapshot{}
	if bundleID == "" {
		return &selected
	}
	if rules, ok := snapshot.Rules[bundleID]; ok {
		selected.Rules = map[string]map[string]overrides.RuleOverride{
			bundleID: cloneRuleOverrides(rules),
		}
	}
	if flags, ok := snapshot.Flags[bundleID]; ok {
		selected.Flags = map[string]map[string]overrides.FlagOverride{
			bundleID: cloneFlagOverrides(flags),
		}
	}
	if flagRules, ok := snapshot.FlagRules[bundleID]; ok {
		selected.FlagRules = map[string]map[string]map[int]overrides.FlagRuleOverride{
			bundleID: cloneFlagRuleSets(flagRules),
		}
	}
	return &selected
}

func bundleIDForOverrideLocator(locator OverrideLocator) string {
	if locator.BundleID != "" {
		return locator.BundleID
	}
	return locator.Name
}

func cloneRuleOverrides(in map[string]overrides.RuleOverride) map[string]overrides.RuleOverride {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]overrides.RuleOverride, len(in))
	for k, v := range in {
		out[k] = cloneRuleOverrideValue(v)
	}
	return out
}

func cloneFlagOverrides(in map[string]overrides.FlagOverride) map[string]overrides.FlagOverride {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]overrides.FlagOverride, len(in))
	for k, v := range in {
		out[k] = cloneFlagOverrideValue(v)
	}
	return out
}

func cloneFlagRuleSets(in map[string]map[int]overrides.FlagRuleOverride) map[string]map[int]overrides.FlagRuleOverride {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[int]overrides.FlagRuleOverride, len(in))
	for flagKey, ruleSet := range in {
		cloned := make(map[int]overrides.FlagRuleOverride, len(ruleSet))
		for idx, ov := range ruleSet {
			cloned[idx] = cloneFlagRuleOverrideValue(ov)
		}
		out[flagKey] = cloned
	}
	return out
}

func cloneRuleOverrideValue(ov overrides.RuleOverride) overrides.RuleOverride {
	out := overrides.RuleOverride{}
	if ov.KillSwitch != nil {
		v := *ov.KillSwitch
		out.KillSwitch = &v
	}
	if ov.Rollout != nil {
		v := *ov.Rollout
		out.Rollout = &v
	}
	return out
}

func cloneFlagOverrideValue(ov overrides.FlagOverride) overrides.FlagOverride {
	out := overrides.FlagOverride{}
	if ov.KillSwitch != nil {
		v := *ov.KillSwitch
		out.KillSwitch = &v
	}
	return out
}

func cloneFlagRuleOverrideValue(ov overrides.FlagRuleOverride) overrides.FlagRuleOverride {
	out := overrides.FlagRuleOverride{}
	if ov.Rollout != nil {
		v := *ov.Rollout
		out.Rollout = &v
	}
	return out
}

type fileOverrideStream struct {
	events  <-chan *OverrideEvent
	errs    <-chan error
	done    chan struct{}
	watcher *fsnotify.Watcher
	once    sync.Once
}

func (s *fileOverrideStream) Recv() (*OverrideEvent, error) {
	select {
	case ev, ok := <-s.events:
		if !ok {
			return nil, io.EOF
		}
		return ev, nil
	case err, ok := <-s.errs:
		if !ok {
			return nil, io.EOF
		}
		if err == nil {
			return nil, io.EOF
		}
		return nil, err
	case <-s.done:
		return nil, io.EOF
	}
}

func (s *fileOverrideStream) Close() error {
	var closeErr error
	s.once.Do(func() {
		close(s.done)
		if s.watcher != nil {
			closeErr = s.watcher.Close()
		}
	})
	return closeErr
}

var _ OverrideControlPlane = (*FileOverrideControlPlane)(nil)
var _ OverrideStream = (*fileOverrideStream)(nil)
