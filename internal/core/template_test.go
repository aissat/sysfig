package core_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
)

func testVars() core.TemplateVars {
	return core.TemplateVars{
		Hostname: "myserver",
		User:     "alice",
		Home:     "/home/alice",
		OS:       "linux",
		Extra:    map[string]string{"GIT_EMAIL": "alice@example.com"},
	}
}

func TestRenderTemplate_BuiltinVars(t *testing.T) {
	src := []byte("host={{hostname}} user={{user}} home={{home}} os={{os}}")
	got, err := core.RenderTemplate(src, testVars())
	require.NoError(t, err)
	assert.Equal(t, "host=myserver user=alice home=/home/alice os=linux", string(got))
}

func TestRenderTemplate_EnvVar(t *testing.T) {
	src := []byte("email={{env.GIT_EMAIL}}")
	got, err := core.RenderTemplate(src, testVars())
	require.NoError(t, err)
	assert.Equal(t, "email=alice@example.com", string(got))
}

func TestRenderTemplate_MultiLine(t *testing.T) {
	src := []byte("[user]\n\tname = {{user}}\n\temail = {{env.GIT_EMAIL}}\n")
	got, err := core.RenderTemplate(src, testVars())
	require.NoError(t, err)
	assert.Equal(t, "[user]\n\tname = alice\n\temail = alice@example.com\n", string(got))
}

func TestRenderTemplate_NoPlaceholders(t *testing.T) {
	src := []byte("plain text, no placeholders")
	got, err := core.RenderTemplate(src, testVars())
	require.NoError(t, err)
	assert.Equal(t, src, got)
}

func TestRenderTemplate_MultipleSamePlaceholder(t *testing.T) {
	src := []byte("{{hostname}} and {{hostname}}")
	got, err := core.RenderTemplate(src, testVars())
	require.NoError(t, err)
	assert.Equal(t, "myserver and myserver", string(got))
}

func TestRenderTemplate_UnknownVar_ReturnsError(t *testing.T) {
	src := []byte("value={{typo_var}}")
	_, err := core.RenderTemplate(src, testVars())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "typo_var"), "error should name the unknown variable")
}

func TestRenderTemplate_UnclosedPlaceholder_Literal(t *testing.T) {
	// Unclosed {{ should be emitted literally (not an error).
	src := []byte("before {{unclosed")
	got, err := core.RenderTemplate(src, testVars())
	require.NoError(t, err)
	assert.Equal(t, "before {{unclosed", string(got))
}

func TestRenderTemplate_EnvVarNotInExtra_ReturnsError(t *testing.T) {
	// SEC-008: env vars not declared in Extra must be rejected — no os.Getenv fallback.
	// Before the fix, an unrecognised {{env.NAME}} silently read from the OS
	// environment, leaking arbitrary secrets into rendered config files.
	src := []byte("val={{env.UNSET_XYZ_999}}")
	_, err := core.RenderTemplate(src, testVars())
	require.Error(t, err, "SEC-008 regression: env var not in Extra must be an error, not silent OS read")
}

// ── SEC-008: arbitrary env var exfiltration via templates ────────────────────
//
// An attacker who controls a shared profile template can add {{env.AWS_SECRET}}
// to steal credentials from whoever runs `sysfig apply`. The fix requires all
// env vars to be explicitly listed in TemplateVars.Extra; the os.Getenv
// fallback is removed.

func TestSEC008_EnvVarFromOSEnvironmentIsRejected(t *testing.T) {
	// Set a "secret" in the OS environment — simulates a victim with cloud creds.
	t.Setenv("SEC008_TEST_SECRET", "super-secret-value")

	// Template controlled by attacker in a shared profile repo.
	src := []byte("AWS_KEY={{env.SEC008_TEST_SECRET}}")

	// testVars() does not include SEC008_TEST_SECRET in Extra.
	_, err := core.RenderTemplate(src, testVars())
	require.Error(t, err,
		"SEC-008 regression: env var set in OS but not in Extra must not be rendered")

	// Ensure it's not a false positive: explicitly allowed vars still work.
	vars := testVars()
	vars.Extra["SEC008_TEST_SECRET"] = "allowed-value"
	got, err := core.RenderTemplate(src, vars)
	require.NoError(t, err)
	assert.Equal(t, "AWS_KEY=allowed-value", string(got))
}
