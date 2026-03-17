package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitShowInTest reads a file from the bare repo at HEAD using the git CLI.
// It is a test-local helper that avoids importing the core package internals.
func gitShowInTest(t *testing.T, repoDir, relPath string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git",
		"--git-dir="+repoDir,
		"show", "HEAD:"+relPath,
	)
	cmd.Env = os.Environ()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("git show HEAD:%s in %q: %v\n%s", relPath, repoDir, err, stderr.String())
	}
	return stdout.Bytes()
}

// gitCommitCountInTest returns the number of commits reachable from HEAD in
// the bare repo.  Used to verify that a second Init call does not create a
// spurious extra commit.
func gitCommitCountInTest(t *testing.T, repoDir string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git",
		"--git-dir="+repoDir,
		"rev-list", "--count", "HEAD",
	)
	cmd.Env = os.Environ()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("git rev-list --count HEAD in %q: %v\n%s", repoDir, err, stderr.String())
	}

	count := 0
	n, err := bytes.NewReader(bytes.TrimSpace(stdout.Bytes())).Read(make([]byte, 0))
	_ = n
	_ = err

	// Parse the integer from stdout.
	s := strings.TrimSpace(stdout.String())
	if _, err := countFromString(s, &count); err != nil {
		t.Fatalf("parse commit count %q: %v", s, err)
	}
	return count
}

// countFromString parses a decimal integer from s into *out.
func countFromString(s string, out *int) (int, error) {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
	}
	*out = n
	return n, nil
}

// TestInit_Fresh verifies that Init on a brand-new directory creates every
// expected artifact and returns AlreadyExisted=false.
func TestInit_Fresh(t *testing.T) {
	base := filepath.Join(t.TempDir(), "sysfig")

	result, err := Init(InitOptions{BaseDir: base})
	if err != nil {
		t.Fatalf("Init() unexpected error: %v", err)
	}

	// AlreadyExisted must be false for a fresh directory.
	if result.AlreadyExisted {
		t.Error("AlreadyExisted = true, want false for a fresh base dir")
	}

	// BaseDir must match what was given.
	if result.BaseDir != base {
		t.Errorf("BaseDir = %q, want %q", result.BaseDir, base)
	}

	// ── Directory existence checks ─────────────────────────────────────────

	// RepoDir must be <BaseDir>/repo.git (bare repo).
	wantRepo := filepath.Join(base, "repo.git")
	if result.RepoDir != wantRepo {
		t.Errorf("RepoDir = %q, want %q", result.RepoDir, wantRepo)
	}

	// BackupsDir must be <BaseDir>/backups.
	wantBackups := filepath.Join(base, "backups")
	if result.BackupsDir != wantBackups {
		t.Errorf("BackupsDir = %q, want %q", result.BackupsDir, wantBackups)
	}

	// StateFile must be <BaseDir>/state.json.
	wantState := filepath.Join(base, "state.json")
	if result.StateFile != wantState {
		t.Errorf("StateFile = %q, want %q", result.StateFile, wantState)
	}

	// Every physical directory must exist with mode 0700.
	for _, dir := range []string{result.BaseDir, result.RepoDir, result.BackupsDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("directory %q: stat error: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q exists but is not a directory", dir)
			continue
		}
		perm := info.Mode().Perm()
		if perm != 0o700 {
			t.Errorf("directory %q has permissions %04o, want 0700", dir, perm)
		}
	}

	// ── Bare repo sanity check ─────────────────────────────────────────────
	// A bare repo must have a HEAD file directly inside RepoDir (no .git subdir).
	headPath := filepath.Join(result.RepoDir, "HEAD")
	if _, err := os.Stat(headPath); err != nil {
		t.Errorf("bare repo HEAD not found at %q: %v", headPath, err)
	}

	// ── Files committed into the bare repo ────────────────────────────────
	// sysfig.yaml must be readable from HEAD in the bare repo.
	manifestData := gitShowInTest(t, result.RepoDir, "sysfig.yaml")
	if len(manifestData) == 0 {
		t.Error("sysfig.yaml committed into bare repo is empty")
	}
	if !strings.Contains(string(manifestData), "tracked_files") {
		t.Errorf("sysfig.yaml does not look like a manifest:\n%s", manifestData)
	}

	// hooks.yaml.example must be readable from HEAD in the bare repo.
	hooksData := gitShowInTest(t, result.RepoDir, "hooks.yaml.example")
	if len(hooksData) == 0 {
		t.Error("hooks.yaml.example committed into bare repo is empty")
	}

	// ── ManifestFile / HooksExample are logical display paths ─────────────
	// They must reference the bare repo dir and the correct object paths.
	if !strings.Contains(result.ManifestFile, result.RepoDir) {
		t.Errorf("ManifestFile %q does not reference RepoDir %q", result.ManifestFile, result.RepoDir)
	}
	if !strings.Contains(result.ManifestFile, "sysfig.yaml") {
		t.Errorf("ManifestFile %q does not reference sysfig.yaml", result.ManifestFile)
	}
	if !strings.Contains(result.HooksExample, result.RepoDir) {
		t.Errorf("HooksExample %q does not reference RepoDir %q", result.HooksExample, result.RepoDir)
	}
	if !strings.Contains(result.HooksExample, "hooks.yaml.example") {
		t.Errorf("HooksExample %q does not reference hooks.yaml.example", result.HooksExample)
	}

	// ── state.json ─────────────────────────────────────────────────────────
	stateData, err := os.ReadFile(result.StateFile)
	if err != nil {
		t.Fatalf("state.json read error: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(stateData, &raw); err != nil {
		t.Errorf("state.json is not valid JSON: %v", err)
	}
}

// TestInit_Idempotent verifies that calling Init twice on the same directory
// succeeds without error and that the second call sets AlreadyExisted=true and
// does not create a duplicate git commit.
func TestInit_Idempotent(t *testing.T) {
	base := filepath.Join(t.TempDir(), "sysfig")

	// First call — fresh init.
	first, err := Init(InitOptions{BaseDir: base})
	if err != nil {
		t.Fatalf("first Init() unexpected error: %v", err)
	}
	if first.AlreadyExisted {
		t.Error("first call: AlreadyExisted = true, want false")
	}

	// Record the commit count and the state.json mod time before the second
	// call.  We use these as idempotency signals instead of file mod times
	// (since sysfig.yaml and hooks.yaml.example are git objects, not loose
	// files whose mtime we can observe directly).
	commitsBefore := gitCommitCountInTest(t, first.RepoDir)
	stateModBefore := modTime(t, first.StateFile)

	// Record the raw content of both files as committed in the first call.
	manifestBefore := gitShowInTest(t, first.RepoDir, "sysfig.yaml")
	hooksBefore := gitShowInTest(t, first.RepoDir, "hooks.yaml.example")

	// Second call — must be idempotent.
	second, err := Init(InitOptions{BaseDir: base})
	if err != nil {
		t.Fatalf("second Init() unexpected error: %v", err)
	}
	if !second.AlreadyExisted {
		t.Error("second call: AlreadyExisted = false, want true")
	}

	// The bare repo must NOT have gained an extra commit.
	commitsAfter := gitCommitCountInTest(t, second.RepoDir)
	if commitsAfter != commitsBefore {
		t.Errorf("commit count changed from %d to %d after second Init — idempotency broken",
			commitsBefore, commitsAfter)
	}

	// The file content in the bare repo must be identical.
	manifestAfter := gitShowInTest(t, second.RepoDir, "sysfig.yaml")
	if !bytes.Equal(manifestBefore, manifestAfter) {
		t.Error("sysfig.yaml content changed after second Init call")
	}
	hooksAfter := gitShowInTest(t, second.RepoDir, "hooks.yaml.example")
	if !bytes.Equal(hooksBefore, hooksAfter) {
		t.Error("hooks.yaml.example content changed after second Init call")
	}

	// state.json must not have been rewritten.
	if got := modTime(t, second.StateFile); !got.Equal(stateModBefore) {
		t.Error("state.json was overwritten on second Init call")
	}

	// All directories must still exist and be directories.
	for _, dir := range []string{second.BaseDir, second.RepoDir, second.BackupsDir} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("directory %q missing or not a dir after second Init", dir)
		}
	}
}

// TestInit_Encrypt verifies that passing Encrypt:true causes sysfig.yaml
// (committed into the bare repo) to contain encrypt_by_default: true.
func TestInit_Encrypt(t *testing.T) {
	base := filepath.Join(t.TempDir(), "sysfig")

	result, err := Init(InitOptions{BaseDir: base, Encrypt: true})
	if err != nil {
		t.Fatalf("Init(Encrypt:true) unexpected error: %v", err)
	}

	// Read sysfig.yaml from the bare repo rather than the filesystem.
	data := gitShowInTest(t, result.RepoDir, "sysfig.yaml")
	content := string(data)

	if !strings.Contains(content, "encrypt_by_default: true") {
		t.Errorf("sysfig.yaml does not contain 'encrypt_by_default: true'\ncontent:\n%s", content)
	}

	// encrypt_by_default: false must NOT appear when Encrypt=true.
	if strings.Contains(content, "encrypt_by_default: false") {
		t.Errorf("sysfig.yaml still contains 'encrypt_by_default: false' when Encrypt=true\ncontent:\n%s", content)
	}
}

// TestInit_RepoDirIsbare verifies that the repo is a bare git repo
// (no .git subdirectory; HEAD lives at the root of RepoDir).
func TestInit_RepoDirIsBare(t *testing.T) {
	base := filepath.Join(t.TempDir(), "sysfig")

	result, err := Init(InitOptions{BaseDir: base})
	if err != nil {
		t.Fatalf("Init() unexpected error: %v", err)
	}

	// A bare repo must NOT have a .git subdirectory.
	dotGit := filepath.Join(result.RepoDir, ".git")
	if _, err := os.Stat(dotGit); err == nil {
		t.Errorf("bare repo has unexpected .git subdirectory at %q", dotGit)
	}

	// HEAD must live directly in RepoDir.
	head := filepath.Join(result.RepoDir, "HEAD")
	if _, err := os.Stat(head); err != nil {
		t.Errorf("HEAD not found directly inside RepoDir %q: %v", result.RepoDir, err)
	}

	// objects/ directory must live directly in RepoDir (bare repo layout).
	objects := filepath.Join(result.RepoDir, "objects")
	if info, err := os.Stat(objects); err != nil || !info.IsDir() {
		t.Errorf("objects/ dir not found directly inside RepoDir %q", result.RepoDir)
	}
}

// modTime is a helper that returns the modification time of a file, failing
// the test if the file cannot be stat'd.
func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("modTime: stat %q: %v", path, err)
	}
	return info.ModTime()
}
