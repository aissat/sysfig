package core

// bundle.go — bundle-based remote transport for environments without a git server.
//
// Supported URL schemes:
//
//	bundle+local:///absolute/path/to/file.bundle  — local filesystem / NFS / SMB mount
//	bundle+ssh://user@host/path/to/file.bundle     — SSH via scp
//
// Design:
//   - Push: git bundle create → copy bundle file to destination
//   - Pull: copy bundle file from destination → git fetch from bundle
//
// The local bare repo, branch-per-track layout, and all existing sync/apply
// logic are completely unchanged. Only the transport (how the bundle file moves
// between machines) differs from a standard git remote.

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RemoteKind classifies how push/pull should move data.
type RemoteKind int

const (
	RemoteGit         RemoteKind = iota // standard git remote (origin push/fetch)
	RemoteBundleLocal                   // bundle+local:// — file copy
	RemoteBundleSSH                     // bundle+ssh:// — scp
)

// ParseRemoteKind inspects a remote URL string and returns its RemoteKind.
// Any URL that does not start with a recognised bundle+ scheme is treated as a
// standard git remote.
func ParseRemoteKind(rawURL string) RemoteKind {
	switch {
	case strings.HasPrefix(rawURL, "bundle+local://"):
		return RemoteBundleLocal
	case strings.HasPrefix(rawURL, "bundle+ssh://"):
		return RemoteBundleSSH
	default:
		return RemoteGit
	}
}

// BundleLocalPath extracts the absolute filesystem path from a
// bundle+local:/// URL.  Returns an error for malformed URLs.
func BundleLocalPath(rawURL string) (string, error) {
	// Strip the custom scheme prefix so url.Parse can handle it.
	trimmed := strings.TrimPrefix(rawURL, "bundle+")
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("bundle: invalid local URL %q: %w", rawURL, err)
	}
	p := u.Path
	if p == "" {
		return "", fmt.Errorf("bundle: empty path in URL %q", rawURL)
	}
	return p, nil
}

// bundleSSHParts parses a bundle+ssh://user@host/path URL into (user@host, /path).
func bundleSSHParts(rawURL string) (target, remotePath string, err error) {
	trimmed := strings.TrimPrefix(rawURL, "bundle+")
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("bundle: invalid ssh URL %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("bundle: no host in URL %q", rawURL)
	}
	if u.Path == "" {
		return "", "", fmt.Errorf("bundle: no path in URL %q", rawURL)
	}
	if u.User != nil {
		target = u.User.Username() + "@" + host
	} else {
		target = host
	}
	if u.Port() != "" {
		// scp uses -P for port, handled by the caller.
		target = target // port resolved separately
	}
	return target, u.Path, nil
}

// bundleSSHPort extracts the port from a bundle+ssh URL, returning "" when
// the default port 22 is intended.
func bundleSSHPort(rawURL string) string {
	trimmed := strings.TrimPrefix(rawURL, "bundle+")
	u, _ := url.Parse(trimmed)
	return u.Port()
}

// ── BundlePushOptions ────────────────────────────────────────────────────────

// BundlePushOptions configures a bundle push operation.
type BundlePushOptions struct {
	// RepoDir is the path to the bare git repo (e.g. ~/.sysfig/repo.git).
	RepoDir string

	// RemoteURL is the configured remote — must start with bundle+local:// or
	// bundle+ssh://.
	RemoteURL string

	// SSHKey is an optional path to an SSH identity file for bundle+ssh.
	SSHKey string
}

// BundlePush creates a git bundle of all local refs and copies it to the
// destination described by opts.RemoteURL.
func BundlePush(opts BundlePushOptions) error {
	if opts.RepoDir == "" {
		return fmt.Errorf("bundle push: RepoDir must not be empty")
	}
	if opts.RemoteURL == "" {
		return fmt.Errorf("bundle push: RemoteURL must not be empty")
	}

	// Create bundle in a temp file next to the repo so rename is atomic on
	// the same filesystem.
	tmp, err := os.CreateTemp("", "sysfig-bundle-*.bundle")
	if err != nil {
		return fmt.Errorf("bundle push: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	// git bundle create <tmp> --all
	if err := bundleGitRun(opts.RepoDir, 60*time.Second,
		"bundle", "create", tmpPath, "--all"); err != nil {
		return fmt.Errorf("bundle push: git bundle create: %w", err)
	}

	switch ParseRemoteKind(opts.RemoteURL) {
	case RemoteBundleLocal:
		dst, err := BundleLocalPath(opts.RemoteURL)
		if err != nil {
			return err
		}
		if err := bundleCopyFile(tmpPath, dst); err != nil {
			return fmt.Errorf("bundle push: copy to %s: %w", dst, err)
		}

	case RemoteBundleSSH:
		target, remotePath, err := bundleSSHParts(opts.RemoteURL)
		if err != nil {
			return err
		}
		port := bundleSSHPort(opts.RemoteURL)
		if err := bundleScpUpload(tmpPath, target, remotePath, port, opts.SSHKey); err != nil {
			return fmt.Errorf("bundle push: scp to %s:%s: %w", target, remotePath, err)
		}

	default:
		return fmt.Errorf("bundle push: unsupported scheme in URL %q", opts.RemoteURL)
	}

	return nil
}

// ── BundlePullOptions ────────────────────────────────────────────────────────

// BundlePullOptions configures a bundle pull operation.
type BundlePullOptions struct {
	// RepoDir is the path to the bare git repo.
	RepoDir string

	// RemoteURL is the configured remote.
	RemoteURL string

	// SSHKey is an optional path to an SSH identity file for bundle+ssh.
	SSHKey string
}

// BundlePullResult reports what happened during a bundle pull.
type BundlePullResult struct {
	AlreadyUpToDate bool
}

// BundlePull downloads the bundle file from the remote and imports its refs
// into the local bare repo using `git fetch`.
func BundlePull(opts BundlePullOptions) (*BundlePullResult, error) {
	if opts.RepoDir == "" {
		return nil, fmt.Errorf("bundle pull: RepoDir must not be empty")
	}
	if opts.RemoteURL == "" {
		return nil, fmt.Errorf("bundle pull: RemoteURL must not be empty")
	}

	tmp, err := os.CreateTemp("", "sysfig-bundle-*.bundle")
	if err != nil {
		return nil, fmt.Errorf("bundle pull: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	// Fetch the bundle file from the remote.
	switch ParseRemoteKind(opts.RemoteURL) {
	case RemoteBundleLocal:
		src, err := BundleLocalPath(opts.RemoteURL)
		if err != nil {
			return nil, err
		}
		if err := bundleCopyFile(src, tmpPath); err != nil {
			return nil, fmt.Errorf("bundle pull: copy from %s: %w", src, err)
		}

	case RemoteBundleSSH:
		target, remotePath, err := bundleSSHParts(opts.RemoteURL)
		if err != nil {
			return nil, err
		}
		port := bundleSSHPort(opts.RemoteURL)
		if err := bundleScpDownload(target, remotePath, tmpPath, port, opts.SSHKey); err != nil {
			return nil, fmt.Errorf("bundle pull: scp from %s:%s: %w", target, remotePath, err)
		}

	default:
		return nil, fmt.Errorf("bundle pull: unsupported scheme in URL %q", opts.RemoteURL)
	}

	// Verify the bundle is intact before importing.
	if err := bundleGitRun(opts.RepoDir, 10*time.Second,
		"bundle", "verify", tmpPath); err != nil {
		return nil, fmt.Errorf("bundle pull: bundle verify: %w", err)
	}

	// Snapshot all local ref SHAs before importing so we can detect whether
	// anything actually changed. We compare all refs (not just HEAD) because
	// track/* branches update independently of master/HEAD.
	refsBefore := bundleAllRefs(opts.RepoDir)

	// Discover which refs the bundle contains, then build explicit refspecs.
	// `git bundle list-heads <bundle>` prints "<sha> <refname>" per line.
	// We turn each "refs/heads/X" into "+refs/heads/X:refs/heads/X" so that
	// `git fetch` force-updates each local branch individually — wildcard
	// refspecs are not reliable across all git versions when fetching from
	// a local bundle file.
	refspecs, err := bundleListHeadRefspecs(opts.RepoDir, tmpPath)
	if err != nil {
		return nil, fmt.Errorf("bundle pull: list bundle heads: %w", err)
	}
	if len(refspecs) == 0 {
		// Bundle is valid but has no refs — treat as up-to-date.
		return &BundlePullResult{AlreadyUpToDate: true}, nil
	}

	fetchArgs := append([]string{"fetch", tmpPath}, refspecs...)
	if err := bundleGitRun(opts.RepoDir, 60*time.Second, fetchArgs...); err != nil {
		return nil, fmt.Errorf("bundle pull: git fetch from bundle: %w", err)
	}

	refsAfter := bundleAllRefs(opts.RepoDir)
	return &BundlePullResult{
		AlreadyUpToDate: refsBefore == refsAfter,
	}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// bundleGitRun runs a git subcommand inside repoDir (GIT_DIR set).
func bundleGitRun(repoDir string, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// bundleListHeadRefspecs runs `git bundle list-heads <bundlePath>` and returns
// a slice of force-update refspecs of the form "+refs/heads/X:refs/heads/X"
// for every branch ref found in the bundle.
func bundleListHeadRefspecs(repoDir, bundlePath string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "bundle", "list-heads", bundlePath)
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	var refspecs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "<sha> <refname>"
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		ref := parts[1]
		if strings.HasPrefix(ref, "refs/heads/") {
			refspecs = append(refspecs, "+"+ref+":"+ref)
		}
	}
	return refspecs, nil
}

// bundleAllRefs returns a sorted snapshot of all ref SHAs in the bare repo as
// a single string (suitable for equality comparison). Returns "" on error.
func bundleAllRefs(repoDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "for-each-ref", "--format=%(refname) %(objectname)")
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// bundleHeadSHA returns the current HEAD SHA of the bare repo, or "" on error.
func bundleHeadSHA(repoDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// bundleCopyFile copies src to dst, creating parent directories as needed.
// Uses a write-then-rename pattern so partial writes are never visible.
func bundleCopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Write to a temp file in the same directory for atomic rename.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".sysfig-bundle-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, dst)
}

// bundleScpUpload copies a local file to user@host:/path using scp.
func bundleScpUpload(localPath, target, remotePath, port, sshKey string) error {
	args := scpArgs(port, sshKey)
	args = append(args, localPath, target+":"+remotePath)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "scp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// bundleScpDownload copies user@host:/path to a local file using scp.
func bundleScpDownload(target, remotePath, localPath, port, sshKey string) error {
	args := scpArgs(port, sshKey)
	args = append(args, target+":"+remotePath, localPath)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "scp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// scpArgs builds the common scp flag list for a given port and key path.
func scpArgs(port, sshKey string) []string {
	args := []string{"-q", "-o", "StrictHostKeyChecking=accept-new"}
	if port != "" && port != "22" {
		args = append(args, "-P", port)
	}
	if sshKey != "" {
		args = append(args, "-i", sshKey)
	}
	return args
}
