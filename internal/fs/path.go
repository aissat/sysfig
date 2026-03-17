package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// knownVars is the set of built-in variable names that have default resolvers.
var knownVars = map[string]func() (string, error){
	"home": func() (string, error) {
		return os.UserHomeDir()
	},
	"user": func() (string, error) {
		return os.Getenv("USER"), nil
	},
	"hostname": func() (string, error) {
		return os.Hostname()
	},
	"os": func() (string, error) {
		return runtime.GOOS, nil
	},
	"arch": func() (string, error) {
		return runtime.GOARCH, nil
	},
}

// varPattern matches any {{...}} token in a path string.
var varPattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// Expand replaces template variables in a path string.
// Supported variables: {{home}}, {{user}}, {{hostname}}, {{os}}, {{arch}}
// Additional variables may be supplied via the vars map, which takes precedence
// over the built-in defaults.
// It returns an error if an unknown variable (not built-in and not in vars) is
// used.
func Expand(path string, vars map[string]string) (string, error) {
	var expandErr error

	result := varPattern.ReplaceAllStringFunc(path, func(match string) string {
		if expandErr != nil {
			return match
		}

		// Extract the variable name from {{name}}.
		name := match[2 : len(match)-2]

		// Caller-supplied vars take highest precedence.
		if val, ok := vars[name]; ok {
			return val
		}

		// Fall back to built-in defaults.
		if resolver, ok := knownVars[name]; ok {
			val, err := resolver()
			if err != nil {
				expandErr = fmt.Errorf("fs.Expand: resolving {{%s}}: %w", name, err)
				return match
			}
			return val
		}

		// Unknown variable — record the error.
		expandErr = fmt.Errorf("fs.Expand: unknown variable {{%s}}", name)
		return match
	})

	if expandErr != nil {
		return "", expandErr
	}

	return result, nil
}

// Normalize cleans a path: calls filepath.Clean, resolves "~" to the home
// directory, and calls Expand with the provided vars map.
func Normalize(path string, vars map[string]string) (string, error) {
	// Resolve a leading "~" to the home directory.
	if strings.HasPrefix(path, "~") {
		home, err := homeDir(vars)
		if err != nil {
			return "", fmt.Errorf("fs.Normalize: resolving ~: %w", err)
		}
		path = home + path[1:]
	}

	// Expand any {{variable}} tokens.
	expanded, err := Expand(path, vars)
	if err != nil {
		return "", fmt.Errorf("fs.Normalize: %w", err)
	}

	// Clean the path last so that any inserted segments are also normalised.
	return filepath.Clean(expanded), nil
}

// homeDir returns the home directory, preferring the caller-supplied vars map
// before falling back to os.UserHomeDir.
func homeDir(vars map[string]string) (string, error) {
	if val, ok := vars["home"]; ok {
		return val, nil
	}
	return os.UserHomeDir()
}
