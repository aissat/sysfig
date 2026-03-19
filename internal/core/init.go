package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sysfig-dev/sysfig/internal/crypto"
	sysfigfs "github.com/sysfig-dev/sysfig/internal/fs"
	"github.com/sysfig-dev/sysfig/internal/state"
	"github.com/sysfig-dev/sysfig/pkg/types"
)

// gitBareRun and gitBareOutput are the package-level bare-repo git helpers.
// Their canonical definitions live in git_helpers.go and are shared by
// init.go, track.go, and any other files in this package that need them.

// InitOptions configures a sysfig init operation.
type InitOptions struct {
	// BaseDir is where sysfig stores its data (default: ~/.sysfig)
	BaseDir string
	// RepoDir is the bare git repo directory (default: <BaseDir>/repo.git)
	RepoDir string
	// Encrypt enables encryption-by-default in the generated sysfig.yaml
	Encrypt bool
}

// InitResult reports what was created.
type InitResult struct {
	BaseDir        string
	RepoDir        string
	BackupsDir     string
	KeysDir        string
	StateFile      string
	ManifestFile   string // logical display path: <RepoDir>::HEAD:sysfig.yaml
	HooksExample   string // logical display path: <RepoDir>::HEAD:hooks.yaml.example
	MasterKeyPath  string // non-empty only when a new key was generated
	AlreadyExisted bool   // true if BaseDir already existed
}

const defaultManifest = `version: "1.0"
variables: {}
settings:
  backup:
    max_count: 10
    max_age: "30d"
    auto_cleanup: true
  security:
    encrypt_by_default: false
    require_signed_commits: false
    auto_backup_before_apply: true
  git:
    auto_commit: false
tracked_files: []
`

const manifestWithEncrypt = `version: "1.0"
variables: {}
settings:
  backup:
    max_count: 10
    max_age: "30d"
    auto_cleanup: true
  security:
    encrypt_by_default: true
    require_signed_commits: false
    auto_backup_before_apply: true
  git:
    auto_commit: false
tracked_files: []
`

const hooksExampleContent = `# sysfig hooks configuration example
# Copy this file to ~/.sysfig/hooks.yaml and customize it.
# hooks.yaml is local-only — it is never committed to the repo.
#
# Each hook has:
#   on:      list of tracked file IDs that trigger this hook (use sysfig status to see IDs)
#   type:    exec | systemd_reload | systemd_restart
#   cmd:     [binary, arg, ...] for exec hooks — binary must be in the allowlist
#   service: unit name for systemd_reload / systemd_restart
#
# Hooks run after sysfig apply writes the file to disk.
# To add a new binary to the exec allowlist edit execAllowlist in internal/core/hooks.go.

# allowlist: binaries permitted in exec hooks — add any tool you need here.
# allowlist: [nginx, sshd, apachectl, haproxy, postfix]

hooks:
  # nginx_validate:
  #   on: [etc_nginx_nginx_conf]
  #   type: exec
  #   cmd: [nginx, -t]
  #
  # nginx_reload:
  #   on: [etc_nginx_nginx_conf]
  #   type: systemd_reload
  #   service: nginx
  #
  # sshd_validate:
  #   on: [etc_ssh_sshd_config]
  #   type: exec
  #   cmd: [sshd, -t]
  #
  # apache_validate:
  #   on: [etc_apache2_apache2_conf]
  #   type: exec
  #   cmd: [apachectl, -t]
  #
  # haproxy_reload:
  #   on: [etc_haproxy_haproxy_cfg]
  #   type: systemd_reload
  #   service: haproxy
`

// isBareRepoInitialised returns true when repoDir already contains a bare git
// repo (i.e. the HEAD file is present inside the directory itself, not in a
// nested .git sub-directory).
func isBareRepoInitialised(repoDir string) bool {
	_, err := os.Stat(filepath.Join(repoDir, "HEAD"))
	return err == nil
}

// initBareRepo runs `git init --bare <repoDir>` with a 30-second timeout.
func initBareRepo(repoDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "init", "--bare", repoDir)
	cmd.Env = os.Environ()
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init --bare %q: %w\n%s", repoDir, err, buf.String())
	}
	// git init --bare creates the directory with 0755; tighten it to 0700 so
	// that the bare repo has the same restrictive permissions as all other
	// sysfig-managed directories under BaseDir.
	if err := os.Chmod(repoDir, 0o700); err != nil {
		return fmt.Errorf("chmod bare repo %q: %w", repoDir, err)
	}
	return nil
}

// commitFilesToBareRepo writes the provided files to a temporary directory,
// stages them via GIT_WORK_TREE=<tmpDir> GIT_DIR=<repoDir>, and creates a
// commit.  The temporary directory is always cleaned up, even on error.
//
// files is a map of relative path (e.g. "sysfig.yaml") → file content.
// commitMsg is the git commit message.
func commitFilesToBareRepo(repoDir, commitMsg string, files map[string]string) error {
	// Create a temp dir to act as the transient work tree.
	tmpDir, err := os.MkdirTemp("", "sysfig-init-worktree-*")
	if err != nil {
		return fmt.Errorf("create temp worktree: %w", err)
	}
	defer os.RemoveAll(tmpDir) // always clean up

	// Write each file into the temp dir, creating sub-directories as needed.
	for relPath, content := range files {
		destPath := filepath.Join(tmpDir, relPath)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
			return fmt.Errorf("mkdir for %q: %w", relPath, err)
		}
		if err := sysfigfs.WriteFileAtomic(destPath, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write %q to temp worktree: %w", relPath, err)
		}
	}

	// Collect the list of relative paths to pass to `git add`.
	relPaths := make([]string, 0, len(files))
	for relPath := range files {
		relPaths = append(relPaths, relPath)
	}

	// env overrides that set the transient work tree.
	wtEnv := []string{"GIT_WORK_TREE=" + tmpDir}

	// Stage the files.
	addArgs := append([]string{"add", "--"}, relPaths...)
	if err := gitBareRun(repoDir, 30*time.Second, wtEnv, addArgs...); err != nil {
		return fmt.Errorf("stage files: %w", err)
	}

	// Commit.  We supply a minimal author identity via env vars so that the
	// commit succeeds even when no global git config is present (e.g. in a CI
	// environment or a brand-new user account).
	commitEnv := append(wtEnv,
		"GIT_AUTHOR_NAME=sysfig",
		"GIT_AUTHOR_EMAIL=sysfig@localhost",
		"GIT_COMMITTER_NAME=sysfig",
		"GIT_COMMITTER_EMAIL=sysfig@localhost",
	)
	if err := gitBareRun(repoDir, 30*time.Second, commitEnv,
		"commit", "--allow-empty", "-m", commitMsg,
	); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// bareRepoHasFile returns true when the given relative path exists as a blob
// reachable from HEAD in the bare repo.  It is used for idempotency checks so
// that a second `sysfig init` does not re-commit the same files.
func bareRepoHasFile(repoDir, relPath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git",
		"--git-dir="+repoDir,
		"cat-file", "-e", "HEAD:"+relPath,
	)
	cmd.Env = os.Environ()
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	return cmd.Run() == nil
}

// Init initialises a new sysfig environment:
//
//  1. Creates BaseDir, <BaseDir>/backups, <BaseDir>/keys (mode 0700).
//  2. Runs `git init --bare <RepoDir>` (only if the bare repo does not already
//     exist — checked by the presence of a HEAD file inside RepoDir).
//  3. Commits sysfig.yaml and hooks.yaml.example INTO the bare repo (only if
//     they are not already present in HEAD).  No loose files are written inside
//     repo.git/.
//  4. Generates a master age key when Encrypt == true (idempotent).
//  5. Creates state.json via state.Manager if it does not exist.
//
// Returns InitResult describing everything that was created/found.
func Init(opts InitOptions) (*InitResult, error) {
	// ── 1. Resolve defaults ────────────────────────────────────────────────
	if opts.BaseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("core: init: %w", err)
		}
		opts.BaseDir = filepath.Join(home, ".sysfig")
	}
	if opts.RepoDir == "" {
		opts.RepoDir = filepath.Join(opts.BaseDir, "repo.git")
	}

	backupsDir := filepath.Join(opts.BaseDir, "backups")
	keysDir := filepath.Join(opts.BaseDir, "keys")
	stateFile := filepath.Join(opts.BaseDir, "state.json")

	// Logical display paths (the files live as git objects, not loose files).
	manifestLogical := opts.RepoDir + ":HEAD:sysfig.yaml"
	hooksLogical := opts.RepoDir + ":HEAD:hooks.yaml.example"

	// Detect whether BaseDir already existed before we create anything.
	alreadyExisted := false
	if _, err := os.Stat(opts.BaseDir); err == nil {
		alreadyExisted = true
	}

	// ── 2. Create required directories (mode 0700) ─────────────────────────
	// Note: opts.RepoDir is intentionally NOT in this list — `git init --bare`
	// creates it.  We only ensure the parent BaseDir exists first so that git
	// can place repo.git inside it.
	for _, dir := range []string{opts.BaseDir, backupsDir, keysDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("core: init: %w", err)
		}
	}

	// ── 3. Initialise the bare git repo ────────────────────────────────────
	if !isBareRepoInitialised(opts.RepoDir) {
		if err := initBareRepo(opts.RepoDir); err != nil {
			return nil, fmt.Errorf("core: init: %w", err)
		}
	}

	// ── 4. Commit sysfig.yaml and hooks.yaml.example into the bare repo ────
	//
	// We check whether the files are already present in HEAD to make Init
	// idempotent: a second call must not create a duplicate commit.
	manifestPresent := bareRepoHasFile(opts.RepoDir, "sysfig.yaml")
	hooksPresent := bareRepoHasFile(opts.RepoDir, "hooks.yaml.example")

	if !manifestPresent || !hooksPresent {
		filesToCommit := make(map[string]string)

		if !manifestPresent {
			content := defaultManifest
			if opts.Encrypt {
				content = manifestWithEncrypt
			}
			filesToCommit["sysfig.yaml"] = content
		}

		if !hooksPresent {
			filesToCommit["hooks.yaml.example"] = hooksExampleContent
		}

		if err := commitFilesToBareRepo(
			opts.RepoDir,
			"sysfig: initialise configuration",
			filesToCommit,
		); err != nil {
			return nil, fmt.Errorf("core: init: commit config files: %w", err)
		}
	}

	// ── 5. Generate a master age key when encryption is requested ──────────
	masterKeyPath := ""
	if opts.Encrypt {
		km := crypto.NewKeyManager(keysDir)
		// Generate returns an error if the key already exists — that's fine;
		// it means init is being re-run on an existing encrypted environment.
		if _, err := km.Generate(); err == nil {
			masterKeyPath = crypto.MasterKeyPath(keysDir)
		}
	}

	// ── 6. Initialise state.json via state.Manager if it doesn't exist ─────
	// Calling WithLock with a no-op is sufficient to create and persist the
	// initial empty state to disk.
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		sm := state.NewManager(stateFile)
		if err := sm.WithLock(func(_ *types.State) error {
			return nil
		}); err != nil {
			return nil, fmt.Errorf("core: init: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("core: init: %w", err)
	}

	return &InitResult{
		BaseDir:        opts.BaseDir,
		RepoDir:        opts.RepoDir,
		BackupsDir:     backupsDir,
		KeysDir:        keysDir,
		StateFile:      stateFile,
		ManifestFile:   manifestLogical,
		HooksExample:   hooksLogical,
		MasterKeyPath:  masterKeyPath,
		AlreadyExisted: alreadyExisted,
	}, nil
}
