package core

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// SnapDir returns the snaps directory inside baseDir.
func SnapDir(baseDir string) string {
	return filepath.Join(baseDir, "snaps")
}

// SnapFile records one file captured in a snapshot.
type SnapFile struct {
	ID         string          `json:"id"`
	SystemPath string          `json:"system_path"`
	RepoPath   string          `json:"repo_path"`
	Hash       string          `json:"hash"`
	Meta       *types.FileMeta `json:"meta,omitempty"`
}

// SnapInfo is the manifest stored as snap.json inside each snapshot directory.
type SnapInfo struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"created_at"`
	Files     []SnapFile `json:"files"`
	// ShortID is a deterministic 8-char hex prefix derived from the ID via
	// SHA-256. Computed at load time — not stored in snap.json.
	ShortID string `json:"-"`
}

// snapShortID returns an 8-character hex string derived from the snapshot ID.
// Deterministic: same ID always produces the same ShortID.
func snapShortID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%x", sum[:4]) // 4 bytes = 8 hex chars
}

// SnapResolveID resolves a user-supplied reference (full ID or short hash) to
// the canonical full snapshot ID. Returns an error if not found or ambiguous.
func SnapResolveID(baseDir, ref string) (string, error) {
	snaps, err := SnapList(baseDir)
	if err != nil {
		return "", err
	}
	// Exact match first.
	for _, s := range snaps {
		if s.ID == ref {
			return s.ID, nil
		}
	}
	// Short-hash prefix match.
	var matches []string
	for _, s := range snaps {
		if strings.HasPrefix(s.ShortID, ref) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("snap: no snapshot matches %q", ref)
	default:
		return "", fmt.Errorf("snap: ambiguous ref %q — matches: %s", ref, strings.Join(matches, ", "))
	}
}

// SnapTakeOptions configures SnapTake.
type SnapTakeOptions struct {
	// BaseDir is the sysfig data directory (e.g. ~/.sysfig).
	BaseDir string
	// SysRoot is the optional system root used during testing.
	SysRoot string
	// Label is an optional human-readable description for the snapshot.
	Label string
	// IDs limits the snapshot to these tracked IDs. Empty = all tracked files.
	IDs []string
}

// SnapTakeResult is returned by SnapTake.
type SnapTakeResult struct {
	SnapInfo
	SnapPath string // absolute path to the snapshot directory
}

// SnapTake captures the current on-disk content of tracked files into a new
// named snapshot directory under <BaseDir>/snaps/<id>/.
func SnapTake(opts SnapTakeOptions) (*SnapTakeResult, error) {
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)
	var st *types.State
	if err := sm.WithLock(func(s *types.State) error {
		st = s
		return nil
	}); err != nil {
		return nil, fmt.Errorf("snap: load state: %w", err)
	}

	// Collect files to snapshot.
	var records []*types.FileRecord
	if len(opts.IDs) == 0 {
		for _, r := range st.Files {
			records = append(records, r)
		}
	} else {
		for _, id := range opts.IDs {
			r, ok := st.Files[id]
			if !ok {
				return nil, fmt.Errorf("snap: unknown id %q", id)
			}
			records = append(records, r)
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("snap: no tracked files found")
	}

	// Stable ordering.
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })

	// Build a unique snapshot ID.
	ts := time.Now()
	rawID := ts.Format("20060102-150405")
	if opts.Label != "" {
		slug := strings.Map(func(r rune) rune {
			if r == ' ' || r == '/' || r == '\\' {
				return '-'
			}
			return r
		}, opts.Label)
		rawID = rawID + "-" + slug
	}

	snapPath := filepath.Join(SnapDir(opts.BaseDir), rawID)
	if err := os.MkdirAll(snapPath, 0700); err != nil {
		return nil, fmt.Errorf("snap: create snap dir: %w", err)
	}

	var snapFiles []SnapFile
	for _, rec := range records {
		sysPath := rec.SystemPath
		if opts.SysRoot != "" && !strings.HasPrefix(sysPath, opts.SysRoot) {
			sysPath = filepath.Join(opts.SysRoot, sysPath)
		}

		// Read the live file.
		src, err := os.Open(sysPath)
		if err != nil {
			// File might be MISSING on disk — store what we know but skip copy.
			snapFiles = append(snapFiles, SnapFile{
				ID:         rec.ID,
				SystemPath: rec.SystemPath,
				RepoPath:   rec.RepoPath,
				Hash:       rec.CurrentHash,
				Meta:       rec.Meta,
			})
			continue
		}

		// Mirror directory structure inside snap dir.
		destDir := filepath.Join(snapPath, "files", filepath.Dir(rec.RepoPath))
		if err := os.MkdirAll(destDir, 0700); err != nil {
			src.Close()
			return nil, fmt.Errorf("snap: mkdir %s: %w", destDir, err)
		}
		destPath := filepath.Join(snapPath, "files", rec.RepoPath)
		dst, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			src.Close()
			return nil, fmt.Errorf("snap: create dest %s: %w", destPath, err)
		}
		if _, err = io.Copy(dst, src); err != nil {
			src.Close()
			dst.Close()
			return nil, fmt.Errorf("snap: copy %s: %w", sysPath, err)
		}
		src.Close()
		dst.Close()

		// Compute hash of the captured content.
		fileHash, err := hash.File(destPath)
		if err != nil {
			fileHash = rec.CurrentHash // fallback
		}

		snapFiles = append(snapFiles, SnapFile{
			ID:         rec.ID,
			SystemPath: rec.SystemPath,
			RepoPath:   rec.RepoPath,
			Hash:       fileHash,
			Meta:       rec.Meta,
		})
	}

	info := SnapInfo{
		ID:        rawID,
		Label:     opts.Label,
		CreatedAt: ts,
		Files:     snapFiles,
	}

	// Write snap.json.
	manifestPath := filepath.Join(snapPath, "snap.json")
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("snap: marshal manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		return nil, fmt.Errorf("snap: write manifest: %w", err)
	}

	info.ShortID = snapShortID(info.ID)
	return &SnapTakeResult{SnapInfo: info, SnapPath: snapPath}, nil
}

// SnapList returns all snapshots sorted newest-first.
func SnapList(baseDir string) ([]SnapInfo, error) {
	snapsDir := SnapDir(baseDir)
	entries, err := os.ReadDir(snapsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("snap: list: %w", err)
	}

	var snaps []SnapInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(snapsDir, e.Name(), "snap.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue // corrupt/partial snap — skip
		}
		var info SnapInfo
		if err := json.Unmarshal(data, &info); err != nil {
			continue
		}
		info.ShortID = snapShortID(info.ID)
		snaps = append(snaps, info)
	}

	// Sort newest-first.
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.After(snaps[j].CreatedAt)
	})
	return snaps, nil
}

// SnapRestoreOptions configures SnapRestore.
type SnapRestoreOptions struct {
	BaseDir string
	SysRoot string
	SnapID  string
	// IDs limits restore to these file IDs. Empty = all files in the snapshot.
	IDs    []string
	DryRun bool
}

// SnapRestoreResult reports what was restored.
type SnapRestoreResult struct {
	Restored []SnapFile
	Skipped  []SnapFile
	DryRun   bool
}

// SnapRestore copies snapshot files back to their system paths.
func SnapRestore(opts SnapRestoreOptions) (*SnapRestoreResult, error) {
	// Resolve short hash or full ID.
	fullID, err := SnapResolveID(opts.BaseDir, opts.SnapID)
	if err != nil {
		return nil, fmt.Errorf("snap: restore: %w", err)
	}
	opts.SnapID = fullID

	snapPath := filepath.Join(SnapDir(opts.BaseDir), opts.SnapID)
	manifestPath := filepath.Join(snapPath, "snap.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("snap: restore: snapshot %q not found", opts.SnapID)
	}
	var info SnapInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("snap: restore: corrupt manifest: %w", err)
	}

	// Build set of requested IDs.
	want := map[string]bool{}
	for _, id := range opts.IDs {
		want[id] = true
	}

	result := &SnapRestoreResult{DryRun: opts.DryRun}

	for _, sf := range info.Files {
		if len(want) > 0 && !want[sf.ID] {
			result.Skipped = append(result.Skipped, sf)
			continue
		}

		snapFilePath := filepath.Join(snapPath, "files", sf.RepoPath)
		if _, err := os.Stat(snapFilePath); err != nil {
			// File not captured (was MISSING at snap time) — skip.
			result.Skipped = append(result.Skipped, sf)
			continue
		}

		sysPath := sf.SystemPath
		if opts.SysRoot != "" && !strings.HasPrefix(sysPath, opts.SysRoot) {
			sysPath = filepath.Join(opts.SysRoot, sysPath)
		}

		if opts.DryRun {
			result.Restored = append(result.Restored, sf)
			continue
		}

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(sysPath), 0755); err != nil {
			return nil, fmt.Errorf("snap: restore: mkdir for %s: %w", sysPath, err)
		}

		// Copy snap file → system path.
		if err := copyFile(snapFilePath, sysPath); err != nil {
			return nil, fmt.Errorf("snap: restore: copy to %s: %w", sysPath, err)
		}

		// Restore permissions if captured.
		if sf.Meta != nil {
			_ = os.Chmod(sysPath, os.FileMode(sf.Meta.Mode))
		}

		result.Restored = append(result.Restored, sf)
	}

	return result, nil
}

// SnapFilterByDir returns only the snapshots that contain at least one file
// whose canonical SystemPath is under dir (prefix match). If dir is empty the
// full list is returned unchanged.
//
// "Under dir" means:
//
//	sf.SystemPath == dir  OR  strings.HasPrefix(sf.SystemPath, dir+"/")
//
// This lets callers scope list/undo to the current working directory so that
// /etc snaps and /home/user snaps are kept separate in the output.
func SnapFilterByDir(snaps []SnapInfo, dir string) []SnapInfo {
	if dir == "" {
		return snaps
	}
	prefix := strings.TrimRight(dir, "/") + "/"
	var out []SnapInfo
	for _, s := range snaps {
		for _, f := range s.Files {
			if f.SystemPath == dir || strings.HasPrefix(f.SystemPath, prefix) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// SnapFilesUnderDir returns the subset of files in info whose SystemPath is
// under dir. If dir is empty all files are returned.
func SnapFilesUnderDir(info SnapInfo, dir string) []SnapFile {
	if dir == "" {
		return info.Files
	}
	prefix := strings.TrimRight(dir, "/") + "/"
	var out []SnapFile
	for _, f := range info.Files {
		if f.SystemPath == dir || strings.HasPrefix(f.SystemPath, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// SnapUndoOptions configures SnapUndo.
type SnapUndoOptions struct {
	BaseDir string
	SysRoot string
	// Dir, when non-empty, scopes the undo to the most recent snapshot that
	// contains files under this directory, and restores only those files.
	// Empty = global undo (most recent snapshot, all files).
	Dir    string
	IDs    []string
	DryRun bool
}

// SnapUndo restores the most recent snapshot. It is a convenience wrapper
// around SnapList + SnapRestore — the caller does not need to know the ID.
// When Dir is set, only the most recent snapshot touching that directory is
// used, and only the matching files are restored.
func SnapUndo(opts SnapUndoOptions) (*SnapRestoreResult, string, error) {
	all, err := SnapList(opts.BaseDir)
	if err != nil {
		return nil, "", fmt.Errorf("snap undo: %w", err)
	}

	snaps := SnapFilterByDir(all, opts.Dir)
	if len(snaps) == 0 {
		if opts.Dir != "" {
			return nil, "", fmt.Errorf("snap undo: no snapshots found for %q — nothing to undo", opts.Dir)
		}
		return nil, "", fmt.Errorf("snap undo: no snapshots found — nothing to undo")
	}

	latest := snaps[0]

	// When scoping by Dir, collect the IDs of matching files and pass them as
	// an ID filter so SnapRestore only touches the relevant files.
	ids := opts.IDs
	if opts.Dir != "" && len(ids) == 0 {
		for _, f := range SnapFilesUnderDir(latest, opts.Dir) {
			ids = append(ids, f.ID)
		}
	}

	result, err := SnapRestore(SnapRestoreOptions{
		BaseDir: opts.BaseDir,
		SysRoot: opts.SysRoot,
		SnapID:  latest.ID,
		IDs:     ids,
		DryRun:  opts.DryRun,
	})
	return result, latest.ID, err
}

// SnapDrop deletes a snapshot directory entirely.
func SnapDrop(baseDir, snapID string) error {
	fullID, err := SnapResolveID(baseDir, snapID)
	if err != nil {
		return fmt.Errorf("snap: drop: %w", err)
	}
	snapPath := filepath.Join(SnapDir(baseDir), fullID)
	if _, err := os.Stat(snapPath); os.IsNotExist(err) {
		return fmt.Errorf("snap: drop: snapshot %q not found", fullID)
	}
	if err := os.RemoveAll(snapPath); err != nil {
		return fmt.Errorf("snap: drop: %w", err)
	}
	return nil
}

// copyFile is a simple src→dst file copy helper.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
