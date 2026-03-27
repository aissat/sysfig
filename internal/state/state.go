package state

import (
	"encoding/json"
	"fmt"
	"os"

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
	lockFile, err := os.OpenFile(m.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("state: open lock file %q: %w", m.lockPath, err)
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
	// Open (or create) the lock file. We use a dedicated .lock file so that
	// state.json can always be read without holding any lock.
	lockFile, err := os.OpenFile(m.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("state: open lock file %q: %w", m.lockPath, err)
	}
	defer lockFile.Close()

	// Acquire an exclusive lock; block until it is available.
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("state: acquire lock: %w", err)
	}
	defer func() {
		// Best-effort unlock — the OS will also release it when the fd closes.
		_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	}()

	// Load the current state while holding the exclusive lock.
	// Calls the private load() to avoid opening a second lock fd.
	s, err := m.load()
	if err != nil {
		return err
	}

	// Let the caller mutate the state.
	if err := fn(s); err != nil {
		// fn signalled an error — do not persist anything.
		return err
	}

	// Marshal and atomically write back.
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}

	// Preserve the existing file's permission bits if it already exists.
	// This allows a shared base dir (e.g. /opt/sysfig, group=sysfig, 640)
	// where a non-root operator user can read state without sudo.
	perm := os.FileMode(0o600)
	if info, err := os.Stat(m.statePath); err == nil {
		perm = info.Mode().Perm()
	}

	if err := sysfigfs.WriteFileAtomic(m.statePath, data, perm); err != nil {
		return fmt.Errorf("state: %w", err)
	}

	return nil
}

// initState returns a new empty State with Version=1 and non-nil maps.
func initState() *types.State {
	return &types.State{
		Version: 1,
		Files:   make(map[string]*types.FileRecord),
		Backups: make(map[string][]types.BackupRecord),
	}
}
