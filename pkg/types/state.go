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
	// HasOwnership is true when UID/GID were explicitly recorded.
	// When false, ApplyMeta skips chown to avoid clobbering ownership
	// with the zero value (uid=0/gid=0 = root).
	// Old records without this field default to false (safe: skip chown).
	HasOwnership bool `json:"has_ownership,omitempty"`
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
	// Group is the directory path used when this file was tracked as part of
	// a directory (e.g. "sysfig track /etc/pacman.d/"). Empty for individually
	// tracked files. Sync uses this to group directory files into one commit.
	Group       string     `json:"group,omitempty"`
	// Branch is the git branch dedicated to this file/group.
	// Format: "track/<repoPath>" for files, "track/<dirPath>" for dir-tracked groups.
	// Empty on old records — sync creates the branch on first commit.
	Branch      string     `json:"branch,omitempty"`
	// Meta holds the file's owner, group, and permission bits captured
	// at the last track or sync. Nil means metadata was never recorded
	// (e.g. records created before this feature was added).
	Meta *FileMeta `json:"meta,omitempty"`
	// SourceProfile, when non-empty, marks this file as owned by a Config
	// Source profile render. Format: "<sourceName>/<profileName>".
	// Empty string means the file was manually tracked via `sysfig track`.
	// Source-managed files are skipped by `sysfig sync` and cannot be
	// re-tracked without --force.
	SourceProfile string `json:"source_profile,omitempty"`
	// LocalOnly marks files that are tracked only in the local state and
	// never pushed to the remote git repo. Use this for sensitive files
	// (e.g. /etc/wireguard/wg0.conf) that must not leave the machine.
	// sync and push skip these records entirely.
	LocalOnly bool `json:"local_only,omitempty"`
	// HashOnly marks files where only the content hash is tracked — no copy
	// is stored in the repo. Useful for integrity monitoring without storing
	// sensitive content. Status reports TAMPERED when the hash drifts.
	HashOnly bool `json:"hash_only,omitempty"`
}

// BackupRecord represents a single backup entry in state.json.
type BackupRecord struct {
	Path      string    `json:"path"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
}

// Node represents a remote machine that can receive encrypted files.
// Each node holds an age X25519 public key; during sync, sysfig encrypts
// every encrypted file to the local master key AND all registered nodes,
// so each machine can independently decrypt its own copy.
type Node struct {
	Name      string            `json:"name"`
	PublicKey string            `json:"public_key"` // age X25519 recipient string (age1…)
	Variables map[string]string `json:"variables,omitempty"`
	AddedAt   time.Time         `json:"added_at"`
}

// State is the top-level structure of state.json.
type State struct {
	Version  int                       `json:"version"`
	Files    map[string]*FileRecord    `json:"files"`
	Backups  map[string][]BackupRecord `json:"backups"`
	Nodes    map[string]*Node          `json:"nodes,omitempty"`
	Excludes []string                  `json:"excludes,omitempty"`
}
