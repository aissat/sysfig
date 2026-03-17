package fs

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/sysfig-dev/sysfig/pkg/types"
)

// ReadMeta reads the ownership and permission metadata of the file at path
// and returns it as a *types.FileMeta. The Owner and Group name fields are
// resolved from the UID/GID where possible; if resolution fails they are left
// empty (the numeric IDs are always populated).
func ReadMeta(path string) (*types.FileMeta, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("fs: meta: stat %q: %w", path, err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("fs: meta: cannot read uid/gid from %q (unsupported platform)", path)
	}

	meta := &types.FileMeta{
		UID:  int(stat.Uid),
		GID:  int(stat.Gid),
		Mode: uint32(info.Mode().Perm()),
	}

	// Best-effort name resolution — never fatal.
	if u, err := user.LookupId(strconv.Itoa(meta.UID)); err == nil {
		meta.Owner = u.Username
	}
	if g, err := user.LookupGroupId(strconv.Itoa(meta.GID)); err == nil {
		meta.Group = g.Name
	}

	return meta, nil
}

// MetaApplyResult reports what happened when ApplyMeta ran.
type MetaApplyResult struct {
	// ChmodOK is true if the permission bits were applied successfully.
	ChmodOK bool
	// ChownOK is true if the ownership was applied successfully.
	ChownOK bool
	// ChownWarning is non-empty when chown failed due to insufficient
	// privilege. It is NOT treated as a hard error — the caller should
	// print it as a warning.
	ChownWarning string
	// Err is set for unexpected (non-permission) failures.
	Err error
}

// ApplyMeta restores the ownership and permission bits described by meta onto
// the file at path.
//
// Permission (chmod) is always attempted and returns a hard error on failure.
//
// Ownership (chown) requires elevated privilege on most systems. If chown
// fails with EPERM or EACCES the error is demoted to a warning in
// MetaApplyResult.ChownWarning so the caller can log it without aborting the
// apply operation. Any other chown error is treated as a hard error.
//
// This mirrors the behaviour of tools like rsync and cp --preserve: do what
// you can, warn about what you cannot.
func ApplyMeta(path string, meta *types.FileMeta) MetaApplyResult {
	result := MetaApplyResult{}

	if meta == nil {
		return result
	}

	// 1. chmod — always attempt, hard error on failure.
	if err := os.Chmod(path, os.FileMode(meta.Mode)); err != nil {
		result.Err = fmt.Errorf("fs: meta: chmod %q: %w", path, err)
		return result
	}
	result.ChmodOK = true

	// 2. chown — privilege-aware, demote EPERM/EACCES to warning.
	if err := os.Lchown(path, meta.UID, meta.GID); err != nil {
		if isPermError(err) {
			owner := meta.Owner
			if owner == "" {
				owner = strconv.Itoa(meta.UID)
			}
			group := meta.Group
			if group == "" {
				group = strconv.Itoa(meta.GID)
			}
			result.ChownWarning = fmt.Sprintf(
				"fs: meta: chown %q to %s:%s failed (permission denied) — run with sudo to restore ownership",
				path, owner, group,
			)
		} else {
			result.Err = fmt.Errorf("fs: meta: chown %q: %w", path, err)
		}
		return result
	}
	result.ChownOK = true

	return result
}

// isPermError returns true when err wraps a syscall permission error
// (EPERM or EACCES) — the two errno values that indicate "not allowed"
// rather than a genuine I/O failure.
func isPermError(err error) bool {
	if err == nil {
		return false
	}
	// os.Lchown wraps the underlying syscall error in *os.PathError.
	// Unwrap to get at the raw errno.
	unwrapped := err
	for {
		if pe, ok := unwrapped.(*os.PathError); ok {
			unwrapped = pe.Err
			continue
		}
		break
	}
	return unwrapped == syscall.EPERM || unwrapped == syscall.EACCES
}
