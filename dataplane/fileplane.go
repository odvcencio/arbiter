package dataplane

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	arbiter "github.com/odvcencio/arbiter"
)

// FileControlPlane is a local stand-in for the future gRPC control plane.
// It serves one bundle source file and emits watch events when the file changes.
type FileControlPlane struct {
	path       string
	bundleName string
}

// NewFileControlPlane creates a file-backed control plane adapter.
func NewFileControlPlane(path, bundleName string) *FileControlPlane {
	return &FileControlPlane{path: path, bundleName: bundleName}
}

// GetBundle reads and returns the current bundle source.
func (f *FileControlPlane) GetBundle(_ context.Context, locator BundleLocator) (*Bundle, error) {
	unit, err := arbiter.LoadFileUnit(f.path)
	if err != nil {
		return nil, err
	}
	return f.bundleFromSource(unit.Source, locator)
}

// WatchBundles watches the file and emits a new bundle on every write.
func (f *FileControlPlane) WatchBundles(ctx context.Context, locator WatchRequest) (BundleStream, error) {
	unit, err := arbiter.LoadFileUnit(f.path)
	if err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	tracked := make(map[string]struct{}, len(unit.Files))
	for _, file := range unit.Files {
		tracked[filepath.Clean(file)] = struct{}{}
	}
	watchedDirs := make(map[string]struct{})
	for file := range tracked {
		dir := filepath.Dir(file)
		if _, ok := watchedDirs[dir]; ok {
			continue
		}
		if err := watcher.Add(dir); err != nil {
			_ = watcher.Close()
			return nil, fmt.Errorf("watch directory %s: %w", dir, err)
		}
		watchedDirs[dir] = struct{}{}
	}

	events := make(chan *BundleEvent, 8)
	errs := make(chan error, 1)
	done := make(chan struct{})

	stream := &fileBundleStream{
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

		sendBundle := func() bool {
			unit, err := arbiter.LoadFileUnit(f.path)
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				return false
			}
			bundle, err := f.bundleFromSource(unit.Source, BundleLocator{Name: locator.Name, BundleID: locator.BundleID})
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				return false
			}
			ev := &BundleEvent{Type: BundleEventPublished, Bundle: *bundle}
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
				if _, ok := tracked[filepath.Clean(event.Name)]; !ok {
					continue
				}
				sendBundle()
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

func (f *FileControlPlane) bundleFromSource(source []byte, locator BundleLocator) (*Bundle, error) {
	name := locator.Name
	if name == "" {
		name = f.bundleName
	}
	if name == "" {
		name = filepath.Base(f.path)
	}
	checksum := sourceChecksum(source)
	id := locator.BundleID
	if id == "" {
		id = bundleIdentity(name, source)
	}

	return &Bundle{
		ID:          id,
		Name:        name,
		Checksum:    checksum,
		Source:      append([]byte(nil), source...),
		PublishedAt: time.Now().UTC(),
		Active:      true,
	}, nil
}

type fileBundleStream struct {
	events  <-chan *BundleEvent
	errs    <-chan error
	done    chan struct{}
	watcher *fsnotify.Watcher
	once    sync.Once
}

func (s *fileBundleStream) Recv() (*BundleEvent, error) {
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

func (s *fileBundleStream) Close() error {
	var closeErr error
	s.once.Do(func() {
		close(s.done)
		if s.watcher != nil {
			closeErr = s.watcher.Close()
		}
	})
	return closeErr
}

var _ BundleStream = (*fileBundleStream)(nil)
var _ ControlPlane = (*FileControlPlane)(nil)
