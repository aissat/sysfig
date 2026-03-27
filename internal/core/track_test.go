package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/pkg/types"
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

// ── SEC-006: incomplete denylist — missing privilege-escalation paths ─────────
//
// The original denylist did not cover sudoers.d, pam.d, polkit, cron, or
// /run/secrets, so an attacker with repo access could track and push those
// files to take over a system. Each entry below maps to a gap found in the
// security assessment.

func TestSEC006_SudoersDotD_IsDenied(t *testing.T) {
	assert.True(t, core.IsDenied("/etc/sudoers.d/90-admin"),
		"SEC-006: /etc/sudoers.d/* grants root via sudo — must be denied")
	assert.True(t, core.IsDenied("/etc/sudoers.d/custom"),
		"SEC-006: /etc/sudoers.d/* grants root via sudo — must be denied")
}

func TestSEC006_PamD_IsDenied(t *testing.T) {
	assert.True(t, core.IsDenied("/etc/pam.d/sshd"),
		"SEC-006: /etc/pam.d/* controls the auth stack — must be denied")
	assert.True(t, core.IsDenied("/etc/pam.d/sudo"),
		"SEC-006: /etc/pam.d/* controls the auth stack — must be denied")
}

func TestSEC006_SecurityDir_IsDenied(t *testing.T) {
	assert.True(t, core.IsDenied("/etc/security/access.conf"),
		"SEC-006: /etc/security/* (PAM limits/access) — must be denied")
}

func TestSEC006_Polkit_IsDenied(t *testing.T) {
	assert.True(t, core.IsDenied("/etc/polkit-1/rules.d/50.rules"),
		"SEC-006: polkit rules grant privilege — must be denied at any depth")
}

func TestSEC006_CronD_IsDenied(t *testing.T) {
	assert.True(t, core.IsDenied("/etc/cron.d/root-job"),
		"SEC-006: /etc/cron.d/* runs as root — must be denied")
	assert.True(t, core.IsDenied("/etc/cron.daily/backup"),
		"SEC-006: /etc/cron.daily/* runs as root — must be denied")
}

func TestSEC006_RunSecrets_IsDenied(t *testing.T) {
	assert.True(t, core.IsDenied("/run/secrets/db_password"),
		"SEC-006: /run/secrets/* holds container/systemd secrets — must be denied")
}

func TestSEC006_SslPrivateNested_IsDenied(t *testing.T) {
	// Original /etc/ssl/private/* only covered one level; nested certs slipped through.
	assert.True(t, core.IsDenied("/etc/ssl/private/sub/key.pem"),
		"SEC-006: nested private keys under /etc/ssl/private/ must also be denied")
}

// Sanity-check: legitimate paths that must remain trackable.
func TestSEC006_LegitPaths_NotDenied(t *testing.T) {
	assert.False(t, core.IsDenied("/etc/nginx/nginx.conf"))
	assert.False(t, core.IsDenied("/etc/ssh/sshd_config"))
	assert.False(t, core.IsDenied("/etc/systemd/system/myapp.service"))
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

// TestTrack_HashOnly_DeniedPathAllowed verifies that hash-only mode bypasses
// the denylist. Storing only a digest carries none of the risk that blocks
// content tracking (secrets leaking into git) — it is useful for integrity
// monitoring of sensitive files like /etc/shadow or /etc/sudoers.
func TestTrack_HashOnly_DeniedPathAllowed(t *testing.T) {
	baseDir := initBareLocalRepo(t)
	sysRoot := t.TempDir()

	// Simulate a denylist-protected file with some fake content.
	writeFile(t, filepath.Join(sysRoot, "etc/shadow"), "root:!:19000::::::\n")

	result, err := core.Track(core.TrackOptions{
		SystemPath: filepath.Join(sysRoot, "etc/shadow"),
		RepoDir:    filepath.Join(baseDir, "repo.git"),
		StateDir:   baseDir,
		SysRoot:    sysRoot,
		HashOnly:   true,
	})
	require.NoError(t, err, "hash-only tracking of a denylist path must succeed")
	require.NotNil(t, result)
	assert.NotEmpty(t, result.Hash, "hash must be recorded")

	// No file content must land in git.
	_, ok := gitShowAt(filepath.Join(baseDir, "repo.git"), "track/etc/shadow", "etc/shadow")
	assert.False(t, ok, "hash-only must never commit content to git, even for denylist paths")
}

// TestTrack_LocalOnly_DeniedPathBlocked confirms that --local does NOT bypass
// the denylist — content is still committed into a local git branch.
func TestTrack_LocalOnly_DeniedPathBlocked(t *testing.T) {
	tmp := t.TempDir()

	opts := core.TrackOptions{
		SystemPath: "/etc/shadow",
		RepoDir:    filepath.Join(tmp, "repo"),
		StateDir:   filepath.Join(tmp, "state"),
		LocalOnly:  true,
	}
	_, err := core.Track(opts)
	require.Error(t, err, "--local must not bypass the denylist (content still goes into git)")
	assert.Contains(t, err.Error(), "denylist")
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

func TestTrack_RelativeDotPath(t *testing.T) {
	tmp := t.TempDir()
	repoDir := initTestBareRepo(t, filepath.Join(tmp, "repo.git"))
	stateDir := filepath.Join(tmp, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	sysRoot := filepath.Join(tmp, "sysroot")
	sysDir := filepath.Join(sysRoot, "etc")
	require.NoError(t, os.MkdirAll(sysDir, 0o755))
	sysFile := filepath.Join(sysDir, "hosts")
	require.NoError(t, os.WriteFile(sysFile, []byte("127.0.0.1 localhost\n"), 0o644))

	// Change working directory to the file's directory and use "." as the
	// dir argument via TrackDir so that relative paths are resolved.
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(sysDir))

	summary, err := core.TrackDir(core.TrackDirOptions{
		DirPath:  ".",
		RepoDir:  repoDir,
		StateDir: stateDir,
		SysRoot:  sysRoot,
	})
	require.NoError(t, err)
	require.Equal(t, 1, summary.Tracked)

	// The stored system_path must be absolute, not ".".
	s := readState(t, stateDir)
	for _, rec := range s.Files {
		assert.True(t, filepath.IsAbs(rec.SystemPath),
			"system_path %q must be absolute", rec.SystemPath)
	}
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

	// With SysRoot stripped, the logical path is /etc/nginx/nginx.conf.
	// The derived ID must be the 8-char SHA-256 hash of that path.
	expectedID := core.DeriveID("/etc/nginx/nginx.conf")
	assert.Equal(t, expectedID, result.ID)

	// The ID must also appear in state.json.
	s := readState(t, stateDir)
	_, ok := s.Files[expectedID]
	assert.True(t, ok, "state.json must contain a record keyed by the derived ID")
}


// ---------------------------------------------------------------------------
// Untrack
// ---------------------------------------------------------------------------

// buildUntrackFixture writes a state.json containing one tracked file.
func buildUntrackFixture(t *testing.T, systemPath string) (string, string) {
	t.Helper()
	baseDir := t.TempDir()
	id := core.DeriveID(systemPath)
	s := map[string]interface{}{
		"version": 1,
		"files": map[string]interface{}{
			id: map[string]interface{}{
				"id":           id,
				"system_path":  systemPath,
				"repo_path":    "etc/myapp.conf",
				"current_hash": "aabbccdd",
				"status":       "tracked",
			},
		},
		"backups": map[string]interface{}{},
	}
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600))
	return baseDir, id
}

func TestUntrack_BySystemPath(t *testing.T) {
	sysPath := "/etc/myapp.conf"
	baseDir, id := buildUntrackFixture(t, sysPath)

	removed, err := core.Untrack(core.UntrackOptions{BaseDir: baseDir, Arg: sysPath})
	require.NoError(t, err)
	require.Equal(t, []string{id}, removed)

	s := readState(t, baseDir)
	_, ok := s.Files[id]
	assert.False(t, ok, "record must be removed from state.json")
}

func TestUntrack_ByID(t *testing.T) {
	baseDir, id := buildUntrackFixture(t, "/etc/myapp.conf")

	removed, err := core.Untrack(core.UntrackOptions{BaseDir: baseDir, Arg: id})
	require.NoError(t, err)
	assert.Equal(t, []string{id}, removed)
}

func TestUntrack_NotTracked(t *testing.T) {
	baseDir, _ := buildUntrackFixture(t, "/etc/myapp.conf")

	_, err := core.Untrack(core.UntrackOptions{BaseDir: baseDir, Arg: "/etc/nonexistent.conf"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not tracked")
}

func TestUntrack_NewPathInGroupDir(t *testing.T) {
	baseDir := t.TempDir()
	groupDir := "/etc/myapp"
	id := core.DeriveID(groupDir + "/app.conf")
	s := map[string]interface{}{
		"version": 1,
		"files": map[string]interface{}{
			id: map[string]interface{}{
				"id":          id,
				"system_path": groupDir + "/app.conf",
				"repo_path":   "etc/myapp/app.conf",
				"current_hash": "aabbccdd",
				"status":      "tracked",
				"group":       groupDir,
			},
		},
		"backups":  map[string]interface{}{},
		"excludes": []string{},
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600)

	newPath := groupDir + "/new-file.conf"
	removed, err := core.Untrack(core.UntrackOptions{BaseDir: baseDir, Arg: newPath})
	require.NoError(t, err)
	assert.Equal(t, []string{newPath}, removed)

	st := readState(t, baseDir)
	assert.Contains(t, st.Excludes, newPath, "new path must be in excludes")
}

func TestUntrack_AlreadyExcluded(t *testing.T) {
	baseDir := t.TempDir()
	groupDir := "/etc/myapp"
	id := core.DeriveID(groupDir + "/app.conf")
	newPath := groupDir + "/new-file.conf"
	s := map[string]interface{}{
		"version": 1,
		"files": map[string]interface{}{
			id: map[string]interface{}{
				"id": id, "system_path": groupDir + "/app.conf",
				"repo_path": "etc/myapp/app.conf", "current_hash": "aabbccdd", "status": "tracked",
				"group": groupDir,
			},
		},
		"backups":  map[string]interface{}{},
		"excludes": []string{newPath},
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600)

	_, err := core.Untrack(core.UntrackOptions{BaseDir: baseDir, Arg: newPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already excluded")
}

func TestUntrack_GroupRemovesAll(t *testing.T) {
	baseDir := t.TempDir()
	groupDir := "/etc/myapp"
	id1 := core.DeriveID(groupDir + "/a.conf")
	id2 := core.DeriveID(groupDir + "/b.conf")
	s := map[string]interface{}{
		"version": 1,
		"files": map[string]interface{}{
			id1: map[string]interface{}{
				"id": id1, "system_path": groupDir + "/a.conf",
				"repo_path": "etc/myapp/a.conf", "current_hash": "aabb", "status": "tracked",
				"group": groupDir,
			},
			id2: map[string]interface{}{
				"id": id2, "system_path": groupDir + "/b.conf",
				"repo_path": "etc/myapp/b.conf", "current_hash": "ccdd", "status": "tracked",
				"group": groupDir,
			},
		},
		"backups": map[string]interface{}{},
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600)

	removed, err := core.Untrack(core.UntrackOptions{BaseDir: baseDir, Arg: groupDir})
	require.NoError(t, err)
	assert.Len(t, removed, 2)

	st := readState(t, baseDir)
	assert.Empty(t, st.Files)
}
