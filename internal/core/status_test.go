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
// helpers shared across status tests
// ---------------------------------------------------------------------------

// requireGitStatus skips the test if git is not available on PATH.
func requireGitStatus(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// runGitStatus runs a git command in dir and fatally fails the test on error.
func runGitStatus(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q: %v\n%s", args, dir, err, out)
	}
}

// initBareRepoStatus creates a bare git repo at repoDir with a committed file
// at relPath containing content.
func initBareRepoStatus(t *testing.T, repoDir, relPath string, content []byte) {
	t.Helper()

	workDir := t.TempDir()

	cmd := exec.Command("git", "init", "--bare", repoDir)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", out)

	runGitStatus(t, workDir, "clone", repoDir, "work")
	workClone := filepath.Join(workDir, "work")
	runGitStatus(t, workClone, "config", "user.email", "test@sysfig.local")
	runGitStatus(t, workClone, "config", "user.name", "sysfig-test")

	destPath := filepath.Join(workClone, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
	require.NoError(t, os.WriteFile(destPath, content, 0o644))

	runGitStatus(t, workClone, "add", relPath)
	runGitStatus(t, workClone, "commit", "-m", "test: add file")
	runGitStatus(t, workClone, "push", "origin")
}

// buildStatusFixture sets up a sysfig environment with a single tracked file
// whose content is committed in the bare repo (repo.git).
//
// It returns:
//   - baseDir  : the sysfig base directory (contains state.json, repo.git/)
//   - id       : the tracking ID
//   - sysPath  : the logical system path recorded in state.json ("/etc/myapp.conf")
func buildStatusFixture(t *testing.T, content []byte) (baseDir, id, sysPath string) {
	t.Helper()
	requireGitStatus(t)

	baseDir = t.TempDir()
	repoDir := filepath.Join(baseDir, "repo.git")

	sysPath = "/etc/myapp.conf"
	id = core.DeriveID(sysPath)
	relPath := "etc/myapp.conf" // git-relative path stored in FileRecord.RepoPath

	// Seed the bare repo with the file committed.
	initBareRepoStatus(t, repoDir, relPath, content)

	// Compute the hash of content to store as the recorded hash.
	recordedHash := hash.Bytes(content)

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
	stateData, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), stateData, 0o600))

	return baseDir, id, sysPath
}

// writeSystemFile writes content to <sysRoot><logicalPath>, creating parent
// directories as needed.
func writeSystemFile(t *testing.T, sysRoot, logicalPath string, content []byte) string {
	t.Helper()
	full := filepath.Join(sysRoot, logicalPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, content, 0o644))
	return full
}

// ---------------------------------------------------------------------------
// TestStatus_Synced
// ---------------------------------------------------------------------------

// TestStatus_Synced verifies that a file whose on-disk content matches the
// repo content (read via git-show from the bare repo) is reported as SYNCED.
func TestStatus_Synced(t *testing.T) {
	content := []byte("key=value\n")
	baseDir, id, sysPath := buildStatusFixture(t, content)

	sysRoot := t.TempDir()
	// Write the same content that was committed — hashes must match.
	writeSystemFile(t, sysRoot, sysPath, content)

	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusSynced, r.Status, "identical content must report SYNCED")
	assert.Equal(t, hash.Bytes(content), r.CurrentHash)
	assert.Equal(t, hash.Bytes(content), r.RecordedHash)
}

// ---------------------------------------------------------------------------
// TestStatus_Dirty
// ---------------------------------------------------------------------------

// TestStatus_Dirty verifies that a file whose on-disk content differs from
// the repo content is reported as DIRTY.
func TestStatus_Dirty(t *testing.T) {
	originalContent := []byte("key=value\n")
	baseDir, id, sysPath := buildStatusFixture(t, originalContent)

	sysRoot := t.TempDir()
	// Write modified content — hashes will differ from repo.
	modifiedContent := []byte("key=changed\n")
	writeSystemFile(t, sysRoot, sysPath, modifiedContent)

	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusDirty, r.Status, "modified content must report DIRTY")
	assert.Equal(t, hash.Bytes(modifiedContent), r.CurrentHash, "CurrentHash must reflect the modified file")
	assert.Equal(t, hash.Bytes(originalContent), r.RecordedHash, "RecordedHash must still reflect the original tracked hash")
}

// ---------------------------------------------------------------------------
// TestStatus_Missing
// ---------------------------------------------------------------------------

// TestStatus_Missing verifies that a file recorded in state.json but absent
// from the filesystem is reported as MISSING.
func TestStatus_Missing(t *testing.T) {
	content := []byte("key=value\n")
	baseDir, id, _ := buildStatusFixture(t, content)

	// Use an empty sysRoot — the system file is never written there.
	sysRoot := t.TempDir()

	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusMissing, r.Status, "absent file must report MISSING")
	assert.Empty(t, r.CurrentHash, "CurrentHash must be empty for a missing file")
}

// ---------------------------------------------------------------------------
// TestStatus_OutputContainsDirty
// ---------------------------------------------------------------------------

// TestStatus_OutputContainsDirty is the exact check the validation script
// performs: the string "DIRTY" must appear in the Status field of a result
// when the tracked file has been modified on disk.
func TestStatus_OutputContainsDirty(t *testing.T) {
	originalContent := []byte("original content\n")
	baseDir, _, sysPath := buildStatusFixture(t, originalContent)

	sysRoot := t.TempDir()
	// Write different content so the file is "dirty".
	writeSystemFile(t, sysRoot, sysPath, []byte("modified content\n"))

	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.NotEmpty(t, results, "must return at least one result")

	// The validation script checks that the literal string "DIRTY" appears in
	// the status output.  Verify that at least one result carries it.
	found := false
	for _, r := range results {
		if string(r.Status) == "DIRTY" {
			found = true
			break
		}
	}
	assert.True(t, found, `expected at least one FileStatusResult with Status == "DIRTY"`)
}

// ---------------------------------------------------------------------------
// TestStatus_FilterByID
// ---------------------------------------------------------------------------

// TestStatus_FilterByID verifies that passing a non-empty ids slice restricts
// the results to only those IDs.
func TestStatus_FilterByID(t *testing.T) {
	content := []byte("key=value\n")
	baseDir, id, sysPath := buildStatusFixture(t, content)

	sysRoot := t.TempDir()
	writeSystemFile(t, sysRoot, sysPath, content)

	// Request only the known ID — should get exactly one result.
	results, err := core.Status(baseDir, []string{id}, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, id, results[0].ID)

	// Request a non-existent ID — should get zero results (no error).
	results, err = core.Status(baseDir, []string{"does_not_exist"}, sysRoot)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// ---------------------------------------------------------------------------
// TestStatus_SysRootPath
// ---------------------------------------------------------------------------

// TestStatus_SysRootPath verifies that the SystemPath field in results reflects
// the sysRoot-prefixed path when sysRoot is set.
func TestStatus_SysRootPath(t *testing.T) {
	content := []byte("cfg=1\n")
	baseDir, _, sysPath := buildStatusFixture(t, content)

	sysRoot := t.TempDir()
	writeSystemFile(t, sysRoot, sysPath, content)

	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)

	expected := filepath.Join(sysRoot, sysPath)
	assert.Equal(t, expected, results[0].SystemPath,
		"SystemPath in result must include the sysRoot prefix")
}

// ---------------------------------------------------------------------------
// TestStatus_Pending
// ---------------------------------------------------------------------------

// TestStatus_Pending verifies that when the bare repo has a newer commit than
// what was last applied (recorded hash lags behind), the status is PENDING.
//
// We simulate this by:
//  1. Committing "original" content to the bare repo and recording its hash.
//  2. Pushing a new commit with "updated" content to the bare repo.
//  3. Leaving the system file at "original" and state.json's current_hash
//     pointing to "original" — the repo has moved ahead.
func TestStatus_Pending(t *testing.T) {
	requireGitStatus(t)

	originalContent := []byte("level=info\n")
	baseDir, id, sysPath := buildStatusFixture(t, originalContent)

	// System file stays at the original applied version.
	sysRoot := t.TempDir()
	writeSystemFile(t, sysRoot, sysPath, originalContent)

	// Push a new commit to the bare repo so it moves ahead of state.json.
	repoDir := filepath.Join(baseDir, "repo.git")
	updatedContent := []byte("level=debug\nverbose=true\n")

	// Clone the bare repo into a temp work dir, update, push back.
	workDir := t.TempDir()
	runGitStatus(t, workDir, "clone", repoDir, "work")
	workClone := filepath.Join(workDir, "work")
	runGitStatus(t, workClone, "config", "user.email", "test@sysfig.local")
	runGitStatus(t, workClone, "config", "user.name", "sysfig-test")

	destPath := filepath.Join(workClone, "etc/myapp.conf")
	require.NoError(t, os.WriteFile(destPath, updatedContent, 0o644))
	runGitStatus(t, workClone, "add", "etc/myapp.conf")
	runGitStatus(t, workClone, "commit", "-m", "test: update file")
	runGitStatus(t, workClone, "push", "origin")

	// Advance the bare repo HEAD to the new commit.
	// `git fetch` in the bare repo updates remote tracking refs; we then
	// reset HEAD to the fetched commit so git-show sees the new content.
	fetchCmd := exec.Command("git", "fetch", "--all")
	fetchCmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	fetchCmd.Run() // best-effort

	// Fast-forward the local branch to match origin.
	resetCmd := exec.Command("git", "reset", "--soft", "FETCH_HEAD")
	resetCmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	resetCmd.Run() // best-effort

	// state.json still records the original hash — the repo moved ahead.
	// Status should detect that repoHash != recordedHash → PENDING.
	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, core.StatusPending, r.Status, "repo ahead of system must report PENDING")
}
