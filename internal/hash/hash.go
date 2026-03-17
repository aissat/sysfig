package hash

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/zeebo/blake3"
)

// File computes the BLAKE3 hash of the file at path and returns it as a
// lowercase hex string. Returns a wrapped error on failure.
func File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash: open %q: %w", path, err)
	}
	defer f.Close()

	h := blake3.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash: read %q: %w", path, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Bytes computes the BLAKE3 hash of b and returns it as a lowercase hex string.
func Bytes(b []byte) string {
	sum := blake3.Sum256(b)
	return hex.EncodeToString(sum[:])
}
