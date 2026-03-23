package core_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/pkg/types"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildSnapFixture creates a sysfig base directory with a state.json that
// has one tracked file. The tracked file content is written to sysRoot.
// Returns (baseDir, sysRoot, fileID, sysPath, fileContent).
func buildSnapFixture(t *testing.T) (baseDir, sysRoot, fileID, sysPath string, content []byte) {
	t.Helper()
	baseDir = t.TempDir()
	sysRoot = t.TempDir()

	sysPath = "/etc/myapp.conf"
	content = []byte("key=value\n")
	fileID = core.DeriveID(sysPath)

	// Write the tracked file to sysRoot.
	fullSysPath := filepath.Join(sysRoot, sysPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullSysPath), 0o755))
	require.NoError(t, os.WriteFile(fullSysPath, content, 0o644))

	// Write state.json.
	now := time.Now()
	s := &types.State{
		Version: 1,
		Files: map[string]*types.FileRecord{
			fileID: {
				ID:          fileID,
				SystemPath:  sysPath,
				RepoPath:    "etc/myapp.conf",
				CurrentHash: "aabbccdd",
				LastSync:    &now,
				Status:      types.StatusTracked,
			},
		},
		Backups: map[string][]types.BackupRecord{},
	}
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600))

	return baseDir, sysRoot, fileID, sysPath, content
}

// ---------------------------------------------------------------------------
// SnapDir
// ---------------------------------------------------------------------------

func TestSnapDir(t *testing.T) {
	got := core.SnapDir("/home/user/.sysfig")
	assert.Equal(t, "/home/user/.sysfig/snaps", got)
}

// ---------------------------------------------------------------------------
// SnapList — empty
// ---------------------------------------------------------------------------

func TestSnapList_Empty(t *testing.T) {
	baseDir := t.TempDir()
	snaps, err := core.SnapList(baseDir)
	require.NoError(t, err)
	assert.Empty(t, snaps, "no snaps dir should return empty list")
}

// ---------------------------------------------------------------------------
// SnapTake
// ---------------------------------------------------------------------------

func TestSnapTake_Basic(t *testing.T) {
	baseDir, sysRoot, fileID, sysPath, content := buildSnapFixture(t)

	result, err := core.SnapTake(core.SnapTakeOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
		Label:   "pre-update",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.NotEmpty(t, result.ID)
	assert.Equal(t, "pre-update", result.Label)
	assert.Len(t, result.Files, 1)
	assert.Equal(t, fileID, result.Files[0].ID)
	assert.Equal(t, sysPath, result.Files[0].SystemPath)
	assert.NotEmpty(t, result.ShortID)

	// Verify snap directory was created.
	snapPath := filepath.Join(core.SnapDir(baseDir), result.ID)
	_, err = os.Stat(snapPath)
	require.NoError(t, err, "snap directory must exist")

	// Verify snap.json was written.
	manifestPath := filepath.Join(snapPath, "snap.json")
	_, err = os.Stat(manifestPath)
	require.NoError(t, err, "snap.json must exist")

	// Verify the file was captured correctly.
	capturedPath := filepath.Join(snapPath, "files", "etc/myapp.conf")
	capturedContent, err := os.ReadFile(capturedPath)
	require.NoError(t, err)
	assert.Equal(t, content, capturedContent)
}

func TestSnapTake_NoTrackedFiles(t *testing.T) {
	baseDir := t.TempDir()
	// Write empty state.json.
	s := &types.State{
		Version: 1,
		Files:   map[string]*types.FileRecord{},
		Backups: map[string][]types.BackupRecord{},
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600)

	_, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tracked files")
}

func TestSnapTake_LabelSlugifiedInID(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	result, err := core.SnapTake(core.SnapTakeOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
		Label:   "my label/test",
	})
	require.NoError(t, err)
	// Spaces and slashes should be replaced with dashes in the ID.
	assert.Contains(t, result.ID, "my-label-test")
}

func TestSnapTake_MissingSystemFile(t *testing.T) {
	// File is tracked in state.json but absent from disk → snap captures
	// the record with the stored hash rather than erroring.
	baseDir := t.TempDir()
	sysRoot := t.TempDir() // intentionally empty
	sysPath := "/etc/absent.conf"
	fileID := core.DeriveID(sysPath)
	now := time.Now()
	s := &types.State{
		Version: 1,
		Files: map[string]*types.FileRecord{
			fileID: {
				ID:          fileID,
				SystemPath:  sysPath,
				RepoPath:    "etc/absent.conf",
				CurrentHash: "stored-hash",
				LastSync:    &now,
				Status:      types.StatusTracked,
			},
		},
		Backups: map[string][]types.BackupRecord{},
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600)

	result, err := core.SnapTake(core.SnapTakeOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, result.Files, 1)
	// Hash falls back to the stored hash when the file is missing.
	assert.Equal(t, "stored-hash", result.Files[0].Hash)
}

// ---------------------------------------------------------------------------
// SnapList
// ---------------------------------------------------------------------------

func TestSnapList_ReturnsSortedNewestFirst(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	// Take two snaps with a short pause to ensure distinct IDs.
	snap1, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot, Label: "first"})
	require.NoError(t, err)
	snap2, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot, Label: "second"})
	require.NoError(t, err)

	snaps, err := core.SnapList(baseDir)
	require.NoError(t, err)
	require.Len(t, snaps, 2)

	// Newest first — snap2 was taken after snap1.
	if snaps[0].CreatedAt.Before(snaps[1].CreatedAt) {
		t.Errorf("snaps not sorted newest-first: %v before %v", snaps[0].ID, snaps[1].ID)
	}

	// Both snaps must have ShortIDs set.
	assert.NotEmpty(t, snaps[0].ShortID)
	assert.NotEmpty(t, snaps[1].ShortID)

	_ = snap1
	_ = snap2
}

// ---------------------------------------------------------------------------
// SnapResolveID
// ---------------------------------------------------------------------------

func TestSnapResolveID_ExactMatch(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	snap, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	resolved, err := core.SnapResolveID(baseDir, snap.ID)
	require.NoError(t, err)
	assert.Equal(t, snap.ID, resolved)
}

func TestSnapResolveID_ShortHashMatch(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	snap, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	resolved, err := core.SnapResolveID(baseDir, snap.ShortID)
	require.NoError(t, err)
	assert.Equal(t, snap.ID, resolved)
}

func TestSnapResolveID_NotFound(t *testing.T) {
	baseDir := t.TempDir()

	_, err := core.SnapResolveID(baseDir, "abcd1234")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshot matches")
}

// ---------------------------------------------------------------------------
// SnapRestore
// ---------------------------------------------------------------------------

func TestSnapRestore_Basic(t *testing.T) {
	baseDir, sysRoot, _, sysPath, origContent := buildSnapFixture(t)

	// Take a snapshot.
	snap, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	// Modify the system file.
	modifiedContent := []byte("key=modified\n")
	require.NoError(t, os.WriteFile(filepath.Join(sysRoot, sysPath), modifiedContent, 0o644))

	// Restore the snapshot.
	restoreRoot := t.TempDir()
	result, err := core.SnapRestore(core.SnapRestoreOptions{
		BaseDir: baseDir,
		SysRoot: restoreRoot,
		SnapID:  snap.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.Restored, 1)
	assert.Empty(t, result.Skipped)

	// Verify the restored file contains the original content.
	restored, err := os.ReadFile(filepath.Join(restoreRoot, sysPath))
	require.NoError(t, err)
	assert.Equal(t, origContent, restored)
}

func TestSnapRestore_DryRun(t *testing.T) {
	baseDir, sysRoot, _, sysPath, _ := buildSnapFixture(t)

	snap, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	restoreRoot := t.TempDir()
	result, err := core.SnapRestore(core.SnapRestoreOptions{
		BaseDir: baseDir,
		SysRoot: restoreRoot,
		SnapID:  snap.ID,
		DryRun:  true,
	})
	require.NoError(t, err)
	assert.True(t, result.DryRun)
	assert.Len(t, result.Restored, 1)

	// DryRun must NOT create any files.
	_, err = os.Stat(filepath.Join(restoreRoot, sysPath))
	assert.True(t, os.IsNotExist(err), "dry-run must not write files to disk")
}

func TestSnapRestore_FilterByID(t *testing.T) {
	// Build a fixture with two tracked files.
	baseDir := t.TempDir()
	sysRoot := t.TempDir()

	id1 := core.DeriveID("/etc/a.conf")
	id2 := core.DeriveID("/etc/b.conf")
	now := time.Now()
	s := &types.State{
		Version: 1,
		Files: map[string]*types.FileRecord{
			id1: {ID: id1, SystemPath: "/etc/a.conf", RepoPath: "etc/a.conf", CurrentHash: "aa", LastSync: &now, Status: types.StatusTracked},
			id2: {ID: id2, SystemPath: "/etc/b.conf", RepoPath: "etc/b.conf", CurrentHash: "bb", LastSync: &now, Status: types.StatusTracked},
		},
		Backups: map[string][]types.BackupRecord{},
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600)

	// Write both system files.
	for _, p := range []string{"/etc/a.conf", "/etc/b.conf"} {
		full := filepath.Join(sysRoot, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("content"), 0o644)
	}

	snap, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	restoreRoot := t.TempDir()
	result, err := core.SnapRestore(core.SnapRestoreOptions{
		BaseDir: baseDir,
		SysRoot: restoreRoot,
		SnapID:  snap.ID,
		IDs:     []string{id1}, // only restore id1
	})
	require.NoError(t, err)
	assert.Len(t, result.Restored, 1)
	assert.Len(t, result.Skipped, 1)
	assert.Equal(t, id1, result.Restored[0].ID)
}

// ---------------------------------------------------------------------------
// SnapFilterByDir
// ---------------------------------------------------------------------------

func TestSnapFilterByDir_EmptyDir(t *testing.T) {
	snaps := []core.SnapInfo{
		{ID: "a", Files: []core.SnapFile{{SystemPath: "/etc/nginx.conf"}}},
		{ID: "b", Files: []core.SnapFile{{SystemPath: "/home/user/.bashrc"}}},
	}
	result := core.SnapFilterByDir(snaps, "")
	assert.Equal(t, snaps, result, "empty dir must return all snaps")
}

func TestSnapFilterByDir_FiltersByPrefix(t *testing.T) {
	snaps := []core.SnapInfo{
		{ID: "a", Files: []core.SnapFile{{SystemPath: "/etc/nginx.conf"}}},
		{ID: "b", Files: []core.SnapFile{{SystemPath: "/etc/pacman.conf"}}},
		{ID: "c", Files: []core.SnapFile{{SystemPath: "/home/user/.bashrc"}}},
	}
	result := core.SnapFilterByDir(snaps, "/etc")
	assert.Len(t, result, 2, "only snaps with files under /etc should be included")
	for _, s := range result {
		assert.True(t, s.ID == "a" || s.ID == "b")
	}
}

func TestSnapFilterByDir_ExactPath(t *testing.T) {
	snaps := []core.SnapInfo{
		{ID: "a", Files: []core.SnapFile{{SystemPath: "/etc"}}},
	}
	result := core.SnapFilterByDir(snaps, "/etc")
	assert.Len(t, result, 1)
}

func TestSnapFilterByDir_NoMatch(t *testing.T) {
	snaps := []core.SnapInfo{
		{ID: "a", Files: []core.SnapFile{{SystemPath: "/var/log/syslog"}}},
	}
	result := core.SnapFilterByDir(snaps, "/etc")
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// SnapFilesUnderDir
// ---------------------------------------------------------------------------

func TestSnapFilesUnderDir_EmptyDir(t *testing.T) {
	info := core.SnapInfo{
		Files: []core.SnapFile{
			{ID: "1", SystemPath: "/etc/a.conf"},
			{ID: "2", SystemPath: "/home/user/.bashrc"},
		},
	}
	result := core.SnapFilesUnderDir(info, "")
	assert.Equal(t, info.Files, result, "empty dir must return all files")
}

func TestSnapFilesUnderDir_FiltersByPrefix(t *testing.T) {
	info := core.SnapInfo{
		Files: []core.SnapFile{
			{ID: "1", SystemPath: "/etc/a.conf"},
			{ID: "2", SystemPath: "/etc/b.conf"},
			{ID: "3", SystemPath: "/home/user/.bashrc"},
		},
	}
	result := core.SnapFilesUnderDir(info, "/etc")
	assert.Len(t, result, 2)
	for _, f := range result {
		assert.True(t, strings.HasPrefix(f.SystemPath, "/etc"))
	}
}

// ---------------------------------------------------------------------------
// SnapUndo
// ---------------------------------------------------------------------------

func TestSnapUndo_RestoresMostRecent(t *testing.T) {
	baseDir, sysRoot, _, sysPath, origContent := buildSnapFixture(t)

	// Take initial snapshot.
	_, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot, Label: "first"})
	require.NoError(t, err)

	// Modify file and take a second snapshot.
	updatedContent := []byte("key=updated\n")
	require.NoError(t, os.WriteFile(filepath.Join(sysRoot, sysPath), updatedContent, 0o644))
	_, err = core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot, Label: "second"})
	require.NoError(t, err)

	// Undo should restore from the MOST RECENT snap (second = updatedContent).
	restoreRoot := t.TempDir()
	result, snapID, err := core.SnapUndo(core.SnapUndoOptions{
		BaseDir: baseDir,
		SysRoot: restoreRoot,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, snapID)
	assert.NotNil(t, result)
	assert.Len(t, result.Restored, 1)

	restored, err := os.ReadFile(filepath.Join(restoreRoot, sysPath))
	require.NoError(t, err)
	// Most recent snapshot had updatedContent.
	assert.Equal(t, updatedContent, restored)

	_ = origContent
}

func TestSnapUndo_NoSnapshots(t *testing.T) {
	baseDir := t.TempDir()

	_, _, err := core.SnapUndo(core.SnapUndoOptions{BaseDir: baseDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshots found")
}

func TestSnapUndo_WithDirScope(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	_, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	restoreRoot := t.TempDir()
	result, _, err := core.SnapUndo(core.SnapUndoOptions{
		BaseDir: baseDir,
		SysRoot: restoreRoot,
		Dir:     "/etc",
	})
	require.NoError(t, err)
	assert.Len(t, result.Restored, 1)
}

func TestSnapUndo_WithDirScope_NoMatch(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	_, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	_, _, err = core.SnapUndo(core.SnapUndoOptions{
		BaseDir: baseDir,
		Dir:     "/var", // no snaps for /var
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshots found for")
}

// ---------------------------------------------------------------------------
// SnapDrop
// ---------------------------------------------------------------------------

func TestSnapDrop_Basic(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	snap, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	err = core.SnapDrop(baseDir, snap.ID)
	require.NoError(t, err)

	// Snap directory must be gone.
	_, err = os.Stat(filepath.Join(core.SnapDir(baseDir), snap.ID))
	assert.True(t, os.IsNotExist(err), "snap directory must be deleted")
}

func TestSnapDrop_NotFound(t *testing.T) {
	baseDir := t.TempDir()
	_ = os.MkdirAll(core.SnapDir(baseDir), 0o700)

	err := core.SnapDrop(baseDir, "abcd1234")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshot matches")
}

func TestSnapDrop_ByShortID(t *testing.T) {
	baseDir, sysRoot, _, _, _ := buildSnapFixture(t)

	snap, err := core.SnapTake(core.SnapTakeOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	// Drop using short ID.
	err = core.SnapDrop(baseDir, snap.ShortID)
	require.NoError(t, err)

	snaps, err := core.SnapList(baseDir)
	require.NoError(t, err)
	assert.Empty(t, snaps)
}
