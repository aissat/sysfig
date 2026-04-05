package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/internal/state"
)

// globalProfile holds the value of the --profile persistent flag.
// Set by newRootCmd() before any subcommand runs.
var globalProfile string

// sysfigHome returns ~/.sysfig for the real user running the process.
//
// Uses user.Current() which reads /etc/passwd by UID — immune to a stale
// $HOME env var (e.g. after `su <user>` without a login shell). Falls back
// to os.UserHomeDir() if user.Current() fails.
//
// When a non-root user runs `sudo sysfig`, the process UID is 0 (root) so
// this correctly returns /root/.sysfig — system configs land in root's repo.
// When ali runs `sysfig` (no sudo), UID is ali's → /home/ali/.sysfig.
func sysfigHome() string {
	// When running under sudo, use the invoking user's home so that
	// "sudo sysfig" stores data in ~/.sysfig of the real user, not root.
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil && u.HomeDir != "" {
			return filepath.Join(u.HomeDir, ".sysfig")
		}
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return filepath.Join(u.HomeDir, ".sysfig")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sysfig"
	}
	return filepath.Join(home, ".sysfig")
}

// fixSudoOwnership re-chowns baseDir to the invoking user when running under
// sudo, so that files written by root land with the correct owner.
func fixSudoOwnership(baseDir string) {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	_ = filepath.WalkDir(baseDir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		uid := toInt(u.Uid)
		gid := toInt(u.Gid)
		_ = os.Lchown(path, uid, gid)
		return nil
	})
}

func toInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// profilesDir returns ~/.sysfig/profiles.
func profilesDir() string { return filepath.Join(sysfigHome(), "profiles") }

// defaultBaseDir returns the active base directory.
// Priority: --profile flag > SYSFIG_PROFILE env var > SYSFIG_BASE_DIR env var > ~/.sysfig
func defaultBaseDir() string {
	profile := globalProfile
	if profile == "" {
		profile = os.Getenv("SYSFIG_PROFILE")
	}
	if profile != "" {
		return filepath.Join(profilesDir(), profile)
	}
	if dir := os.Getenv("SYSFIG_BASE_DIR"); dir != "" {
		return dir
	}
	return sysfigHome()
}

// resolveBaseDir returns baseDir if non-empty, otherwise calls defaultBaseDir().
// Every command should call this at the top of RunE so that --profile is
// honoured even though flag defaults are evaluated at command-creation time.
func resolveBaseDir(baseDir string) string {
	if baseDir != "" {
		return baseDir
	}
	return defaultBaseDir()
}

// resolveSysRoot returns sysRoot if non-empty, then SYSFIG_SYS_ROOT env var,
// then "" (meaning real system root — no prefix stripping).
// This allows labs and CI to export SYSFIG_SYS_ROOT=/sysroot once instead of
// passing --sys-root on every command.
func resolveSysRoot(sysRoot string) string {
	if sysRoot != "" {
		return sysRoot
	}
	return os.Getenv("SYSFIG_SYS_ROOT")
}

// isatty returns true if stdout is connected to a terminal.
func isatty() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// autoTrackNewInTarget stages any untracked files found in group directories
// that fall under the given target path. Called before sync so new files are
// included in the commit without requiring a separate `sysfig track`.
func autoTrackNewInTarget(baseDir, target string) {
	statePath := filepath.Join(baseDir, "state.json")
	sm := state.NewManager(statePath)
	s, err := sm.Load()
	if err != nil {
		return
	}
	repoDir := filepath.Join(baseDir, "repo.git")

	absTarget := target
	if abs, err := filepath.Abs(target); err == nil {
		absTarget = abs
	}

	tracked := make(map[string]bool, len(s.Files))
	for _, rec := range s.Files {
		tracked[rec.SystemPath] = true
	}
	excluded := make(map[string]bool, len(s.Excludes))
	for _, ex := range s.Excludes {
		excluded[ex] = true
	}

	for _, rec := range s.Files {
		if rec.Group == "" {
			continue
		}
		// Only scan group dirs under the target.
		if !strings.HasPrefix(rec.Group, absTarget) && rec.Group != absTarget {
			continue
		}
		filepath.WalkDir(rec.Group, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if excluded[path] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if tracked[path] {
				return nil
			}
			core.Track(core.TrackOptions{ //nolint:errcheck
				SystemPath: path,
				StateDir:   baseDir,
				RepoDir:    repoDir,
				Group:      rec.Group,
			})
			tracked[path] = true
			return nil
		})
	}
}

// resolveSyncTarget resolves a sync target (path, hash, or CWD) to a list of
// file IDs. Returns nil (= all files) if target is empty and CWD doesn't match.
func resolveSyncTarget(baseDir, target string) []string {
	statePath := filepath.Join(baseDir, "state.json")
	sm := state.NewManager(statePath)
	s, err := sm.Load()
	if err != nil {
		return nil
	}

	absTarget := target
	if target != "" {
		if abs, err := filepath.Abs(target); err == nil {
			absTarget = abs
		}
	}

	var ids []string
	for id, rec := range s.Files {
		match := false
		switch {
		case target == "":
			// CWD mode: match files whose path starts with absTarget (CWD).
			match = strings.HasPrefix(rec.SystemPath, absTarget+"/") ||
				rec.SystemPath == absTarget ||
				rec.Group == absTarget ||
				strings.HasPrefix(rec.Group, absTarget+"/")
		case id == target || id[:min8(len(id))] == target:
			// Hash/ID match (full or short 8-char prefix).
			match = true
		case rec.SystemPath == absTarget ||
			strings.HasPrefix(rec.SystemPath, absTarget+"/") ||
			rec.Group == absTarget ||
			strings.HasPrefix(rec.Group, absTarget+"/"):
			// Path match.
			match = true
		}
		if match {
			ids = append(ids, id)
		}
	}
	return ids
}

func min8(n int) int {
	if n < 8 {
		return n
	}
	return 8
}

// autoSyncTracked commits only the given file IDs to their track branches.
// Called automatically after `sysfig track` so newly tracked files appear in
// `sysfig log` immediately without requiring a separate `sysfig sync`.
func autoSyncTracked(baseDir string, ids []string) {
	_, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "sysfig: track",
		FileIDs: ids,
	})
	if err != nil {
		fmt.Printf("  %s auto-sync: %v\n", clrWarn.Sprint("warn:"), err)
	}
}

// filterIDsByHost restricts a file ID list to records matching SYSFIG_HOST.
// When SYSFIG_HOST is set: keep only IDs whose Remote matches that host.
// When SYSFIG_HOST is not set: return ids unchanged.
// When ids is nil (= all files): builds the filtered list from state.
func filterIDsByHost(baseDir string, ids []string) []string {
	host := os.Getenv("SYSFIG_HOST")
	if host == "" {
		return ids
	}
	sm := state.NewManager(filepath.Join(baseDir, "state.json"))
	s, err := sm.Load()
	if err != nil {
		return ids
	}
	// Build set of candidate IDs.
	candidates := ids
	if candidates == nil {
		for id := range s.Files {
			candidates = append(candidates, id)
		}
	}
	var filtered []string
	for _, id := range candidates {
		rec, ok := s.Files[id]
		if ok && rec.Remote == host {
			filtered = append(filtered, id)
		}
	}
	return filtered
}
