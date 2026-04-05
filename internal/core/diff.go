package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	sysfigfs "github.com/aissat/sysfig/internal/fs"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// DiffResult holds the unified diff output for one tracked file.
type DiffResult struct {
	ID         string
	SystemPath string // resolved (sysRoot-prefixed) path
	RepoPath   string // git-relative path (e.g. "etc/nginx/nginx.conf"), for display only
	Remote     string // SSH target if file is remote-tracked (e.g. "user@host"), empty otherwise
	Status     FileStatusLabel
	Diff       string // unified diff text; empty if files are identical
	Skipped    bool   // true for ENCRYPTED / MISSING files (no diff possible)
	SkipReason string // human-readable reason when Skipped == true
}

// DiffOptions configures a Diff run.
type DiffOptions struct {
	// BaseDir is the sysfig data directory (e.g. ~/.sysfig).
	BaseDir string

	// IDs filters the diff to specific tracked IDs.
	// An empty slice means "all tracked files".
	IDs []string

	// SysRoot, when non-empty, is prepended to every system path.
	// Mirrors the --sys-root sandbox override used by status and apply.
	SysRoot string
}

// Diff computes a unified diff for each tracked file.
//
// Direction depends on status:
//   - DIRTY/MODIFIED  → diff repo (a) vs system (b)  — shows what sync would capture
//   - PENDING/APPLY   → diff system (a) vs repo (b)   — shows what apply would deploy
//   - SYNCED          → files identical, DiffResult.Diff is empty
//   - ENCRYPTED       → skipped (cannot diff ciphertext)
//   - MISSING         → skipped (system file absent)
//
// Repo content is read from the bare repo at <BaseDir>/repo.git via
// `git --git-dir=<repoDir> show HEAD:<relPath>` and written to a temp file
// before being passed to `diff -u`. Temp files are cleaned up after each diff.
//
// Diff shells out to `diff -u` which is universally available and produces
// familiar output. No wrapper object is introduced — consistent with the
// project guardrail of calling tools directly.
func Diff(opts DiffOptions) ([]DiffResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: diff: BaseDir must not be empty")
	}

	// Bare repo directory — all git-show calls use --git-dir=repoDir.
	repoDir := filepath.Join(opts.BaseDir, "repo.git")

	// Re-use StatusWithOptions — always fetch remote files live so that
	// remote-tracked files show DIRTY/SYNCED rather than STALE.
	statuses, err := StatusWithOptions(StatusOptions{
		BaseDir:     opts.BaseDir,
		IDs:         opts.IDs,
		SysRoot:     opts.SysRoot,
		FetchRemote: true,
	})
	if err != nil {
		return nil, fmt.Errorf("core: diff: %w", err)
	}

	// Load state so we can resolve RepoPath for each record.
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)
	s, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: diff: load state: %w", err)
	}

	// Build a lookup by ID (stable — remote records share SystemPath across hosts).
	byID := make(map[string]*types.FileRecord, len(s.Files))
	for _, rec := range s.Files {
		byID[rec.ID] = rec
	}

	// trackRef returns the git ref for a file's track branch.
	trackRef := func(rec *types.FileRecord) string {
		if rec.Branch != "" {
			return rec.Branch
		}
		return "track/" + SanitizeBranchName(rec.RepoPath)
	}

	var results []DiffResult

	for _, sr := range statuses {
		rec, ok := byID[sr.ID]
		if !ok {
			continue
		}

		isRemote := rec.Remote != ""

		// Display path: "user@host:/etc/path" for remote, system path for local.
		displayPath := sr.SystemPath
		if isRemote {
			displayPath = rec.Remote + ":" + sr.SystemPath
		}

		dr := DiffResult{
			ID:         sr.ID,
			SystemPath: sr.SystemPath,
			RepoPath:   rec.RepoPath,
			Remote:     rec.Remote,
			Status:     sr.Status,
		}

		switch sr.Status {
		case StatusEncrypted:
			dr.Skipped = true
			dr.SkipReason = "file is encrypted — cannot diff ciphertext"

		case StatusMissing:
			dr.Skipped = true
			if isRemote {
				dr.SkipReason = fmt.Sprintf("remote file unreachable: %s", rec.Remote)
			} else {
				dr.SkipReason = "system file is missing"
			}

		case StatusSynced:
			// Files are identical — leave Diff empty, not skipped.

		case StatusDirty:
			// Drifted from repo. Show what `sysfig sync` would capture:
			// a = repo (last committed), b = live content.
			repoContent, err := gitShowBytesAt(repoDir, trackRef(rec), rec.RepoPath)
			if err != nil {
				return nil, fmt.Errorf("core: diff: %s: read repo: %w", sr.ID, err)
			}
			repoTmp, cleanup, err := writeTempFile("repo", repoContent)
			if err != nil {
				return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
			}
			defer cleanup()

			var diffText string
			if isRemote {
				// Fetch live remote content and diff against it.
				liveData, fetchErr := FetchFromSSH(rec.Remote, rec.RemoteSSHKey, 0, rec.SystemPath)
				if fetchErr != nil {
					cleanup()
					dr.Skipped = true
					dr.SkipReason = fmt.Sprintf("SSH fetch failed: %s", fetchErr)
					results = append(results, dr)
					continue
				}
				liveTmp, liveCleanup, err := writeTempFile("remote", liveData)
				if err != nil {
					cleanup()
					return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
				}
				defer liveCleanup()
				diffText, err = runDiff(repoTmp, liveTmp,
					labelA("repo", rec.RepoPath),
					labelB(rec.Remote, sr.SystemPath))
				liveCleanup()
				if err != nil {
					cleanup()
					return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
				}
			} else {
				diffText, err = runDiff(repoTmp, sr.SystemPath,
					labelA("repo", rec.RepoPath),
					labelB("system", displayPath))
				if err != nil {
					cleanup()
					return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
				}
			}
			cleanup()
			dr.Diff = diffText

		case StatusPending:
			// Repo pulled ahead. Show what `sysfig apply` would deploy:
			// a = current (system or remote), b = repo (incoming).
			repoContent, err := gitShowBytesAt(repoDir, trackRef(rec), rec.RepoPath)
			if err != nil {
				return nil, fmt.Errorf("core: diff: %s: read repo: %w", sr.ID, err)
			}
			repoTmp, cleanup, err := writeTempFile("repo", repoContent)
			if err != nil {
				return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
			}
			defer cleanup()

			var diffText string
			if isRemote {
				liveData, fetchErr := FetchFromSSH(rec.Remote, rec.RemoteSSHKey, 0, rec.SystemPath)
				if fetchErr != nil {
					cleanup()
					dr.Skipped = true
					dr.SkipReason = fmt.Sprintf("SSH fetch failed: %s", fetchErr)
					results = append(results, dr)
					continue
				}
				liveTmp, liveCleanup, err := writeTempFile("remote", liveData)
				if err != nil {
					cleanup()
					return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
				}
				defer liveCleanup()
				diffText, err = runDiff(liveTmp, repoTmp,
					labelA(rec.Remote, sr.SystemPath),
					labelB("repo", rec.RepoPath))
				liveCleanup()
				if err != nil {
					cleanup()
					return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
				}
			} else {
				diffText, err = runDiff(sr.SystemPath, repoTmp,
					labelA("system", displayPath),
					labelB("repo", rec.RepoPath))
				if err != nil {
					cleanup()
					return nil, fmt.Errorf("core: diff: %s: %w", sr.ID, err)
				}
			}
			cleanup()
			dr.Diff = diffText

		default:
			// Unknown status (e.g. STALE for remote not yet fetched) — skip.
			dr.Skipped = true
			dr.SkipReason = fmt.Sprintf("status %s — run with --fetch to check live state", sr.Status)
		}

		_ = displayPath // used in labels above
		results = append(results, dr)
	}

	// Return results sorted by ID for deterministic output.
	sort.Slice(results, func(i, j int) bool {
		return results[i].ID < results[j].ID
	})

	return results, nil
}

// runDiff shells out to `diff -u fileA fileB` and returns the unified diff
// text. It is not an error for files to differ — diff exits 1 in that case.
// A real error (exit 2, binary files, missing executable, etc.) is returned
// as a Go error.
//
// labelA and labelB are the strings used in the --- / +++ header lines,
// e.g. "repo etc/nginx/nginx.conf" and "system /etc/nginx/nginx.conf".
func runDiff(fileA, fileB, labelA, labelB string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "diff", "-u",
		"--label", labelA,
		"--label", labelB,
		fileA, fileB)
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	if err != nil {
		// Exit code 1 means "files differ" — that is not an error for us.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return buf.String(), nil
		}
		// Exit code 2 or any other failure is a real error.
		return "", fmt.Errorf("diff: %w\n%s", err, buf.String())
	}

	// Exit code 0 means files are identical (should match StatusSynced).
	return buf.String(), nil
}

// labelA returns the --- header label: "repo <path>".
func labelA(side, path string) string {
	return fmt.Sprintf("%s\t%s", side, path)
}

// labelB returns the +++ header label: "system <path>".
func labelB(side, path string) string {
	return fmt.Sprintf("%s\t%s", side, path)
}

// HasDiff returns true if any DiffResult in the slice has a non-empty diff.
// Useful for scripts that want an exit-code-style check without inspecting
// each result individually.
func HasDiff(results []DiffResult) bool {
	for _, r := range results {
		if r.Diff != "" {
			return true
		}
	}
	return false
}

// isExecutableAvailable checks whether the named executable exists in PATH.
// Used at startup to give a clear error if `diff` is not found.
func isExecutableAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// CheckDiffPrereqs returns an error if the external tools required by Diff
// are not available in PATH. Call this before Diff to give a clear message.
func CheckDiffPrereqs() error {
	if !isExecutableAvailable("diff") {
		return fmt.Errorf("core: diff: 'diff' executable not found in PATH — install diffutils")
	}
	return nil
}

// writeTempFile writes data to a temporary file and returns its path.
// The caller is responsible for calling the returned cleanup func to remove
// the file when done. Used when we need to diff content that is not directly
// on disk (e.g. bare-repo content read via git-show).
func writeTempFile(prefix string, data []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp(sysfigfs.SecureTempDir(), "sysfig-diff-"+prefix+"-*")
	if err != nil {
		return "", nil, fmt.Errorf("core: diff: create temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("core: diff: write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("core: diff: close temp file: %w", err)
	}
	name := f.Name()
	var once bool
	return name, func() {
		if !once {
			once = true
			os.Remove(name)
		}
	}, nil
}
