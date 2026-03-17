package types

import (
	"time"
)

// FileStatus represents the tracking state of a file.
type FileStatus string

const (
	StatusTracked   FileStatus = "tracked"
	StatusModified  FileStatus = "modified"
	StatusConflict  FileStatus = "conflict"
	StatusUntracked FileStatus = "untracked"
)

// FileMeta holds file system metadata captured at track/sync time.
// All fields are recorded so that apply can restore them exactly.
type FileMeta struct {
	// UID and GID are the numeric owner and group IDs.
	UID int `json:"uid"`
	GID int `json:"gid"`
	// Owner and Group are the human-readable names (informational only;
	// apply uses UID/GID for the actual chown call).
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
	// Mode is the file permission bits as a decimal uint32
	// (e.g. 0o644 → 420). Stored as a number for reliable JSON round-trip.
	Mode uint32 `json:"mode"`
}

// FileRecord represents a tracked file's state in state.json.
type FileRecord struct {
	ID          string     `json:"id"`
	SystemPath  string     `json:"system_path"`
	RepoPath    string     `json:"repo_path"`
	CurrentHash string     `json:"current_hash"`
	LastSync    *time.Time `json:"last_sync,omitempty"`
	LastApply   *time.Time `json:"last_apply,omitempty"`
	Status      FileStatus `json:"status"`
	Encrypt     bool       `json:"encrypt,omitempty"`
	Template    bool       `json:"template,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	// Meta holds the file's owner, group, and permission bits captured
	// at the last track or sync. Nil means metadata was never recorded
	// (e.g. records created before this feature was added).
	Meta *FileMeta `json:"meta,omitempty"`
}

// BackupRecord represents a single backup entry in state.json.
type BackupRecord struct {
	Path      string    `json:"path"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
}

// State is the top-level structure of state.json.
type State struct {
	Version int                       `json:"version"`
	Files   map[string]*FileRecord    `json:"files"`
	Backups map[string][]BackupRecord `json:"backups"`
}
