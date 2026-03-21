package core_test

// trackdir_test.go — tests for TrackDir (directory tracking) and related
// helpers: isExcluded, isAlreadyTracked (exercised indirectly).

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
)

// ── TestTrackDir_Basic ────────────────────────────────────────────────────────

// TestTrackDir_Basic verifies that TrackDir tracks all files in a directory
// and records them in state.json with the correct group field.
func TestTrackDir_Basic(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	sysRoot := filepath.Join(tmp, "sysroot")
	dirPath := filepath.Join(sysRoot, "etc", "myapp")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "a.conf"), []byte("A\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "b.conf"), []byte("B\n"), 0o644))

	summary, err := core.TrackDir(core.TrackDirOptions{
		DirPath:  dirPath,
		RepoDir:  repoDir,
		StateDir: stateDir,
		SysRoot:  sysRoot,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, summary.Tracked, "all 2 files must be tracked")
	assert.Equal(t, 0, summary.Skipped)
	assert.Equal(t, 0, summary.Errors)

	// Each record must have the group field set.
	st := readState(t, stateDir)
	require.Len(t, st.Files, 2)
	for _, rec := range st.Files {
		assert.NotEmpty(t, rec.Group, "group must be set for dir-tracked files")
		// Group must be the logical path (sysRoot-stripped).
		assert.True(t, filepath.IsAbs(rec.Group))
	}
}

// ── TestTrackDir_Recursive ────────────────────────────────────────────────────

// TestTrackDir_Recursive verifies that files in subdirectories are also tracked.
func TestTrackDir_Recursive(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	sysRoot := filepath.Join(tmp, "sysroot")
	topDir := filepath.Join(sysRoot, "etc", "nested")
	subDir := filepath.Join(topDir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(topDir, "top.conf"), []byte("top\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "deep.conf"), []byte("deep\n"), 0o644))

	summary, err := core.TrackDir(core.TrackDirOptions{
		DirPath:  topDir,
		RepoDir:  repoDir,
		StateDir: stateDir,
		SysRoot:  sysRoot,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, summary.Tracked, "recursive scan must find files in subdirs")
}

// ── TestTrackDir_ExcludePath ─────────────────────────────────────────────────

// TestTrackDir_ExcludePath verifies that files matching an exclude path are
// skipped. The exclude pattern must match the actual absolute filesystem path
// (isExcluded checks against the real path from WalkDir).
func TestTrackDir_ExcludePath(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	dirPath := filepath.Join(tmp, "mixed")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "keep.conf"), []byte("keep\n"), 0o644))
	skipPath := filepath.Join(dirPath, "skip.conf")
	require.NoError(t, os.WriteFile(skipPath, []byte("skip\n"), 0o644))

	// Exclude the actual absolute path.
	summary, err := core.TrackDir(core.TrackDirOptions{
		DirPath:  dirPath,
		RepoDir:  repoDir,
		StateDir: stateDir,
		Excludes: []string{skipPath},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Tracked)
	assert.Equal(t, 1, summary.Skipped)

	st := readState(t, stateDir)
	for _, rec := range st.Files {
		assert.NotContains(t, rec.SystemPath, "skip.conf",
			"excluded file must not appear in state.json")
	}
}

// ── TestTrackDir_ExcludeGlob ──────────────────────────────────────────────────

// TestTrackDir_ExcludeGlob verifies that glob patterns in the exclude list
// are matched via filepath.Match against the full absolute path.
// filepath.Match("*.bak", fullPath) only matches when the full path has no
// directory component — so for files in subdirs the glob must include the
// parent, e.g. "/parent/*.bak".
func TestTrackDir_ExcludeGlob(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	dirPath := filepath.Join(tmp, "glob")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "main.conf"), []byte("main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "backup.bak"), []byte("bak\n"), 0o644))

	// Use a directory-qualified glob so filepath.Match works against full path.
	glob := filepath.Join(dirPath, "*.bak")
	summary, err := core.TrackDir(core.TrackDirOptions{
		DirPath:  dirPath,
		RepoDir:  repoDir,
		StateDir: stateDir,
		Excludes: []string{glob},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Tracked, "only main.conf must be tracked; .bak excluded by glob")
	assert.Equal(t, 1, summary.Skipped)
}

// ── TestTrackDir_SkipsAlreadyTracked ─────────────────────────────────────────

// TestTrackDir_SkipsAlreadyTracked verifies that TrackDir skips files that are
// already in state.json (idempotent).
func TestTrackDir_SkipsAlreadyTracked(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	sysRoot := filepath.Join(tmp, "sysroot")
	dirPath := filepath.Join(sysRoot, "etc", "idem")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "once.conf"), []byte("once\n"), 0o644))

	opts := core.TrackDirOptions{
		DirPath:  dirPath,
		RepoDir:  repoDir,
		StateDir: stateDir,
		SysRoot:  sysRoot,
	}

	// First call — must succeed.
	s1, err := core.TrackDir(opts)
	require.NoError(t, err)
	assert.Equal(t, 1, s1.Tracked)

	// Second call — already tracked, must skip without error.
	s2, err := core.TrackDir(opts)
	require.NoError(t, err)
	assert.Equal(t, 0, s2.Tracked, "second call must skip already-tracked file")
	assert.Equal(t, 1, s2.Skipped)
}

// ── TestTrackDir_EmptyDir ─────────────────────────────────────────────────────

// TestTrackDir_EmptyDir verifies that tracking an empty directory succeeds
// and returns Tracked=0.
func TestTrackDir_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	emptyDir := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))

	summary, err := core.TrackDir(core.TrackDirOptions{
		DirPath:  emptyDir,
		RepoDir:  repoDir,
		StateDir: stateDir,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Tracked)
}

// ── TestTrackDir_DeniedFileSkipped ───────────────────────────────────────────

// TestTrackDir_NonRegularFileSkipped verifies that non-regular files (FIFOs,
// sockets, etc.) inside a tracked directory are skipped and counted as Skipped.
func TestTrackDir_NonRegularFileSkipped(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	dirPath := filepath.Join(tmp, "withpipe")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "regular.conf"), []byte("ok\n"), 0o644))

	// Create a FIFO (named pipe) — non-regular file.
	pipePath := filepath.Join(dirPath, "mypipe")
	if err := exec.Command("mkfifo", pipePath).Run(); err != nil {
		t.Skip("mkfifo not available or failed")
	}

	summary, err := core.TrackDir(core.TrackDirOptions{
		DirPath:  dirPath,
		RepoDir:  repoDir,
		StateDir: stateDir,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Tracked, "only regular.conf must be tracked")
	assert.Equal(t, 1, summary.Skipped, "the FIFO must be skipped")
}
