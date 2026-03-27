package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// hookType is the action to run — one of: exec, systemd_reload, systemd_restart.
type hookType string

// HookDef is a single hook entry from hooks.yaml.
type HookDef struct {
	// On is the list of tracked file IDs that trigger this hook.
	On []string `yaml:"on"`
	// Type is the action: exec | systemd_reload | systemd_restart
	Type hookType `yaml:"type"`
	// Cmd is used by exec hooks: [binary, arg1, arg2, ...]
	Cmd []string `yaml:"cmd,omitempty"`
	// Service is required for systemd_reload and systemd_restart.
	Service string `yaml:"service,omitempty"`
}

// hooksFile is the top-level structure of hooks.yaml.
type hooksFile struct {
	// Allowlist is the set of binaries permitted in exec hooks.
	// If empty, exec hooks are disabled entirely.
	Allowlist []string           `yaml:"allowlist"`
	Hooks     map[string]HookDef `yaml:"hooks"`
}

// HookResult holds the outcome of running a single hook.
type HookResult struct {
	Name   string
	Type   hookType
	Output string // combined stdout+stderr
	Err    error
}

// validServiceName allows only safe systemd unit names.
var validServiceName = regexp.MustCompile(`^[a-zA-Z0-9\-_.@:]+$`)

// LoadHooks reads and parses <baseDir>/hooks.yaml.
// Returns nil (not an error) when the file is absent.
// HooksConfig holds the parsed hooks.yaml content.
type HooksConfig struct {
	Hooks     map[string]HookDef
	Allowlist map[string]bool
}

func LoadHooks(baseDir string) (*HooksConfig, error) {
	p := filepath.Join(baseDir, "hooks.yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &HooksConfig{}, nil
		}
		return nil, fmt.Errorf("hooks: read %q: %w", p, err)
	}

	var hf hooksFile
	if err := yaml.Unmarshal(data, &hf); err != nil {
		return nil, fmt.Errorf("hooks: parse %q: %w", p, err)
	}

	allowlist := make(map[string]bool, len(hf.Allowlist))
	for _, b := range hf.Allowlist {
		allowlist[b] = true
	}
	return &HooksConfig{Hooks: hf.Hooks, Allowlist: allowlist}, nil
}

// RunHooksForID runs every hook whose On list contains fileID.
func RunHooksForID(cfg *HooksConfig, fileID string) []HookResult {
	if cfg == nil || len(cfg.Hooks) == 0 {
		return nil
	}
	var results []HookResult
	for name, h := range cfg.Hooks {
		if !hookMatches(h, fileID) {
			continue
		}
		out, err := runHook(h, cfg.Allowlist)
		results = append(results, HookResult{Name: name, Type: h.Type, Output: out, Err: err})
	}
	return results
}

func hookMatches(h HookDef, fileID string) bool {
	for _, id := range h.On {
		if id == fileID || id == "*" {
			return true
		}
	}
	return false
}

func runHook(h HookDef, allowlist map[string]bool) (string, error) {
	switch h.Type {
	case "exec":
		return runExec(h.Cmd, allowlist)

	case "systemd_reload":
		if err := validateService(h.Service); err != nil {
			return "", err
		}
		return runCmd(30*time.Second, "systemctl", "reload", h.Service)

	case "systemd_restart":
		if err := validateService(h.Service); err != nil {
			return "", err
		}
		return runCmd(30*time.Second, "systemctl", "restart", h.Service)

	default:
		return "", fmt.Errorf("hooks: unknown type %q", h.Type)
	}
}

// runExec validates cmd against the allowlist then runs it.
// The allowlist must contain full absolute paths; basenames are not accepted.
func runExec(cmd []string, allowlist map[string]bool) (string, error) {
	if len(cmd) == 0 {
		return "", fmt.Errorf("hooks: exec: cmd is empty")
	}
	resolved, err := exec.LookPath(cmd[0])
	if err != nil {
		return "", fmt.Errorf("hooks: exec: binary not found: %w", err)
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("hooks: exec: resolve path: %w", err)
	}
	if !allowlist[absPath] {
		return "", fmt.Errorf("hooks: exec: %q is not in the allowlist", absPath)
	}
	return runCmd(30*time.Second, cmd[0], cmd[1:]...)
}

func validateService(name string) error {
	if name == "" {
		return fmt.Errorf("hooks: service name is required")
	}
	if !validServiceName.MatchString(name) {
		return fmt.Errorf("hooks: invalid service name %q", name)
	}
	return nil
}

func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		return output, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output, nil
}
