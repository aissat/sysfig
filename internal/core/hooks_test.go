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
// LoadHooks
// ---------------------------------------------------------------------------

func TestLoadHooks_Missing(t *testing.T) {
	cfg, err := core.LoadHooks(t.TempDir())
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Empty(t, cfg.Hooks)
}

func TestLoadHooks_Valid(t *testing.T) {
	dir := t.TempDir()
	yaml := `
allowlist:
  - /usr/bin/echo
hooks:
  reload-nginx:
    on: [abc12345]
    type: exec
    cmd: [/usr/bin/echo, reload]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(yaml), 0o600))

	cfg, err := core.LoadHooks(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Len(t, cfg.Hooks, 1)
	assert.True(t, cfg.Allowlist["echo"])
}

func TestLoadHooks_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(":\t:\tinvalid"), 0o600))

	_, err := core.LoadHooks(dir)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// RunHooksForID
// ---------------------------------------------------------------------------

func TestRunHooksForID_NilConfig(t *testing.T) {
	results := core.RunHooksForID(nil, "abc12345")
	assert.Nil(t, results)
}

func TestRunHooksForID_EmptyHooks(t *testing.T) {
	cfg := &core.HooksConfig{Hooks: map[string]core.HookDef{}}
	results := core.RunHooksForID(cfg, "abc12345")
	assert.Nil(t, results)
}

func TestRunHooksForID_NoMatch(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"hook1": {On: []string{"other-id"}, Type: "exec", Cmd: []string{"/usr/bin/echo"}},
		},
		Allowlist: map[string]bool{"echo": true},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	assert.Empty(t, results)
}

func TestRunHooksForID_WildcardMatch(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"any-hook": {On: []string{"*"}, Type: "exec", Cmd: []string{"/usr/bin/echo", "hi"}},
		},
		Allowlist: map[string]bool{"echo": true},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.NoError(t, results[0].Err)
	assert.Equal(t, "hi", results[0].Output)
}

func TestRunHooksForID_ExactMatch(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"my-hook": {On: []string{"abc12345"}, Type: "exec", Cmd: []string{"/usr/bin/echo", "matched"}},
		},
		Allowlist: map[string]bool{"echo": true},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.NoError(t, results[0].Err)
}

func TestRunHooksForID_ExecNotInAllowlist(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"bad-hook": {On: []string{"*"}, Type: "exec", Cmd: []string{"/usr/bin/echo", "hi"}},
		},
		Allowlist: map[string]bool{}, // echo not allowed
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.Error(t, results[0].Err)
}

func TestRunHooksForID_UnknownType(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"bad-type": {On: []string{"*"}, Type: "unknown_type"},
		},
		Allowlist: map[string]bool{},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.Error(t, results[0].Err)
}

func TestRunHooksForID_ExecEmptyCmd(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"empty-cmd": {On: []string{"*"}, Type: "exec", Cmd: []string{}},
		},
		Allowlist: map[string]bool{},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.Error(t, results[0].Err)
}

func TestRunHooksForID_SystemdInvalidService(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"bad-svc": {On: []string{"*"}, Type: "systemd_reload", Service: "../../bad"},
		},
		Allowlist: map[string]bool{},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.Error(t, results[0].Err)
}

func TestRunHooksForID_SystemdEmptyService(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"no-svc": {On: []string{"*"}, Type: "systemd_restart", Service: ""},
		},
		Allowlist: map[string]bool{},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.Error(t, results[0].Err)
}
