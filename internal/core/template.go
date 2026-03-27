package core

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"strings"
)

// TemplateVars holds the variable values used when rendering a template file.
// All built-in variables are populated lazily from the OS at render time;
// callers may override individual values by setting them before calling Render.
type TemplateVars struct {
	// Built-in variables resolved from the OS.
	Hostname string // {{hostname}}
	User     string // {{user}}
	Home     string // {{home}}
	OS       string // {{os}}  — always "linux" / "darwin" / "windows"

	// Extra holds arbitrary extra variables sourced from the environment or
	// the caller.  Keys correspond to {{env.KEY}} in the template.
	Extra map[string]string
}

// DefaultTemplateVars resolves all built-in variables from the current OS
// environment.  Missing values fall back to empty strings so that rendering
// never hard-fails on a missing variable — only unknown/unsupported variable
// names return an error.
func DefaultTemplateVars() TemplateVars {
	v := TemplateVars{Extra: map[string]string{}}

	if h, err := os.Hostname(); err == nil {
		v.Hostname = h
	}
	if u, err := user.Current(); err == nil {
		v.User = u.Username
		v.Home = u.HomeDir
	} else {
		v.User = os.Getenv("USER")
		v.Home = os.Getenv("HOME")
	}
	v.OS = runtimeGOOS() // thin wrapper so tests can override

	return v
}

// RenderTemplate replaces all {{variable}} placeholders in src and returns
// the rendered bytes.
//
// Supported placeholders:
//
//	{{hostname}}   — os.Hostname()
//	{{user}}       — current username
//	{{home}}       — current user's home directory
//	{{os}}         — operating system ("linux", "darwin", …)
//	{{env.NAME}}   — value of environment variable NAME
//
// Unknown placeholders return an error so typos are caught at apply time,
// not silently written to disk.
func RenderTemplate(src []byte, vars TemplateVars) ([]byte, error) {
	input := src
	var out bytes.Buffer

	for len(input) > 0 {
		open := bytes.Index(input, []byte("{{"))
		if open == -1 {
			out.Write(input)
			break
		}
		out.Write(input[:open])
		input = input[open+2:]

		close := bytes.Index(input, []byte("}}"))
		if close == -1 {
			// Unclosed placeholder — treat rest as literal.
			out.WriteString("{{")
			out.Write(input)
			break
		}

		key := strings.TrimSpace(string(input[:close]))
		input = input[close+2:]

		value, err := resolveVar(key, vars)
		if err != nil {
			return nil, err
		}
		out.WriteString(value)
	}

	return out.Bytes(), nil
}

func resolveVar(key string, vars TemplateVars) (string, error) {
	switch key {
	case "hostname":
		return vars.Hostname, nil
	case "user":
		return vars.User, nil
	case "home":
		return vars.Home, nil
	case "os":
		return vars.OS, nil
	}

	// {{env.NAME}} — environment variable declared in the profile or Extra.
	// SEC-008: the os.Getenv fallback is intentionally absent. Allowing
	// arbitrary env var reads via shared templates is a supply-chain vector
	// (attacker adds {{env.AWS_SECRET}} → victim's creds land in git history).
	// Callers must opt-in by populating TemplateVars.Extra explicitly.
	if strings.HasPrefix(key, "env.") {
		envKey := strings.TrimPrefix(key, "env.")
		if v, ok := vars.Extra[envKey]; ok {
			return v, nil
		}
		return "", fmt.Errorf("core: template: env var %q not in allowed vars — declare it in profile variables or TemplateVars.Extra", envKey)
	}

	// Check Extra map for arbitrary custom variables (e.g. source profile
	// variables like {{proxy_url}} or {{bypass_list}}).
	if v, ok := vars.Extra[key]; ok {
		return v, nil
	}

	return "", fmt.Errorf("core: template: unknown variable %q", key)
}
