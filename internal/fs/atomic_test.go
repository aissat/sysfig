package fs_test

import (
	"os"
	"path/filepath"
	"testing"

	sysfigfs "github.com/sysfig-dev/sysfig/internal/fs"
)

func TestWriteFileAtomic_Basic(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "output.txt")
	want := []byte("hello, atomic world")

	if err := sysfigfs.WriteFileAtomic(targetPath, want, 0o644); err != nil {
		t.Fatalf("WriteFileAtomic returned unexpected error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read back written file: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("content mismatch: got %q, want %q", got, want)
	}
}

func TestWriteFileAtomic_Permissions(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "perms.txt")
	wantPerm := os.FileMode(0o640)

	if err := sysfigfs.WriteFileAtomic(targetPath, []byte("data"), wantPerm); err != nil {
		t.Fatalf("WriteFileAtomic returned unexpected error: %v", err)
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("failed to stat written file: %v", err)
	}

	// Mask to the lower 9 permission bits to avoid sticky/setuid noise.
	gotPerm := info.Mode().Perm()
	if gotPerm != wantPerm {
		t.Errorf("permission mismatch: got %04o, want %04o", gotPerm, wantPerm)
	}
}

func TestWriteFileAtomic_Overwrite(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "overwrite.txt")

	first := []byte("first write")
	second := []byte("second write — this should win")

	if err := sysfigfs.WriteFileAtomic(targetPath, first, 0o644); err != nil {
		t.Fatalf("first WriteFileAtomic returned unexpected error: %v", err)
	}

	if err := sysfigfs.WriteFileAtomic(targetPath, second, 0o644); err != nil {
		t.Fatalf("second WriteFileAtomic returned unexpected error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read back written file: %v", err)
	}

	if string(got) != string(second) {
		t.Errorf("content mismatch after overwrite: got %q, want %q", got, second)
	}
}

// TestWriteFileAtomic_DirMissing verifies that WriteFileAtomic creates missing
// intermediate directories rather than returning an error. This matches the
// behaviour of fs.go which calls os.MkdirAll before creating the temp file.
func TestWriteFileAtomic_DirMissing(t *testing.T) {
	base := t.TempDir()
	// Target lives several levels deep — none of these directories exist yet.
	targetPath := filepath.Join(base, "a", "b", "c", "output.txt")
	want := []byte("data written into auto-created dirs")

	if err := sysfigfs.WriteFileAtomic(targetPath, want, 0o644); err != nil {
		t.Fatalf("WriteFileAtomic returned unexpected error for missing dirs: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read back file after auto-dir creation: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content mismatch: got %q, want %q", got, want)
	}
}

// TestWriteFileAtomic_ImpossiblePath verifies that WriteFileAtomic returns an
// error (rather than panicking) when the path is genuinely unwritable — here
// by using an existing regular file as a directory component.
func TestWriteFileAtomic_ImpossiblePath(t *testing.T) {
	base := t.TempDir()

	// Create a regular file that will be used as if it were a directory.
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("i am a file"), 0o644); err != nil {
		t.Fatalf("setup: failed to create blocker file: %v", err)
	}

	// Attempt to write through the file as though it were a directory.
	targetPath := filepath.Join(blocker, "child.txt")

	err := sysfigfs.WriteFileAtomic(targetPath, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("expected an error when a path component is a regular file, got nil")
	}
}
