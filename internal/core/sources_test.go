package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
)

// ---------------------------------------------------------------------------
// SourceCacheDir / SourceRepoDir
// ---------------------------------------------------------------------------

func TestSourceCacheDir(t *testing.T) {
	got := core.SourceCacheDir("/home/user/.sysfig", "corporate")
	assert.Equal(t, "/home/user/.sysfig/sources/corporate", got)
}

func TestSourceRepoDir(t *testing.T) {
	got := core.SourceRepoDir("/home/user/.sysfig", "corporate")
	assert.Equal(t, "/home/user/.sysfig/sources/corporate/repo.git", got)
}

// ---------------------------------------------------------------------------
// LoadSourcesConfig
// ---------------------------------------------------------------------------

func TestLoadSourcesConfig_NonExistent(t *testing.T) {
	// A missing sources.yaml should return an empty config without error.
	baseDir := t.TempDir()
	cfg, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.Sources)
	assert.Empty(t, cfg.Profiles)
}

func TestLoadSourcesConfig_ValidYAML(t *testing.T) {
	baseDir := t.TempDir()
	yaml := `
sources:
  - name: corporate
    url: bundle+local:///mnt/usb/bundle.tar.gz
profiles:
  - source: corporate/system-proxy
    variables:
      HTTP_PROXY: http://proxy.corp.example.com:3128
`
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "sources.yaml"), []byte(yaml), 0o600))

	cfg, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)
	require.Len(t, cfg.Sources, 1)
	assert.Equal(t, "corporate", cfg.Sources[0].Name)
	assert.Equal(t, "bundle+local:///mnt/usb/bundle.tar.gz", cfg.Sources[0].URL)
	require.Len(t, cfg.Profiles, 1)
	assert.Equal(t, "corporate/system-proxy", cfg.Profiles[0].Source)
	assert.Equal(t, "http://proxy.corp.example.com:3128", cfg.Profiles[0].Variables["HTTP_PROXY"])
}

func TestLoadSourcesConfig_InvalidYAML(t *testing.T) {
	baseDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "sources.yaml"), []byte(":\tinvalid:\tyaml"), 0o600))

	_, err := core.LoadSourcesConfig(baseDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// ---------------------------------------------------------------------------
// SaveSourcesConfig
// ---------------------------------------------------------------------------

func TestSaveSourcesConfig_WritesYAML(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &core.SourcesConfig{
		Sources: []core.SourceDecl{
			{Name: "myrepo", URL: "https://github.com/example/configs.git"},
		},
	}

	require.NoError(t, core.SaveSourcesConfig(baseDir, cfg))

	// File must exist with correct content.
	data, err := os.ReadFile(filepath.Join(baseDir, "sources.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "myrepo")
	assert.Contains(t, string(data), "https://github.com/example/configs.git")
}

func TestSaveSourcesConfig_RoundTrip(t *testing.T) {
	baseDir := t.TempDir()
	orig := &core.SourcesConfig{
		Sources: []core.SourceDecl{
			{Name: "corp", URL: "bundle+local:///srv/bundle.tar.gz"},
		},
		Profiles: []core.ProfileActivation{
			{Source: "corp/system-proxy", Variables: map[string]string{"PROXY": "http://proxy:3128"}},
		},
	}
	require.NoError(t, core.SaveSourcesConfig(baseDir, orig))

	got, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)

	require.Len(t, got.Sources, 1)
	assert.Equal(t, orig.Sources[0].Name, got.Sources[0].Name)
	assert.Equal(t, orig.Sources[0].URL, got.Sources[0].URL)

	require.Len(t, got.Profiles, 1)
	assert.Equal(t, orig.Profiles[0].Source, got.Profiles[0].Source)
	assert.Equal(t, orig.Profiles[0].Variables["PROXY"], got.Profiles[0].Variables["PROXY"])
}

// ---------------------------------------------------------------------------
// SourceAdd
// ---------------------------------------------------------------------------

func TestSourceAdd_Basic(t *testing.T) {
	baseDir := t.TempDir()

	err := core.SourceAdd(baseDir, "myrepo", "https://github.com/example/configs.git")
	require.NoError(t, err)

	cfg, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)
	require.Len(t, cfg.Sources, 1)
	assert.Equal(t, "myrepo", cfg.Sources[0].Name)
	assert.Equal(t, "https://github.com/example/configs.git", cfg.Sources[0].URL)
}

func TestSourceAdd_DuplicateName(t *testing.T) {
	baseDir := t.TempDir()

	require.NoError(t, core.SourceAdd(baseDir, "myrepo", "https://first.example.com/repo.git"))

	// Adding a second source with the same name must fail.
	err := core.SourceAdd(baseDir, "myrepo", "https://second.example.com/other.git")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestSourceAdd_MultipleSourcesAllowed(t *testing.T) {
	baseDir := t.TempDir()

	require.NoError(t, core.SourceAdd(baseDir, "corp", "https://corp.example.com/repo.git"))
	require.NoError(t, core.SourceAdd(baseDir, "personal", "https://github.com/me/dots.git"))

	cfg, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)
	assert.Len(t, cfg.Sources, 2)
}

// ---------------------------------------------------------------------------
// SourceUse
// ---------------------------------------------------------------------------

func TestSourceUse_EmptyBaseDir(t *testing.T) {
	err := core.SourceUse(core.SourceUseOptions{BaseDir: "", SourceProfile: "corp/proxy"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseDir")
}

func TestSourceUse_EmptySourceProfile(t *testing.T) {
	err := core.SourceUse(core.SourceUseOptions{BaseDir: t.TempDir(), SourceProfile: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SourceProfile")
}

func TestSourceUse_AddsNewActivation(t *testing.T) {
	baseDir := t.TempDir()

	err := core.SourceUse(core.SourceUseOptions{
		BaseDir:       baseDir,
		SourceProfile: "corp/system-proxy",
		Variables:     map[string]string{"PROXY": "http://proxy:3128"},
	})
	require.NoError(t, err)

	cfg, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)
	require.Len(t, cfg.Profiles, 1)
	assert.Equal(t, "corp/system-proxy", cfg.Profiles[0].Source)
	assert.Equal(t, "http://proxy:3128", cfg.Profiles[0].Variables["PROXY"])
}

func TestSourceUse_UpdatesExistingActivation(t *testing.T) {
	baseDir := t.TempDir()

	// Add initial activation.
	require.NoError(t, core.SourceUse(core.SourceUseOptions{
		BaseDir:       baseDir,
		SourceProfile: "corp/system-proxy",
		Variables:     map[string]string{"PROXY": "http://old-proxy:3128"},
	}))

	// Update with same source+vars — should be a no-op duplicate.
	require.NoError(t, core.SourceUse(core.SourceUseOptions{
		BaseDir:       baseDir,
		SourceProfile: "corp/system-proxy",
		Variables:     map[string]string{"PROXY": "http://old-proxy:3128"},
	}))

	cfg, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)
	// Should still be one profile (updated in-place, not appended).
	assert.Len(t, cfg.Profiles, 1)
}

func TestSourceUse_DifferentVarsCreatesNewActivation(t *testing.T) {
	baseDir := t.TempDir()

	// Add activation for user "alice".
	require.NoError(t, core.SourceUse(core.SourceUseOptions{
		BaseDir:       baseDir,
		SourceProfile: "corp/git-identity",
		Variables:     map[string]string{"GIT_NAME": "Alice", "GIT_EMAIL": "alice@corp.example.com"},
	}))

	// Add activation for user "bob" (different vars → new activation).
	require.NoError(t, core.SourceUse(core.SourceUseOptions{
		BaseDir:       baseDir,
		SourceProfile: "corp/git-identity",
		Variables:     map[string]string{"GIT_NAME": "Bob", "GIT_EMAIL": "bob@corp.example.com"},
	}))

	cfg, err := core.LoadSourcesConfig(baseDir)
	require.NoError(t, err)
	// Two distinct activations because the variables differ.
	assert.Len(t, cfg.Profiles, 2)
}
