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
	Push     bool          // push to remote after each successful sync
	// OnEvent is called after each sync attempt.
	// info carries best-effort process/user/time context for the change;
	// its Source field indicates how reliable the attribution is.
	// A nil result with no error means DryRun or no-op.
	OnEvent func(path string, info ChangeInfo, result *SyncResult, err error)
}

// mergeAttribution returns the ChangeInfo to store for a pending file event.
// Always takes the latest (incoming) attribution except when that would
// downgrade a reliable fanotify entry to an unreliable proc-scan one.
func mergeAttribution(existing ChangeInfo, hasExisting bool, incoming ChangeInfo) ChangeInfo {
	if !hasExisting || !existing.Reliable || incoming.Reliable {
		return incoming
	}
	return existing
}

// Watch monitors every tracked file for changes and runs sysfig sync
// automatically when a change is detected. It blocks until the stop channel
// is closed.
//
// Debouncing: rapid successive writes to the same file (e.g. editors that
// write via a temp file) trigger only one sync after the debounce window.
//
// Process attribution: on Linux, Watch attempts to identify which process
// caused each change. See ChangeInfo and ChangeSource for reliability details.
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

	// Process attribution: best-effort, degrades silently on non-Linux or
	// when CAP_SYS_ADMIN is absent (nullWatcher / procScanner fallback).
	pw := newProcWatcher(paths)
	defer pw.Close()

	// pending holds files that changed but have not yet been synced.
	// The value is the ChangeInfo captured at first-detection time.
	// The debounce timer fires a sync after Debounce silence.
	pending := map[string]ChangeInfo{}
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
				name := event.Name
				if _, isTracked := trackedFiles[name]; !isTracked {
					continue
				}
				// Re-register the (possibly new) inode so future writes are
				// caught even when the editor replaced the file atomically.
				_ = watcher.Add(name)

				// Lookup proc info right now — before debounce — so the process
				// is more likely to still be alive with the file open.
				info := pw.Lookup(name)
				existing, hasExisting := pending[name]
				pending[name] = mergeAttribution(existing, hasExisting, info)
				resetTimer()
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			if opts.OnEvent != nil {
				opts.OnEvent("", ChangeInfo{ChangedAt: time.Now()}, nil, fmt.Errorf("watcher error: %w", err))
			}

		case <-timer.C:
			timerActive = false
			if len(pending) == 0 {
				continue
			}

			// Snapshot and clear pending so new events can accumulate while
			// the sync runs.
			snapshot := pending
			pending = map[string]ChangeInfo{}

			if opts.DryRun {
				for p, info := range snapshot {
					if opts.OnEvent != nil {
						opts.OnEvent(p, info, nil, nil)
					}
				}
				continue
			}

			// Run sync.
			result, err := Sync(SyncOptions{
				BaseDir: opts.BaseDir,
				SysRoot: opts.SysRoot,
			})
			// Push to remote if requested and sync produced new commits.
			if err == nil && opts.Push && result != nil && result.Committed {
				if pushErr := Push(PushOptions{BaseDir: opts.BaseDir}); pushErr != nil {
					if opts.OnEvent != nil {
						opts.OnEvent("", ChangeInfo{ChangedAt: time.Now()}, nil, fmt.Errorf("push: %w", pushErr))
					}
				}
			}
			for p, info := range snapshot {
				if opts.OnEvent != nil {
					opts.OnEvent(p, info, result, err)
				}
			}
		}
	}
}
