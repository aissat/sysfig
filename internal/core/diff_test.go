package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/pkg/types"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// requireGitDiff skips the test if git is not available on PATH.
func requireGitDiff(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// runGitDiff executes a git command in dir and fatally fails the test on error.
func runGitDiff(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q failed: %v\n%s", args, dir, err, out)
	}
}

// initBareRepoDiff creates a bare git repo at repoDir with a single committed
// file at relPath (git-relative, no leading slash) containing content.
func initBareRepoDiff(t *testing.T, repoDir, relPath string, content []byte) {
	t.Helper()

	workDir := t.TempDir()

	// Initialise bare repo.
	cmd := exec.Command("git", "init", "--bare", repoDir)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", out)

	// Clone into a work tree, write the file, commit, and push back.
	runGitDiff(t, workDir, "clone", repoDir, "work")
	workClone := filepath.Join(workDir, "work")
	runGitDiff(t, workClone, "config", "user.email", "test@sysfig.local")
	runGitDiff(t, workClone, "config", "user.name", "sysfig-test")

	destPath := filepath.Join(workClone, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
	require.NoError(t, os.WriteFile(destPath, content, 0o644))

	runGitDiff(t, workClone, "add", relPath)
	runGitDiff(t, workClone, "commit", "-m", "test: add file")
	runGitDiff(t, workClone, "push", "origin")
}

// pushNewContentDiff updates the committed content in the bare repo at repoDir
// by cloning it, rewriting relPath, committing, and pushing. This simulates a
// `sysfig pull` that advances the repo ahead of the system file.
func pushNewContentDiff(t *testing.T, repoDir, relPath string, content []byte) {
	t.Helper()

	workDir := t.TempDir()
	runGitDiff(t, workDir, "clone", repoDir, "work")
	workClone := filepath.Join(workDir, "work")
	runGitDiff(t, workClone, "config", "user.email", "test@sysfig.local")
	runGitDiff(t, workClone, "config", "user.name", "sysfig-test")

	destPath := filepath.Join(workClone, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
	require.NoError(t, os.WriteFile(destPath, content, 0o644))

	runGitDiff(t, workClone, "add", relPath)
	runGitDiff(t, workClone, "commit", "-m", "test: update file")
	runGitDiff(t, workClone, "push", "origin")

	// Advance the bare repo HEAD to the new commit so git-show sees it.
	fetchCmd := exec.Command("git", "fetch", "--all")
	fetchCmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	fetchCmd.Run() //nolint:errcheck // best-effort

	resetCmd := exec.Command("git", "reset", "--soft", "FETCH_HEAD")
	resetCmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	resetCmd.Run() //nolint:errcheck // best-effort
}

// buildDiffFixture creates a sysfig environment with a single tracked file.
//
// It commits repoContent into the bare repo (repo.git) and records
// repoContent's hash as the current_hash in state.json (i.e. baseline =
// last committed state). RepoPath in the FileRecord is the git-relative path.
//
// Returns:
//
//	baseDir  – the sysfig data dir (contains state.json and repo.git/)
//	id       – the tracking ID ("etc_app_conf")
//	sysPath  – the logical system path ("/etc/app.conf")
//	relPath  – git-relative path stored in FileRecord.RepoPath ("etc/app.conf")
func buildDiffFixture(t *testing.T, repoContent []byte) (baseDir, id, sysPath, relPath string) {
	t.Helper()
	requireGitDiff(t)

	baseDir = t.TempDir()
	repoDir := filepath.Join(baseDir, "repo.git")

	sysPath = "/etc/app.conf"
	id = core.DeriveID(sysPath)
	relPath = "etc/app.conf"

	// Seed the bare repo with the committed file.
	initBareRepoDiff(t, repoDir, relPath, repoContent)

	recordedHash := hash.Bytes(repoContent)
	now := time.Now()
	s := &types.State{
		Version: 1,
		Files: map[string]*types.FileRecord{
			id: {
				ID:          id,
				SystemPath:  sysPath,
				RepoPath:    relPath, // git-relative, no leading slash
				CurrentHash: recordedHash,
				LastSync:    &now,
				Status:      types.StatusTracked,
				Encrypt:     false,
			},
		},
		Backups: map[string][]types.BackupRecord{},
	}
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600))

	return baseDir, id, sysPath, relPath
}

// writeSysFile writes content to <sysRoot><logicalPath>, creating parents.
func writeSysFile(t *testing.T, sysRoot, logicalPath string, content []byte) {
	t.Helper()
	full := filepath.Join(sysRoot, logicalPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, content, 0o644))
}

// writeStateHash overwrites current_hash in state.json for the given id.
// Used to simulate a stale recorded hash (e.g. after a pull).
func writeStateHash(t *testing.T, baseDir, id, newHash string) {
	t.Helper()
	statePath := filepath.Join(baseDir, "state.json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)

	var s types.State
	require.NoError(t, json.Unmarshal(raw, &s))
	rec := s.Files[id]
	require.NotNil(t, rec)
	rec.CurrentHash = newHash
	s.Files[id] = rec

	out, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o600))
}

// ---------------------------------------------------------------------------
// TestDiff_Synced
// ---------------------------------------------------------------------------

// TestDiff_Synced verifies that identical system and repo files yield an
// empty Diff string and Skipped == false.
func TestDiff_Synced(t *testing.T) {
	content := []byte("key=value\nfoo=bar\n")
	baseDir, id, sysPath, _ := buildDiffFixture(t, content)

	sysRoot := t.TempDir()
	writeSysFile(t, sysRoot, sysPath, content) // identical content

	results, err := core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusSynced, r.Status)
	assert.Empty(t, r.Diff, "identical files must produce no diff output")
	assert.False(t, r.Skipped)
}

// ---------------------------------------------------------------------------
// TestDiff_Dirty
// ---------------------------------------------------------------------------

// TestDiff_Dirty verifies that a system file that has diverged from the repo
// produces a non-empty unified diff and is labelled DIRTY.
//
// Direction: a = repo (last committed), b = system (what sync would capture).
func TestDiff_Dirty(t *testing.T) {
	repoContent := []byte("key=original\n")
	baseDir, id, sysPath, _ := buildDiffFixture(t, repoContent)

	sysRoot := t.TempDir()
	// Write a different version to the system file.
	sysContent := []byte("key=modified\n")
	writeSysFile(t, sysRoot, sysPath, sysContent)

	results, err := core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusDirty, r.Status)
	assert.False(t, r.Skipped)
	assert.NotEmpty(t, r.Diff, "diverged files must produce diff output")

	// The diff must show the added system line.
	assert.Contains(t, r.Diff, "+key=modified")
	assert.Contains(t, r.Diff, "-key=original")

	// Labels: --- repo  +++ system
	assert.Contains(t, r.Diff, "--- repo")
	assert.Contains(t, r.Diff, "+++ system")
}

// ---------------------------------------------------------------------------
// TestDiff_Pending
// ---------------------------------------------------------------------------

// TestDiff_Pending verifies that when the repo copy has moved ahead of the
// system (as happens after `sysfig pull`), the diff is labelled PENDING and
// shows the incoming changes.
//
// Direction: a = system (current), b = repo (incoming from pull).
func TestDiff_Pending(t *testing.T) {
	// The repo starts at "original" — this is what was last applied.
	originalContent := []byte("level=info\n")
	baseDir, id, sysPath, relPath := buildDiffFixture(t, originalContent)

	// System file is still at the original applied version.
	sysRoot := t.TempDir()
	writeSysFile(t, sysRoot, sysPath, originalContent)

	// Simulate a pull: push a new commit to the bare repo so it moves ahead.
	// state.json recorded hash still points to originalContent.
	pulledContent := []byte("level=debug\nverbose=true\n")
	repoDir := filepath.Join(baseDir, "repo.git")
	pushNewContentDiff(t, repoDir, relPath, pulledContent)
	// Do NOT update state.json's current_hash — that's the whole point:
	// after a pull the recorded hash lags behind the repo copy.

	results, err := core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusPending, r.Status)
	assert.False(t, r.Skipped)
	assert.NotEmpty(t, r.Diff, "pending files must produce diff output")

	// The diff shows what apply would deploy: lines added by the remote.
	assert.Contains(t, r.Diff, "+level=debug")
	assert.Contains(t, r.Diff, "+verbose=true")
	assert.Contains(t, r.Diff, "-level=info")

	// Labels: --- system  +++ repo
	assert.Contains(t, r.Diff, "--- system")
	assert.Contains(t, r.Diff, "+++ repo")
}

// ---------------------------------------------------------------------------
// TestDiff_Missing
// ---------------------------------------------------------------------------

// TestDiff_Missing verifies that a tracked file that is absent from the
// system is reported as Skipped with an appropriate reason.
func TestDiff_Missing(t *testing.T) {
	content := []byte("x=1\n")
	baseDir, id, _, _ := buildDiffFixture(t, content)

	// sysRoot is empty — the system file is never written there.
	sysRoot := t.TempDir()

	results, err := core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusMissing, r.Status)
	assert.True(t, r.Skipped)
	assert.NotEmpty(t, r.SkipReason)
	assert.Empty(t, r.Diff)
}

// ---------------------------------------------------------------------------
// TestDiff_Encrypted
// ---------------------------------------------------------------------------

// TestDiff_Encrypted verifies that encrypted files are skipped with a clear
// reason rather than attempting to diff ciphertext.
func TestDiff_Encrypted(t *testing.T) {
	content := []byte("secret=hunter2\n")
	baseDir, id, sysPath, _ := buildDiffFixture(t, content)

	// Mark the record as encrypted in state.json.
	statePath := filepath.Join(baseDir, "state.json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var s types.State
	require.NoError(t, json.Unmarshal(raw, &s))
	rec := s.Files[id]
	rec.Encrypt = true
	s.Files[id] = rec
	out, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o600))

	// Write the system file so it exists (shouldn't be read for encrypted).
	sysRoot := t.TempDir()
	writeSysFile(t, sysRoot, sysPath, content)

	results, err := core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusEncrypted, r.Status)
	assert.True(t, r.Skipped)
	assert.NotEmpty(t, r.SkipReason)
	assert.Empty(t, r.Diff)
}

// ---------------------------------------------------------------------------
// TestDiff_FilterByID
// ---------------------------------------------------------------------------

// TestDiff_FilterByID verifies that passing IDs restricts results to only
// those IDs, and that an unknown ID returns zero results without error.
func TestDiff_FilterByID(t *testing.T) {
	content := []byte("a=1\n")
	baseDir, id, sysPath, _ := buildDiffFixture(t, content)

	sysRoot := t.TempDir()
	writeSysFile(t, sysRoot, sysPath, content)

	// Filter to the known ID.
	results, err := core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		IDs:     []string{id},
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, id, results[0].ID)

	// Filter to a non-existent ID — zero results, no error.
	results, err = core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		IDs:     []string{"does_not_exist"},
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	assert.Empty(t, results)
}

// ---------------------------------------------------------------------------
// TestDiff_ResultsSortedByID
// ---------------------------------------------------------------------------

// TestDiff_ResultsSortedByID verifies that results are returned in
// deterministic alphabetical order by ID when multiple files are tracked.
func TestDiff_ResultsSortedByID(t *testing.T) {
	requireGitDiff(t)

	baseDir := t.TempDir()
	repoDir := filepath.Join(baseDir, "repo.git")

	now := time.Now()
	content := []byte("v=1\n")
	recordedHash := hash.Bytes(content)

	sysPaths := []string{"/alpha/file.conf", "/beta/file.conf", "/gamma/file.conf"}
	files := make(map[string]*types.FileRecord, len(sysPaths))

	// Initialise the bare repo with the first file to get HEAD established,
	// then push additional files in subsequent commits.
	firstRelPath := sysPaths[0][1:] // strip leading /
	initBareRepoDiff(t, repoDir, firstRelPath, content)
	firstID := core.DeriveID(sysPaths[0])
	files[firstID] = &types.FileRecord{
		ID:          firstID,
		SystemPath:  sysPaths[0],
		RepoPath:    firstRelPath,
		CurrentHash: recordedHash,
		LastSync:    &now,
		Status:      types.StatusTracked,
	}

	// Push additional files into the same bare repo.
	for _, sysPath := range sysPaths[1:] {
		relPath := sysPath[1:]
		pushNewContentDiff(t, repoDir, relPath, content)
		fileID := core.DeriveID(sysPath)
		files[fileID] = &types.FileRecord{
			ID:          fileID,
			SystemPath:  sysPath,
			RepoPath:    relPath,
			CurrentHash: recordedHash,
			LastSync:    &now,
			Status:      types.StatusTracked,
		}
	}

	s := &types.State{Version: 1, Files: files, Backups: map[string][]types.BackupRecord{}}
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600))

	sysRoot := t.TempDir()
	for _, rec := range files {
		writeSysFile(t, sysRoot, rec.SystemPath, content)
	}

	results, err := core.Diff(core.DiffOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Results must be sorted by ID (hash) — just verify ordering is consistent.
	for i := 1; i < len(results); i++ {
		assert.True(t, results[i-1].ID <= results[i].ID, "results must be sorted by ID")
	}
}

// ---------------------------------------------------------------------------
// TestHasDiff
// ---------------------------------------------------------------------------

// TestHasDiff verifies the HasDiff helper returns true only when at least
// one result contains a non-empty Diff string.
func TestHasDiff(t *testing.T) {
	empty := []core.DiffResult{
		{ID: "a", Diff: ""},
		{ID: "b", Diff: ""},
	}
	assert.False(t, core.HasDiff(empty), "no diff content → HasDiff must be false")

	withDiff := []core.DiffResult{
		{ID: "a", Diff: ""},
		{ID: "b", Diff: "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n"},
	}
	assert.True(t, core.HasDiff(withDiff), "at least one diff → HasDiff must be true")

	assert.False(t, core.HasDiff(nil), "nil slice → HasDiff must be false")
	assert.False(t, core.HasDiff([]core.DiffResult{}), "empty slice → HasDiff must be false")
}

// ---------------------------------------------------------------------------
// TestDiff_EmptyBaseDir
// ---------------------------------------------------------------------------

// TestDiff_EmptyBaseDir verifies that Diff returns a clear error when
// BaseDir is not provided.
func TestDiff_EmptyBaseDir(t *testing.T) {
	_, err := core.Diff(core.DiffOptions{BaseDir: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseDir")
}
