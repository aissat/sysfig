package core_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aissat/sysfig/internal/core"
)

func requireGitClone(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// makeBareRepo creates a bare git repo and seeds it with an initial commit
// containing a file at relPath with content.
func makeBareRepo(t *testing.T, relPath string, content []byte) string {
	t.Helper()

	repoDir := filepath.Join(t.TempDir(), "repo.git")
	workDir := t.TempDir()

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run(workDir, "init", "--bare", repoDir)
	run(workDir, "clone", repoDir, "work")
	workClone := filepath.Join(workDir, "work")
	run(workClone, "config", "user.email", "test@sysfig.local")
	run(workClone, "config", "user.name", "sysfig-test")

	dest := filepath.Join(workClone, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	require.NoError(t, os.WriteFile(dest, content, 0o644))
	run(workClone, "add", relPath)
	run(workClone, "commit", "-m", "init")
	run(workClone, "push", "origin")

	return repoDir
}

// ---------------------------------------------------------------------------
// Clone — already-existing repo (no-op path)
// ---------------------------------------------------------------------------

func TestClone_AlreadyExists(t *testing.T) {
	requireGitClone(t)

	repoDir := makeBareRepo(t, "etc/app.conf", []byte("key=val\n"))
	baseDir := filepath.Dir(repoDir) // repoDir is <baseDir>/repo.git

	result, err := core.Clone(core.CloneOptions{
		BaseDir:   baseDir,
		RemoteURL: "", // no URL needed — repo already exists
	})
	require.NoError(t, err)
	assert.True(t, result.AlreadyExisted)
	assert.Equal(t, baseDir, result.BaseDir)
}

// ---------------------------------------------------------------------------
// Clone — fresh from local bare repo
// ---------------------------------------------------------------------------

func TestClone_FreshNoManifest(t *testing.T) {
	requireGitClone(t)

	// Create a local bare repo that has no sysfig.yaml.
	srcRepo := makeBareRepo(t, "etc/app.conf", []byte("key=val\n"))

	baseDir := t.TempDir()

	result, err := core.Clone(core.CloneOptions{
		BaseDir:   baseDir,
		RemoteURL: srcRepo,
	})
	require.NoError(t, err)
	assert.False(t, result.AlreadyExisted)
	assert.Equal(t, 0, result.Seeded, "no manifest → nothing seeded")
}

func TestClone_FreshWithManifest(t *testing.T) {
	requireGitClone(t)

	manifest := `tracked_files:
  - id: abc12345
    system_path: /etc/app.conf
    repo_path: etc/app.conf
`
	workDir := t.TempDir()
	repoDir := filepath.Join(t.TempDir(), "repo.git")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run(workDir, "init", "--bare", repoDir)
	run(workDir, "clone", repoDir, "work")
	workClone := filepath.Join(workDir, "work")
	run(workClone, "config", "user.email", "test@sysfig.local")
	run(workClone, "config", "user.name", "sysfig-test")

	require.NoError(t, os.WriteFile(filepath.Join(workClone, "sysfig.yaml"), []byte(manifest), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workClone, "etc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workClone, "etc/app.conf"), []byte("key=val\n"), 0o644))
	run(workClone, "add", ".")
	run(workClone, "commit", "-m", "init")
	run(workClone, "push", "origin")

	baseDir := t.TempDir()
	result, err := core.Clone(core.CloneOptions{
		BaseDir:   baseDir,
		RemoteURL: repoDir,
	})
	require.NoError(t, err)
	assert.False(t, result.AlreadyExisted)
	assert.Equal(t, 1, result.Seeded)
}

// ---------------------------------------------------------------------------
// Clone — no remote URL and no existing repo → error
// ---------------------------------------------------------------------------

func TestClone_NoURLNoRepo(t *testing.T) {
	baseDir := t.TempDir()
	_, err := core.Clone(core.CloneOptions{
		BaseDir:   baseDir,
		RemoteURL: "",
	})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Clone — invalid remote URL → error
// ---------------------------------------------------------------------------

func TestClone_InvalidRemote(t *testing.T) {
	requireGitClone(t)

	baseDir := t.TempDir()
	_, err := core.Clone(core.CloneOptions{
		BaseDir:   baseDir,
		RemoteURL: "/nonexistent/path/does-not-exist.git",
	})
	assert.Error(t, err)
}
