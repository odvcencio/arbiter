package flags

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/odvcencio/arbiter"
)

// Watch loads flags from a file and watches for changes, hot-reloading on write.
// Returns the Flags instance and a stop function.
func Watch(path string) (*Flags, func(), error) {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return nil, nil, err
	}
	full, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, nil, arbiter.WrapFileError(unit, err)
	}
	f, err := LoadParsed(parsed, full)
	if err != nil {
		return nil, nil, arbiter.WrapFileError(unit, err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, fmt.Errorf("create watcher: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		watcher.Close()
		return nil, nil, fmt.Errorf("resolve path: %w", err)
	}

	watchedDirs := make(map[string]struct{})
	trackedFiles := fileSet(unit.Files)
	if err := syncWatchedDirs(watcher, watchedDirs, unit.Files); err != nil {
		watcher.Close()
		return nil, nil, err
	}

	done := make(chan struct{})

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				eventPath := filepath.Clean(event.Name)
				if _, ok := trackedFiles[eventPath]; ok && (event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0) {
					nextUnit, nextParsed, err := arbiter.LoadFileParsed(absPath)
					if err != nil {
						log.Printf("flags: reload error: %v", err)
						continue
					}
					nextFull, err := arbiter.CompileFullParsed(nextParsed)
					if err != nil {
						log.Printf("flags: reload error: %v", arbiter.WrapFileError(nextUnit, err))
						continue
					}
					newF, err := LoadParsed(nextParsed, nextFull)
					if err != nil {
						log.Printf("flags: reload error: %v", arbiter.WrapFileError(nextUnit, err))
						continue
					}
					f.mu.Lock()
					f.defs = newF.defs
					f.segments = newF.segments
					f.source = newF.source
					f.mu.Unlock()
					if err := syncWatchedDirs(watcher, watchedDirs, nextUnit.Files); err != nil {
						log.Printf("flags: watcher sync error: %v", err)
					}
					trackedFiles = fileSet(nextUnit.Files)
					log.Printf("flags: reloaded %s", filepath.Base(absPath))
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("flags: watcher error: %v", err)
			case <-done:
				return
			}
		}
	}()

	stop := func() {
		close(done)
		watcher.Close()
	}

	return f, stop, nil
}

func fileSet(files []string) map[string]struct{} {
	out := make(map[string]struct{}, len(files))
	for _, file := range files {
		out[filepath.Clean(file)] = struct{}{}
	}
	return out
}

func syncWatchedDirs(watcher *fsnotify.Watcher, current map[string]struct{}, files []string) error {
	next := make(map[string]struct{})
	for _, file := range files {
		next[filepath.Dir(file)] = struct{}{}
	}

	for dir := range current {
		if _, ok := next[dir]; ok {
			continue
		}
		if err := watcher.Remove(dir); err != nil {
			return fmt.Errorf("unwatch directory %s: %w", dir, err)
		}
		delete(current, dir)
	}

	for dir := range next {
		if _, ok := current[dir]; ok {
			continue
		}
		if err := watcher.Add(dir); err != nil {
			return fmt.Errorf("watch directory %s: %w", dir, err)
		}
		current[dir] = struct{}{}
	}

	return nil
}
