package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aissat/sysfig/internal/crypto"
	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
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
		if isGitNotFound(err) {
			return fmt.Errorf("git not found on $PATH — install git (e.g. sudo apt install git): %w", err)
		}
		return fmt.Errorf("git %v: %w\n%s", args, err, buf.String())
	}
	return nil
}

// isGitNotFound returns true when the error is caused by the git binary not
// being present on $PATH, so callers can show a clear actionable message.
func isGitNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
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
// syncHashBlob stores data as a git blob object and returns its 40-char SHA.
// The blob is written to the object store but NOT added to any index.
func syncHashBlob(repoDir string, data []byte) (string, error) {
	tmp, err := os.CreateTemp("", "sysfig-sync-blob-*")
	if err != nil {
		return "", fmt.Errorf("core: sync: hash blob: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("core: sync: hash blob: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("core: sync: hash blob: close temp: %w", err)
	}

	out, err := syncGitBareOutput(repoDir, 15*time.Second, nil, "hash-object", "-w", tmpPath)
	if err != nil {
		return "", fmt.Errorf("core: sync: hash blob: hash-object: %w", err)
	}
	blobHash := strings.TrimSpace(string(out))
	if blobHash == "" {
		return "", fmt.Errorf("core: sync: hash blob: hash-object returned empty hash")
	}
	return blobHash, nil
}

func syncStageBlob(repoDir, relPath string, data []byte) error {
	blobHash, err := syncHashBlob(repoDir, data)
	if err != nil {
		return fmt.Errorf("core: sync: stage blob %q: %w", relPath, err)
	}
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

	// Force, when true, passes --force to git push.
	// Use for first-push to a non-empty remote (e.g. a fresh GitHub repo).
	Force bool

	// SysRoot, when non-empty, is prepended to every system path when
	// re-hashing files after a commit. This mirrors the --sys-root sandbox
	// override used by `sysfig status` and `sysfig apply`.
	SysRoot string

	// FileIDs, when non-empty, restricts the sync to only these tracking IDs.
	// All other tracked files are skipped.
	FileIDs []string
}

// SyncResult reports what happened during a sync.
type SyncResult struct {
	RepoDir         string
	Pulled          bool     // true if pull ran and succeeded
	PullErr         error    // non-nil if pull was attempted but failed (non-fatal)
	Committed       bool     // true if at least one commit was created
	Pushed          bool     // true if push was attempted and succeeded
	Message         string   // message of the last commit
	CommittedFiles  []string // repo-relative paths committed in this sync
	UpdatedFiles    []string // IDs of files whose state hash was refreshed
	NodeWarnings    []string // non-fatal warnings from invalid node public keys
}

// buildSyncMessage inspects the git index to produce a meaningful commit
// message summarising which top-level directories changed.
//
// Examples:
//
//	"sysfig: update home/aye7/.zshrc"
//	"sysfig: update etc/pacman.d, home/aye7"
//	"sysfig: sync 3 directories"  (when more than 2)
func buildSyncMessage(repoDir string) string {
	cmd := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
		"diff", "--cached", "--name-only")
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Sprintf("sysfig: sync %s", time.Now().UTC().Format("2006-01-02"))
	}

	// Collect unique top-level paths (dir/file or dir/subdir).
	seen := map[string]bool{}
	var parts []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f = strings.TrimSpace(f)
		if f == "" || f == "sysfig.yaml" {
			continue
		}
		segs := strings.SplitN(f, "/", 3)
		key := segs[0]
		if len(segs) > 1 {
			key = segs[0] + "/" + segs[1]
		}
		if !seen[key] {
			seen[key] = true
			parts = append(parts, key)
		}
	}

	if len(parts) == 0 {
		return fmt.Sprintf("sysfig: sync %s", time.Now().UTC().Format("2006-01-02"))
	}
	if len(parts) <= 2 {
		return "sysfig: update " + strings.Join(parts, ", ")
	}
	return fmt.Sprintf("sysfig: update %d paths (%s, …)", len(parts), strings.Join(parts[:2], ", "))
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

	// Resolve node recipients once — used for every encrypted file.
	nodeRecipients, nodeWarnings := NodeRecipients(currentState.Nodes)
	result.NodeWarnings = nodeWarnings

	now := time.Now()

	// fileEntry holds one changed file ready to be staged.
	type fileEntry struct {
		id       string
		rec      *types.FileRecord
		relPath  string
		sysPath  string
		data     []byte
		blobHash string // populated after git hash-object
	}

	// Step 1: read all file data and skip unchanged files.
	// Group by rec.Group (set when tracked as a directory) so that all files
	// from the same directory land in one commit. Individually tracked files
	// (empty Group) each get their own commit — keyed by their unique ID.
	groups := map[string][]fileEntry{} // key = group dir path or file ID
	var groupOrder []string            // preserve insertion order for deterministic commits

	// Build a quick lookup set for FileIDs filter.
	fileIDSet := make(map[string]bool, len(opts.FileIDs))
	for _, fid := range opts.FileIDs {
		fileIDSet[fid] = true
	}

	for id, rec := range currentState.Files {
		if len(fileIDSet) > 0 && !fileIDSet[id] {
			continue
		}
		relPath := rec.RepoPath
		sysPath := rec.SystemPath
		if opts.SysRoot != "" {
			sysPath = filepath.Join(opts.SysRoot, rec.SystemPath)
		}

		var data []byte
		if rec.Encrypt {
			plaintext, err := os.ReadFile(sysPath)
			if err != nil {
				continue
			}
			keysDir := filepath.Join(opts.BaseDir, "keys")
			km := crypto.NewKeyManager(keysDir)
			master, err := km.Load()
			if err != nil {
				return nil, fmt.Errorf("core: sync: encrypt %q: master key not found: %w", id, err)
			}
			encrypted, err := crypto.EncryptForFile(plaintext, master, id, nodeRecipients...)
			if err != nil {
				return nil, fmt.Errorf("core: sync: encrypt %q: %w", id, err)
			}
			data = encrypted
		} else {
			d, err := os.ReadFile(sysPath)
			if err != nil {
				continue
			}
			data = d
		}

		// Skip unchanged files, but still refresh their state hash.
		// Read from the file's own branch (falls back to HEAD for old records).
		showRef := rec.Branch
		if showRef == "" {
			showRef = "HEAD"
		}
		if existing, err := gitShowBytesAt(repoDir, showRef, relPath); err == nil && bytes.Equal(existing, data) {
			if sysHash, err := hash.File(sysPath); err == nil {
				_ = sm.WithLock(func(s *types.State) error {
					r := s.Files[id]
					r.CurrentHash = sysHash
					r.LastSync = &now
					if newMeta, err := sysfigfs.ReadMeta(sysPath); err == nil {
						r.Meta = newMeta
					}
					s.Files[id] = r
					return nil
				})
			}
			continue
		}

		// Files tracked as part of a directory share a Group key → one commit.
		// Individually tracked files use their unique ID → one commit each.
		groupKey := rec.Group
		if groupKey == "" {
			groupKey = id
		}
		if _, exists := groups[groupKey]; !exists {
			groupOrder = append(groupOrder, groupKey)
		}
		groups[groupKey] = append(groups[groupKey], fileEntry{id, rec, relPath, sysPath, data, ""})
	}

	// Step 2: for each group, stage all files then create ONE commit.
	for _, groupKey := range groupOrder {
		entries := groups[groupKey]

		// Hash all blobs for this group (writes to object store, no index touch).
		blobs := make([]BlobEntry, 0, len(entries))
		for i := range entries {
			blobHash, err := syncHashBlob(repoDir, entries[i].data)
			if err != nil {
				return nil, err
			}
			entries[i].blobHash = blobHash
			blobs = append(blobs, BlobEntry{BlobHash: blobHash, RelPath: entries[i].relPath})
		}

		// Build commit message.
		groupMsg := msg
		if groupMsg == "" {
			paths := make([]string, len(entries))
			for i, e := range entries {
				paths[i] = e.relPath
			}
			if len(paths) == 1 {
				groupMsg = "sysfig: update " + paths[0]
			} else {
				groupMsg = "sysfig: update " + groupKey + " (" + strings.Join(paths, ", ") + ")"
			}
		}

		// Determine which branch this group commits to.
		groupBranch := entries[0].rec.Branch
		if groupBranch == "" {
			if entries[0].rec.Group != "" {
				groupBranch = "track/" + strings.TrimPrefix(entries[0].rec.Group, "/")
			} else {
				groupBranch = "track/" + entries[0].relPath
			}
		}

		// Commit using isolated index — only these blobs, no shared index state.
		commitErr := gitCommitToBranch(repoDir, groupBranch, groupMsg, blobs, 30*time.Second)
		if commitErr != nil {
			return nil, fmt.Errorf("core: sync: commit %q: %w", groupKey, commitErr)
		}
		result.Committed = true
		result.Message = groupMsg
		for _, e := range entries {
			result.CommittedFiles = append(result.CommittedFiles, e.relPath)
		}

		// Refresh state hashes for all files in the group.
		for _, e := range entries {
			_ = sm.WithLock(func(s *types.State) error {
				r := s.Files[e.id]
				if e.rec.Encrypt {
					sysHash, err := hash.File(e.sysPath)
					if err != nil {
						return nil
					}
					r.CurrentHash = sysHash
				} else {
					repoContent, err := gitShowBytesAt(repoDir, groupBranch, e.relPath)
					if err != nil {
						return nil
					}
					sysHash, err := hash.File(e.sysPath)
					if err != nil {
						return nil
					}
					if hash.Bytes(repoContent) != sysHash {
						return nil
					}
					r.CurrentHash = sysHash
				}
				r.LastSync = &now
				if newMeta, err := sysfigfs.ReadMeta(e.sysPath); err == nil {
					r.Meta = newMeta
				}
				s.Files[e.id] = r
				result.UpdatedFiles = append(result.UpdatedFiles, e.id)
				return nil
			})
		}
	}

	// Update sysfig.yaml manifest on the "manifest" branch if it changed.
	if len(currentState.Files) > 0 {
		if manifestData, err := buildManifest(currentState); err == nil {
			existing, _ := gitShowBytesAt(repoDir, "manifest", "sysfig.yaml")
			if !bytes.Equal(existing, manifestData) {
				if blobHash, err := syncHashBlob(repoDir, manifestData); err == nil {
					_ = gitCommitToBranch(repoDir, "manifest", "sysfig: update manifest",
						[]BlobEntry{{BlobHash: blobHash, RelPath: "sysfig.yaml"}},
						30*time.Second)
				}
			}
		}
	}

	// Push only when explicitly requested — never automatic.
	if opts.Push {
		// Push all track/* branches and the manifest branch in one operation.
		// refs/heads/track/*:refs/heads/track/* pushes every track branch.
		refspecs := []string{
			"refs/heads/track/*:refs/heads/track/*",
			"refs/heads/manifest:refs/heads/manifest",
		}
		pushArgs := []string{"push", "origin"}
		if opts.Force {
			pushArgs = append(pushArgs, "--force")
		}
		pushArgs = append(pushArgs, refspecs...)
		if pushErr := syncGitBareRun(repoDir, 60*time.Second, nil, pushArgs...); pushErr != nil {
			return nil, fmt.Errorf("core: sync: push: %w", pushErr)
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

// buildManifest generates sysfig.yaml bytes from current state so that
// `sysfig setup` on a new host can seed state.json automatically.
// Staged into the bare repo as part of every sync commit.
func buildManifest(s *types.State) ([]byte, error) {
	type entry struct {
		ID         string   `yaml:"id"`
		SystemPath string   `yaml:"system_path"`
		RepoPath   string   `yaml:"repo_path"`
		Encrypt    bool     `yaml:"encrypt,omitempty"`
		Template   bool     `yaml:"template,omitempty"`
		Tags       []string `yaml:"tags,omitempty"`
	}
	type manifest struct {
		Version      int     `yaml:"version"`
		TrackedFiles []entry `yaml:"tracked_files"`
	}

	entries := make([]entry, 0, len(s.Files))
	for _, rec := range s.Files {
		entries = append(entries, entry{
			ID:         rec.ID,
			SystemPath: rec.SystemPath,
			RepoPath:   rec.RepoPath,
			Encrypt:    rec.Encrypt,
			Template:   rec.Template,
			Tags:       rec.Tags,
		})
	}
	// Insertion sort for deterministic output.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].ID < entries[j-1].ID; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	m := manifest{Version: 1, TrackedFiles: entries}
	return yaml.Marshal(m)
}

