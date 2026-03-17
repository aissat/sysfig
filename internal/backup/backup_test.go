package backup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sysfig-dev/sysfig/internal/backup"
)

// writeTempFile creates a temporary file with the given content inside dir and
// returns its path. It fails the test immediately on any error.
func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempFile: failed to write %s: %v", p, err)
	}
	return p
}

func TestBackup_CreatesFile(t *testing.T) {
	baseDir := t.TempDir()
	srcDir := t.TempDir()

	src := writeTempFile(t, srcDir, "config.txt", "original content")

	m := backup.NewManager(baseDir)
	backupPath, err := m.Backup("test_file", src)
	if err != nil {
		t.Fatalf("Backup returned unexpected error: %v", err)
	}

	// Backup file must exist.
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file does not exist at %s: %v", backupPath, err)
	}

	// Backup file content must match the source.
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("failed to read backup file: %v", err)
	}
	if string(got) != "original content" {
		t.Errorf("backup content mismatch: got %q, want %q", got, "original content")
	}

	// Backup must live under <baseDir>/<fileID>/.
	wantDir := filepath.Join(baseDir, "test_file")
	if filepath.Dir(backupPath) != wantDir {
		t.Errorf("backup path directory: got %s, want %s", filepath.Dir(backupPath), wantDir)
	}
}

func TestBackup_List_NewestFirst(t *testing.T) {
	baseDir := t.TempDir()
	srcDir := t.TempDir()

	src := writeTempFile(t, srcDir, "config.txt", "data")

	m := backup.NewManager(baseDir)

	var paths []string
	for i := 0; i < 3; i++ {
		p, err := m.Backup("ordered_file", src)
		if err != nil {
			t.Fatalf("Backup #%d returned unexpected error: %v", i+1, err)
		}
		paths = append(paths, p)

		// Sleep long enough that each backup gets a distinct second-resolution
		// timestamp (RFC3339 has 1-second precision).
		if i < 2 {
			time.Sleep(1100 * time.Millisecond)
		}
	}

	listed, err := m.List("ordered_file")
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}

	if len(listed) != 3 {
		t.Fatalf("List returned %d entries, want 3", len(listed))
	}

	// Verify strictly descending order (newest first means each entry's
	// base filename is lexicographically greater than the next).
	for i := 0; i < len(listed)-1; i++ {
		a := filepath.Base(listed[i])
		b := filepath.Base(listed[i+1])
		if a <= b {
			t.Errorf("list not sorted newest-first at index %d: %q <= %q", i, a, b)
		}
	}

	// The most recently created backup must be first.
	if listed[0] != paths[2] {
		t.Errorf("first listed entry: got %s, want %s", listed[0], paths[2])
	}
}

func TestBackup_Prune(t *testing.T) {
	baseDir := t.TempDir()
	srcDir := t.TempDir()

	src := writeTempFile(t, srcDir, "config.txt", "data")

	m := backup.NewManager(baseDir)

	for i := 0; i < 5; i++ {
		if _, err := m.Backup("prune_file", src); err != nil {
			t.Fatalf("Backup #%d returned unexpected error: %v", i+1, err)
		}
		if i < 4 {
			time.Sleep(1100 * time.Millisecond)
		}
	}

	listed, err := m.List("prune_file")
	if err != nil {
		t.Fatalf("List before prune returned unexpected error: %v", err)
	}
	if len(listed) != 5 {
		t.Fatalf("expected 5 backups before prune, got %d", len(listed))
	}

	const keep = 2
	if err := m.Prune("prune_file", keep); err != nil {
		t.Fatalf("Prune returned unexpected error: %v", err)
	}

	remaining, err := m.List("prune_file")
	if err != nil {
		t.Fatalf("List after prune returned unexpected error: %v", err)
	}

	if len(remaining) != keep {
		t.Fatalf("expected %d backups after prune, got %d", keep, len(remaining))
	}

	// The survivors must be the two newest entries from the pre-prune list.
	for i, p := range remaining {
		if p != listed[i] {
			t.Errorf("remaining[%d]: got %s, want %s", i, p, listed[i])
		}
	}

	// The three oldest entries must no longer exist on disk.
	for _, p := range listed[keep:] {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("pruned file still exists: %s", p)
		}
	}
}
