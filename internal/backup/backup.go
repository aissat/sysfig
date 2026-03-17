package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sysfigfs "github.com/sysfig-dev/sysfig/internal/fs"
)

// Manager manages backups stored under a base directory (e.g. ~/.sysfig/backups).
type Manager struct {
	baseDir string
}

// NewManager creates a Manager rooted at baseDir.
func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// backupDir returns the directory used to store backups for a given fileID.
func (m *Manager) backupDir(fileID string) string {
	return filepath.Join(m.baseDir, fileID)
}

// timestampToFilename converts a time.Time to a backup filename using RFC3339
// with colons replaced by dashes, e.g. "2024-01-15T10-30-00Z.bak".
func timestampToFilename(t time.Time) string {
	ts := t.UTC().Format(time.RFC3339)
	ts = strings.ReplaceAll(ts, ":", "-")
	return ts + ".bak"
}

// Backup copies src to <baseDir>/<fileID>/<timestamp>.bak atomically.
// fileID is the sysfig tracking ID (e.g. "nginx_main").
// Returns the path of the created backup file.
func (m *Manager) Backup(fileID string, src string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	perm := srcInfo.Mode().Perm()

	dir := m.backupDir(fileID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}

	filename := timestampToFilename(time.Now())
	destPath := filepath.Join(dir, filename)

	if err := sysfigfs.WriteFileAtomic(destPath, data, perm); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}

	return destPath, nil
}

// List returns all backup paths for fileID, sorted newest-first.
func (m *Manager) List(fileID string) ([]string, error) {
	dir := m.backupDir(fileID)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("backup: %w", err)
	}

	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".bak") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}

	// Sort newest-first: since filenames are RFC3339-derived timestamps with
	// colons replaced by dashes, lexicographic descending order is correct.
	sort.Slice(paths, func(i, j int) bool {
		return filepath.Base(paths[i]) > filepath.Base(paths[j])
	})

	return paths, nil
}

// Prune deletes backups for fileID, keeping only the `keep` most recent ones.
func (m *Manager) Prune(fileID string, keep int) error {
	paths, err := m.List(fileID)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	if len(paths) <= keep {
		return nil
	}

	// paths is sorted newest-first; entries beyond index keep are the oldest.
	toDelete := paths[keep:]
	for _, p := range toDelete {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("backup: %w", err)
		}
	}

	return nil
}
