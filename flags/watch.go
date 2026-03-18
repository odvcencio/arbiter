package flags

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// Watch loads flags from a file and watches for changes, hot-reloading on write.
// Returns the Flags instance and a stop function.
func Watch(path string) (*Flags, func(), error) {
	f, err := LoadFile(path)
	if err != nil {
		return nil, nil, err
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

	// Watch the directory (fsnotify works better with dirs than individual files)
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)

	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, nil, fmt.Errorf("watch directory: %w", err)
	}

	done := make(chan struct{})

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) == base && (event.Op&fsnotify.Write != 0 || event.Op&fsnotify.Create != 0) {
					if err := f.ReloadFile(absPath); err != nil {
						log.Printf("flags: reload error: %v", err)
					} else {
						log.Printf("flags: reloaded %s", base)
					}
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
