package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
	"gopkg.in/yaml.v3"
)

// CloneOptions configures a sysfig clone operation.
type CloneOptions struct {
	// RemoteURL is the git remote to clone from (SSH or HTTPS).
	RemoteURL string

	// BaseDir is where sysfig stores its data (default: ~/.sysfig).
	// The bare shadow repo will live at <BaseDir>/repo.git.
	BaseDir string

	// ConfigsOnly skips package installation and only applies config files.
	ConfigsOnly bool

	// SkipEncrypted silently skips encrypted files when no master key is present.
	SkipEncrypted bool

	// Yes skips interactive confirmation prompts (non-interactive / script mode).
	Yes bool
}

// CloneResult reports the outcome of a clone operation.
type CloneResult struct {
	BaseDir       string
	RepoDir       string
	HooksWarning  string // non-empty if hooks.yaml.example was copied to hooks.yaml
	Seeded        int    // number of FileRecords seeded into state.json from manifest
	AlreadyExisted bool  // true if the bare repo was already present (no-op clone)
}

// manifestFile is the minimal shape of sysfig.yaml needed to seed state.json.
type manifestFile struct {
	TrackedFiles []manifestEntry `yaml:"tracked_files"`
}

type manifestEntry struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	SystemPath  string   `yaml:"system_path"`
	RepoPath    string   `yaml:"repo_path"`
	Branch      string   `yaml:"branch"`
	Group       string   `yaml:"group"`
	Encrypt     bool     `yaml:"encrypt"`
	Template    bool     `yaml:"template"`
	Encryption  struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"encryption"`
	Tags []string `yaml:"tags"`
}

// isGitRepo returns true if dir contains a HEAD file, which is present in both
// regular repos (inside .git/) and bare repos (at the root of the repo dir).
// For a bare repo at <dir>, HEAD lives directly at <dir>/HEAD.
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "HEAD"))
	return err == nil
}

// gitClone runs `git clone --bare <remoteURL> <destDir>` with a 120 s network
// timeout, producing a bare repository with no checked-out working tree.
func gitClone(remoteURL, destDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", remoteURL, destDir)
	cmd.Env = os.Environ()
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone --bare failed: %w\n%s", err, buf.String())
	}
	return nil
}

// gitShow reads the content of a file at a given git ref from a bare repo.
// It is equivalent to: git --git-dir=<repoDir> show <ref>:<relPath>
// The returned []byte is the raw file content; it is never nil on success.
func gitShow(repoDir, ref, relPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	objectRef := ref + ":" + relPath

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git",
		"--git-dir="+repoDir,
		"show", objectRef,
	)
	cmd.Env = os.Environ()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %q: %w\n%s", objectRef, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// gitShowHash returns the BLAKE3 hash of a file stored in the bare repo,
// computed by streaming the object content through hash.Bytes.
// It avoids writing the content to disk just to hash it.
func gitShowHash(repoDir, ref, relPath string) (string, error) {
	data, err := gitShow(repoDir, ref, relPath)
	if err != nil {
		return "", err
	}
	return hash.Bytes(data), nil
}

// Clone bootstraps a sysfig environment from a remote git repository.
//
// Design principle — offline safety:
//
//	The shadow repo is a bare git repository on disk. sysfig never touches the
//	remote automatically. Network operations (push/pull) only happen when the
//	user explicitly runs `sysfig push` or `sysfig pull`. This means:
//
//	  • track, apply, sync, status — all work 100% offline.
//	  • clone — one-time bootstrap; clones only if the bare repo does NOT exist
//	    yet. If the repo already exists locally, clone is a no-op (user must
//	    run `sysfig pull` to fetch new changes from the remote).
//
// Steps:
//
//  1. Create BaseDir, <BaseDir>/backups, <BaseDir>/keys (mode 0700).
//  2. If <BaseDir>/repo.git does not contain a bare git repo:
//     `git clone --bare <RemoteURL> <repoDir>`.
//     If it already exists: skip (offline-safe; no silent pull).
//  3. Seed state.json from sysfig.yaml manifest read from the bare repo via
//     `git show HEAD:sysfig.yaml` (idempotent — skips existing IDs).
//     FileRecord.RepoPath is stored as the git-relative path (no leading slash,
//     e.g. "etc/nginx/nginx.conf"). Callers that need the file content use
//     gitShow(repoDir, "HEAD", repoRelPath).
//  4. Copy hooks.yaml.example → <BaseDir>/hooks.yaml (only if hooks.yaml absent).
func Clone(opts CloneOptions) (*CloneResult, error) {
	// ── 1. Resolve defaults ────────────────────────────────────────────────
	if opts.BaseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("core: clone: resolve home dir: %w", err)
		}
		opts.BaseDir = filepath.Join(home, ".sysfig")
	}

	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	backupsDir := filepath.Join(opts.BaseDir, "backups")
	keysDir := filepath.Join(opts.BaseDir, "keys")
	stateFile := filepath.Join(opts.BaseDir, "state.json")

	// ── 2. Create base directories (mode 0700) ─────────────────────────────
	// Note: repoDir is intentionally excluded — `git clone --bare` creates it.
	// We only need BaseDir to exist so git can place repo.git inside it.
	for _, dir := range []string{opts.BaseDir, backupsDir, keysDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("core: clone: create dir %q: %w", dir, err)
		}
	}

	// ── 3. Clone if the bare shadow repo does not exist yet ────────────────
	//
	// If it already exists we do NOT pull automatically. The local repo is the
	// source of truth for offline work. The user explicitly runs `sysfig pull`
	// when they want to sync with the remote. Silently pulling here would
	// destroy offline changes or cause unexpected divergence.
	alreadyExisted := isGitRepo(repoDir)
	if !alreadyExisted {
		if opts.RemoteURL == "" {
			return nil, fmt.Errorf("core: clone: no remote URL provided and no local repo found at %q", repoDir)
		}
		if ParseRemoteKind(opts.RemoteURL) != RemoteGit {
			// Bundle remote: init a bare repo, record the remote URL, then pull.
			if err := bundleBootstrap(opts.RemoteURL, repoDir); err != nil {
				return nil, fmt.Errorf("core: clone: bundle bootstrap: %w", err)
			}
		} else {
			if err := gitClone(opts.RemoteURL, repoDir); err != nil {
				return nil, fmt.Errorf("core: clone: %w", err)
			}
		}
	}
	// else: bare repo already exists locally → nothing to do here.

	// ── 4. Seed state.json from sysfig.yaml manifest ───────────────────────
	//
	// state.json is the *local cache*. After a fresh clone it is empty, so we
	// populate it from the manifest so that `apply` and `status` work
	// immediately without requiring a separate `sysfig track` for every file.
	//
	// FileRecord.RepoPath stores the git-relative path only (no leading slash,
	// e.g. "etc/nginx/nginx.conf"). The bare repo is always at
	// <BaseDir>/repo.git. Any operation that needs to read file content calls
	// gitShow(repoDir, "HEAD", rec.RepoPath).
	seeded := 0
	sm := state.NewManager(stateFile)

	if err := sm.WithLock(func(s *types.State) error {
		// Read sysfig.yaml from the manifest branch (branch-per-track arch).
		// Fall back to HEAD for repos created before the branch-per-track migration.
		data, err := gitShow(repoDir, "manifest", "sysfig.yaml")
		if err != nil {
			data, err = gitShow(repoDir, "HEAD", "sysfig.yaml")
		}
		if err != nil {
			// sysfig.yaml not present in the remote repo — nothing to seed.
			// state.json is still initialised (empty) by WithLock.
			return nil
		}

		var mf manifestFile
		if err := yaml.Unmarshal(data, &mf); err != nil {
			return fmt.Errorf("parse manifest: %w", err)
		}

		now := time.Now()
		for _, entry := range mf.TrackedFiles {
			if entry.ID == "" || entry.SystemPath == "" {
				continue // skip malformed entries silently
			}
			// Idempotent: skip IDs that are already in state.
			if _, exists := s.Files[entry.ID]; exists {
				continue
			}

			// Derive the git-relative repo path.
			// If the manifest already has a repo_path, use it (stripped of
			// any leading slash so it is safe for git object references).
			// Fall back to deriving the path from system_path.
			repoRelPath := entry.RepoPath
			if repoRelPath == "" {
				repoRelPath = strings.TrimPrefix(entry.SystemPath, "/")
			} else {
				repoRelPath = strings.TrimPrefix(repoRelPath, "/")
			}

			// Attempt to hash the repo blob — gives us a baseline for
			// `sysfig status` comparisons even before `apply` has run.
			// A missing blob is non-fatal (the file may not have been
			// tracked yet in this repo).
			fileHash := ""
			if h, err := gitShowHash(repoDir, "HEAD", repoRelPath); err == nil {
				fileHash = h
			}

			s.Files[entry.ID] = &types.FileRecord{
				ID:          entry.ID,
				SystemPath:  entry.SystemPath,
				RepoPath:    repoRelPath, // git-relative, no leading slash
				Branch:      entry.Branch,
				Group:       entry.Group,
				CurrentHash: fileHash,
				LastSync:    &now,
				Status:      types.StatusTracked,
				Encrypt:     entry.Encrypt || entry.Encryption.Enabled,
				Template:    entry.Template,
				Tags:        entry.Tags,
			}
			seeded++
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("core: clone: seed state: %w", err)
	}

	// ── 5. Copy hooks.yaml.example → hooks.yaml if present ────────────────
	//
	// hooks.yaml lives outside the bare repo (in BaseDir) so it is never
	// committed to git — hooks are intentionally local-only per the security
	// model.  We read hooks.yaml.example from the bare repo and write it as a
	// loose file in BaseDir.
	hooksWarning := ""
	hooksDest := filepath.Join(opts.BaseDir, "hooks.yaml")

	// Only create hooks.yaml if it does not already exist — never overwrite
	// user edits.
	if _, err := os.Stat(hooksDest); os.IsNotExist(err) {
		exampleData, gitErr := gitShow(repoDir, "HEAD", "hooks.yaml.example")
		if gitErr == nil {
			// hooks.yaml.example exists in the bare repo — write it out.
			if err := sysfigfs.WriteFileAtomic(hooksDest, exampleData, 0o600); err != nil {
				return nil, fmt.Errorf("core: clone: write hooks.yaml: %w", err)
			}
			hooksWarning = fmt.Sprintf(
				"⚠  hooks.yaml created from template.\n"+
					"   Review it before running any apply commands:\n"+
					"     %s", hooksDest,
			)
		}
		// If hooks.yaml.example is absent from the repo, silently skip.
	}

	return &CloneResult{
		BaseDir:        opts.BaseDir,
		RepoDir:        repoDir,
		HooksWarning:   hooksWarning,
		Seeded:         seeded,
		AlreadyExisted: alreadyExisted,
	}, nil
}
