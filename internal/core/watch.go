package core

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/aissat/sysfig/internal/state"
)

// WatchOptions configures a sysfig watch operation.
type WatchOptions struct {
	BaseDir  string        // e.g. ~/.sysfig
	SysRoot  string        // sandbox prefix (testing override)
	Debounce time.Duration // how long to wait after the last change before syncing
	DryRun   bool          // print what would be synced without committing
	// OnEvent is called after each sync attempt with the changed path and the
	// sync result (nil result means DryRun or no-op).
	OnEvent func(path string, result *SyncResult, err error)
}

// Watch monitors every tracked file for changes and runs sysfig sync
// automatically when a change is detected. It blocks until ctx is cancelled
// via the stop channel.
//
// Debouncing: rapid successive writes to the same file (e.g. editors that
// write via a temp file) trigger only one sync after the debounce window.
func Watch(opts WatchOptions, stop <-chan struct{}) error {
	if opts.Debounce == 0 {
		opts.Debounce = 2 * time.Second
	}

	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("core: watch: create watcher: %w", err)
	}
	defer watcher.Close()

	// loadPaths reads state.json and returns every tracked system path.
	loadPaths := func() ([]string, error) {
		s, err := sm.Load()
		if err != nil {
			return nil, err
		}
		var paths []string
		for _, rec := range s.Files {
			p := rec.SystemPath
			if opts.SysRoot != "" {
				p = filepath.Join(opts.SysRoot, p)
			}
			paths = append(paths, p)
		}
		return paths, nil
	}

	// Register all tracked paths AND their parent directories with the OS
	// watcher. Watching the parent is required to detect atomic saves (e.g.
	// `sed -i`, vim, nano) that replace the file with a new inode: inotify
	// loses the watch on the old inode, but fires CREATE/RENAME on the dir.
	paths, err := loadPaths()
	if err != nil {
		return fmt.Errorf("core: watch: load state: %w", err)
	}
	// trackedFiles is the set of absolute paths we care about — used to filter
	// directory events so we only react to changes in tracked files.
	trackedFiles := map[string]struct{}{}
	for _, p := range paths {
		trackedFiles[p] = struct{}{}
		_ = watcher.Add(p) // non-fatal if file missing
		if dir := filepath.Dir(p); dir != "." {
			_ = watcher.Add(dir) // non-fatal
		}
	}

	// pending holds files that changed but have not been synced yet.
	// The debounce timer fires a sync after Debounce silence.
	pending := map[string]struct{}{}
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	timerActive := false

	resetTimer := func() {
		if timerActive {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		timer.Reset(opts.Debounce)
		timerActive = true
	}

	for {
		select {
		case <-stop:
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
				// If the event is on a directory (parent watch), only proceed
				// when the affected path is one of our tracked files.
				name := event.Name
				if _, isTracked := trackedFiles[name]; !isTracked {
					// Not a directly tracked file — skip unless it's a dir event
					// that happens to land on a tracked file's path (no-op check).
					continue
				}
				// Re-register the (possibly new) inode so future writes are
				// caught even when the editor replaced the file atomically.
				_ = watcher.Add(name)
				pending[name] = struct{}{}
				resetTimer()
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			if opts.OnEvent != nil {
				opts.OnEvent("", nil, fmt.Errorf("watcher error: %w", err))
			}

		case <-timer.C:
			timerActive = false
			if len(pending) == 0 {
				continue
			}

			// Collect changed paths for display, then clear.
			changed := make([]string, 0, len(pending))
			for p := range pending {
				changed = append(changed, p)
			}
			pending = map[string]struct{}{}

			if opts.DryRun {
				for _, p := range changed {
					if opts.OnEvent != nil {
						opts.OnEvent(p, nil, nil)
					}
				}
				continue
			}

			// Run sync.
			result, err := Sync(SyncOptions{
				BaseDir: opts.BaseDir,
				SysRoot: opts.SysRoot,
			})
			for _, p := range changed {
				if opts.OnEvent != nil {
					opts.OnEvent(p, result, err)
				}
			}
		}
	}
}
