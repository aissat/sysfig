package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpand_KnownVars verifies that all five built-in variables are replaced
// when explicitly provided through the vars map.
func TestExpand_KnownVars(t *testing.T) {
	vars := map[string]string{
		"home":     "/home/testuser",
		"user":     "testuser",
		"hostname": "testhost",
		"os":       "linux",
		"arch":     "amd64",
	}

	path := "{{home}}/{{user}}@{{hostname}}/{{os}}/{{arch}}"
	got, err := Expand(path, vars)

	require.NoError(t, err)
	assert.Equal(t, "/home/testuser/testuser@testhost/linux/amd64", got)
}

// TestExpand_DefaultVars verifies that built-in defaults are applied when an
// empty vars map is provided (i.e. no overrides).
func TestExpand_DefaultVars(t *testing.T) {
	vars := map[string]string{}

	// Each variable should expand to a non-empty string derived from the
	// running environment / runtime.
	tests := []struct {
		name     string
		template string
		wantFunc func() string
	}{
		{
			name:     "home",
			template: "{{home}}",
			wantFunc: func() string {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatalf("os.UserHomeDir() failed: %v", err)
				}
				return home
			},
		},
		{
			name:     "user",
			template: "{{user}}",
			wantFunc: func() string { return os.Getenv("USER") },
		},
		{
			name:     "hostname",
			template: "{{hostname}}",
			wantFunc: func() string {
				h, err := os.Hostname()
				if err != nil {
					t.Fatalf("os.Hostname() failed: %v", err)
				}
				return h
			},
		},
		{
			name:     "os",
			template: "{{os}}",
			wantFunc: func() string { return runtime.GOOS },
		},
		{
			name:     "arch",
			template: "{{arch}}",
			wantFunc: func() string { return runtime.GOARCH },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(tc.template, vars)
			require.NoError(t, err)
			want := tc.wantFunc()
			assert.Equal(t, want, got, "default expansion of %s should match environment value", tc.template)
			assert.NotEmpty(t, got, "default expansion of %s must not be empty", tc.template)
		})
	}
}

// TestExpand_UnknownVar verifies that an error is returned when a variable
// token is neither a built-in nor present in the vars map.
func TestExpand_UnknownVar(t *testing.T) {
	vars := map[string]string{}

	_, err := Expand("{{unknown}}", vars)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown variable")
	assert.Contains(t, err.Error(), "unknown")
}

// TestExpand_UnknownVar_WithOtherVars verifies that an error is still returned
// even when the path also contains valid variables.
func TestExpand_UnknownVar_WithOtherVars(t *testing.T) {
	vars := map[string]string{
		"home": "/home/testuser",
	}

	_, err := Expand("{{home}}/{{ghost}}", vars)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
}

// TestExpand_VarsMapOverridesDefaults verifies that a value in the caller-
// supplied vars map takes precedence over the built-in default resolver.
func TestExpand_VarsMapOverridesDefaults(t *testing.T) {
	vars := map[string]string{
		"os": "overridden-os",
	}

	got, err := Expand("{{os}}", vars)

	require.NoError(t, err)
	assert.Equal(t, "overridden-os", got)
}

// TestNormalize_Tilde verifies that a leading "~" is replaced by the home
// directory and the remainder of the path is preserved.
func TestNormalize_Tilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "os.UserHomeDir() must succeed for this test to run")

	got, err := Normalize("~/foo", map[string]string{})

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "foo"), got)
}

// TestNormalize_Tilde_WithVarsMapHome verifies that when "home" is supplied in
// the vars map, the tilde resolves to that value rather than os.UserHomeDir.
func TestNormalize_Tilde_WithVarsMapHome(t *testing.T) {
	vars := map[string]string{
		"home": "/custom/home",
	}

	got, err := Normalize("~/bar/baz", vars)

	require.NoError(t, err)
	assert.Equal(t, "/custom/home/bar/baz", got)
}

// TestNormalize_Clean verifies that filepath.Clean is applied, collapsing any
// redundant elements such as "..".
func TestNormalize_Clean(t *testing.T) {
	got, err := Normalize("/etc/../etc/nginx", map[string]string{})

	require.NoError(t, err)
	assert.Equal(t, "/etc/nginx", got)
}

// TestNormalize_Clean_TrailingSlash verifies that trailing slashes are removed
// by the Clean step (except for root "/").
func TestNormalize_Clean_TrailingSlash(t *testing.T) {
	got, err := Normalize("/var/log/", map[string]string{})

	require.NoError(t, err)
	assert.Equal(t, "/var/log", got)
}

// TestNormalize_Combined verifies that tilde expansion, variable substitution,
// and path cleaning all work together in a single call.
func TestNormalize_Combined(t *testing.T) {
	vars := map[string]string{
		"home":     "/home/alice",
		"hostname": "myhost",
	}

	// "~" resolves to /home/alice (from vars["home"]),
	// "{{hostname}}" resolves to "myhost",
	// the double slash introduced by the join is cleaned away.
	got, err := Normalize("~/{{hostname}}/../{{hostname}}/config", vars)

	require.NoError(t, err)
	assert.Equal(t, "/home/alice/myhost/config", got)
}

// TestNormalize_Combined_NoDoubleDot verifies the straightforward case:
// tilde + variable with no cleaning needed.
func TestNormalize_Combined_Straightforward(t *testing.T) {
	vars := map[string]string{
		"home":     "/home/bob",
		"hostname": "devbox",
	}

	got, err := Normalize("~/{{hostname}}/config", vars)

	require.NoError(t, err)
	assert.Equal(t, "/home/bob/devbox/config", got)
}

// TestNormalize_UnknownVar verifies that Normalize propagates errors from
// Expand when an unknown variable is encountered.
func TestNormalize_UnknownVar(t *testing.T) {
	_, err := Normalize("/etc/{{nope}}/config", map[string]string{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

// TestExpand_NoVars verifies that a path with no template tokens is returned
// unchanged.
func TestExpand_NoVars(t *testing.T) {
	path := "/some/static/path"
	got, err := Expand(path, map[string]string{})

	require.NoError(t, err)
	assert.Equal(t, path, got)
}

// TestExpand_CustomVarOnly verifies that a custom (non-built-in) variable
// supplied through the vars map is substituted correctly.
func TestExpand_CustomVarOnly(t *testing.T) {
	vars := map[string]string{
		"project": "myapp",
	}

	got, err := Expand("/opt/{{project}}/bin", vars)

	require.NoError(t, err)
	assert.Equal(t, "/opt/myapp/bin", got)
}

// TestExpand_EmptyPath verifies that an empty path is returned as-is with no
// error.
func TestExpand_EmptyPath(t *testing.T) {
	got, err := Expand("", map[string]string{})

	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// TestNormalize_TildeOnly verifies that a bare "~" expands to the home
// directory with no trailing separator.
func TestNormalize_TildeOnly(t *testing.T) {
	vars := map[string]string{
		"home": "/home/carol",
	}

	got, err := Normalize("~", vars)

	require.NoError(t, err)
	assert.Equal(t, "/home/carol", got)
}

// TestExpand_AllDefaultsNonEmpty is a quick integration check that all five
// default variables resolve to non-empty strings in any reasonable CI/dev
// environment.
func TestExpand_AllDefaultsNonEmpty(t *testing.T) {
	builtins := []string{"home", "user", "hostname", "os", "arch"}

	for _, v := range builtins {
		t.Run(v, func(t *testing.T) {
			got, err := Expand(fmt.Sprintf("{{%s}}", v), map[string]string{})
			require.NoError(t, err)
			// "user" may be empty in minimal containers; skip the emptiness
			// assertion for it rather than failing in CI.
			if v != "user" {
				assert.NotEmpty(t, got, "default for {{%s}} should not be empty", v)
			}
			_ = strings.TrimSpace(got) // ensure no panic on the result
		})
	}
}
