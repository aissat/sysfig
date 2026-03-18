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
	"github.com/sysfig-dev/sysfig/internal/core"
	"github.com/sysfig-dev/sysfig/pkg/types"
)

// ---------------------------------------------------------------------------
// helpers shared across apply tests
// ---------------------------------------------------------------------------

// requireGitApply skips the test if git is not available on PATH.
func requireGitApply(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// runGitApply runs a git command in dir and fatally fails the test on error.
func runGitApply(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q: %v\n%s", args, dir, err, out)
	}
}

// initBareRepoApply creates a bare git repo at repoDir with a committed file
// at relPath containing content. Returns the bare repo dir.
//
// relPath is the git-relative path, e.g. "etc/myapp.conf".
func initBareRepoApply(t *testing.T, repoDir, relPath string, content []byte) {
	t.Helper()

	// Create a temporary working tree to seed the bare repo via a clone.
	workDir := t.TempDir()

	// Initialise the bare repo.
	cmd := exec.Command("git", "init", "--bare", repoDir)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", out)

	// Clone it into a working directory so we can commit files.
	runGitApply(t, workDir, "clone", repoDir, "work")
	workClone := filepath.Join(workDir, "work")
	runGitApply(t, workClone, "config", "user.email", "test@sysfig.local")
	runGitApply(t, workClone, "config", "user.name", "sysfig-test")

	// Write the file at relPath inside the working clone.
	destPath := filepath.Join(workClone, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
	require.NoError(t, os.WriteFile(destPath, content, 0o644))

	// Stage, commit, and push back to the bare repo.
	runGitApply(t, workClone, "add", relPath)
	runGitApply(t, workClone, "commit", "-m", "test: add file")
	runGitApply(t, workClone, "push", "origin")
}

// buildApplyFixture creates a minimal sysfig environment with a single
// tracked file already committed in the bare repo (repo.git) and recorded
// in state.json.
//
// It returns:
//   - baseDir  : the sysfig base directory (contains state.json, repo.git/, backups/)
//   - relPath  : git-relative path stored in FileRecord.RepoPath (e.g. "etc/myapp.conf")
//   - id       : the tracking ID used
//   - content  : the byte content committed to the bare repo
func buildApplyFixture(t *testing.T) (baseDir, relPath, id string, content []byte) {
	t.Helper()
	requireGitApply(t)

	baseDir = t.TempDir()
	repoDir := filepath.Join(baseDir, "repo.git")

	id = "etc_myapp_conf"
	relPath = "etc/myapp.conf"
	content = []byte("key=value\n")

	// Seed the bare repo with the file committed.
	initBareRepoApply(t, repoDir, relPath, content)

	// Write a minimal state.json containing the record.
	// RepoPath is the git-relative path (no leading slash).
	now := time.Now()
	s := &types.State{
		Version: 1,
		Files: map[string]*types.FileRecord{
			id: {
				ID:          id,
				SystemPath:  "/etc/myapp.conf",
				RepoPath:    relPath,
				CurrentHash: "deadbeef",
				LastSync:    &now,
				Status:      types.StatusTracked,
				Encrypt:     false,
				Meta: &types.FileMeta{
					Mode: 0o644,
				},
			},
		},
		Backups: map[string][]types.BackupRecord{},
	}
	stateData, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), stateData, 0o600))

	return baseDir, relPath, id, content
}

// ---------------------------------------------------------------------------
// TestApply_Basic
// ---------------------------------------------------------------------------

// TestApply_Basic verifies that Apply writes the repo file to the correct
// destination derived from FileRecord.SystemPath when SysRoot is set.
func TestApply_Basic(t *testing.T) {
	baseDir, _, id, content := buildApplyFixture(t)

	// Use a sysRoot so we control the destination without requiring root.
	sysRoot := t.TempDir()

	opts := core.ApplyOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	}

	results, err := core.Apply(opts)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.False(t, r.Skipped)

	// The file must exist at the resolved destination.
	destPath := filepath.Join(sysRoot, "/etc/myapp.conf")
	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, content, got, "applied file content must match the repo file")
}

// ---------------------------------------------------------------------------
// TestApply_SysRoot
// ---------------------------------------------------------------------------

// TestApply_SysRoot verifies that when SysRoot is set, the destination is
// <sysroot><system_path> and the real system path is NOT written.
func TestApply_SysRoot(t *testing.T) {
	baseDir, _, id, content := buildApplyFixture(t)

	sysRoot := t.TempDir()
	expectedDest := filepath.Join(sysRoot, "/etc/myapp.conf")

	opts := core.ApplyOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	}

	results, err := core.Apply(opts)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.Equal(t, expectedDest, r.SystemPath, "SystemPath in result must include SysRoot prefix")

	// File must be at the sysroot-relative path.
	got, err := os.ReadFile(expectedDest)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// The raw system path (/etc/myapp.conf) must NOT have been created.
	_, err = os.Stat("/etc/myapp.conf")
	assert.True(t, os.IsNotExist(err), "real system path must not be touched when SysRoot is set")
}

// ---------------------------------------------------------------------------
// TestApply_DryRun
// ---------------------------------------------------------------------------

// TestApply_DryRun verifies that DryRun=true causes Apply to return results
// without actually writing any files.
func TestApply_DryRun(t *testing.T) {
	baseDir, _, id, _ := buildApplyFixture(t)

	sysRoot := t.TempDir()
	expectedDest := filepath.Join(sysRoot, "/etc/myapp.conf")

	opts := core.ApplyOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
		DryRun:  true,
	}

	results, err := core.Apply(opts)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.True(t, r.Skipped, "DryRun result must have Skipped=true")
	assert.Empty(t, r.BackupPath, "DryRun must not create a backup")

	// Destination file must NOT have been written.
	_, err = os.Stat(expectedDest)
	assert.True(t, os.IsNotExist(err), "DryRun must not write the destination file")
}

// ---------------------------------------------------------------------------
// TestApply_CreatesBackup
// ---------------------------------------------------------------------------

// TestApply_CreatesBackup verifies that when the destination file already
// exists and NoBackup is false, a backup is created in the backups directory.
func TestApply_CreatesBackup(t *testing.T) {
	baseDir, _, id, _ := buildApplyFixture(t)

	sysRoot := t.TempDir()
	destPath := filepath.Join(sysRoot, "/etc/myapp.conf")

	// Pre-create the destination so there is something to back up.
	require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
	require.NoError(t, os.WriteFile(destPath, []byte("old content\n"), 0o644))

	opts := core.ApplyOptions{
		BaseDir:  baseDir,
		SysRoot:  sysRoot,
		NoBackup: false,
		Force:    true, // pre-existing file has different hash → treat as force-overwrite
	}

	results, err := core.Apply(opts)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, id, r.ID)
	assert.NotEmpty(t, r.BackupPath, "a backup path must be set when pre-existing file is backed up")

	// The backup file must actually exist on disk.
	_, err = os.Stat(r.BackupPath)
	require.NoError(t, err, "backup file must exist at the reported path")

	// The backup content must match the OLD system file content.
	backupData, err := os.ReadFile(r.BackupPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("old content\n"), backupData, "backup must contain the pre-apply content")

	// The backup directory structure must be under <baseDir>/backups/<id>/.
	expectedBackupDir := filepath.Join(baseDir, "backups", id)
	assert.True(t,
		filepath.HasPrefix(r.BackupPath, expectedBackupDir),
		"backup path %q must be under %q", r.BackupPath, expectedBackupDir,
	)
}

// ---------------------------------------------------------------------------
// TestApply_NoBackup
// ---------------------------------------------------------------------------

// TestApply_NoBackup verifies that when NoBackup=true no backup directory is
// created even if the destination file exists.
func TestApply_NoBackup(t *testing.T) {
	baseDir, _, _, _ := buildApplyFixture(t)

	sysRoot := t.TempDir()
	destPath := filepath.Join(sysRoot, "/etc/myapp.conf")

	// Pre-create the destination.
	require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
	require.NoError(t, os.WriteFile(destPath, []byte("old content\n"), 0o644))

	opts := core.ApplyOptions{
		BaseDir:  baseDir,
		SysRoot:  sysRoot,
		NoBackup: true,
		Force:    true, // pre-existing file has different hash → treat as force-overwrite
	}

	results, err := core.Apply(opts)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Empty(t, r.BackupPath, "NoBackup=true must not produce a backup path")

	// The backups/<id> directory must not have been created.
	backupsDir := filepath.Join(baseDir, "backups")
	entries, err := os.ReadDir(backupsDir)
	if err == nil {
		// Directory may or may not exist; if it does, it must be empty.
		assert.Empty(t, entries, "backups directory must remain empty when NoBackup=true")
	}
	// os.IsNotExist is also acceptable — backups dir never created.
}

// ---------------------------------------------------------------------------
// TestApply_DirtyProtection
// ---------------------------------------------------------------------------

// TestApply_DirtyProtection verifies that Apply skips files whose on-disk
// content differs from the recorded CurrentHash (DIRTY), and that Force=true
// overrides the protection.
func TestApply_DirtyProtection(t *testing.T) {
	baseDir, _, id, repoContent := buildApplyFixture(t)

	sysRoot := t.TempDir()
	destPath := filepath.Join(sysRoot, "/etc/myapp.conf")

	// Write a locally-modified version to disk — this is the "DIRTY" state.
	require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
	dirtyContent := []byte("locally modified\n")
	require.NoError(t, os.WriteFile(destPath, dirtyContent, 0o644))

	// Without --force: file must be skipped (DirtySkipped=true).
	results, err := core.Apply(core.ApplyOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
		Force:   false,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].DirtySkipped, "DIRTY file must be skipped without --force")

	// On-disk content must be unchanged.
	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, dirtyContent, got, "DIRTY file must not be overwritten without --force")

	// With --force: file must be applied.
	results, err = core.Apply(core.ApplyOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
		Force:   true,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].DirtySkipped, "Force=true must not skip DIRTY file")

	got, err = os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, repoContent, got, "--force must overwrite DIRTY file with repo content")

	_ = id
}
