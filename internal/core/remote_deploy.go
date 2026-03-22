package core

import (
	"bytes"
	"fmt"
	"net"
	"os"
	osuser "os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/aissat/sysfig/internal/crypto"
	"github.com/aissat/sysfig/internal/state"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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

	// IDs limits the deploy to specific tracked IDs (full or 8-char prefix). Empty = deploy all.
	IDs []string

	// Tags limits the deploy to files that carry at least one of these tags.
	// Use this to target machine-specific file sets, e.g. --tag arch or --tag ubuntu.
	Tags []string

	// Paths limits the deploy to files whose SystemPath matches one of these.
	Paths []string

	// All deploys every tracked file regardless of tags, IDs, or paths.
	// Required when none of IDs/Tags/Paths are specified — prevents accidental
	// full-fleet deploys.
	All bool

	// DryRun prints what would happen without writing anything to the remote.
	DryRun bool

	// SkipEncrypted silently skips encrypted files when no master key is present.
	SkipEncrypted bool

	// Sudo wraps each remote write command with sudo, required for /etc/ and
	// other root-owned paths on the remote host.
	Sudo bool

	// Progress, if set, is called with each file result as it completes.
	// Always called from the same goroutine — no locking needed by callers.
	Progress func(RemoteFileResult)
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
//  1. Read the blob from the local bare repo (per-file track branch)
//  2. Decrypt if the file is encrypted (uses the local master key)
//  3. Run `mkdir -p <dir> && cat > <path> && chmod <mode> <path>` via SSH exec,
//     piping file content through sess.Stdin (native Go SSH — no ssh binary).
//
// No sysfig binary is required on the remote host — only a POSIX shell and
// standard coreutils (mkdir, cat, chmod).
func RemoteDeploy(opts RemoteDeployOptions) (*RemoteDeployResult, error) {
	if opts.Host == "" {
		return nil, fmt.Errorf("core: remote deploy: host is required")
	}
	if !opts.All && len(opts.IDs) == 0 && len(opts.Tags) == 0 && len(opts.Paths) == 0 {
		return nil, fmt.Errorf("core: remote deploy: specify --tag <tag>, --id <id>, --path <path>, or --all")
	}

	// ── Resolve base dir ────────────────────────────────────────────────
	if opts.BaseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("core: remote deploy: resolve home dir: %w", err)
		}
		opts.BaseDir = filepath.Join(home, ".sysfig")
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
	pathSet := make(map[string]bool, len(opts.Paths))
	for _, p := range opts.Paths {
		pathSet[p] = true
	}

	tagSet := make(map[string]bool, len(opts.Tags))
	for _, t := range opts.Tags {
		tagSet[t] = true
	}

	result := &RemoteDeployResult{Host: opts.Host}

	emit := func(fr RemoteFileResult) {
		result.Results = append(result.Results, fr)
		if opts.Progress != nil {
			opts.Progress(fr)
		}
	}

	// ── Dial SSH ─────────────────────────────────────────────────────────
	sshCfg, err := buildSSHClientConfig(opts.SSHKey)
	if err != nil {
		return nil, fmt.Errorf("core: remote deploy: %w", err)
	}

	sshUser, sshAddr := parseSSHTarget(opts.Host, opts.SSHPort)
	sshCfg.User = sshUser

	// Dial TCP manually so we can force-close cleanly at the end.
	netConn, err := net.DialTimeout("tcp", sshAddr, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("core: remote deploy: tcp dial %s: %w", sshAddr, err)
	}
	sshConn, chans, reqs, err := gossh.NewClientConn(netConn, sshAddr, sshCfg)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("core: remote deploy: ssh handshake %s: %w", sshAddr, err)
	}
	sshClient := gossh.NewClient(sshConn, chans, reqs)

	// ── Deploy each file ─────────────────────────────────────────────────
	for id, rec := range currentState.Files {
		if len(idSet) > 0 && !(idSet[id] || hasIDPrefixInSet(id, idSet)) {
			continue
		}
		if len(pathSet) > 0 && !pathSet[rec.SystemPath] {
			continue
		}

		if len(tagSet) > 0 {
			effectiveTags := rec.Tags
			if len(effectiveTags) == 0 {
				effectiveTags = DetectPlatformTags()
			}
			if !fileHasTag(effectiveTags, tagSet) {
				continue
			}
		}

		fr := RemoteFileResult{
			ID:         id,
			SystemPath: rec.SystemPath,
		}

		// Local-only and hash-only files have no content in the repo — skip.
		if rec.LocalOnly || rec.HashOnly {
			fr.Skipped = true
			fr.SkipReason = "local-only"
			if rec.HashOnly {
				fr.SkipReason = "hash-only"
			}
			emit(fr)
			result.Skipped++
			continue
		}

		// Read content from local repo (per-file track branch).
		trackBranch := rec.Branch
		if trackBranch == "" {
			trackBranch = "track/" + SanitizeBranchName(rec.RepoPath)
		}
		content, err := gitShowBytesAt(repoDir, trackBranch, rec.RepoPath)
		if err != nil {
			fr.Err = fmt.Errorf("read from repo: %w", err)
			emit(fr)
			result.Failed++
			continue
		}

		// Decrypt if needed.
		if rec.Encrypt {
			km := crypto.NewKeyManager(keysDir)
			identity, err := km.Load()
			if err != nil {
				if opts.SkipEncrypted {
					fr.Skipped = true
					fr.SkipReason = "encrypted, no master key"
					emit(fr)
					result.Skipped++
					continue
				}
				fr.Err = fmt.Errorf("load master key: %w", err)
				emit(fr)
				result.Failed++
				continue
			}
			decrypted, err := crypto.DecryptForFile(content, identity, id)
			if err != nil {
				fr.Err = fmt.Errorf("decrypt: %w", err)
				emit(fr)
				result.Failed++
				continue
			}
			content = decrypted
		}

		if opts.DryRun {
			fr.Skipped = true
			fr.SkipReason = "dry-run"
			emit(fr)
			result.Skipped++
			continue
		}

		dstPath := rec.SystemPath
		destDir := filepath.Dir(dstPath)
		mode := uint32(0o644)
		if rec.Meta != nil && rec.Meta.Mode != 0 {
			mode = rec.Meta.Mode
		}

		remoteShell := fmt.Sprintf(
			"mkdir -p %s && cat > %s && chmod %04o %s",
			shellQuote(destDir),
			shellQuote(dstPath),
			mode,
			shellQuote(dstPath),
		)
		// Use sudo only when needed: --sudo is set AND the destination is
		// not inside the SSH user's home directory (which they already own).
		useSudo := opts.Sudo && needsSudo(dstPath, sshUser)
		if useSudo {
			remoteShell = "sudo sh -c " + shellQuote(remoteShell)
		}

		writeErr := sshExecWithStdin(sshClient, remoteShell, content)
		// If write failed with permission denied and --sudo is set but we
		// skipped sudo (home-dir heuristic), retry with sudo.
		if writeErr != nil && opts.Sudo && !useSudo && strings.Contains(writeErr.Error(), "Permission denied") {
			sudoShell := "sudo sh -c " + shellQuote(fmt.Sprintf(
				"mkdir -p %s && cat > %s && chmod %04o %s",
				shellQuote(destDir), shellQuote(dstPath), mode, shellQuote(dstPath),
			))
			writeErr = sshExecWithStdin(sshClient, sudoShell, content)
		}
		if writeErr != nil {
			fr.Err = writeErr
			emit(fr)
			result.Failed++
			continue
		}

		emit(fr)
		result.Applied++
	}

	// Force-close the raw TCP connection. sshClient.Close() can block waiting
	// for the SSH disconnect ACK; closing the underlying net.Conn is immediate.
	netConn.Close()

	return result, nil
}

// ── RemoteDeployRendered ──────────────────────────────────────────────────────

// RemoteRenderedOptions configures a push of pre-rendered files to a remote host.
type RemoteRenderedOptions struct {
	Host    string
	SSHKey  string
	SSHPort int
	Files   []RenderedFile
	Sudo    bool
	DryRun  bool
	Progress func(path string, err error)
}

// RemoteDeployRendered pushes a slice of already-rendered files to a remote
// host over SSH. Used by `deploy --from <template-repo> --profile <name>`.
func RemoteDeployRendered(opts RemoteRenderedOptions) (applied, failed int, err error) {
	sshCfg, err := buildSSHClientConfig(opts.SSHKey)
	if err != nil {
		return 0, 0, fmt.Errorf("remote deploy rendered: %w", err)
	}
	sshUser, sshAddr := parseSSHTarget(opts.Host, opts.SSHPort)
	sshCfg.User = sshUser

	netConn, err := net.DialTimeout("tcp", sshAddr, 30*time.Second)
	if err != nil {
		return 0, 0, fmt.Errorf("remote deploy rendered: tcp dial %s: %w", sshAddr, err)
	}
	sshConn, chans, reqs, connErr := gossh.NewClientConn(netConn, sshAddr, sshCfg)
	if connErr != nil {
		netConn.Close()
		return 0, 0, fmt.Errorf("remote deploy rendered: ssh handshake: %w", connErr)
	}
	sshClient := gossh.NewClient(sshConn, chans, reqs)

	for _, rf := range opts.Files {
		if opts.DryRun {
			if opts.Progress != nil {
				opts.Progress(rf.Dest, nil)
			}
			continue
		}
		destDir := filepath.Dir(rf.Dest)
		shell := fmt.Sprintf("mkdir -p %s && cat > %s && chmod %04o %s",
			shellQuote(destDir), shellQuote(rf.Dest), rf.Mode, shellQuote(rf.Dest))
		useSudo := opts.Sudo && needsSudo(rf.Dest, sshUser)
		if useSudo {
			shell = "sudo sh -c " + shellQuote(shell)
		}
		writeErr := sshExecWithStdin(sshClient, shell, rf.Content)
		if writeErr != nil && opts.Sudo && !useSudo && strings.Contains(writeErr.Error(), "Permission denied") {
			sudo := fmt.Sprintf("mkdir -p %s && cat > %s && chmod %04o %s",
				shellQuote(destDir), shellQuote(rf.Dest), rf.Mode, shellQuote(rf.Dest))
			writeErr = sshExecWithStdin(sshClient, "sudo sh -c "+shellQuote(sudo), rf.Content)
		}
		if opts.Progress != nil {
			opts.Progress(rf.Dest, writeErr)
		}
		if writeErr != nil {
			failed++
		} else {
			applied++
		}
	}

	netConn.Close()
	return applied, failed, nil
}

// sshExecWithStdin runs cmd on the remote host, piping content to its stdin.
// Uses Go's native SSH client — no ssh binary required.
func sshExecWithStdin(client *gossh.Client, cmd string, content []byte) error {
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	sess.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	sess.Stderr = &stderr

	if err := sess.Run(cmd); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w — %s", err, msg)
		}
		return err
	}
	return nil
}

// sshExec runs a command on the remote host with no stdin.
func sshExec(client *gossh.Client, cmd string) error {
	return sshExecWithStdin(client, cmd, nil)
}

// buildSSHClientConfig creates an ssh.ClientConfig using a key file and/or
// the SSH agent (SSH_AUTH_SOCK).
func buildSSHClientConfig(sshKey string) (*gossh.ClientConfig, error) {
	var authMethods []gossh.AuthMethod

	if sshKey != "" {
		expanded := sshKey
		if strings.HasPrefix(expanded, "~/") {
			home, _ := os.UserHomeDir()
			expanded = filepath.Join(home, expanded[2:])
		}
		keyBytes, err := os.ReadFile(expanded)
		if err != nil {
			return nil, fmt.Errorf("read ssh key %s: %w", sshKey, err)
		}
		signer, err := gossh.ParsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse ssh key %s: %w", sshKey, err)
		}
		authMethods = append(authMethods, gossh.PublicKeys(signer))
	}

	// Also try SSH agent.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			authMethods = append(authMethods, gossh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH auth available — provide --ssh-key or set SSH_AUTH_SOCK")
	}

	return &gossh.ClientConfig{
		Auth:            authMethods,
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec — matches prior StrictHostKeyChecking=accept-new behaviour
	}, nil
}

// parseSSHTarget splits "user@host" into (user, "host:port").
// Falls back to the current OS user if no @ is present.
func parseSSHTarget(target string, port int) (user, addr string) {
	host := target
	u := ""
	if cu, err := osuser.Current(); err == nil {
		u = cu.Username
	}
	if i := strings.LastIndex(target, "@"); i >= 0 {
		u = target[:i]
		host = target[i+1:]
	}
	p := 22
	if port > 0 {
		p = port
	}
	return u, fmt.Sprintf("%s:%d", host, p)
}

// needsSudo reports whether writing to dstPath requires elevated privileges.
// Returns false when the path is under the SSH user's home directory — they
// already own it. Returns true for system paths (/etc/, /usr/, /var/, etc.).
func needsSudo(dstPath, sshUser string) bool {
	if sshUser == "" {
		return true
	}
	userHome := "/home/" + sshUser + "/"
	return !strings.HasPrefix(dstPath, userHome)
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// hasIDPrefixInSet reports whether any key in set is a prefix (≥4 chars) of id,
// or id itself starts with any set key that is a prefix.
func hasIDPrefixInSet(id string, set map[string]bool) bool {
	for k := range set {
		if hasIDPrefix(id, k) {
			return true
		}
	}
	return false
}

// fileHasTag reports whether any tag in fileTags is present in the wanted set.
func fileHasTag(fileTags []string, wanted map[string]bool) bool {
	for _, t := range fileTags {
		if wanted[t] {
			return true
		}
	}
	return false
}
