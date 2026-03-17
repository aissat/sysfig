package hash_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sysfig-dev/sysfig/internal/hash"
)

func TestBytes_Deterministic(t *testing.T) {
	input := []byte("hello, sysfig")

	first := hash.Bytes(input)
	second := hash.Bytes(input)

	assert.NotEmpty(t, first)
	assert.Equal(t, first, second, "same input must always produce the same hash")
}

func TestBytes_Different(t *testing.T) {
	a := hash.Bytes([]byte("input-a"))
	b := hash.Bytes([]byte("input-b"))

	assert.NotEqual(t, a, b, "different inputs must produce different hashes")
}

func TestFile_Basic(t *testing.T) {
	content := []byte("the quick brown fox jumps over the lazy dog")

	dir := t.TempDir()
	path := filepath.Join(dir, "testfile.txt")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	got, err := hash.File(path)
	require.NoError(t, err)

	want := hash.Bytes(content)
	assert.Equal(t, want, got, "File hash must match Bytes hash of the same content")
}

func TestFile_Missing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.txt")

	_, err := hash.File(missing)
	assert.Error(t, err, "hashing a non-existent file must return an error")
}
