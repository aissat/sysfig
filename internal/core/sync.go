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

	"github.com/sysfig-dev/sysfig/internal/crypto"
	sysfigfs "github.com/sysfig-dev/sysfig/internal/fs"
	"github.com/sysfig-dev/sysfig/internal/hash"
	"github.com/sysfig-dev/sysfig/internal/state"
	"github.com/sysfig-dev/sysfig/pkg/types"
)

// gitRun executes an arbitrary git command with cmd.Dir = repoDir.
// Used for push/pull/fetch where the working directory is sufficient.
// Captures combined stdout+stderr and wraps any non-zero exit in an error.
func gitRun(repoDir string, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, buf.String())
	}
	return nil
}

// syncGitBareRun executes a git command against the bare repo, setting
// GIT_DIR=repoDir plus any additional env vars in extraEnv (e.g.
// []string{"GIT_WORK_TREE=/"}).  Combined stdout+stderr is captured and
// included in any error message.
func syncGitBareRun(repoDir string, timeout time.Duration, extraEnv []string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), append([]string{"GIT_DIR=" + repoDir}, extraEnv...)...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, buf.String())
	}
	return nil
}

// syncGitBareOutput is like syncGitBareRun but returns stdout bytes.
func syncGitBareOutput(repoDir string, timeout time.Duration, extraEnv []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), append([]string{"GIT_DIR=" + repoDir}, extraEnv...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %v: %w\n%s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// syncStagePlain stages the real on-disk file at /<relPath> directly into
// the bare repo index using GIT_WORK_TREE=/.
//
//	GIT_DIR=repoDir GIT_WORK_TREE=/ git add <relPath>
func syncStagePlain(repoDir, relPath string) error {
	if err := syncGitBareRun(repoDir, 30*time.Second,
		[]string{"GIT_WORK_TREE=/"},
		"add", relPath,
	); err != nil {
		return fmt.Errorf("core: sync: stage plain %q: %w", relPath, err)
	}
	return nil
}

// syncStageBlob stores data as a git blob object and adds it to the bare repo
// index under relPath.  Used for encrypted files and sandbox (SysRoot) runs.
//
// Steps:
//  1. git hash-object -w <tmpfile>  → blob hash
//  2. git update-index --add --cacheinfo 100644,<blobHash>,<relPath>
func syncStageBlob(repoDir, relPath string, data []byte) error {
	// Write to a temp file so hash-object can read it.
	tmp, err := os.CreateTemp("", "sysfig-sync-blob-*")
	if err != nil {
		return fmt.Errorf("core: sync: stage blob %q: create temp: %w", relPath, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("core: sync: stage blob %q: write temp: %w", relPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("core: sync: stage blob %q: close temp: %w", relPath, err)
	}

	// 1. Store the blob, get its SHA.
	out, err := syncGitBareOutput(repoDir, 15*time.Second, nil,
		"hash-object", "-w", tmpPath,
	)
	if err != nil {
		return fmt.Errorf("core: sync: stage blob %q: hash-object: %w", relPath, err)
	}
	blobHash := strings.TrimSpace(string(out))
	if blobHash == "" {
		return fmt.Errorf("core: sync: stage blob %q: hash-object returned empty hash", relPath)
	}

	// 2. Add blob to the index.
	cacheinfo := "100644," + blobHash + "," + relPath
	if err := syncGitBareRun(repoDir, 15*time.Second, nil,
		"update-index", "--add", "--cacheinfo", cacheinfo,
	); err != nil {
		return fmt.Errorf("core: sync: stage blob %q: update-index: %w", relPath, err)
	}
	return nil
}

// SyncOptions configures a sysfig commit+push (sync) operation.
type SyncOptions struct {
	// BaseDir is the sysfig data directory (e.g. ~/.sysfig).
	// The bare repo is expected at <BaseDir>/repo.git.
	BaseDir string

	// Message is the git commit message.
	// Defaults to "sysfig: sync <timestamp>" if empty.
	Message string

	// Pull, when true, runs `git pull` from the remote BEFORE staging and
	// committing local changes. Useful for a full round-trip in one command.
	// Pull failure is non-fatal — sync continues with the local repo.
	Pull bool

	// Push, when true, also runs `git push` after the commit.
	// When false (default), changes are committed locally only — safe offline.
	Push bool

	// SysRoot, when non-empty, is prepended to every system path when
	// re-hashing files after a commit. This mirrors the --sys-root sandbox
	// override used by `sysfig status` and `sysfig apply`.
	SysRoot string
}

// SyncResult reports what happened during a sync.
type SyncResult struct {
	RepoDir      string
	Pulled       bool     // true if pull ran and succeeded
	PullErr      error    // non-nil if pull was attempted but failed (non-fatal)
	Committed    bool     // true if a commit was created (i.e. there were staged changes)
	Pushed       bool     // true if push was attempted and succeeded
	Message      string
	UpdatedFiles []string // IDs of files whose state hash was refreshed
}

// Sync stages all tracked files into the bare repo and creates a git commit.
// If opts.Push is true it also pushes to the configured remote.
//
// Design principle — offline safety:
//
//	Sync (commit) is always local. Pushing is opt-in. This means you can
//	run `sysfig sync` on an air-gapped machine and the commit is recorded
//	locally. Run `sysfig push` (or `sysfig sync --push`) when online.
func Sync(opts SyncOptions) (*SyncResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: sync: BaseDir must not be empty")
	}

	repoDir := filepath.Join(opts.BaseDir, "repo.git")

	msg := opts.Message
	if msg == "" {
		msg = fmt.Sprintf("sysfig: sync %s", time.Now().UTC().Format(time.RFC3339))
	}

	result := &SyncResult{
		RepoDir: repoDir,
		Message: msg,
	}

	// Optional pull before committing local changes.
	if opts.Pull {
		pr, err := Pull(PullOptions{BaseDir: opts.BaseDir})
		if err != nil {
			// Non-fatal — note the error and continue with local repo.
			result.PullErr = err
		} else {
			result.Pulled = !pr.AlreadyUpToDate
		}
	}

	// Load state so we know which files to stage.
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	currentState, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: sync: load state: %w", err)
	}

	// Stage each tracked file individually into the bare repo.
	// We never do `git add -A` against the bare repo — that would try to walk
	// the entire filesystem through GIT_WORK_TREE=/ which is not what we want.
	for id, rec := range currentState.Files {
		relPath := rec.RepoPath // git-relative, e.g. "etc/nginx/nginx.conf"

		if rec.Encrypt {
			// Encrypted files: read the live system file, re-encrypt, and
			// store the ciphertext as a blob.
			sysPath := rec.SystemPath
			if opts.SysRoot != "" {
				sysPath = filepath.Join(opts.SysRoot, rec.SystemPath)
			}

			plaintext, err := os.ReadFile(sysPath)
			if err != nil {
				// System file missing — skip this record gracefully.
				continue
			}

			keysDir := filepath.Join(opts.BaseDir, "keys")
			km := crypto.NewKeyManager(keysDir)
			master, err := km.Load()
			if err != nil {
				return nil, fmt.Errorf("core: sync: encrypt %q: master key not found: %w", id, err)
			}
			encrypted, err := crypto.EncryptForFile(plaintext, master, id)
			if err != nil {
				return nil, fmt.Errorf("core: sync: encrypt %q: %w", id, err)
			}

			if err := syncStageBlob(repoDir, relPath, encrypted); err != nil {
				return nil, err
			}
		} else {
			// Plaintext files.
			// When SysRoot is empty (production) the real file lives exactly
			// at /<relPath>, so GIT_WORK_TREE=/ git add <relPath> works.
			// When SysRoot is set (sandbox/test) the real file lives at
			// opts.SysRoot+rec.SystemPath, so we read it and store as a blob.
			if opts.SysRoot == "" {
				if err := syncStagePlain(repoDir, relPath); err != nil {
					return nil, err
				}
			} else {
				sysPath := filepath.Join(opts.SysRoot, rec.SystemPath)
				data, err := os.ReadFile(sysPath)
				if err != nil {
					continue
				}
				if err := syncStageBlob(repoDir, relPath, data); err != nil {
					return nil, err
				}
			}
		}
	}

	// Commit. `git commit` exits non-zero when there is nothing to commit;
	// we treat that as a successful no-op (Committed stays false).
	commitErr := syncGitBareRun(repoDir, 30*time.Second,
		[]string{"GIT_WORK_TREE=/"},
		"commit", "-m", msg,
	)
	if commitErr != nil {
		// "nothing to commit" is not an error for sysfig.
		if !isNothingToCommit(repoDir) {
			return nil, fmt.Errorf("core: sync: commit: %w", commitErr)
		}
		// Fall through — still refresh state hashes even when there was
		// nothing new to commit (e.g. git already has the change but
		// state.json was never updated from a previous run).
	} else {
		result.Committed = true
	}

	// Refresh state.json so that current_hash reflects what is now committed.
	// This runs whether or not a new commit was created.
	_ = sm.WithLock(func(s *types.State) error {
		now := time.Now()
		for id, rec := range s.Files {
			// Resolve the real on-disk path, honouring the sandbox SysRoot.
			sysPath := rec.SystemPath
			if opts.SysRoot != "" {
				sysPath = filepath.Join(opts.SysRoot, rec.SystemPath)
			}

			if rec.Encrypt {
				// Encrypted files: hash the plaintext system file.
				// We cannot recover plaintext from the repo ciphertext,
				// so we re-hash the live system file directly.
				sysHash, err := hash.File(sysPath)
				if err != nil {
					// System file missing or unreadable — leave hash alone.
					continue
				}
				rec.CurrentHash = sysHash
				rec.LastSync = &now
				if newMeta, err := sysfigfs.ReadMeta(sysPath); err == nil {
					rec.Meta = newMeta
				}
				s.Files[id] = rec
				result.UpdatedFiles = append(result.UpdatedFiles, id)
			} else {
				// Plain files: read the committed blob from the bare repo and
				// compare its hash to the live system file.  Only update the
				// record when they agree — if they differ the user still needs
				// to run `sysfig apply`.
				repoContent, err := gitShowBytes(repoDir, rec.RepoPath)
				if err != nil {
					// File not yet committed (e.g. HEAD doesn't exist yet).
					continue
				}
				repoHash := hash.Bytes(repoContent)

				sysHash, err := hash.File(sysPath)
				if err != nil {
					continue
				}

				if repoHash == sysHash {
					rec.CurrentHash = sysHash
					rec.LastSync = &now
					if newMeta, err := sysfigfs.ReadMeta(sysPath); err == nil {
						rec.Meta = newMeta
					}
					s.Files[id] = rec
					result.UpdatedFiles = append(result.UpdatedFiles, id)
				}
			}
		}
		return nil
	})

	// Push only when explicitly requested — never automatic.
	if opts.Push {
		// For bare repos, push works fine with just GIT_DIR set.
		if err := syncGitBareRun(repoDir, 60*time.Second, nil, "push"); err != nil {
			return nil, fmt.Errorf("core: sync: push: %w", err)
		}
		result.Pushed = true
	}

	return result, nil
}

// PushOptions configures a `sysfig push` operation.
type PushOptions struct {
	BaseDir string
}

// Push pushes the local bare repo commits to the configured remote.
// This is the only place sysfig initiates outbound network traffic for git.
// It will fail gracefully when offline — the local commits are preserved.
func Push(opts PushOptions) error {
	if opts.BaseDir == "" {
		return fmt.Errorf("core: push: BaseDir must not be empty")
	}
	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	// GIT_DIR is enough for push — no worktree needed.
	if err := syncGitBareRun(repoDir, 60*time.Second, nil, "push"); err != nil {
		return fmt.Errorf("core: push: %w", err)
	}
	return nil
}

// PullOptions configures a `sysfig pull` operation.
type PullOptions struct {
	BaseDir string
}

// PullResult reports what happened during a pull.
type PullResult struct {
	RepoDir         string
	AlreadyUpToDate bool
}

// Pull fetches updates from the remote into the local bare repo.
// For a bare repo we use `git fetch origin` (there is no working tree to merge
// into). Callers that need the content apply it via `sysfig apply`.
//
// Design principle — offline safety:
//
//	Pull is always explicit. sysfig never calls Pull automatically (not during
//	apply, status, track, or sync). If the machine is offline, Pull returns a
//	clear network error and all local operations continue to work.
func Pull(opts PullOptions) (*PullResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: pull: BaseDir must not be empty")
	}

	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	result := &PullResult{RepoDir: repoDir}

	// Record the HEAD SHA before fetching so we can detect whether anything
	// actually changed (more reliable than parsing git's output).
	headBefore, _ := gitBareOutput(repoDir, 5*time.Second, nil,
		"rev-parse", "--verify", "HEAD")

	// Fetch from the named remote "origin".  Using "origin" explicitly (rather
	// than --all) means we get a clear error when no remote is configured.
	if err := gitBareRun(repoDir, 60*time.Second, nil, "fetch", "origin"); err != nil {
		return nil, fmt.Errorf("core: pull: %w", err)
	}

	// Advance the local branch ref to match its remote tracking counterpart
	// so that `git show HEAD:<path>` reflects the fetched content.
	_ = advanceBareHEAD(repoDir)

	// Compare HEAD before and after; identical SHA means nothing new arrived.
	headAfter, _ := gitBareOutput(repoDir, 5*time.Second, nil,
		"rev-parse", "--verify", "HEAD")
	result.AlreadyUpToDate = bytes.Equal(
		bytes.TrimSpace(headBefore),
		bytes.TrimSpace(headAfter),
	)

	return result, nil
}

// advanceBareHEAD fast-forwards the current branch in the bare repo after a
// fetch. It resolves HEAD to a branch name, then tries candidate source refs
// in order until one succeeds:
//
//  1. FETCH_HEAD — always written by `git fetch`, works regardless of
//     whether the remote tracking refs are under refs/remotes/origin/* (regular
//     clone behaviour) or refs/heads/* (bare clone default behaviour).
//  2. refs/remotes/origin/<branch> — fallback for repos configured with a
//     standard non-bare remote fetch refspec.
//
// This is best-effort; errors are returned but callers may ignore them since
// the objects are already present and a failed advance only delays visibility
// until the next explicit operation.
func advanceBareHEAD(repoDir string) error {
	// Resolve HEAD → branch name.
	branchBytes, err := gitBareOutput(repoDir, 10*time.Second, nil,
		"symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err // detached HEAD or uninitialised repo
	}
	branch := strings.TrimSpace(string(branchBytes))
	if branch == "" {
		return fmt.Errorf("advanceBareHEAD: empty branch name")
	}

	// Try each candidate source in preference order.
	candidates := []string{
		"FETCH_HEAD",
		"refs/remotes/origin/" + branch,
	}
	for _, src := range candidates {
		if err := gitBareRun(repoDir, 10*time.Second, nil,
			"update-ref", "refs/heads/"+branch, src,
		); err == nil {
			return nil
		}
	}
	return fmt.Errorf("advanceBareHEAD: could not advance refs/heads/%s", branch)
}

// isNothingToCommit returns true when the bare repo index has no staged
// changes relative to HEAD.
//
// We use `git diff --cached --quiet` rather than `git status --porcelain`
// because status (with GIT_WORK_TREE=/) also reports unstaged worktree
// differences which are irrelevant here — we only care whether the index
// has changes that would be captured by a `git commit`.
//
// Exit codes: 0 = nothing staged (index == HEAD), 1 = staged changes exist.
func isNothingToCommit(repoDir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)

	err := cmd.Run()
	if err == nil {
		return true // exit 0 → index matches HEAD, nothing staged
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false // exit 1 → staged changes present
	}
	return false // unexpected error → assume not clean
}

