package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/pkg/types"
	"golang.org/x/sys/unix"
)

// Manager manages the state.json file with flock-based concurrency control.
type Manager struct {
	statePath string
	lockPath  string
}

// NewManager creates a Manager for statePath (e.g. ~/.sysfig/state.json).
// The lock file will be at <statePath>.lock.
func NewManager(statePath string) *Manager {
	return &Manager{
		statePath: statePath,
		lockPath:  statePath + ".lock",
	}
}

// load reads and parses state.json without acquiring any lock.
// Must only be called while the caller already holds at least LOCK_SH
// on m.lockPath (enforced by Load and WithLock).
func (m *Manager) load() (*types.State, error) {
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return initState(), nil
		}
		return nil, fmt.Errorf("state: read %q: %w", m.statePath, err)
	}

	var s types.State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state: unmarshal: %w", err)
	}

	// Ensure maps are never nil even if the JSON omitted them.
	if s.Files == nil {
		s.Files = make(map[string]*types.FileRecord)
	}
	if s.Backups == nil {
		s.Backups = make(map[string][]types.BackupRecord)
	}

	return &s, nil
}

// Load acquires a shared lock, reads and parses state.json, then releases
// the lock. Returns an empty State if the file does not exist.
// Use WithLock when you need to mutate state.
func (m *Manager) Load() (*types.State, error) {
	lockFile, err := openLockFile(m.lockPath)
	if err != nil {
		return nil, fmt.Errorf("state: open lock file %q: %w", m.lockPath, err)
	}
	if lockFile == nil {
		// Lock file is inaccessible (e.g. left root-owned by a previous sudo
		// run). Fall back to an unlocked read — safe for the single-user case
		// and preferable to an opaque "permission denied" on the lock file.
		return m.load()
	}
	defer lockFile.Close()

	// FUN-002: acquire a shared lock so concurrent WithLock writes cannot
	// produce a torn read on filesystems without atomic rename guarantees.
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_SH); err != nil {
		return nil, fmt.Errorf("state: acquire shared lock: %w", err)
	}
	defer unix.Flock(int(lockFile.Fd()), unix.LOCK_UN) //nolint:errcheck

	return m.load()
}

// WithLock acquires an exclusive flock on the lock file, loads the current
// state, calls fn with it, and — if fn returns nil — atomically writes the
// (possibly mutated) state back. The lock is released when WithLock returns.
// If fn returns an error no write occurs and the original state is preserved.
func (m *Manager) WithLock(fn func(s *types.State) error) error {
	lockFile, err := openLockFile(m.lockPath)
	if err != nil {
		return fmt.Errorf("state: open lock file %q: %w", m.lockPath, err)
	}
	if lockFile == nil {
		// Lock file is root-owned; we cannot safely write without a lock.
		user := os.Getenv("USER")
		if user == "" {
			user = "<your-username>"
		}
		return fmt.Errorf("state: lock file %q is not accessible\n  fix: sudo chown %s:%s %s",
			m.lockPath, user, user, m.lockPath)
	}
	defer lockFile.Close()

	// Acquire an exclusive lock; block until it is available.
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("state: acquire lock: %w", err)
	}
	defer func() {
		_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	}()

	// Load the current state while holding the exclusive lock.
	s, err := m.load()
	if err != nil {
		return err
	}

	if err := fn(s); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}

	// Preserve the existing file's permission bits if it already exists.
	perm := os.FileMode(0o600)
	if info, err := os.Stat(m.statePath); err == nil {
		perm = info.Mode().Perm()
	}

	if err := sysfigfs.WriteFileAtomic(m.statePath, data, perm); err != nil {
		return fmt.Errorf("state: %w", err)
	}

	// When running as root in another user's base directory (e.g. sudo sysfig
	// watch for fanotify access), restore state.json ownership to the
	// directory owner so they can still read it afterwards.
	if os.Getuid() == 0 {
		chownToParentOwner(m.statePath)
	}

	return nil
}

// openLockFile opens (or creates) the flock lock file.
//
// When running as root, it immediately chowns the file to the parent
// directory's owner so non-root users retain access after the root run.
//
// Returns (nil, nil) on EACCES — the lock file exists but is inaccessible
// to this process (e.g. a previous root run left it root-owned). Callers
// decide how to handle this: Load falls back to an unlocked read; WithLock
// returns an actionable error.
func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		// Only treat as stale root-owned lock when the file itself exists
		// but is inaccessible. Other permission failures (directory not
		// accessible, hidepid, etc.) are real errors — propagate them so
		// the diagnostic is accurate.
		if os.IsPermission(err) {
			if _, statErr := os.Stat(path); statErr == nil {
				return nil, nil // file exists but unreadable → stale lock
			}
		}
		return nil, err
	}
	// When running as root, chown the lock file to the directory owner so
	// the real user can open it on the next run.
	if os.Getuid() == 0 {
		chownToParentOwner(path)
	}
	return f, nil
}

// chownToParentOwner transfers ownership of path to match its parent directory.
// No-op when the directory owner is already the current user or when the stat
// fails (e.g. the path is a filesystem root).
func chownToParentOwner(path string) {
	var st unix.Stat_t
	if err := unix.Stat(filepath.Dir(path), &st); err != nil {
		return
	}
	uid, gid := int(st.Uid), int(st.Gid)
	if uid != os.Getuid() {
		_ = os.Chown(path, uid, gid)
	}
}

// initState returns a new empty State with Version=1 and non-nil maps.
func initState() *types.State {
	return &types.State{
		Version: 1,
		Files:   make(map[string]*types.FileRecord),
		Backups: make(map[string][]types.BackupRecord),
	}
}
