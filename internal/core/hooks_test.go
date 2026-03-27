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
	assert.True(t, cfg.Allowlist["/usr/bin/echo"])
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
		Allowlist: map[string]bool{"/usr/bin/echo": true},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	assert.Empty(t, results)
}

func TestRunHooksForID_WildcardMatch(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"any-hook": {On: []string{"*"}, Type: "exec", Cmd: []string{"/usr/bin/echo", "hi"}},
		},
		Allowlist: map[string]bool{"/usr/bin/echo": true},
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
		Allowlist: map[string]bool{"/usr/bin/echo": true},
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

// ── SEC-003: allowlist bypass via same-basename binary ────────────────────────
//
// Before the fix, only filepath.Base(cmd[0]) was checked against the allowlist,
// so /tmp/echo bypassed an allowlist containing "echo" (intended: /usr/bin/echo).
// After the fix the full resolved absolute path is compared.

func TestSEC003_AllowlistPathTraversalIsBlocked(t *testing.T) {
	// Create a fake binary in a temp dir with the same basename as an
	// allowlisted binary (/usr/bin/echo).
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "echo")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\necho fake\n"), 0o755))

	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"hook": {On: []string{"*"}, Type: "exec", Cmd: []string{fakeBin}},
		},
		// Allowlist contains the REAL echo — not the fake one.
		Allowlist: map[string]bool{"/usr/bin/echo": true},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.Error(t, results[0].Err,
		"SEC-003 regression: binary at different path must be blocked even if basename matches")
	assert.Contains(t, results[0].Err.Error(), "not in the allowlist")
}

func TestSEC003_AllowlistFullPathIsAccepted(t *testing.T) {
	cfg := &core.HooksConfig{
		Hooks: map[string]core.HookDef{
			"hook": {On: []string{"*"}, Type: "exec", Cmd: []string{"/usr/bin/echo", "ok"}},
		},
		Allowlist: map[string]bool{"/usr/bin/echo": true},
	}
	results := core.RunHooksForID(cfg, "abc12345")
	require.Len(t, results, 1)
	assert.NoError(t, results[0].Err)
	assert.Equal(t, "ok", results[0].Output)
}

func TestSEC003_LoadHooks_AllowlistStoresFullPath(t *testing.T) {
	dir := t.TempDir()
	yaml := `
allowlist:
  - /usr/bin/echo
  - /usr/sbin/nginx
hooks: {}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(yaml), 0o600))

	cfg, err := core.LoadHooks(dir)
	require.NoError(t, err)

	// Full paths must be present.
	assert.True(t, cfg.Allowlist["/usr/bin/echo"])
	assert.True(t, cfg.Allowlist["/usr/sbin/nginx"])
	// Basenames must NOT be in the allowlist (regression: used to be stored as basename).
	assert.False(t, cfg.Allowlist["echo"], "SEC-003 regression: basename must not be stored as allowlist key")
	assert.False(t, cfg.Allowlist["nginx"], "SEC-003 regression: basename must not be stored as allowlist key")
}
