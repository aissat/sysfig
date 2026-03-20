package core

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aissat/sysfig/internal/crypto"
	"github.com/aissat/sysfig/internal/state"
)

// RemoteDeployOptions configures an SSH-based remote deploy operation.
// The local machine reads its own repo and pushes each tracked file to the
// remote host over SSH. No sysfig installation is required on the remote.
type RemoteDeployOptions struct {
	// Host is the SSH target in user@hostname or hostname format. Required.
	Host string

	// SSHKey is the path to the SSH identity file. Empty = rely on ssh-agent.
	SSHKey string

	// SSHPort is the SSH port (default 22).
	SSHPort int

	// BaseDir is the local ~/.sysfig directory. Defaults to ~/.sysfig.
	BaseDir string

	// IDs limits the deploy to specific tracked IDs. Empty = deploy all.
	IDs []string

	// DryRun prints what would happen without writing anything to the remote.
	DryRun bool

	// SkipEncrypted silently skips encrypted files when no master key is present.
	SkipEncrypted bool
}

// RemoteFileResult is the outcome for a single tracked file.
type RemoteFileResult struct {
	ID         string
	SystemPath string
	Skipped    bool
	SkipReason string
	Err        error
}

// RemoteDeployResult is the full outcome of a RemoteDeploy call.
type RemoteDeployResult struct {
	Host    string
	Results []RemoteFileResult
	Applied int
	Skipped int
	Failed  int
}

// RemoteDeploy reads the local sysfig repo (state.json + repo.git) and pushes
// every tracked file to opts.Host over SSH.
//
// For each tracked file:
//  1. Read the blob from the local bare repo (git show HEAD:<path>)
//  2. Decrypt if the file is encrypted (uses the local master key)
//  3. SSH: `mkdir -p <dir> && cat > <path> && chmod <mode> <path>`
//
// No sysfig binary is required on the remote host — only a POSIX shell and
// standard coreutils (mkdir, cat, chmod).
func RemoteDeploy(opts RemoteDeployOptions) (*RemoteDeployResult, error) {
	if opts.Host == "" {
		return nil, fmt.Errorf("core: remote deploy: host is required")
	}

	// ── Resolve base dir ────────────────────────────────────────────────
	if opts.BaseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("core: remote deploy: resolve home dir: %w", err)
		}
		opts.BaseDir = filepath.Join(home, ".sysfig")
	}

	// ── Verify local ssh binary ──────────────────────────────────────────
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, fmt.Errorf("core: remote deploy: ssh not found in PATH — install OpenSSH client")
	}

	// ── Load local state ─────────────────────────────────────────────────
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)
	currentState, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: remote deploy: load state: %w", err)
	}

	if len(currentState.Files) == 0 {
		return &RemoteDeployResult{Host: opts.Host}, nil
	}

	// ── Build target file list ───────────────────────────────────────────
	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	keysDir := filepath.Join(opts.BaseDir, "keys")

	idSet := make(map[string]bool, len(opts.IDs))
	for _, id := range opts.IDs {
		idSet[id] = true
	}

	sshArgs := buildSSHArgs(opts.Host, opts.SSHKey, opts.SSHPort)

	result := &RemoteDeployResult{Host: opts.Host}

	for id, rec := range currentState.Files {
		// ID filter
		if len(idSet) > 0 && !idSet[id] {
			continue
		}

		fr := RemoteFileResult{
			ID:         id,
			SystemPath: rec.SystemPath,
		}

		// ── Read content from local repo ──────────────────────────────
		content, err := gitShowBytes(repoDir, rec.RepoPath)
		if err != nil {
			fr.Err = fmt.Errorf("read from repo: %w", err)
			result.Results = append(result.Results, fr)
			result.Failed++
			continue
		}

		// ── Decrypt if needed ─────────────────────────────────────────
		if rec.Encrypt {
			km := crypto.NewKeyManager(keysDir)
			identity, err := km.Load()
			if err != nil {
				if opts.SkipEncrypted {
					fr.Skipped = true
					fr.SkipReason = "encrypted, no master key"
					result.Results = append(result.Results, fr)
					result.Skipped++
					continue
				}
				fr.Err = fmt.Errorf("load master key: %w", err)
				result.Results = append(result.Results, fr)
				result.Failed++
				continue
			}
			decrypted, err := crypto.DecryptForFile(content, identity, id)
			if err != nil {
				fr.Err = fmt.Errorf("decrypt: %w", err)
				result.Results = append(result.Results, fr)
				result.Failed++
				continue
			}
			content = decrypted
		}

		// ── Dry run ───────────────────────────────────────────────────
		if opts.DryRun {
			fr.Skipped = true
			fr.SkipReason = "dry-run"
			result.Results = append(result.Results, fr)
			result.Skipped++
			continue
		}

		// ── Push to remote via SSH ────────────────────────────────────
		destPath := rec.SystemPath
		destDir := filepath.Dir(destPath)

		mode := uint32(0o644)
		if rec.Meta != nil && rec.Meta.Mode != 0 {
			mode = rec.Meta.Mode
		}

		// Build the remote shell command: create dir, write file, set mode.
		remoteShell := fmt.Sprintf(
			"mkdir -p %s && cat > %s && chmod %04o %s",
			shellQuote(destDir),
			shellQuote(destPath),
			mode,
			shellQuote(destPath),
		)

		pushArgs := append(sshArgs, remoteShell)
		cmd := exec.Command("ssh", pushArgs...)
		cmd.Stdin = bytes.NewReader(content)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			fr.Err = fmt.Errorf("ssh push: %w — %s", err, strings.TrimSpace(stderr.String()))
			result.Results = append(result.Results, fr)
			result.Failed++
			continue
		}

		result.Results = append(result.Results, fr)
		result.Applied++
	}

	return result, nil
}

// buildSSHArgs builds the ssh flag list (excluding the remote command).
func buildSSHArgs(host, sshKey string, port int) []string {
	var args []string
	args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	if sshKey != "" {
		args = append(args, "-i", sshKey)
	}
	if port > 0 && port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args, host)
	return args
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
