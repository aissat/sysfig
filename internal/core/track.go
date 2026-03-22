package core

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aissat/sysfig/internal/crypto"
	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// systemDenylist is the hardcoded set of paths that sysfig refuses to track.
// Patterns ending in /* match any file under that directory.
// Patterns containing * are matched via filepath.Match.
var systemDenylist = []string{
	"/etc/shadow",
	"/etc/passwd",
	"/etc/group",
	"/etc/gshadow",
	"/etc/subuid",
	"/etc/subgid",
	"/etc/sudoers",
	"/etc/ssh/ssh_host_*",
	"/etc/ssl/private/*",
	"/var/lib/machines/*",
	"/boot/*",
	"/proc/*",
	"/sys/*",
	"/dev/*",
}

// IsDenied returns true if the given absolute path matches any entry in the
// built-in system denylist. Patterns ending in /* match any file under that
// directory. Patterns containing * use glob matching via filepath.Match.
func IsDenied(path string) bool {
	for _, pattern := range systemDenylist {
		// Exact match — fast path.
		if path == pattern {
			return true
		}

		// Glob match (handles ssh_host_*, private/*, boot/*, etc.)
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// TrackOptions holds the parameters for tracking a file.
type TrackOptions struct {
	SystemPath string // absolute path to the file on disk
	RepoDir    string // bare git repo directory (e.g. ~/.sysfig/repo.git)
	StateDir   string // directory containing state.json (e.g. ~/.sysfig)
	ID         string // user-supplied ID; if empty, derived from path
	Tags       []string
	Platform   []string
	Encrypt    bool
	Template   bool
	// SysRoot, when non-empty, is stripped from SystemPath before deriving
	// the repo-relative path, the tracking ID, and the logical SystemPath
	// stored in state.json. This lets sandbox/testing use a fake root (e.g.
	// /tmp/sysfig-sandbox/real_system) while the repo and state still record
	// the canonical system path (e.g. /var/app/settings.conf).
	SysRoot string
	// Group, when non-empty, marks this file as part of a directory track.
	// Sync uses this to commit all files in the same group together.
	Group string
	// Force, when true, allows tracking a file that is currently owned by a
	// Config Source profile. Without Force, Track returns an error if the path
	// is source-managed, to protect the ownership model.
	Force bool
	// LocalOnly marks the file as local-only — it is recorded in state.json
	// but never staged in the git repo or pushed to the remote.
	LocalOnly bool
	// HashOnly marks the file for hash-only integrity tracking — the content
	// hash is recorded but no copy is stored in the repo. The file is never
	// staged or pushed.
	HashOnly bool
}

// TrackResult is returned by Track on success.
type TrackResult struct {
	ID       string
	RepoPath string // git-relative path where the file is stored (e.g. "etc/nginx/nginx.conf")
	Hash     string // BLAKE3 hex hash of the file
}

// stageFilePlain stages a real on-disk file (at its absolute system path)
// directly into the bare repo index under relPath (git-relative, no leading /).
//
// Uses: GIT_DIR=repoDir GIT_WORK_TREE=/ git add <relPath>
func stageFilePlain(repoDir, relPath string) error {
	if err := gitBareRun(repoDir, 30*time.Second,
		[]string{"GIT_WORK_TREE=/"},
		"add", relPath,
	); err != nil {
		return fmt.Errorf("core: stage plain %q: %w", relPath, err)
	}
	return nil
}

// stageBlob writes data to a temp file, stores it in the bare repo object
// store via `git hash-object -w`, then registers it in the index under relPath
// with `git update-index --add --cacheinfo`.
//
// This is the staging path for:
//   - Encrypted files (ciphertext bytes)
//   - Plaintext files when SysRoot is set (sandbox/test mode)
//
// The temporary file is always removed before the function returns.
func stageBlob(repoDir, relPath string, data []byte) error {
	// Write bytes to a temp file outside the repo so hash-object can read it.
	tmpFile, err := os.CreateTemp("", "sysfig-blob-*")
	if err != nil {
		return fmt.Errorf("core: stage blob %q: create temp file: %w", relPath, err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("core: stage blob %q: write temp file: %w", relPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("core: stage blob %q: close temp file: %w", relPath, err)
	}

	// 1. Store the blob in the object store and obtain its SHA.
	blobHashBytes, err := gitBareOutput(repoDir, 15*time.Second, nil,
		"hash-object", "-w", tmpPath,
	)
	if err != nil {
		return fmt.Errorf("core: stage blob %q: hash-object: %w", relPath, err)
	}
	blobHash := strings.TrimSpace(string(blobHashBytes))
	if blobHash == "" {
		return fmt.Errorf("core: stage blob %q: hash-object returned empty hash", relPath)
	}

	// 2. Add the blob to the index under relPath with regular file mode.
	cacheinfo := "100644," + blobHash + "," + relPath
	if err := gitBareRun(repoDir, 15*time.Second, nil,
		"update-index", "--add", "--cacheinfo", cacheinfo,
	); err != nil {
		return fmt.Errorf("core: stage blob %q: update-index: %w", relPath, err)
	}

	return nil
}

// Track adds a file to sysfig tracking:
//  1. Validates the path (not in denylist, is a regular file, is readable)
//  2. Derives an ID from the path if opts.ID is empty
//  3. Stages the file into the bare repo:
//     - Plaintext + no SysRoot: GIT_DIR=repoDir GIT_WORK_TREE=/ git add <relPath>
//     - Plaintext + SysRoot   : git hash-object -w + git update-index (from real path)
//     - Encrypted             : encrypt in memory, git hash-object -w + git update-index
//  4. Computes the BLAKE3 hash of the real on-disk file
//  5. Updates state.json via state.Manager.WithLock
//
// Returns TrackResult or a descriptive error.
func Track(opts TrackOptions) (*TrackResult, error) {
	// Resolve relative paths (e.g. ".", "../foo") to absolute so that IDs,
	// repo paths, and system_path entries are always canonical.
	absPath, err := filepath.Abs(opts.SystemPath)
	if err != nil {
		return nil, fmt.Errorf("core: track: resolve path %q: %w", opts.SystemPath, err)
	}
	opts.SystemPath = absPath
	path := opts.SystemPath

	// 1a. Denylist check.
	if IsDenied(path) {
		return nil, fmt.Errorf("core: path %q is in the system denylist", path)
	}

	// 1b. Stat the file — must exist and be a regular file.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("core: path %q is not a regular file", path)
	}

	// 1c. Verify the file is readable by opening it.
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}
	f.Close()

	// 2. Derive the logical (canonical) system path by stripping SysRoot.
	//    This is what gets stored in state.json and used as the repo layout.
	logicalPath := path
	if opts.SysRoot != "" {
		rel, err := filepath.Rel(opts.SysRoot, path)
		if err == nil && !strings.HasPrefix(rel, "..") {
			logicalPath = "/" + rel
		}
	}

	// 3. Derive ID if not supplied — always from the logical path.
	id := opts.ID
	if id == "" {
		id = deriveID(logicalPath)
	}

	// 4. Build the git-relative path (no leading slash).
	//    e.g. logicalPath="/etc/nginx/nginx.conf" → repoRel="etc/nginx/nginx.conf"
	repoRel := strings.TrimPrefix(logicalPath, "/")

	// 5. Read the file content for hashing (and encryption / sandbox staging).
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}

	// 6. Stage the file into the bare repo index.
	// HashOnly files are never staged — only their hash is recorded.
	// LocalOnly files are staged normally in the local repo (just never pushed).
	if opts.HashOnly {
		// Skip all git staging — hash-only records store no content.
	} else if opts.Encrypt {
		// Encrypted path: encrypt the plaintext in memory, then store the
		// ciphertext as a git blob via hash-object + update-index.
		keysDir := filepath.Join(opts.StateDir, "keys")
		km := crypto.NewKeyManager(keysDir)
		master, err := km.Load()
		if err != nil {
			return nil, fmt.Errorf("core: encrypt: master key not found — run 'sysfig init --encrypt' first: %w", err)
		}
		encrypted, err := crypto.EncryptForFile(data, master, id)
		if err != nil {
			return nil, fmt.Errorf("core: encrypt: %w", err)
		}
		if err := stageBlob(opts.RepoDir, repoRel, encrypted); err != nil {
			return nil, err
		}
	} else if opts.SysRoot != "" {
		// Sandbox/test mode: the real file lives under SysRoot rather than at
		// the canonical path. Stage the plaintext bytes via hash-object so we
		// don't need GIT_WORK_TREE to point at an arbitrary temp directory.
		if err := stageBlob(opts.RepoDir, repoRel, data); err != nil {
			return nil, fmt.Errorf("core: stage plain (sandbox) %q: %w", repoRel, err)
		}
	} else {
		// Production path: file content is already in `data` — stage it via
		// hash-object + update-index. This avoids GIT_WORK_TREE resolution
		// issues that occur with paths outside a typical project root.
		if err := stageBlob(opts.RepoDir, repoRel, data); err != nil {
			return nil, err
		}
	}

	// 7. Compute the BLAKE3 hash of the real on-disk file.
	fileHash, err := hash.File(path)
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}

	// 8. Capture filesystem metadata (uid, gid, mode).
	meta, err := sysfigfs.ReadMeta(path)
	if err != nil {
		// Non-fatal — record without metadata rather than aborting.
		meta = nil
	}

	// 9. Update state.json under an exclusive lock.
	statePath := filepath.Join(opts.StateDir, "state.json")
	sm := state.NewManager(statePath)

	if err := sm.WithLock(func(s *types.State) error {
		if existing, exists := s.Files[id]; exists {
			// If the file is source-managed, refuse unless --force is given.
			if existing.SourceProfile != "" && !opts.Force {
				return fmt.Errorf("core: %q is managed by source profile %q — use --force to take manual ownership", logicalPath, existing.SourceProfile)
			}
			if existing.SourceProfile == "" {
				return fmt.Errorf("core: id %q is already tracked", id)
			}
			// --force on a source-managed file: clear ownership, fall through.
		}

		now := time.Now()
		// Branch name: "track/<repoPath>" for individual files,
		// "track/<group>" for directory-tracked groups.
		// HashOnly files have no branch — nothing is staged.
		// LocalOnly files use a "local/" prefix so push can exclude them.
		// Normal files use the "track/" prefix.
		var branch string
		if !opts.HashOnly {
			branchBase := repoRel
			if opts.Group != "" {
				branchBase = strings.TrimPrefix(opts.Group, "/")
			}
			prefix := "track/"
			if opts.LocalOnly {
				prefix = "local/"
			}
			branch = resolveTrackBranch(opts.RepoDir, prefix+SanitizeBranchName(branchBase))
		}

		s.Files[id] = &types.FileRecord{
			ID:          id,
			SystemPath:  logicalPath,
			RepoPath:    repoRel, // git-relative path, e.g. "etc/nginx/nginx.conf"
			CurrentHash: fileHash,
			LastSync:    &now,
			Status:      types.StatusTracked,
			Encrypt:     opts.Encrypt,
			Template:    opts.Template,
			Tags:        opts.Tags,
			Meta:        meta,
			Group:       opts.Group,
			Branch:      branch,
			LocalOnly:   opts.LocalOnly,
			HashOnly:    opts.HashOnly,
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return &TrackResult{
		ID:       id,
		RepoPath: repoRel,
		Hash:     fileHash,
	}, nil
}

// UntrackOptions configures an Untrack call.
type UntrackOptions struct {
	BaseDir string // ~/.sysfig
	// Arg is either a bare ID (e.g. "home_aye7_zshrc") or an absolute path
	// (e.g. "/home/aye7/.zshrc"). Both are accepted.
	Arg string
}

// Untrack removes one or more files from sysfig tracking. The system files are
// left untouched. Arg may be:
//   - a bare ID           → removes that single entry
//   - an absolute path    → removes that single file
//   - an absolute dir     → removes all files under that directory
//
// Returns the list of IDs removed, or an error if nothing matched.
func Untrack(opts UntrackOptions) ([]string, error) {
	// Normalise: strip trailing slash so dir matching is consistent.
	arg := strings.TrimRight(opts.Arg, "/")

	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)
	var removed []string
	err := sm.WithLock(func(s *types.State) error {
		for id, rec := range s.Files {
			match := id == arg ||
				rec.SystemPath == arg ||
				strings.HasPrefix(rec.SystemPath, arg+"/") ||
				rec.Group == arg ||
				strings.HasPrefix(rec.Group, arg+"/")
			if match {
				delete(s.Files, id)
				removed = append(removed, id)
			}
		}
		if len(removed) == 0 {
			// Not a tracked file — check if it's a NEW path inside a group dir.
			// If so, add it to the excludes list so it won't show as NEW again.
			for _, rec := range s.Files {
				if rec.Group == "" {
					continue
				}
				if arg == rec.Group ||
					strings.HasPrefix(arg, rec.Group+"/") ||
					strings.HasPrefix(rec.Group, arg+"/") {
					// Add to excludes if not already there.
					for _, ex := range s.Excludes {
						if ex == arg {
							return fmt.Errorf("already excluded: %q", opts.Arg)
						}
					}
					s.Excludes = append(s.Excludes, arg)
					removed = append(removed, arg)
					return nil
				}
			}
			return fmt.Errorf("not tracked: %q", opts.Arg)
		}
		return nil
	})
	return removed, err
}

// DeriveID returns a stable 8-character hex ID for absPath derived from SHA-256.
func DeriveID(absPath string) string {
	return deriveID(absPath)
}

// deriveID returns a stable 8-character hex ID for absPath derived from SHA-256.
func deriveID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return fmt.Sprintf("%x", sum[:4])
}

// deriveSlug converts an absolute path into a human-readable tracking slug.
//
//	/etc/nginx/nginx.conf  → "etc_nginx_nginx_conf"
//	/home/aye7/.zshrc      → "home_aye7_zshrc"
func deriveSlug(absPath string) string {
	parts := strings.Split(strings.TrimPrefix(absPath, "/"), "/")
	for i, p := range parts {
		parts[i] = strings.TrimLeft(p, ".")
	}
	s := strings.Join(parts, "_")
	s = strings.ReplaceAll(s, ".", "_")
	return strings.ToLower(s)
}

// TrackDirOptions configures a recursive directory track operation.
type TrackDirOptions struct {
	// DirPath is the root directory to walk. Must be an absolute path to a
	// directory (not a file — use Track for single files).
	DirPath  string
	RepoDir  string
	StateDir string
	Tags     []string
	Platform []string
	Encrypt  bool
	Template bool
	// SysRoot mirrors TrackOptions.SysRoot — stripped from every file path
	// before deriving repo layout, ID, and logical SystemPath in state.json.
	SysRoot string
	// Excludes is an optional list of paths or glob patterns to skip during
	// the recursive walk. Each entry is matched against the absolute file
	// path using filepath.Match; a plain path prefix also matches any file
	// underneath it (i.e. excluding /etc/ssl skips /etc/ssl/certs/ca.pem).
	Excludes  []string
	LocalOnly bool
	HashOnly  bool
}

// TrackDirEntry reports the outcome for a single file encountered during a
// recursive track walk.
type TrackDirEntry struct {
	Path    string       // absolute system path
	ID      string       // derived tracking ID (empty when Skipped or Err != nil)
	Skipped bool         // true when the file was intentionally skipped (denylist, already tracked)
	Reason  string       // human-readable skip reason (only set when Skipped == true)
	Err     error        // non-nil if tracking this specific file failed unexpectedly
	Result  *TrackResult // non-nil on success
}

// TrackDirSummary is the aggregate result of a TrackDir call.
type TrackDirSummary struct {
	Entries []TrackDirEntry // one entry per regular file found in the walk
	Tracked int             // files successfully tracked
	Skipped int             // files intentionally skipped
	Errors  int             // files that failed with an unexpected error
}

// TrackDir walks dirPath recursively and calls Track for every regular,
// non-denied file it finds.
//
// Rules:
//   - Symlinks are silently skipped (no-symlinks guardrail).
//   - Directories are traversed but never tracked themselves.
//   - Files matching the system denylist are skipped with a reason.
//   - Files already tracked (duplicate ID) are skipped with a reason.
//   - All other per-file errors are collected in TrackDirEntry.Err and do
//     NOT abort the walk — the caller receives full results.
//
// Returns TrackDirSummary and a top-level error only if the root walk itself
// fails (e.g. dirPath does not exist or is not a directory).
func TrackDir(opts TrackDirOptions) (*TrackDirSummary, error) {
	// Resolve relative paths to absolute before walking.
	absDir, err := filepath.Abs(opts.DirPath)
	if err != nil {
		return nil, fmt.Errorf("core: trackdir: resolve path %q: %w", opts.DirPath, err)
	}
	opts.DirPath = absDir

	// Validate that the root path is an existing directory.
	info, err := os.Stat(opts.DirPath)
	if err != nil {
		return nil, fmt.Errorf("core: trackdir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("core: trackdir: %q is not a directory — use 'track' for single files", opts.DirPath)
	}

	summary := &TrackDirSummary{}

	walkErr := filepath.WalkDir(opts.DirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission or I/O error entering this path — record and continue.
			summary.Entries = append(summary.Entries, TrackDirEntry{
				Path: path,
				Err:  fmt.Errorf("core: trackdir: walk: %w", err),
			})
			summary.Errors++
			return nil
		}

		// Skip the root directory itself; prune excluded subdirectories.
		if d.IsDir() {
			if path != opts.DirPath && isExcluded(path, opts.Excludes) {
				return fs.SkipDir
			}
			return nil
		}

		// Skip symlinks — no-symlinks guardrail.
		if d.Type()&fs.ModeSymlink != 0 {
			summary.Entries = append(summary.Entries, TrackDirEntry{
				Path:    path,
				Skipped: true,
				Reason:  "symlink — skipped (no-symlinks guardrail)",
			})
			summary.Skipped++
			return nil
		}

		// Skip non-regular files (devices, pipes, sockets, …).
		if !d.Type().IsRegular() {
			summary.Entries = append(summary.Entries, TrackDirEntry{
				Path:    path,
				Skipped: true,
				Reason:  "not a regular file",
			})
			summary.Skipped++
			return nil
		}

		// User-supplied exclude patterns.
		if isExcluded(path, opts.Excludes) {
			summary.Entries = append(summary.Entries, TrackDirEntry{
				Path:    path,
				Skipped: true,
				Reason:  "excluded by --exclude pattern",
			})
			summary.Skipped++
			return nil
		}

		// Denylist check.
		if IsDenied(path) {
			summary.Entries = append(summary.Entries, TrackDirEntry{
				Path:    path,
				Skipped: true,
				Reason:  "path is in the system denylist",
			})
			summary.Skipped++
			return nil
		}

		// Attempt to track the file.
		result, trackErr := Track(TrackOptions{
			SystemPath: path,
			RepoDir:    opts.RepoDir,
			StateDir:   opts.StateDir,
			Tags:       opts.Tags,
			Platform:   opts.Platform,
			Encrypt:    opts.Encrypt,
			Template:   opts.Template,
			SysRoot:    opts.SysRoot,
			Group:      opts.DirPath,
			LocalOnly:  opts.LocalOnly,
			HashOnly:   opts.HashOnly,
		})

		entry := TrackDirEntry{Path: path}

		if trackErr != nil {
			// "already tracked" is an expected condition — treat as a skip.
			if isAlreadyTracked(trackErr) {
				entry.Skipped = true
				entry.Reason = "already tracked"
				summary.Skipped++
			} else {
				entry.Err = trackErr
				summary.Errors++
			}
		} else {
			entry.ID = result.ID
			entry.Result = result
			summary.Tracked++
		}

		summary.Entries = append(summary.Entries, entry)
		return nil
	})

	if walkErr != nil {
		return nil, fmt.Errorf("core: trackdir: walk failed: %w", walkErr)
	}

	return summary, nil
}

// isExcluded reports whether path matches any of the caller-supplied exclude
// patterns. Each pattern is tested in two ways:
//  1. filepath.Match(pattern, path) — glob syntax (e.g. "*.bak", "/etc/ssl/*")
//  2. plain prefix: path == pattern or strings.HasPrefix(path, pattern+"/")
func isExcluded(path string, patterns []string) bool {
	for _, p := range patterns {
		// Glob match.
		if matched, _ := filepath.Match(p, path); matched {
			return true
		}
		// Prefix match — excludes the directory and everything inside it.
		if path == p || strings.HasPrefix(path, strings.TrimRight(p, "/")+"/") {
			return true
		}
	}
	return false
}

// isAlreadyTracked returns true when the error from Track indicates that the
// file's derived ID is already present in state.json. This is an expected
// condition during recursive walks (e.g. re-running trackdir on the same tree)
// and should be treated as a skip rather than a hard failure.
func isAlreadyTracked(err error) bool {
	if err == nil {
		return false
	}
	// Track wraps the duplicate-ID error as:
	//   fmt.Errorf("core: id %q is already tracked", id)
	// We check for the substring rather than a sentinel error to avoid
	// coupling the check to the exact wrapping chain.
	return errors.Is(err, err) && strings.Contains(err.Error(), "is already tracked")
}
