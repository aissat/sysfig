package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WriteFileAtomic writes data to targetPath atomically:
// 1. Creates a temp file in the same directory as targetPath (same filesystem → rename is atomic)
// 2. Writes data to the temp file
// 3. Syncs the temp file (fdatasync)
// 4. Renames temp file to targetPath
// 5. Preserves the given perm on the final file
// Returns a wrapped error on any failure step.
func WriteFileAtomic(targetPath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(targetPath)

	// Mirror the file's world-read bit onto the directory: if the file is
	// not world-readable, the containing directory should not be either.
	dirMode := os.FileMode(0o755)
	if perm&0o004 == 0 {
		dirMode = 0o700
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("fs: create directory %q: %w", dir, err)
	}

	// Tighten directory permissions if the existing mode is more permissive
	// than dirMode (i.e. it has bits set that dirMode does not). Never loosen
	// permissions that a caller intentionally set stricter than dirMode.
	if info, err := os.Stat(dir); err == nil {
		if info.Mode().Perm()&^dirMode != 0 {
			if err := os.Chmod(dir, dirMode); err != nil {
				return fmt.Errorf("fs: chmod directory %q to %04o: %w", dir, dirMode, err)
			}
		}
	} else {
		return fmt.Errorf("fs: stat directory %q: %w", dir, err)
	}

	// Create the temp file in the same directory as the target so that the
	// subsequent rename is guaranteed to be on the same filesystem mount point
	// (POSIX rename is atomic only within a single filesystem).
	tmp, err := os.CreateTemp(dir, ".sysfig-atomic-*")
	if err != nil {
		return fmt.Errorf("fs: create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Track whether we've successfully committed so the defer knows whether to
	// clean up the temp file.
	committed := false
	defer func() {
		if !committed {
			// Best-effort cleanup; ignore secondary errors.
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	// Apply the desired permissions before writing any data so the file is
	// never visible with wrong permissions at the final path.
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("fs: chmod temp file: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("fs: write temp file: %w", err)
	}

	// Sync (fdatasync) to ensure data is durable before the rename makes the
	// file visible at the target path.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fs: sync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fs: close temp file: %w", err)
	}

	// Atomically replace targetPath with the fully written temp file.
	if err := os.Rename(tmpPath, targetPath); err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == syscall.EBUSY {
			return fmt.Errorf("fs: cannot write %q — file is in use by the system (bind-mounted or locked)", targetPath)
		}
		return fmt.Errorf("fs: rename temp file to %q: %w", targetPath, err)
	}

	committed = true
	return nil
}
