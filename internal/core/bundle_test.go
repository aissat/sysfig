package core_test

// bundle_test.go — tests for the bundle-based remote transport.
//
// Coverage:
//   - ParseRemoteKind (URL scheme detection)
//   - BundleLocalPath (URL path extraction)
//   - BundlePush + BundlePull round-trip via bundle+local://
//   - BundlePull detects AlreadyUpToDate correctly
//   - BundlePull with corrupt bundle returns an error
//   - Full integration: Track → Push (bundle) → Pull (bundle) → Sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
)

// ── ParseRemoteKind ───────────────────────────────────────────────────────────

func TestParseRemoteKind_Git(t *testing.T) {
	cases := []string{
		"git@github.com:you/conf.git",
		"https://github.com/you/conf.git",
		"",
		"/some/local/path.git",
	}
	for _, u := range cases {
		assert.Equal(t, core.RemoteGit, core.ParseRemoteKind(u), "URL: %q", u)
	}
}

func TestParseRemoteKind_BundleLocal(t *testing.T) {
	cases := []string{
		"bundle+local:///mnt/share/host.bundle",
		"bundle+local:///tmp/sysfig/host.bundle",
	}
	for _, u := range cases {
		assert.Equal(t, core.RemoteBundleLocal, core.ParseRemoteKind(u), "URL: %q", u)
	}
}

func TestParseRemoteKind_BundleSSH(t *testing.T) {
	cases := []string{
		"bundle+ssh://user@host/srv/host.bundle",
		"bundle+ssh://backup@192.168.1.10/var/sysfig/host.bundle",
	}
	for _, u := range cases {
		assert.Equal(t, core.RemoteBundleSSH, core.ParseRemoteKind(u), "URL: %q", u)
	}
}

// ── BundleLocalPath ───────────────────────────────────────────────────────────

func TestBundleLocalPath_OK(t *testing.T) {
	p, err := core.BundleLocalPath("bundle+local:///mnt/nfs/sysfig/host.bundle")
	require.NoError(t, err)
	assert.Equal(t, "/mnt/nfs/sysfig/host.bundle", p)
}

func TestBundleLocalPath_Empty(t *testing.T) {
	_, err := core.BundleLocalPath("bundle+local://")
	assert.Error(t, err)
}

// ── BundlePush / BundlePull round-trip ────────────────────────────────────────

// TestBundleLocalRoundTrip is the primary integration test:
//   1. Track a file and sync (commit to track branch) in baseDirA.
//   2. Push to a bundle+local:// URL (a path in TempDir).
//   3. Create a second base dir (baseDirB — the "remote machine").
//   4. Pull the bundle into baseDirB.
//   5. Verify the track branch exists in baseDirB's repo.
func TestBundleLocalRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	baseDirA := initBareLocalRepo(t)
	baseDirB := initBareLocalRepo(t)
	repoA := filepath.Join(baseDirA, "repo.git")
	repoB := filepath.Join(baseDirB, "repo.git")
	bundlePath := filepath.Join(tmp, "share", "test.bundle")
	remoteURL := "bundle+local://" + bundlePath

	// Track a file and commit it in baseDirA.
	sysFile := filepath.Join(tmp, "app.conf")
	require.NoError(t, os.WriteFile(sysFile, []byte("key=val\n"), 0o644))
	_, err := core.Track(core.TrackOptions{
		SystemPath: sysFile,
		RepoDir:    repoA,
		StateDir:   baseDirA,
	})
	require.NoError(t, err)
	_, err = core.Sync(core.SyncOptions{BaseDir: baseDirA, Message: "test commit"})
	require.NoError(t, err)

	// Push repoA → bundle file.
	err = core.BundlePush(core.BundlePushOptions{
		RepoDir:   repoA,
		RemoteURL: remoteURL,
	})
	require.NoError(t, err)

	// Bundle file must exist and be non-empty.
	fi, err := os.Stat(bundlePath)
	require.NoError(t, err)
	assert.Greater(t, fi.Size(), int64(0))

	// Pull bundle → repoB.
	result, err := core.BundlePull(core.BundlePullOptions{
		RepoDir:   repoB,
		RemoteURL: remoteURL,
	})
	require.NoError(t, err)
	assert.False(t, result.AlreadyUpToDate, "first pull should import new refs")

	// The track branch committed in repoA must now exist in repoB.
	repoRel := strings.TrimPrefix(sysFile, "/")
	branchName := core.SanitizeBranchName(repoRel)
	assert.True(t, gitBranchExists(t, repoB, "track/"+branchName),
		"track branch must be present in repoB after pull")
}

// TestBundleLocalAlreadyUpToDate verifies that a second pull with no new
// commits is reported as AlreadyUpToDate.
func TestBundleLocalAlreadyUpToDate(t *testing.T) {
	tmp := t.TempDir()
	baseDirA := initBareLocalRepo(t)
	baseDirB := initBareLocalRepo(t)
	repoA := filepath.Join(baseDirA, "repo.git")
	repoB := filepath.Join(baseDirB, "repo.git")
	bundlePath := filepath.Join(tmp, "test.bundle")
	remoteURL := "bundle+local://" + bundlePath

	// Track + commit in baseDirA.
	sysFile := filepath.Join(tmp, "cfg.conf")
	require.NoError(t, os.WriteFile(sysFile, []byte("x=1\n"), 0o644))
	_, err := core.Track(core.TrackOptions{SystemPath: sysFile, RepoDir: repoA, StateDir: baseDirA})
	require.NoError(t, err)
	_, err = core.Sync(core.SyncOptions{BaseDir: baseDirA, Message: "test"})
	require.NoError(t, err)

	// First push + pull.
	require.NoError(t, core.BundlePush(core.BundlePushOptions{RepoDir: repoA, RemoteURL: remoteURL}))
	r1, err := core.BundlePull(core.BundlePullOptions{RepoDir: repoB, RemoteURL: remoteURL})
	require.NoError(t, err)
	assert.False(t, r1.AlreadyUpToDate, "first pull must import new refs")

	// Second pull — same bundle, nothing new.
	r2, err := core.BundlePull(core.BundlePullOptions{RepoDir: repoB, RemoteURL: remoteURL})
	require.NoError(t, err)
	assert.True(t, r2.AlreadyUpToDate, "second pull from unchanged bundle must be AlreadyUpToDate")
}

// TestBundleCorruptReturnsError verifies that a truncated bundle file is
// rejected by BundlePull before it can corrupt the local repo.
func TestBundleCorruptReturnsError(t *testing.T) {
	tmp := t.TempDir()
	baseDirB := initBareLocalRepo(t)
	repoB := filepath.Join(baseDirB, "repo.git")
	bundlePath := filepath.Join(tmp, "corrupt.bundle")

	// Write garbage — not a valid git bundle.
	require.NoError(t, os.WriteFile(bundlePath, []byte("not a bundle\n"), 0o644))
	remoteURL := "bundle+local://" + bundlePath

	_, err := core.BundlePull(core.BundlePullOptions{
		RepoDir:   repoB,
		RemoteURL: remoteURL,
	})
	assert.Error(t, err, "corrupt bundle must return an error")
}

// TestBundlePushMissingDir verifies that BundlePush creates parent directories
// for the bundle path automatically (NFS share subdirectory that does not exist
// yet on the first push).
func TestBundlePushMissingDir(t *testing.T) {
	tmp := t.TempDir()
	baseDirA := initBareLocalRepo(t)
	repoA := filepath.Join(baseDirA, "repo.git")

	// Track + commit one file so the repo is non-empty.
	sysFile := filepath.Join(tmp, "a.conf")
	require.NoError(t, os.WriteFile(sysFile, []byte("a=1\n"), 0o644))
	_, err := core.Track(core.TrackOptions{SystemPath: sysFile, RepoDir: repoA, StateDir: baseDirA})
	require.NoError(t, err)
	_, err = core.Sync(core.SyncOptions{BaseDir: baseDirA, Message: "test"})
	require.NoError(t, err)

	// Bundle target in a subdirectory that does not exist yet.
	bundlePath := filepath.Join(tmp, "deep", "nested", "dir", "host.bundle")
	remoteURL := "bundle+local://" + bundlePath

	err = core.BundlePush(core.BundlePushOptions{RepoDir: repoA, RemoteURL: remoteURL})
	require.NoError(t, err)

	_, err = os.Stat(bundlePath)
	assert.NoError(t, err, "bundle file must be created including parent dirs")
}

// TestBundleIntegration_SyncPushPull is an end-to-end test that exercises the
// full sysfig workflow with a bundle+local remote:
//
//   Machine A: Track a file → Sync (commit) → Push bundle to NFS share
//   Machine B: Pull bundle from NFS share → track branch visible in repo
func TestBundleIntegration_SyncPushPull(t *testing.T) {
	tmp := t.TempDir()
	shareDir := filepath.Join(tmp, "nfs-share")
	require.NoError(t, os.MkdirAll(shareDir, 0o755))
	bundlePath := filepath.Join(shareDir, "machineA.bundle")
	remoteURL := "bundle+local://" + bundlePath

	// ── Machine A setup ────────────────────────────────────────────────────
	baseDirA := initBareLocalRepo(t)
	repoA := filepath.Join(baseDirA, "repo.git")
	gitConfigSet(t, repoA, "remote.origin.url", remoteURL)

	// Track + commit a config file.
	sysFileA := filepath.Join(tmp, "app.conf")
	require.NoError(t, os.WriteFile(sysFileA, []byte("env=prod\n"), 0o644))
	_, err := core.Track(core.TrackOptions{
		SystemPath: sysFileA,
		RepoDir:    repoA,
		StateDir:   baseDirA,
	})
	require.NoError(t, err)
	syncResult, err := core.Sync(core.SyncOptions{BaseDir: baseDirA, Message: "track app.conf"})
	require.NoError(t, err)
	require.True(t, syncResult.Committed, "Sync must create a commit for the newly tracked file")

	// Push — bundle transport detected from remote URL in git config.
	err = core.Push(core.PushOptions{BaseDir: baseDirA})
	require.NoError(t, err)

	// Bundle file must exist.
	_, err = os.Stat(bundlePath)
	require.NoError(t, err, "bundle file must exist after push")

	// ── Machine B setup ────────────────────────────────────────────────────
	baseDirB := initBareLocalRepo(t)
	repoB := filepath.Join(baseDirB, "repo.git")
	gitConfigSet(t, repoB, "remote.origin.url", remoteURL)

	// Pull — imports track branches from bundle.
	pr, err := core.Pull(core.PullOptions{BaseDir: baseDirB})
	require.NoError(t, err)
	assert.False(t, pr.AlreadyUpToDate)

	// The track branch must now be in machineB's repo.
	repoRel := strings.TrimPrefix(sysFileA, "/")
	branchName := core.SanitizeBranchName(repoRel)
	// Also verify the branch exists in repoA before asserting repoB.
	require.True(t, gitBranchExists(t, repoA, "track/"+branchName),
		"track branch must exist in repoA before push")
	assert.True(t, gitBranchExists(t, repoB, "track/"+branchName),
		"track branch must be present in machineB after pull")
}

// gitConfigSet is a test helper that writes a git config key/value into a bare repo.
func gitConfigSet(t *testing.T, repoDir, key, value string) {
	t.Helper()
	runGitSyncEnv(t, append(os.Environ(), "GIT_DIR="+repoDir), "config", key, value)
}
