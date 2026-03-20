package fs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/pkg/types"
)

func TestReadMeta_Basic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o644))

	meta, err := sysfigfs.ReadMeta(p)
	require.NoError(t, err)
	require.NotNil(t, meta)

	assert.Equal(t, uint32(0o644), meta.Mode)
	assert.GreaterOrEqual(t, meta.UID, 0)
	assert.GreaterOrEqual(t, meta.GID, 0)
}

func TestReadMeta_Missing(t *testing.T) {
	_, err := sysfigfs.ReadMeta("/nonexistent/path/file.txt")
	assert.Error(t, err)
}

func TestReadMeta_PermissionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(p, []byte("data"), 0o600))

	meta, err := sysfigfs.ReadMeta(p)
	require.NoError(t, err)
	assert.Equal(t, uint32(0o600), meta.Mode)
}

func TestApplyMeta_NilMeta(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o644))

	result := sysfigfs.ApplyMeta(p, nil)
	assert.False(t, result.ChmodOK)
	assert.False(t, result.ChownOK)
	assert.NoError(t, result.Err)
}

func TestApplyMeta_Chmod(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o644))

	meta := &types.FileMeta{
		Mode: 0o600,
		UID:  os.Getuid(),
		GID:  os.Getgid(),
	}
	result := sysfigfs.ApplyMeta(p, meta)
	require.NoError(t, result.Err)
	assert.True(t, result.ChmodOK)

	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestApplyMeta_ChownSameUser(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o644))

	// chown to ourselves — should succeed without privilege.
	meta := &types.FileMeta{
		Mode: 0o644,
		UID:  os.Getuid(),
		GID:  os.Getgid(),
	}
	result := sysfigfs.ApplyMeta(p, meta)
	require.NoError(t, result.Err)
	assert.True(t, result.ChmodOK)
}

func TestApplyMeta_InvalidPath(t *testing.T) {
	meta := &types.FileMeta{Mode: 0o644, UID: os.Getuid(), GID: os.Getgid()}
	result := sysfigfs.ApplyMeta("/nonexistent/path/file.txt", meta)
	assert.Error(t, result.Err)
}
