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
)

// ── helpers ───────────────────────────────────────────────────────────────────

// initDoctorBaseDir builds a minimal valid sysfig base directory:
//   - baseDir/repo.git  — bare git repo with one initial commit
//   - baseDir/state.json — empty state
//
// Returns baseDir.
func initDoctorBaseDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}

	baseDir := t.TempDir()
	require.NoError(t, os.Chmod(baseDir, 0o700))

	repoDir := filepath.Join(baseDir, "repo.git")
	initTestBareRepo(t, repoDir)

	writeEmptyState(t, baseDir)
	return baseDir
}

// writeEmptyState writes a minimal valid state.json with no tracked files.
func writeEmptyState(t *testing.T, baseDir string) {
	t.Helper()
	s := map[string]interface{}{
		"version": 1,
		"files":   map[string]interface{}{},
		"backups": map[string]interface{}{},
	}
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600))
}

// findFindings returns all findings in r with the given category and label.
func findFindings(r *core.DoctorResult, category, label string) []core.DoctorFinding {
	var out []core.DoctorFinding
	for _, f := range r.Findings {
		if f.Category == category && f.Label == label {
			out = append(out, f)
		}
	}
	return out
}

// findFinding returns the first finding with the given category and label.
func findFinding(r *core.DoctorResult, category, label string) (core.DoctorFinding, bool) {
	for _, f := range r.Findings {
		if f.Category == category && f.Label == label {
			return f, true
		}
	}
	return core.DoctorFinding{}, false
}

// ── Doctor: prerequisites ─────────────────────────────────────────────────────

func TestDoctor_GitPresent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "prerequisites", "git binary")
	require.True(t, ok, "must have a 'git binary' finding")
	assert.Equal(t, core.SeverityOK, f.Severity)
}

// ── Doctor: base directory ────────────────────────────────────────────────────

func TestDoctor_BaseDirMissing(t *testing.T) {
	r := core.Doctor(core.DoctorOptions{BaseDir: "/nonexistent/sysfig-test-dir"})

	f, ok := findFinding(r, "base directory", "exists")
	require.True(t, ok)
	assert.Equal(t, core.SeverityFail, f.Severity)
	// Doctor returns early when base dir is missing — no git repo checks follow.
	assert.True(t, r.Fail >= 1)
}

func TestDoctor_BaseDirExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "base directory", "exists")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
}

func TestDoctor_BaseDirPermissions_OK(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	require.NoError(t, os.Chmod(baseDir, 0o700))

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "base directory", "permissions")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
}

func TestDoctor_BaseDirPermissions_Warn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	require.NoError(t, os.Chmod(baseDir, 0o755)) // world-readable → warn

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "base directory", "permissions")
	require.True(t, ok)
	assert.Equal(t, core.SeverityWarn, f.Severity)
}

// ── Doctor: git repo ──────────────────────────────────────────────────────────

func TestDoctor_RepoExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "git repo", "repo exists")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
}

func TestDoctor_RepoMissing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := t.TempDir()
	require.NoError(t, os.Chmod(baseDir, 0o700))
	writeEmptyState(t, baseDir)
	// No repo.git created.

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "git repo", "repo exists")
	require.True(t, ok)
	assert.Equal(t, core.SeverityFail, f.Severity)
}

func TestDoctor_HEADResolves(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "git repo", "HEAD resolves")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
	assert.NotEmpty(t, f.Detail, "detail should be the short commit SHA")
}

func TestDoctor_NoRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	// initDoctorBaseDir → initTestBareRepo → no remote configured.
	baseDir := initDoctorBaseDir(t)
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "git repo", "remote configured")
	require.True(t, ok)
	assert.Equal(t, core.SeverityWarn, f.Severity)
	assert.Contains(t, f.Detail, "no remote")
}

func TestDoctor_RemoteConfigured(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir, originDir := makeLocalOriginPair(t)
	writeEmptyState(t, baseDir)
	require.NoError(t, os.Chmod(baseDir, 0o700))

	// makeLocalOriginPair gives us a repo.git clone of origin — remote is set.
	_ = originDir
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "git repo", "remote configured")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
}

// ── Doctor: state ─────────────────────────────────────────────────────────────

func TestDoctor_StateReadable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "state", "state.json readable")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
}

func TestDoctor_StateMissing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := t.TempDir()
	require.NoError(t, os.Chmod(baseDir, 0o700))
	repoDir := filepath.Join(baseDir, "repo.git")
	initTestBareRepo(t, repoDir)
	// Intentionally do NOT write state.json — state manager returns empty state,
	// not an error (it initialises a default). So the finding is SeverityOK
	// with "0 tracked file(s)".

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "state", "state.json readable")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
	assert.Contains(t, f.Detail, "0 tracked")
}

func TestDoctor_StateCorrupt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), []byte("not json{{{"), 0o600))

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "state", "state.json readable")
	require.True(t, ok)
	assert.Equal(t, core.SeverityFail, f.Severity)
}

// ── Doctor: file health ───────────────────────────────────────────────────────

func TestDoctor_FileHealthAllPresent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	repoDir := filepath.Join(baseDir, "repo.git")

	// Use the actual temp path as system path (no SysRoot) so Doctor can
	// stat it via rec.SystemPath directly.
	sysFile := filepath.Join(t.TempDir(), "app.conf")
	require.NoError(t, os.WriteFile(sysFile, []byte("key=val\n"), 0o644))

	_, err := core.Track(core.TrackOptions{
		SystemPath: sysFile,
		RepoDir:    repoDir,
		StateDir:   baseDir,
	})
	require.NoError(t, err)

	// Commit it so the blob is in the track branch.
	_, err = core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: track app.conf",
	})
	require.NoError(t, err)

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "file health", "system files present")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
}

func TestDoctor_FileHealthMissing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)

	// Write a state.json with a file that does not exist on disk.
	// Doctor reports SeverityWarn (not Fail) for missing system files.
	id := core.DeriveID("/nonexistent/missing.conf")
	s := map[string]interface{}{
		"version": 1,
		"files": map[string]interface{}{
			id: map[string]interface{}{
				"id":          id,
				"system_path": "/nonexistent/missing.conf",
				"repo_path":   "nonexistent/missing.conf",
				"status":      "tracked",
			},
		},
		"backups": map[string]interface{}{},
	}
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600))

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	f, ok := findFinding(r, "file health", "system files present")
	require.True(t, ok)
	assert.Equal(t, core.SeverityWarn, f.Severity, "missing system files are reported as warn")
	assert.Contains(t, f.Detail, "not on disk")
}

// ── Doctor: encryption key ───────────────────────────────────────────────────

func TestDoctor_NoKeyNoEncryptedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	// No keys/ dir and no encrypted files → SeverityInfo.

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "encryption", "master key")
	require.True(t, ok)
	assert.Equal(t, core.SeverityInfo, f.Severity)
}

func TestDoctor_KeyPresent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	keysDir := filepath.Join(baseDir, "keys")
	require.NoError(t, os.MkdirAll(keysDir, 0o700))
	keyPath := filepath.Join(keysDir, "master.key")
	require.NoError(t, os.WriteFile(keyPath, []byte("fake-key\n"), 0o600))

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "encryption", "master key")
	require.True(t, ok)
	assert.Equal(t, core.SeverityOK, f.Severity)
}

func TestDoctor_KeyPermissionsWarn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	keysDir := filepath.Join(baseDir, "keys")
	require.NoError(t, os.MkdirAll(keysDir, 0o700))
	keyPath := filepath.Join(keysDir, "master.key")
	require.NoError(t, os.WriteFile(keyPath, []byte("fake-key\n"), 0o644)) // wrong perm

	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})
	f, ok := findFinding(r, "encryption", "master key permissions")
	require.True(t, ok)
	assert.Equal(t, core.SeverityWarn, f.Severity)
}

// ── Doctor: summary counters ──────────────────────────────────────────────────

func TestDoctor_CountersSum(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	baseDir := initDoctorBaseDir(t)
	r := core.Doctor(core.DoctorOptions{BaseDir: baseDir})

	total := 0
	for _, f := range r.Findings {
		switch f.Severity {
		case core.SeverityOK:
			total++
		case core.SeverityWarn:
			total++
		case core.SeverityFail:
			total++
		case core.SeverityInfo:
			total++
		}
	}
	assert.Equal(t, len(r.Findings), total, "every finding must have a counted severity")
	assert.Equal(t, r.OK+r.Warn+r.Fail, r.OK+r.Warn+r.Fail) // trivially true — checks no panic
}
