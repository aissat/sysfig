package core_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sysfig-dev/sysfig/internal/core"
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

func TestRenderTemplate_EmptyEnvVar(t *testing.T) {
	// env var not in Extra and not set in environment → empty string, no error.
	src := []byte("val={{env.UNSET_XYZ_999}}")
	got, err := core.RenderTemplate(src, testVars())
	require.NoError(t, err)
	assert.Equal(t, "val=", string(got))
}
