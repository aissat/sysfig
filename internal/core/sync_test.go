package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aissat/sysfig/internal/core"
)

// requireGitSync skips the test if the git binary is not available on PATH.
func requireGitSync(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// runGitSync executes a git command in dir and fails the test on non-zero exit.
func runGitSync(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q failed: %v\n%s", args, dir, err, out)
	}
}

// runGitSyncEnv executes a git command with extra environment variables set
// (in addition to os.Environ()). Used for bare-repo operations where
// GIT_DIR must be passed instead of cmd.Dir.
func runGitSyncEnv(t *testing.T, extraEnv []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (env=%v) failed: %v\n%s", args, extraEnv, err, out)
	}
}

// initBareLocalRepo creates a minimal bare git repo at baseDir/repo.git
// suitable for Sync tests. It seeds an initial commit (via a temporary clone)
// so that HEAD resolves to a valid ref, then syncs the bare repo's index
// to match HEAD (required so that subsequent `git commit` calls see a clean
// baseline and do not spuriously commit the seed files as "deleted").
//
// Returns baseDir.
func initBareLocalRepo(t *testing.T) string {
	t.Helper()
	baseDir := t.TempDir()
	repoDir := filepath.Join(baseDir, "repo.git")

	// Initialise the bare repo.
	cmd := exec.Command("git", "init", "--bare", repoDir)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init --bare %q: %v\n%s", repoDir, err, out)
	}

	// Clone into a temp work dir so we can create the first commit.
	workRoot := t.TempDir()
	workDir := filepath.Join(workRoot, "work")
	runGitSync(t, workRoot, "clone", repoDir, workDir)
	runGitSync(t, workDir, "config", "user.email", "test@sysfig.local")
	runGitSync(t, workDir, "config", "user.name", "sysfig-test")

	// Seed an initial commit so HEAD exists.
	keepFile := filepath.Join(workDir, ".gitkeep")
	if err := os.WriteFile(keepFile, []byte(""), 0o644); err != nil {
		t.Fatalf("write .gitkeep: %v", err)
	}
	runGitSync(t, workDir, "add", ".gitkeep")
	runGitSync(t, workDir, "commit", "-m", "chore: initial commit")
	runGitSync(t, workDir, "push", "origin")

	// Advance the bare repo HEAD to the pushed commit.
	advanceBareHead(t, repoDir)

	// Sync the bare repo index to HEAD so that subsequent commits see a
	// clean baseline. Without this, the index is empty while HEAD contains
	// the .gitkeep blob, causing `git commit` to record a spurious deletion.
	syncBareIndex(t, repoDir)

	// Set user identity so git commit works without a global config.
	gitEnv := []string{"GIT_DIR=" + repoDir}
	runGitSyncEnv(t, append(os.Environ(), gitEnv...), "config", "user.email", "test@sysfig.local")
	runGitSyncEnv(t, append(os.Environ(), gitEnv...), "config", "user.name", "sysfig-test")

	return baseDir
}

// syncBareIndex reads the HEAD tree into the bare repo's index so that the
// index matches HEAD. Without this, the index is empty after `git init --bare`
// + push-from-clone, causing subsequent `git commit` calls to record the
// seed files as deleted rather than seeing a clean slate.
func syncBareIndex(t *testing.T, repoDir string) {
	t.Helper()
	cmd := exec.Command("git", "read-tree", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git read-tree HEAD in %q: %v\n%s", repoDir, err, out)
	}
}

// advanceBareHead fast-forwards the current branch in the bare repo to match
// its remote tracking branch (FETCH_HEAD after a fetch).
func advanceBareHead(t *testing.T, repoDir string) {
	t.Helper()
	gitEnv := []string{"GIT_DIR=" + repoDir}

	fetchCmd := exec.Command("git", "fetch", "--all")
	fetchCmd.Env = append(os.Environ(), gitEnv...)
	fetchCmd.Run() //nolint:errcheck // best-effort

	resetCmd := exec.Command("git", "reset", "--soft", "FETCH_HEAD")
	resetCmd.Env = append(os.Environ(), gitEnv...)
	resetCmd.Run() //nolint:errcheck // best-effort
}

// makeLocalOriginPair creates:
//   - origin.git  — bare repo with one commit
//   - baseDir/repo.git — bare repo cloned from origin (via an intermediate
//     working clone), with its HEAD initialised to the seeded commit.
//
// Returns (baseDir, originDir).
func makeLocalOriginPair(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	originDir := filepath.Join(root, "origin.git")

	// Bare origin.
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitSyncEnv(t, os.Environ(), "init", "--bare", originDir)

	// Seed a commit into origin via a temporary working clone.
	seedRoot := t.TempDir()
	seedWork := filepath.Join(seedRoot, "work")
	runGitSync(t, seedRoot, "clone", originDir, seedWork)
	runGitSync(t, seedWork, "config", "user.email", "test@sysfig.local")
	runGitSync(t, seedWork, "config", "user.name", "sysfig-test")

	seedFile := filepath.Join(seedWork, "seed.txt")
	if err := os.WriteFile(seedFile, []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSync(t, seedWork, "add", "seed.txt")
	runGitSync(t, seedWork, "commit", "-m", "chore: seed commit")
	runGitSync(t, seedWork, "push", "origin")

	// Build the baseDir/repo.git bare repo that sysfig expects.
	// We do a bare clone of origin so it has the remote configured.
	baseDir := t.TempDir()
	repoDir := filepath.Join(baseDir, "repo.git")

	runGitSyncEnv(t, os.Environ(), "clone", "--bare", originDir, repoDir)

	gitEnv := []string{"GIT_DIR=" + repoDir}

	// Set user identity in the bare repo's config for commits.
	runGitSyncEnv(t, append(os.Environ(), gitEnv...), "config", "user.email", "test@sysfig.local")
	runGitSyncEnv(t, append(os.Environ(), gitEnv...), "config", "user.name", "sysfig-test")

	// Configure branch tracking so `git push` works without explicit refspec.
	// `git clone --bare` does not set branch.<name>.remote / merge, which git
	// requires for `push` with push.default=simple (the modern default).
	branchCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	branchCmd.Env = append(os.Environ(), gitEnv...)
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("git symbolic-ref HEAD in bare clone: %v", err)
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		t.Fatal("could not determine branch name in bare clone")
	}
	runGitSyncEnv(t, append(os.Environ(), gitEnv...), "config", "branch."+branch+".remote", "origin")
	runGitSyncEnv(t, append(os.Environ(), gitEnv...), "config", "branch."+branch+".merge", "refs/heads/"+branch)

	// Sync index to HEAD (same reason as in initBareLocalRepo).
	syncBareIndex(t, repoDir)

	return baseDir, originDir
}

// commitFileToBareRepo writes content into the bare repo at repoDir under
// relPath (git-relative, no leading slash) and creates a new commit.
// Used in tests to simulate files being staged and committed.
func commitFileToBareRepo(t *testing.T, repoDir, relPath string, content []byte) {
	t.Helper()

	// We need a temporary work dir to commit via a clone.
	workRoot := t.TempDir()
	workDir := filepath.Join(workRoot, "work")
	runGitSync(t, workRoot, "clone", repoDir, workDir)
	runGitSync(t, workDir, "config", "user.email", "test@sysfig.local")
	runGitSync(t, workDir, "config", "user.name", "sysfig-test")

	destPath := filepath.Join(workDir, relPath)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	runGitSync(t, workDir, "add", relPath)
	runGitSync(t, workDir, "commit", "-m", "test: add "+relPath)
	runGitSync(t, workDir, "push", "origin")

	// Advance the bare repo HEAD.
	advanceBareHead(t, repoDir)
}

// ── Sync ──────────────────────────────────────────────────────────────────────

// TestSync_NothingToCommit verifies that Sync on a repo with no staged changes
// returns Committed=false and no error.
func TestSync_NothingToCommit(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)

	result, err := core.Sync(core.SyncOptions{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Committed {
		t.Error("expected Committed=false on a repo with nothing to stage")
	}
}

// TestSync_CommitsNewFile verifies that when a file is staged into the bare
// repo (via a blob + index entry) before Sync is called, Sync creates a commit.
//
// Because Sync now stages files from state.json records (not from a walk of the
// working tree), we pre-populate state.json with a record whose system file
// exists under SysRoot, and let Sync stage + commit it.
func TestSync_CommitsNewFile(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)

	// Create a system file under a SysRoot so Sync can read it.
	sysRoot := t.TempDir()
	sysFileDir := filepath.Join(sysRoot, "etc", "nginx")
	if err := os.MkdirAll(sysFileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sysFile := filepath.Join(sysFileDir, "nginx.conf")
	if err := os.WriteFile(sysFile, []byte("worker_processes auto;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a minimal state.json so Sync knows which file to stage.
	writeMinimalState(t, baseDir, "nginx_conf", "/etc/nginx/nginx.conf", "etc/nginx/nginx.conf", "")

	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: add nginx.conf",
		SysRoot: sysRoot,
	})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if !result.Committed {
		t.Error("expected Committed=true after staging a new file")
	}
	if result.Message != "test: add nginx.conf" {
		t.Errorf("unexpected message: %q", result.Message)
	}
}

// TestSync_DefaultMessage verifies that an empty Message is replaced by the
// default "sysfig: sync <timestamp>" format.
func TestSync_DefaultMessage(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)

	// Stage a file via state.json + SysRoot so there is something to commit.
	sysRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sysRoot, "dummy.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMinimalState(t, baseDir, "dummy", "/dummy.txt", "dummy.txt", "")

	result, err := core.Sync(core.SyncOptions{BaseDir: baseDir, SysRoot: sysRoot})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if !strings.HasPrefix(result.Message, "sysfig: update ") {
		t.Errorf("expected default message prefix, got: %q", result.Message)
	}
}

// TestSync_ModifiedFile verifies that syncing after a system file has changed
// creates a new commit.
func TestSync_ModifiedFile(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")

	// Seed the bare repo with the original file.
	commitFileToBareRepo(t, repoDir, "tracked.conf", []byte("original\n"))

	// Update the system file so Sync will stage a new version.
	sysRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sysRoot, "tracked.conf"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMinimalState(t, baseDir, "tracked_conf", "/tracked.conf", "tracked.conf", "")

	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: update tracked.conf",
		SysRoot: sysRoot,
	})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if !result.Committed {
		t.Error("expected Committed=true for a modified tracked file")
	}
}

// TestSync_PushedFalseByDefault verifies that Sync does NOT push unless
// explicitly asked (offline-safe default).
func TestSync_PushedFalseByDefault(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)

	sysRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sysRoot, "offline.txt"), []byte("offline work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMinimalState(t, baseDir, "offline_txt", "/offline.txt", "offline.txt", "")

	result, err := core.Sync(core.SyncOptions{BaseDir: baseDir, SysRoot: sysRoot, Push: false})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if result.Pushed {
		t.Error("expected Pushed=false when Push option is false")
	}
}

// TestSync_EmptyBaseDir verifies that Sync returns a clear error when BaseDir
// is empty, before any git operations are attempted.
func TestSync_EmptyBaseDir(t *testing.T) {
	_, err := core.Sync(core.SyncOptions{BaseDir: ""})
	if err == nil {
		t.Fatal("expected error for empty BaseDir, got nil")
	}
}

// ── Push ──────────────────────────────────────────────────────────────────────

// TestPush_LocalOrigin verifies that Push succeeds when the bare repo has a
// reachable local remote with pending commits.
func TestPush_LocalOrigin(t *testing.T) {
	requireGitSync(t)
	baseDir, _ := makeLocalOriginPair(t)
	repoDir := filepath.Join(baseDir, "repo.git")

	// Add a new commit to the bare repo so there is something to push.
	commitFileToBareRepo(t, repoDir, "push-test.txt", []byte("push test\n"))

	// Now push the new commit to origin via sysfig Push.
	if err := core.Push(core.PushOptions{BaseDir: baseDir}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
}

// TestPush_EmptyBaseDir verifies that Push returns a clear error for an empty
// BaseDir.
func TestPush_EmptyBaseDir(t *testing.T) {
	err := core.Push(core.PushOptions{BaseDir: ""})
	if err == nil {
		t.Fatal("expected error for empty BaseDir, got nil")
	}
}

// TestPush_NoRemote verifies that Push fails with a meaningful error when
// the bare repo has no remote configured (simulates offline / no-remote).
func TestPush_NoRemote(t *testing.T) {
	requireGitSync(t)
	// initBareLocalRepo creates a standalone bare repo with no remote.
	baseDir := initBareLocalRepo(t)

	err := core.Push(core.PushOptions{BaseDir: baseDir})
	if err == nil {
		t.Fatal("expected error pushing with no remote, got nil")
	}
	if !strings.Contains(err.Error(), "push") {
		t.Errorf("error should mention 'push', got: %v", err)
	}
}

// ── Pull ──────────────────────────────────────────────────────────────────────

// TestPull_AlreadyUpToDate verifies that pulling when there is nothing new
// sets AlreadyUpToDate=true and returns no error.
func TestPull_AlreadyUpToDate(t *testing.T) {
	requireGitSync(t)
	baseDir, _ := makeLocalOriginPair(t)

	result, err := core.Pull(core.PullOptions{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("Pull failed: %v", err)
	}
	if !result.AlreadyUpToDate {
		t.Error("expected AlreadyUpToDate=true when nothing to pull")
	}
}

// TestPull_FetchesNewCommit verifies that Pull retrieves a new commit that
// was pushed to origin after the clone was made.
func TestPull_FetchesNewCommit(t *testing.T) {
	requireGitSync(t)
	baseDir, originDir := makeLocalOriginPair(t)

	// Push a new commit to origin from a separate working clone.
	root := t.TempDir()
	secondWork := filepath.Join(root, "work2")
	runGitSync(t, root, "clone", originDir, secondWork)
	runGitSync(t, secondWork, "config", "user.email", "test@sysfig.local")
	runGitSync(t, secondWork, "config", "user.name", "sysfig-test")

	newFile := filepath.Join(secondWork, "new-from-remote.txt")
	if err := os.WriteFile(newFile, []byte("from remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSync(t, secondWork, "add", "new-from-remote.txt")
	runGitSync(t, secondWork, "commit", "-m", "test: new remote commit")
	runGitSync(t, secondWork, "push", "origin")

	// Now pull in our baseDir/repo.git.
	result, err := core.Pull(core.PullOptions{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("Pull failed: %v", err)
	}
	if result.AlreadyUpToDate {
		t.Error("expected AlreadyUpToDate=false after a new commit was pushed to origin")
	}

	// Verify the new file is accessible via git-show in the bare repo.
	repoDir := filepath.Join(baseDir, "repo.git")
	showCmd := exec.Command("git", "--git-dir="+repoDir, "show", "HEAD:new-from-remote.txt")
	showCmd.Env = os.Environ()
	out, err := showCmd.CombinedOutput()
	if err != nil {
		t.Errorf("expected new-from-remote.txt to be accessible in bare repo after pull: %v\n%s", err, out)
	}
	if string(out) != "from remote\n" {
		t.Errorf("unexpected content in pulled file: %q", string(out))
	}
}

// TestPull_EmptyBaseDir verifies that Pull returns a clear error for an empty
// BaseDir.
func TestPull_EmptyBaseDir(t *testing.T) {
	_, err := core.Pull(core.PullOptions{BaseDir: ""})
	if err == nil {
		t.Fatal("expected error for empty BaseDir, got nil")
	}
}

// TestPull_NoRemote verifies that Pull fails with a meaningful error when
// the repo has no remote configured.
func TestPull_NoRemote(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)

	_, err := core.Pull(core.PullOptions{BaseDir: baseDir})
	if err == nil {
		t.Fatal("expected error pulling with no remote, got nil")
	}
}

// ── Offline simulation ────────────────────────────────────────────────────────

// TestSync_WorksOffline verifies the core offline guarantee: track + sync
// (commit) must succeed even when there is no remote and no network.
// This is the fundamental contract — local work is never blocked by connectivity.
func TestSync_WorksOffline(t *testing.T) {
	requireGitSync(t)
	// initBareLocalRepo has NO remote — simulates a fully offline machine.
	baseDir := initBareLocalRepo(t)

	// Create a system file to track, under a SysRoot sandbox.
	sysRoot := t.TempDir()
	confDir := filepath.Join(sysRoot, "etc", "app")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	confFile := filepath.Join(confDir, "config.conf")
	if err := os.WriteFile(confFile, []byte("APP_ENV=dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wire up state.json so Sync knows which file to stage.
	writeMinimalState(t, baseDir, "app_config", "/etc/app/config.conf", "etc/app/config.conf", "")

	// Sync (local commit) must succeed with no remote.
	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "offline: track config.conf",
		Push:    false, // never push — offline
		SysRoot: sysRoot,
	})
	if err != nil {
		t.Fatalf("Sync failed in offline mode: %v", err)
	}
	if !result.Committed {
		t.Error("expected a commit to be created in offline mode")
	}
	if result.Pushed {
		t.Error("Push should be false in offline mode")
	}

	// The commit must be accessible in the bare repo via git-show.
	repoDir := filepath.Join(baseDir, "repo.git")
	showCmd := exec.Command("git", "--git-dir="+repoDir, "show", "HEAD:etc/app/config.conf")
	showCmd.Env = os.Environ()
	out, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show HEAD:etc/app/config.conf failed: %v\n%s", err, out)
	}
	if string(out) != "APP_ENV=dev\n" {
		t.Errorf("unexpected committed content: %q", string(out))
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// writeMinimalState writes a state.json containing a single FileRecord into
// baseDir. relPath is the git-relative path (e.g. "etc/nginx/nginx.conf").
// recordedHash may be empty (will be stored as-is).
func writeMinimalState(t *testing.T, baseDir, id, systemPath, relPath, recordedHash string) {
	t.Helper()

	type fileMeta struct {
		UID   int    `json:"uid"`
		GID   int    `json:"gid"`
		Mode  uint32 `json:"mode"`
	}
	type fileRecord struct {
		ID          string    `json:"id"`
		SystemPath  string    `json:"system_path"`
		RepoPath    string    `json:"repo_path"`
		CurrentHash string    `json:"current_hash"`
		Status      string    `json:"status"`
		Encrypt     bool      `json:"encrypt,omitempty"`
		Meta        *fileMeta `json:"meta,omitempty"`
	}
	type state struct {
		Version int                       `json:"version"`
		Files   map[string]*fileRecord    `json:"files"`
		Backups map[string][]interface{}  `json:"backups"`
	}

	s := &state{
		Version: 1,
		Files: map[string]*fileRecord{
			id: {
				ID:          id,
				SystemPath:  systemPath,
				RepoPath:    relPath,
				CurrentHash: recordedHash,
				Status:      "tracked",
				Meta:        &fileMeta{Mode: 0o644},
			},
		},
		Backups: map[string][]interface{}{},
	}

	data, err := jsonMarshalIndent(s)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
}

// jsonMarshalIndent marshals v to indented JSON.
func jsonMarshalIndent(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
