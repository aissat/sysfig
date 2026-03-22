package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadState reads and parses state.json from baseDir.
func loadState(t *testing.T, baseDir string) *types.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(baseDir, "state.json"))
	require.NoError(t, err)
	var s types.State
	require.NoError(t, json.Unmarshal(data, &s))
	return &s
}

// writeFile is a tiny helper that creates a file with given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// gitShowAt runs `git --git-dir=repoDir show <ref>:<relPath>` and returns
// (content, ok) — ok is false when the object does not exist.
func gitShowAt(repoDir, ref, relPath string) ([]byte, bool) {
	cmd := exec.Command("git", "--git-dir="+repoDir, "show", ref+":"+relPath)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// ---------------------------------------------------------------------------
// Track --local
// ---------------------------------------------------------------------------

// TestTrack_LocalOnly verifies that a file tracked with --local is staged in
// a local/ branch (not a track/ branch) and is never included in push refspecs.
func TestTrack_LocalOnly(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")
	sysRoot := t.TempDir()

	writeFile(t, filepath.Join(sysRoot, "etc/wireguard/wg0.conf"), "[Interface]\nPrivateKey = secret\n")

	result, err := core.Track(core.TrackOptions{
		SystemPath: filepath.Join(sysRoot, "etc/wireguard/wg0.conf"),
		RepoDir:    repoDir,
		StateDir:   baseDir,
		SysRoot:    sysRoot,
		LocalOnly:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Branch must start with "local/", not "track/".
	assert.True(t, strings.HasPrefix(result.Branch, "local/"),
		"expected branch to start with local/, got %q", result.Branch)

	// Sync to create the commit on the local/ branch.
	_, err = core.Sync(core.SyncOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	// Content must be committed in the local branch.
	content, ok := gitShowAt(repoDir, result.Branch, "etc/wireguard/wg0.conf")
	assert.True(t, ok, "local/ branch must contain the committed file")
	assert.Contains(t, string(content), "PrivateKey")

	// State record must have LocalOnly set.
	s := loadState(t, baseDir)
	found := false
	for _, r := range s.Files {
		if r.SystemPath == "/etc/wireguard/wg0.conf" {
			assert.True(t, r.LocalOnly, "state record must have LocalOnly=true")
			assert.False(t, r.HashOnly, "LocalOnly must not imply HashOnly")
			assert.True(t, strings.HasPrefix(r.Branch, "local/"),
				"stored branch must start with local/")
			found = true
		}
	}
	assert.True(t, found, "state record must exist for the tracked file")
}

// ---------------------------------------------------------------------------
// Track --hash-only
// ---------------------------------------------------------------------------

// TestTrack_HashOnly verifies that a hash-only file records the hash but
// stores no content in the repo and gets no branch.
func TestTrack_HashOnly(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")
	sysRoot := t.TempDir()

	writeFile(t, filepath.Join(sysRoot, "etc/ssh/sshd_config"), "PermitRootLogin no\n")

	result, err := core.Track(core.TrackOptions{
		SystemPath: filepath.Join(sysRoot, "etc/ssh/sshd_config"),
		RepoDir:    repoDir,
		StateDir:   baseDir,
		SysRoot:    sysRoot,
		HashOnly:   true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Hash must be recorded.
	assert.NotEmpty(t, result.Hash)

	// No content in any branch.
	_, ok := gitShowAt(repoDir, "track/etc/ssh/sshd_config", "etc/ssh/sshd_config")
	assert.False(t, ok, "hash-only must not stage content in track/ branch")
	_, ok = gitShowAt(repoDir, "local/etc/ssh/sshd_config", "etc/ssh/sshd_config")
	assert.False(t, ok, "hash-only must not stage content in local/ branch")

	// State record must have HashOnly set and Branch empty.
	s := loadState(t, baseDir)
	found := false
	for _, r := range s.Files {
		if r.SystemPath == "/etc/ssh/sshd_config" {
			assert.True(t, r.HashOnly, "state record must have HashOnly=true")
			assert.Empty(t, r.Branch, "hash-only records must have no branch")
			assert.NotEmpty(t, r.CurrentHash, "hash must be stored in state")
			found = true
		}
	}
	assert.True(t, found, "state record must exist for the tracked file")
}

// ---------------------------------------------------------------------------
// Status: hash-only — SYNCED then TAMPERED
// ---------------------------------------------------------------------------

// TestStatus_HashOnly_SyncedThenTampered exercises the SYNCED→TAMPERED
// transition for a hash-only tracked file.
func TestStatus_HashOnly_SyncedThenTampered(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")
	sysRoot := t.TempDir()
	filePath := filepath.Join(sysRoot, "etc/ssh/sshd_config")

	writeFile(t, filePath, "PermitRootLogin no\n")

	_, err := core.Track(core.TrackOptions{
		SystemPath: filePath,
		RepoDir:    repoDir,
		StateDir:   baseDir,
		SysRoot:    sysRoot,
		HashOnly:   true,
	})
	require.NoError(t, err)

	// Initial status must be SYNCED.
	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, core.StatusSynced, results[0].Status, "freshly tracked hash-only file must be SYNCED")
	assert.True(t, results[0].HashOnly)

	// Tamper the file.
	require.NoError(t, os.WriteFile(filePath, []byte("PermitRootLogin yes\n# tampered\n"), 0o644))

	// Status must now be TAMPERED.
	results, err = core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, core.StatusTampered, results[0].Status, "modified hash-only file must be TAMPERED")
}

// ---------------------------------------------------------------------------
// Status: local-only — SYNCED then DIRTY
// ---------------------------------------------------------------------------

// TestStatus_LocalOnly_DirtyAfterModification verifies that a local-only file
// shows SYNCED initially and DIRTY after modification.
func TestStatus_LocalOnly_DirtyAfterModification(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")
	sysRoot := t.TempDir()
	filePath := filepath.Join(sysRoot, "etc/wireguard/wg0.conf")

	writeFile(t, filePath, "[Interface]\nPrivateKey = original\n")

	_, err := core.Track(core.TrackOptions{
		SystemPath: filePath,
		RepoDir:    repoDir,
		StateDir:   baseDir,
		SysRoot:    sysRoot,
		LocalOnly:  true,
	})
	require.NoError(t, err)

	// Sync to create the initial commit so status has a repo baseline.
	_, err = core.Sync(core.SyncOptions{BaseDir: baseDir, SysRoot: sysRoot})
	require.NoError(t, err)

	results, err := core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, core.StatusSynced, results[0].Status)
	assert.True(t, results[0].LocalOnly)

	// Modify the file.
	require.NoError(t, os.WriteFile(filePath, []byte("[Interface]\nPrivateKey = changed\n"), 0o644))

	results, err = core.Status(baseDir, nil, sysRoot)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, core.StatusDirty, results[0].Status, "modified local-only file must be DIRTY")
}

// ---------------------------------------------------------------------------
// Sync: local-only creates commit; hash-only is skipped
// ---------------------------------------------------------------------------

// TestSync_LocalOnly_CreatesLocalCommit verifies that sync creates a commit
// on the local/ branch for a local-only file.
func TestSync_LocalOnly_CreatesLocalCommit(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")
	sysRoot := t.TempDir()
	filePath := filepath.Join(sysRoot, "etc/wireguard/wg0.conf")

	writeFile(t, filePath, "[Interface]\nPrivateKey = secret\n")

	trackResult, err := core.Track(core.TrackOptions{
		SystemPath: filePath,
		RepoDir:    repoDir,
		StateDir:   baseDir,
		SysRoot:    sysRoot,
		LocalOnly:  true,
	})
	require.NoError(t, err)

	syncResult, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
		Message: "test: local-only sync",
	})
	require.NoError(t, err)
	assert.True(t, syncResult.Committed, "sync must produce a commit for the local-only file")

	// The local/ branch must contain the file.
	content, ok := gitShowAt(repoDir, trackResult.Branch, "etc/wireguard/wg0.conf")
	assert.True(t, ok, "file must be present in local/ branch after sync")
	assert.Contains(t, string(content), "PrivateKey")
}

// TestSync_HashOnly_IsSkipped verifies that hash-only files are skipped by sync.
func TestSync_HashOnly_IsSkipped(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")
	sysRoot := t.TempDir()
	filePath := filepath.Join(sysRoot, "etc/ssh/sshd_config")

	writeFile(t, filePath, "PermitRootLogin no\n")

	_, err := core.Track(core.TrackOptions{
		SystemPath: filePath,
		RepoDir:    repoDir,
		StateDir:   baseDir,
		SysRoot:    sysRoot,
		HashOnly:   true,
	})
	require.NoError(t, err)

	syncResult, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	// No commit should be created — hash-only file has nothing to stage.
	assert.False(t, syncResult.Committed, "sync must not commit anything for a hash-only file")
}

// ---------------------------------------------------------------------------
// sys-root UX: track via sys-root records logical path
// ---------------------------------------------------------------------------

// TestTrack_SysRoot_LogicalPath verifies that when SysRoot is set, the path
// argument can be the real disk path and the logical path stored in state is
// the canonical system path (with SysRoot stripped).
func TestTrack_SysRoot_LogicalPath(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")
	sysRoot := t.TempDir()

	realPath := filepath.Join(sysRoot, "etc/nginx/nginx.conf")
	writeFile(t, realPath, "worker_processes 1;\n")

	// Pass the full real disk path — sys-root strips the prefix for storage.
	_, err := core.Track(core.TrackOptions{
		SystemPath: realPath,
		RepoDir:    repoDir,
		StateDir:   baseDir,
		SysRoot:    sysRoot,
	})
	require.NoError(t, err)

	s := loadState(t, baseDir)
	found := false
	for _, r := range s.Files {
		if r.SystemPath == "/etc/nginx/nginx.conf" {
			found = true
			assert.Equal(t, "etc/nginx/nginx.conf", r.RepoPath)
		}
	}
	assert.True(t, found, "logical path /etc/nginx/nginx.conf must be in state")
}
