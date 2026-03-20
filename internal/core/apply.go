package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aissat/sysfig/internal/backup"
	"github.com/aissat/sysfig/internal/crypto"
	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// ApplyOptions configures a sysfig apply operation.
type ApplyOptions struct {
	BaseDir  string   // e.g. ~/.sysfig
	IDs      []string // specific IDs to apply; empty = apply all
	DryRun   bool     // if true, print what would happen but don't write
	NoBackup bool     // skip backup step (dangerous)
	Force    bool     // overwrite DIRTY (locally-modified) files without prompting
	SysRoot  string   // if non-empty, prepend to all system paths (sandbox override)
	// e.g. SysRoot="/tmp/sandbox" → /etc/nginx.conf becomes /tmp/sandbox/etc/nginx.conf
}

// ApplyResult summarises a single file's apply outcome.
type ApplyResult struct {
	ID           string
	SystemPath   string // final destination (may have SysRoot prepended)
	RepoPath     string
	BackupPath   string // empty if NoBackup or DryRun
	Skipped      bool   // true if DryRun
	DirtySkipped     bool   // true when file is DIRTY and Force==false
	Encrypted        bool
	TemplateRendered bool         // true when {{variable}} substitution was performed
	ChownWarning     string       // non-empty if chown failed due to insufficient privilege
	Hooks      []HookResult // results of post-apply hooks
	HookFailed bool         // true if any hook returned an error
}

// Apply reads all (or specified) FileRecords from state.json, then for each:
//  1. Resolves the final destination: if SysRoot != "", prepend it to SystemPath
//  2. Unless NoBackup or DryRun: backs up the current system file via backup.Manager
//  3. Reads the repo file from the bare repo via gitShowBytes:
//     - If FileRecord.Encrypt == true: loads master key, calls crypto.DecryptForFile
//     - Otherwise: uses plaintext bytes directly
//  4. Writes the file to the destination atomically (WriteFileAtomic)
//     using the mode from rec.Meta.Mode if available, defaulting to 0o600
//  5. Updates state.json: sets FileRecord.LastApply = now, Status = StatusTracked
//
// If DryRun == true: prints each action and returns without writing anything.
// Returns a slice of ApplyResult and any fatal error.
func Apply(opts ApplyOptions) ([]ApplyResult, error) {
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	// Load current state (read-only for resolving the list of records).
	currentState, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: apply: load state: %w", err)
	}

	// Build a set of requested IDs for quick lookup (nil set = all).
	wantIDs := make(map[string]bool, len(opts.IDs))
	for _, id := range opts.IDs {
		wantIDs[id] = true
	}

	// Collect the records to apply.
	var records []*types.FileRecord
	for id, rec := range currentState.Files {
		if len(opts.IDs) > 0 && !wantIDs[id] {
			continue
		}
		records = append(records, rec)
	}

	bm := backup.NewManager(filepath.Join(opts.BaseDir, "backups"))

	hooksCfg, err := LoadHooks(opts.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("core: apply: %w", err)
	}

	var results []ApplyResult
	var errs []error

	for _, rec := range records {
		result, err := applyOne(opts, bm, rec)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !result.Skipped && !result.DirtySkipped {
			result.Hooks = RunHooksForID(hooksCfg, rec.ID)
			for _, hr := range result.Hooks {
				if hr.Err != nil {
					result.HookFailed = true
					errs = append(errs, fmt.Errorf("hook %q failed for %q: %w", hr.Name, rec.ID, hr.Err))
				}
			}
		}
		results = append(results, result)
	}

	if len(errs) > 0 {
		return results, errors.Join(errs...)
	}

	// If dry-run we never mutate state, return early.
	if opts.DryRun {
		return results, nil
	}

	// Update state.json for all successfully applied records.
	appliedIDs := make(map[string]bool, len(results))
	for _, r := range results {
		appliedIDs[r.ID] = true
	}

	if err := sm.WithLock(func(s *types.State) error {
		now := time.Now()
		for id, rec := range s.Files {
			if !appliedIDs[id] {
				continue
			}
			rec.LastApply = &now
			rec.Status = types.StatusTracked

			// Refresh the recorded metadata and hash from the live system
			// file so that the next `status` run reflects the applied state.
			sysPath := rec.SystemPath
			if opts.SysRoot != "" {
				sysPath = filepath.Join(opts.SysRoot, rec.SystemPath)
			}
			if newMeta, err := sysfigfs.ReadMeta(sysPath); err == nil {
				rec.Meta = newMeta
			}
			if newHash, err := hash.File(sysPath); err == nil {
				rec.CurrentHash = newHash
			}
		}
		return nil
	}); err != nil {
		return results, fmt.Errorf("core: apply: update state: %w", err)
	}

	return results, nil
}

// applyOne applies a single FileRecord according to opts.
func applyOne(opts ApplyOptions, bm *backup.Manager, rec *types.FileRecord) (ApplyResult, error) {
	result := ApplyResult{
		ID:        rec.ID,
		RepoPath:  rec.RepoPath,
		Encrypted: rec.Encrypt,
	}

	// 1. Resolve destination path.
	destPath := rec.SystemPath
	if opts.SysRoot != "" {
		destPath = filepath.Join(opts.SysRoot, rec.SystemPath)
	}
	result.SystemPath = destPath

	// DryRun: just print the action and return.
	if opts.DryRun {
		fmt.Printf("[dry-run] would apply %q → %q (encrypted=%v)\n", rec.RepoPath, destPath, rec.Encrypt)
		result.Skipped = true
		return result, nil
	}

	// Conflict check: if the on-disk file has been modified since the last
	// sync (DIRTY), refuse to overwrite unless --force is set.
	// We detect DIRTY by comparing the live file hash against the recorded
	// CurrentHash in state.json.  If the file is missing we proceed normally
	// (it is MISSING, not DIRTY).
	if !opts.Force && rec.CurrentHash != "" {
		if liveHash, err := hash.File(destPath); err == nil && liveHash != rec.CurrentHash {
			result.DirtySkipped = true
			return result, nil
		}
	}

	// 2. Backup the current destination file if it exists.
	if !opts.NoBackup {
		if _, err := os.Stat(destPath); err == nil {
			backupPath, err := bm.Backup(rec.ID, destPath)
			if err != nil {
				return result, fmt.Errorf("core: apply: backup %q: %w", rec.ID, err)
			}
			result.BackupPath = backupPath
		}
	}

	// 3. Read the repo file from the bare repo via git-show.
	//    rec.RepoPath is the git-relative path (e.g. "etc/nginx/nginx.conf").
	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	repoData, err := gitShowBytes(repoDir, rec.RepoPath)
	if err != nil {
		return result, fmt.Errorf("core: apply: repo file missing for id %q: %w", rec.ID, err)
	}

	// 4. Determine the file permission to use.
	//    Prefer the mode recorded in state.json metadata; fall back to 0o600.
	perm := os.FileMode(0o600)
	if rec.Meta != nil {
		perm = os.FileMode(rec.Meta.Mode)
	}

	var plaintext []byte
	if rec.Encrypt {
		keysDir := filepath.Join(opts.BaseDir, "keys")
		km := crypto.NewKeyManager(keysDir)
		master, err := km.Load()
		if err != nil {
			return result, fmt.Errorf("core: apply: decrypt %q: %w", rec.ID, err)
		}
		decrypted, err := crypto.DecryptForFile(repoData, master, rec.ID)
		if err != nil {
			return result, fmt.Errorf("core: apply: decrypt %q: %w", rec.ID, err)
		}
		plaintext = decrypted
	} else {
		plaintext = repoData
	}

	// 4b. Template rendering: substitute {{variable}} placeholders.
	if rec.Template {
		rendered, err := RenderTemplate(plaintext, DefaultTemplateVars())
		if err != nil {
			return result, fmt.Errorf("core: apply: render template %q: %w", rec.ID, err)
		}
		plaintext = rendered
		result.TemplateRendered = true
	}

	// 5. Write atomically to the destination, using the resolved perm.
	if err := sysfigfs.WriteFileAtomic(destPath, plaintext, perm); err != nil {
		return result, fmt.Errorf("core: apply: write %q: %w", rec.ID, err)
	}

	// 6. Restore metadata (chmod + chown) if recorded in state.json.
	// chmod is always applied; chown is privilege-aware — failure demoted to
	// a warning so the apply operation is never aborted by a missing sudo.
	if rec.Meta != nil {
		mr := sysfigfs.ApplyMeta(destPath, rec.Meta)
		if mr.Err != nil {
			return result, fmt.Errorf("core: apply: metadata %q: %w", rec.ID, mr.Err)
		}
		if mr.ChownWarning != "" {
			result.ChownWarning = mr.ChownWarning
		}
	}

	return result, nil
}
