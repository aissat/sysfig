package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// FileStatusLabel is the display status string.
type FileStatusLabel string

const (
	StatusSynced    FileStatusLabel = "SYNCED"
	StatusDirty     FileStatusLabel = "DIRTY"    // alias: MODIFIED
	StatusModified  FileStatusLabel = "MODIFIED"
	StatusMissing   FileStatusLabel = "MISSING"
	StatusEncrypted FileStatusLabel = "ENCRYPTED"
	StatusPending   FileStatusLabel = "PENDING"  // repo ahead of system; apply needed
	StatusNew       FileStatusLabel = "NEW"       // file exists on disk but not yet tracked
)

// FileStatusResult holds the computed status for one tracked file.
type FileStatusResult struct {
	ID           string
	Slug         string // human-readable path slug, e.g. "home_aye7_zshrc"
	SystemPath   string
	RepoPath     string
	RecordedHash string          // hash stored in state.json
	CurrentHash  string          // hash of the current system file ("" if missing)
	Status       FileStatusLabel
	Encrypted    bool

	// Metadata drift fields — populated when FileMeta was recorded at track
	// time and the live system file's metadata has since changed.
	MetaDrift    bool            // true if any metadata field differs
	RecordedMeta *types.FileMeta // what state.json holds
	CurrentMeta  *types.FileMeta // what the live system file has right now
}

// Status loads state.json, then for each tracked FileRecord performs a
// three-way comparison: system file, recorded hash (state.json), repo copy.
//
//  1. Encrypted → StatusEncrypted, CurrentHash = "(locked)"
//  2. System file missing → StatusMissing
//  3. sysHash == repoHash == recordedHash → StatusSynced
//  4. sysHash != repoHash AND repoHash != recordedHash
//     → StatusPending  (repo pulled ahead; `sysfig apply` needed)
//  5. sysHash != recordedHash (but repo matches recorded)
//     → StatusDirty    (system changed locally; `sysfig sync` needed)
//  6. Everything else → StatusDirty (safe fallback)
//
// The repo hash is obtained by reading the file content from the bare repo at
// <baseDir>/repo.git using `git --git-dir=<repoDir> show HEAD:<relPath>` and
// then hashing the bytes. If the file is not yet committed in the bare repo,
// rec.CurrentHash is used as the fallback repo hash (same two-way behaviour as
// before).
//
// If ids is non-empty, only report on those IDs.
// Returns []FileStatusResult sorted by ID, and any error.
func Status(baseDir string, ids []string, sysRoot string) ([]FileStatusResult, error) {
	statePath := filepath.Join(baseDir, "state.json")
	sm := state.NewManager(statePath)

	s, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: status: load state: %w", err)
	}

	// Bare repo directory — all git operations use --git-dir=repoDir.
	repoDir := filepath.Join(baseDir, "repo.git")

	// Build a set of requested IDs for quick lookup (empty set = all).
	wantIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		wantIDs[id] = true
	}

	var results []FileStatusResult

	for _, rec := range s.Files {
		computedID := deriveID(rec.SystemPath)
		dirID := deriveID(filepath.Dir(rec.SystemPath))
		if len(ids) > 0 && !wantIDs[computedID] && !wantIDs[dirID] {
			continue
		}

		// Resolve system path, honoring sysRoot sandbox override.
		sysPath := rec.SystemPath
		if sysRoot != "" {
			sysPath = filepath.Join(sysRoot, rec.SystemPath)
		}

		r := FileStatusResult{
			ID:           deriveID(rec.SystemPath),
			Slug:         deriveSlug(rec.SystemPath),
			SystemPath:   sysPath,
			RepoPath:     rec.RepoPath,
			RecordedHash: rec.CurrentHash,
			Encrypted:    rec.Encrypt,
		}

		switch {
		case rec.Encrypt:
			// Encrypted files cannot be compared — report as locked.
			r.Status = StatusEncrypted
			r.CurrentHash = "(locked)"

		default:
			// Check whether the system file exists.
			if _, err := os.Stat(sysPath); os.IsNotExist(err) {
				r.Status = StatusMissing
				r.CurrentHash = ""
			} else if err != nil {
				// Unexpected stat error — surface it but continue.
				r.Status = StatusMissing
				r.CurrentHash = ""
			} else {
				// Three-way comparison: system file, recorded hash, repo copy.
				sysHash, err := hash.File(sysPath)
				if err != nil {
					return nil, fmt.Errorf("core: status: hash system %q: %w", sysPath, err)
				}
				r.CurrentHash = sysHash

				// Read the repo copy from the bare repo and hash its bytes.
				// rec.RepoPath is the git-relative path (e.g. "etc/nginx/nginx.conf").
				// Fall back to rec.CurrentHash if the file is not yet committed.
				repoHash := rec.CurrentHash
				if repoContent, err := gitShowBytes(repoDir, rec.RepoPath); err == nil {
					repoHash = hash.Bytes(repoContent)
				}

				switch {
				case sysHash == repoHash:
					// System and repo agree — fully synced regardless of
					// what the recorded hash says.
					r.Status = StatusSynced

				case repoHash != rec.CurrentHash:
					// Repo moved ahead of what was last applied (e.g. after
					// `sysfig pull`). System hasn't been updated yet.
					r.Status = StatusPending

				default:
					// System diverged from the last known-good hash while
					// the repo copy is still at the recorded baseline.
					r.Status = StatusDirty
				}

				// Metadata drift check — runs regardless of content status.
				// If FileMeta was recorded and the live metadata differs,
				// mark MetaDrift and escalate SYNCED → DIRTY.
				if rec.Meta != nil {
					currentMeta, err := sysfigfs.ReadMeta(sysPath)
					if err == nil {
						r.RecordedMeta = rec.Meta
						r.CurrentMeta = currentMeta
						if metaDiffers(rec.Meta, currentMeta) {
							r.MetaDrift = true
							if r.Status == StatusSynced {
								// Content is fine but metadata changed —
								// still report as DIRTY so the user knows.
								r.Status = StatusDirty
							}
						}
					}
				}
			}
		}

		results = append(results, r)
	}

	// Scan group directories for untracked files.
	// Build a set of all tracked system paths for fast lookup.
	trackedPaths := make(map[string]bool, len(s.Files))
	groupDirs := make(map[string]bool)
	for _, rec := range s.Files {
		trackedPaths[rec.SystemPath] = true
		if rec.Group != "" {
			groupDirs[rec.Group] = true
		}
	}
	excludeSet := make(map[string]bool, len(s.Excludes))
	for _, ex := range s.Excludes {
		excludeSet[ex] = true
	}

	for dir := range groupDirs {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			// Skip excluded paths (files or whole directories).
			if excludeSet[path] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if trackedPaths[path] {
				return nil
			}
			sysPath := path
			if sysRoot != "" {
				sysPath = filepath.Join(sysRoot, path)
			}
			results = append(results, FileStatusResult{
				ID:         deriveID(path),
				Slug:       deriveSlug(path),
				SystemPath: sysPath,
				Status:     StatusNew,
			})
			return nil
		})
	}

	// Return results sorted by ID for deterministic output.
	sort.Slice(results, func(i, j int) bool {
		return results[i].ID < results[j].ID
	})

	return results, nil
}

// metaDiffers returns true if any tracked metadata field differs between
// recorded (from state.json) and current (from the live system file).
func metaDiffers(recorded, current *types.FileMeta) bool {
	if recorded == nil || current == nil {
		return false
	}
	return recorded.UID != current.UID ||
		recorded.GID != current.GID ||
		recorded.Mode != current.Mode
}
