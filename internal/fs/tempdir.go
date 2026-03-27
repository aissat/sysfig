package fs

import (
	"os"
	"path/filepath"
)

// SecureTempDir returns a path to a user-private temporary directory
// ($HOME/.sysfig/tmp) and ensures it exists with mode 0700.
//
// SEC-004: using the system /tmp is unsafe on multi-user machines because
// /tmp is world-readable. Temporary files created by sysfig may contain
// decrypted secrets or git index data that should not be visible to other
// local users.
//
// Falls back to os.TempDir() only if $HOME cannot be determined (e.g.
// inside a rootless container with no home directory).
func SecureTempDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	dir := filepath.Join(home, ".sysfig", "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	return dir
}
