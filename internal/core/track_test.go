package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sysfig-dev/sysfig/internal/core"
	"github.com/sysfig-dev/sysfig/internal/hash"
	"github.com/sysfig-dev/sysfig/pkg/types"
)

// initTestBareRepo creates a bare git repo at repoDir and seeds it with an
// initial commit so HEAD is valid. Returns repoDir.
func initTestBareRepo(t *testing.T, repoDir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}

	cmd := exec.Command("git", "init", "--bare", repoDir)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", out)

	gitEnv := []string{"GIT_DIR=" + repoDir}

	// Set identity so commits work without global git config.
	for _, cfg := range [][]string{
		{"config", "user.email", "test@sysfig.local"},
		{"config", "user.name", "sysfig-test"},
	} {
		c := exec.Command("git", cfg...)
		c.Env = append(os.Environ(), gitEnv...)
		c.CombinedOutput() //nolint:errcheck
	}

	// Seed an initial commit via a temporary clone so HEAD resolves.
	workRoot := t.TempDir()
	workDir := filepath.Join(workRoot, "work")
	clone := exec.Command("git", "clone", repoDir, workDir)
	clone.Env = os.Environ()
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("git clone for seed: %v\n%s", err, out)
	}
	for _, cfg := range [][]string{
		{"config", "user.email", "test@sysfig.local"},
		{"config", "user.name", "sysfig-test"},
	} {
		c := exec.Command("git", cfg...)
		c.Dir = workDir
		c.Env = os.Environ()
		c.CombinedOutput() //nolint:errcheck
	}
	keepPath := filepath.Join(workDir, ".gitkeep")
	require.NoError(t, os.WriteFile(keepPath, []byte(""), 0o644))
	for _, args := range [][]string{
		{"add", ".gitkeep"},
		{"commit", "-m", "chore: initial commit"},
		{"push", "origin"},
	} {
		c := exec.Command("git", args...)
		c.Dir = workDir
		c.Env = os.Environ()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Sync the bare repo index to HEAD.
	readTree := exec.Command("git", "read-tree", "HEAD")
	readTree.Env = append(os.Environ(), gitEnv...)
	if out, err := readTree.CombinedOutput(); err != nil {
		t.Fatalf("git read-tree HEAD: %v\n%s", err, out)
	}

	return repoDir
}

// gitIndexContent reads a staged (index) blob from the bare repo.
// Uses `git show :<relPath>` which reads from the index, not HEAD.
func gitIndexContent(t *testing.T, repoDir, relPath string) []byte {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+repoDir, "show", ":"+relPath)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	require.NoError(t, err, "git show :%s", relPath)
	return out
}

// ---------------------------------------------------------------------------
// IsDenied
// ---------------------------------------------------------------------------

func TestIsDenied_Exact(t *testing.T) {
	assert.True(t, core.IsDenied("/etc/shadow"), "/etc/shadow must be denied")
	assert.False(t, core.IsDenied("/etc/nginx/nginx.conf"), "/etc/nginx/nginx.conf must not be denied")
}

func TestIsDenied_Glob(t *testing.T) {
	assert.True(t, core.IsDenied("/etc/ssh/ssh_host_rsa_key"), "ssh_host_* glob must match ssh_host_rsa_key")
	assert.False(t, core.IsDenied("/etc/ssh/sshd_config"), "ssh_host_* glob must NOT match sshd_config")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------


// readState loads state.json from stateDir and returns the parsed State.
func readState(t *testing.T, stateDir string) *types.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	require.NoError(t, err)
	var s types.State
	require.NoError(t, json.Unmarshal(data, &s))
	return &s
}

// ---------------------------------------------------------------------------
// Track
// ---------------------------------------------------------------------------

func TestTrack_Basic(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	// Create a system file under a SysRoot so Track uses the blob path.
	sysRoot := filepath.Join(tmp, "sysroot")
	sysFileDir := filepath.Join(sysRoot, "etc", "nginx")
	require.NoError(t, os.MkdirAll(sysFileDir, 0o755))
	sysFile := filepath.Join(sysFileDir, "nginx.conf")
	content := []byte("worker_processes auto;\n")
	require.NoError(t, os.WriteFile(sysFile, content, 0o640))

	opts := core.TrackOptions{
		SystemPath: sysFile,
		RepoDir:    repoDir,
		StateDir:   stateDir,
		ID:         "nginx_main",
		SysRoot:    sysRoot,
	}
	result, err := core.Track(opts)
	require.NoError(t, err)
	require.NotNil(t, result)

	// RepoPath is now the git-relative path (no leading slash).
	assert.Equal(t, "etc/nginx/nginx.conf", result.RepoPath)

	// --- file is staged in the git index and content matches ---
	indexed := gitIndexContent(t, repoDir, result.RepoPath)
	assert.Equal(t, content, indexed, "indexed content must match the source")

	// --- hash matches Bytes() of the same content ---
	wantHash := hash.Bytes(content)
	assert.Equal(t, wantHash, result.Hash, "hash must be the BLAKE3 hex of the file content")

	// --- state.json contains the record ---
	s := readState(t, stateDir)
	rec, ok := s.Files["nginx_main"]
	require.True(t, ok, "state.json must contain the tracked record")
	assert.Equal(t, "nginx_main", rec.ID)
	assert.Equal(t, types.StatusTracked, rec.Status)
	assert.Equal(t, wantHash, rec.CurrentHash)
	assert.NotNil(t, rec.LastSync)
}

func TestTrack_DeniedPath(t *testing.T) {
	tmp := t.TempDir()

	// /etc/shadow is in the denylist — no file needs to exist on disk because
	// the denylist check is performed before any I/O.
	opts := core.TrackOptions{
		SystemPath: "/etc/shadow",
		RepoDir:    filepath.Join(tmp, "repo"),
		StateDir:   filepath.Join(tmp, "state"),
	}

	_, err := core.Track(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denylist", "error must mention the denylist")
}

func TestTrack_AlreadyTracked(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	sysRoot := filepath.Join(tmp, "sysroot")
	require.NoError(t, os.MkdirAll(sysRoot, 0o755))
	sysFile := filepath.Join(sysRoot, "myapp.conf")
	require.NoError(t, os.WriteFile(sysFile, []byte("key=value\n"), 0o644))

	opts := core.TrackOptions{
		SystemPath: sysFile,
		RepoDir:    repoDir,
		StateDir:   stateDir,
		ID:         "myapp_conf",
		SysRoot:    sysRoot,
	}

	// First call must succeed.
	_, err := core.Track(opts)
	require.NoError(t, err)

	// Second call with the same ID must fail.
	_, err = core.Track(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already tracked", "error must mention the file is already tracked")
}

func TestTrack_IDDerivation(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	// Build a fake path under sysRoot so Track derives a clean ID.
	sysRoot := filepath.Join(tmp, "sysroot")
	fakeSysDir := filepath.Join(sysRoot, "etc", "nginx")
	require.NoError(t, os.MkdirAll(fakeSysDir, 0o755))
	sysFile := filepath.Join(fakeSysDir, "nginx.conf")
	require.NoError(t, os.WriteFile(sysFile, []byte("# nginx config\n"), 0o644))

	// Track without supplying an ID so that it is derived automatically.
	opts := core.TrackOptions{
		SystemPath: sysFile,
		RepoDir:    repoDir,
		StateDir:   stateDir,
		SysRoot:    sysRoot,
		// ID intentionally left empty
	}

	result, err := core.Track(opts)
	require.NoError(t, err)
	require.NotNil(t, result)

	// With SysRoot stripped, the logical path is /etc/nginx/nginx.conf
	// so the derived ID must be "etc_nginx_nginx_conf".
	assert.Equal(t, "etc_nginx_nginx_conf", result.ID)

	// The ID must also appear in state.json.
	s := readState(t, stateDir)
	_, ok := s.Files["etc_nginx_nginx_conf"]
	assert.True(t, ok, "state.json must contain a record keyed by the derived ID")
}

