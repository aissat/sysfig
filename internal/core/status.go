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
	StatusDirty     FileStatusLabel = "DIRTY" // alias: MODIFIED
	StatusModified  FileStatusLabel = "MODIFIED"
	StatusMissing   FileStatusLabel = "MISSING"
	StatusEncrypted FileStatusLabel = "ENCRYPTED"
	StatusPending   FileStatusLabel = "PENDING"  // repo ahead of system; apply needed
	StatusNew       FileStatusLabel = "NEW"      // file exists on disk but not yet tracked
	StatusSource    FileStatusLabel = "SOURCE"   // file is managed by a Config Source profile
	StatusTampered  FileStatusLabel = "TAMPERED" // hash-only: on-disk hash differs from recorded
	StatusStale     FileStatusLabel = "STALE"    // remote tracked file not explicitly checked in status
)

// FileStatusResult holds the computed status for one tracked file.
type FileStatusResult struct {
	ID           string
	Slug         string // human-readable path slug, e.g. "home_aye7_zshrc"
	SystemPath   string
	RepoPath     string
	RecordedHash string // hash stored in state.json
	CurrentHash  string // hash of the current system file ("" if missing)
	Status       FileStatusLabel
	Encrypted    bool
	Group        string // non-empty when file is part of a directory-tracked group (rec.Group)

	// Metadata drift fields — populated when FileMeta was recorded at track
	// time and the live system file's metadata has since changed.
	MetaDrift    bool            // true if any metadata field differs
	RecordedMeta *types.FileMeta // what state.json holds
	CurrentMeta  *types.FileMeta // what the live system file has right now
	// LocalOnly and HashOnly mirror the flags in the underlying FileRecord.
	LocalOnly bool
	HashOnly  bool
	// Tags are the labels attached at track time (e.g. "arch", "ubuntu").
	Tags []string
	// Remote is the SSH target from which this file is fetched (e.g. "user@host").
	// Empty for locally-tracked files.
	Remote string
}

// StatusOptions configures a Status call.
type StatusOptions struct {
	BaseDir string
	IDs     []string
	SysRoot string
	// FetchRemote, when true, re-fetches every remote-tracked file via SSH
	// and compares the live hash against the recorded hash, enabling accurate
	// DIRTY/SYNCED reporting for remote files. When false (default), status
	// is instantaneous but shows the last-recorded hash only.
	FetchRemote bool
}

// If ids is non-empty, only report on those IDs.
// Returns []FileStatusResult sorted by ID, and any error.
func Status(baseDir string, ids []string, sysRoot string) ([]FileStatusResult, error) {
	return StatusWithOptions(StatusOptions{BaseDir: baseDir, IDs: ids, SysRoot: sysRoot})
}

// StatusWithOptions loads state.json and computes the three-way status for each
// tracked FileRecord: system file, recorded hash (state.json), repo copy.
//
//  1. Encrypted → StatusEncrypted, CurrentHash = "(locked)"
//  2. System file missing → StatusMissing
//  3. sysHash == repoHash → StatusSynced
//  4. repoHash != recorded → StatusPending  (repo ahead; sysfig apply needed)
//  5. sysHash != recorded → StatusDirty    (system changed; sysfig sync needed)
//
// The repo hash is read from each file's track branch in the bare repo;
// falls back to rec.CurrentHash when the file is not yet committed.
func StatusWithOptions(opts StatusOptions) ([]FileStatusResult, error) {
	baseDir, ids, sysRoot := opts.BaseDir, opts.IDs, opts.SysRoot
	statePath := filepath.Join(baseDir, "state.json")
	sm := state.NewManager(statePath)

	s, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: status: load state: %w", err)
	}

	repoDir := filepath.Join(baseDir, "repo.git")

	wantIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		wantIDs[id] = true
	}

	var results []FileStatusResult

	for _, rec := range s.Files {
		// Remote files compute their ID using "host:path". Local files use just "path".
		sysInfo := rec.SystemPath
		dirInfo := filepath.Dir(rec.SystemPath)
		if rec.Remote != "" {
			sysInfo = rec.Remote + ":" + rec.SystemPath
			dirInfo = rec.Remote + ":" + dirInfo
		}

		computedID := deriveID(sysInfo)
		dirID := deriveID(dirInfo)
		if len(ids) > 0 && !wantIDs[computedID] && !wantIDs[dirID] {
			continue
		}

		sysPath := rec.SystemPath
		if sysRoot != "" {
			sysPath = filepath.Join(sysRoot, rec.SystemPath)
		}

		r := FileStatusResult{
			ID:           computedID,
			Slug:         deriveSlug(sysInfo),
			SystemPath:   sysPath,
			RepoPath:     rec.RepoPath,
			RecordedHash: rec.CurrentHash,
			Encrypted:    rec.Encrypt,
			Group:        rec.Group,
			LocalOnly:    rec.LocalOnly,
			HashOnly:     rec.HashOnly,
			Tags:         rec.Tags,
			Remote:       rec.Remote,
		}

		// Remote-tracked files have no local system path.
		if rec.Remote != "" {
			if opts.FetchRemote {
				// Three-way comparison for remote files (mirrors the local logic):
				//   liveHash  = hash of content currently on the remote host (SSH fetch)
				//   repoHash  = hash of content committed in the repo branch
				//   recorded  = rec.CurrentHash (last synced hash)
				//
				//   liveHash == repoHash              → SYNCED
				//   liveHash != repoHash && repoHash != recorded → DIRTY (remote drifted)
				//   repoHash != recorded && liveHash == recorded → PENDING (repo pulled ahead)
				//   liveHash != recorded && repoHash == recorded → DIRTY

				fetched, fetchErr := FetchFromSSH(rec.Remote, rec.RemoteSSHKey, 0, rec.SystemPath)
				if fetchErr != nil {
					r.Status = StatusMissing
					r.CurrentHash = ""
				} else {
					liveHash := hash.Bytes(fetched)
					r.CurrentHash = liveHash

					// Read repo branch to detect PENDING.
					trackBranch := BranchFor(rec)
					repoHash := rec.CurrentHash
					if repoContent, err := gitShowBytesAt(repoDir, trackBranch, rec.RepoPath); err == nil {
						repoHash = hash.Bytes(repoContent)
					}

					switch {
					case liveHash == repoHash:
						r.Status = StatusSynced
					case repoHash != rec.CurrentHash:
						// Repo branch moved ahead of last sync — remote host hasn't
						// received the new version yet.
						r.Status = StatusPending
					default:
						// Remote host changed since last sync.
						r.Status = StatusDirty
					}
				}
			} else {
				// Fast path: show last-recorded hash, assume stale unless fetched.
				r.CurrentHash = rec.CurrentHash
				r.Status = StatusStale
			}
			results = append(results, r)
			continue
		}

		switch {
		case rec.HashOnly:
			// Hash-only: compare current on-disk hash against the recorded hash.
			// No repo copy exists. Reports SYNCED (unchanged) or TAMPERED (drifted).
			if _, err := os.Stat(sysPath); os.IsNotExist(err) {
				r.Status = StatusMissing
				r.CurrentHash = ""
				break
			}
			sysHash, err := hash.File(sysPath)
			if err != nil {
				return nil, fmt.Errorf("core: status: hash system %q: %w", sysPath, err)
			}
			r.CurrentHash = sysHash
			if sysHash == rec.CurrentHash {
				r.Status = StatusSynced
			} else {
				r.Status = StatusTampered
			}

		case rec.Encrypt:
			// Encrypted files cannot be compared — report as locked.
			r.Status = StatusEncrypted
			r.CurrentHash = "(locked)"

		case rec.SourceProfile != "":
			// Source-managed files: use SOURCE label when content matches
			// the committed render; fall through to standard DIRTY/PENDING
			// check when the on-disk copy has drifted.
			if _, err := os.Stat(sysPath); os.IsNotExist(err) {
				r.Status = StatusMissing
				r.CurrentHash = ""
				break
			} else if err != nil {
				r.Status = StatusMissing
				r.CurrentHash = ""
				break
			}
			sysHash, err := hash.File(sysPath)
			if err != nil {
				return nil, fmt.Errorf("core: status: hash system %q: %w", sysPath, err)
			}
			r.CurrentHash = sysHash
			trackBranch := BranchFor(rec)
			repoHash := rec.CurrentHash
			if repoContent, err := gitShowBytesAt(repoDir, trackBranch, rec.RepoPath); err == nil {
				repoHash = hash.Bytes(repoContent)
			}
			switch {
			case sysHash == repoHash:
				r.Status = StatusSource
			case repoHash != rec.CurrentHash:
				r.Status = StatusPending
			default:
				r.Status = StatusDirty
			}

		default:
			if _, err := os.Stat(sysPath); os.IsNotExist(err) {
				r.Status = StatusMissing
				r.CurrentHash = ""
			} else if err != nil {
				// Unexpected stat error — surface it but continue.
				r.Status = StatusMissing
				r.CurrentHash = ""
			} else {
				sysHash, err := hash.File(sysPath)
				if err != nil {
					return nil, fmt.Errorf("core: status: hash system %q: %w", sysPath, err)
				}
				r.CurrentHash = sysHash

				// Fall back to rec.CurrentHash if the file is not yet committed.
				repoHash := rec.CurrentHash
				trackBranch := BranchFor(rec)
				if repoContent, err := gitShowBytesAt(repoDir, trackBranch, rec.RepoPath); err == nil {
					repoHash = hash.Bytes(repoContent)
				}

				switch {
				case sysHash == repoHash:
					r.Status = StatusSynced
				case repoHash != rec.CurrentHash:
					// Repo pulled ahead (e.g. after sysfig pull) — system not yet updated.
					r.Status = StatusPending
				default:
					r.Status = StatusDirty
				}

				// Metadata drift: escalate SYNCED → DIRTY when permissions/ownership changed.
				if rec.Meta != nil {
					currentMeta, err := sysfigfs.ReadMeta(sysPath)
					if err == nil {
						r.RecordedMeta = rec.Meta
						r.CurrentMeta = currentMeta
						if metaDiffers(rec.Meta, currentMeta) {
							r.MetaDrift = true
							if r.Status == StatusSynced {
								r.Status = StatusDirty
							}
						}
					}
				}
			}
		}

		results = append(results, r)
	}

	trackedPaths := make(map[string]bool, len(s.Files))
	groupDirs := make(map[string]bool)
	for _, rec := range s.Files {
		trackedPaths[rec.SystemPath] = true
		// Remote records have their group on a different host; never scan the
		// local filesystem for them.
		if rec.Group != "" && rec.Remote == "" {
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
