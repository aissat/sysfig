package types_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aissat/sysfig/pkg/types"
)

// TestFileStatus_Values verifies that the FileStatus constants have the correct
// string values that state.json consumers depend on.
func TestFileStatus_Values(t *testing.T) {
	tests := []struct {
		status types.FileStatus
		want   string
	}{
		{types.StatusTracked, "tracked"},
		{types.StatusModified, "modified"},
		{types.StatusConflict, "conflict"},
		{types.StatusUntracked, "untracked"},
	}
	for _, tc := range tests {
		if string(tc.status) != tc.want {
			t.Errorf("FileStatus %q = %q, want %q", tc.status, string(tc.status), tc.want)
		}
	}
}

// TestFileMeta_JSONRoundTrip verifies that FileMeta serialises and deserialises
// correctly via JSON (the format used in state.json).
func TestFileMeta_JSONRoundTrip(t *testing.T) {
	orig := types.FileMeta{
		UID:   1000,
		GID:   1001,
		Owner: "alice",
		Group: "users",
		Mode:  0o644,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal FileMeta: %v", err)
	}
	var got types.FileMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal FileMeta: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

// TestFileMeta_OmitEmptyOwnerGroup verifies that the optional Owner and Group
// fields are omitted when empty (omitempty tag).
func TestFileMeta_OmitEmptyOwnerGroup(t *testing.T) {
	m := types.FileMeta{UID: 0, GID: 0, Mode: 0o755}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := raw["owner"]; ok {
		t.Error("owner key must be absent when empty")
	}
	if _, ok := raw["group"]; ok {
		t.Error("group key must be absent when empty")
	}
}

// TestFileRecord_JSONRoundTrip verifies a full FileRecord round-trips cleanly.
func TestFileRecord_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	rec := types.FileRecord{
		ID:            "abc12345",
		SystemPath:    "/etc/myapp.conf",
		RepoPath:      "etc/myapp.conf",
		CurrentHash:   "deadbeef",
		LastSync:      &now,
		LastApply:     &now,
		Status:        types.StatusTracked,
		Encrypt:       true,
		Template:      true,
		Tags:          []string{"linux", "arch"},
		Group:         "/etc/myapp",
		Branch:        "track/etc/myapp.conf",
		SourceProfile: "corp/proxy",
		LocalOnly:     true,
		HashOnly:      true,
		Meta:          &types.FileMeta{UID: 1000, GID: 1000, Mode: 0o600},
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got types.FileRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Compare key fields (time is compared separately due to timezone).
	if got.ID != rec.ID {
		t.Errorf("ID: got %q, want %q", got.ID, rec.ID)
	}
	if got.SystemPath != rec.SystemPath {
		t.Errorf("SystemPath: got %q, want %q", got.SystemPath, rec.SystemPath)
	}
	if got.Status != rec.Status {
		t.Errorf("Status: got %q, want %q", got.Status, rec.Status)
	}
	if got.Encrypt != rec.Encrypt {
		t.Errorf("Encrypt: got %v, want %v", got.Encrypt, rec.Encrypt)
	}
	if got.Template != rec.Template {
		t.Errorf("Template: got %v, want %v", got.Template, rec.Template)
	}
	if got.LocalOnly != rec.LocalOnly {
		t.Errorf("LocalOnly: got %v, want %v", got.LocalOnly, rec.LocalOnly)
	}
	if got.HashOnly != rec.HashOnly {
		t.Errorf("HashOnly: got %v, want %v", got.HashOnly, rec.HashOnly)
	}
	if len(got.Tags) != len(rec.Tags) {
		t.Errorf("Tags: got %v, want %v", got.Tags, rec.Tags)
	}
	if got.Meta == nil || *got.Meta != *rec.Meta {
		t.Errorf("Meta: got %v, want %v", got.Meta, rec.Meta)
	}
	if got.LastSync == nil || !got.LastSync.Equal(*rec.LastSync) {
		t.Errorf("LastSync mismatch")
	}
}

// TestFileRecord_OmitEmptyFields verifies that optional fields with zero values
// are omitted from JSON output, keeping state.json small.
func TestFileRecord_OmitEmptyFields(t *testing.T) {
	rec := types.FileRecord{
		ID:         "abc12345",
		SystemPath: "/etc/x.conf",
		RepoPath:   "etc/x.conf",
		Status:     types.StatusTracked,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	// These optional fields should not appear when zero/empty.
	for _, key := range []string{"last_sync", "last_apply", "tags", "meta", "source_profile", "local_only", "hash_only"} {
		if _, ok := raw[key]; ok {
			t.Errorf("key %q must be absent for zero-value FileRecord", key)
		}
	}
}

// TestBackupRecord_JSONRoundTrip verifies BackupRecord serialises correctly.
func TestBackupRecord_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	orig := types.BackupRecord{
		Path:      "/var/backups/myapp.conf.bak",
		Hash:      "cafebabe",
		CreatedAt: now,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got types.BackupRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Path != orig.Path {
		t.Errorf("Path: got %q, want %q", got.Path, orig.Path)
	}
	if got.Hash != orig.Hash {
		t.Errorf("Hash: got %q, want %q", got.Hash, orig.Hash)
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, orig.CreatedAt)
	}
}

// TestNode_JSONRoundTrip verifies that a Node serialises and deserialises cleanly.
func TestNode_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	orig := types.Node{
		Name:      "laptop",
		PublicKey: "age1qjsz5yrqctdmq6q85e2v8xhvuwa3g4ggd6ltz7g3sj3hj7k9d4qsxkyd5",
		Variables: map[string]string{"ZONE": "eu-west-1"},
		AddedAt:   now,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got types.Node
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != orig.Name {
		t.Errorf("Name: got %q, want %q", got.Name, orig.Name)
	}
	if got.PublicKey != orig.PublicKey {
		t.Errorf("PublicKey mismatch")
	}
	if got.Variables["ZONE"] != orig.Variables["ZONE"] {
		t.Errorf("Variables mismatch")
	}
}

// TestNode_OmitEmptyVariables verifies that Variables is omitted when nil.
func TestNode_OmitEmptyVariables(t *testing.T) {
	n := types.Node{
		Name:      "server",
		PublicKey: "age1abc",
		AddedAt:   time.Now(),
	}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := raw["variables"]; ok {
		t.Error("variables key must be absent when nil")
	}
}

// TestState_JSONRoundTrip verifies the top-level State struct.
func TestState_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	orig := types.State{
		Version: 1,
		Files: map[string]*types.FileRecord{
			"abc12345": {
				ID:         "abc12345",
				SystemPath: "/etc/myapp.conf",
				RepoPath:   "etc/myapp.conf",
				Status:     types.StatusTracked,
			},
		},
		Backups: map[string][]types.BackupRecord{
			"/etc/myapp.conf": {
				{Path: "/var/backups/myapp.conf.bak", Hash: "deadbeef", CreatedAt: now},
			},
		},
		Nodes: map[string]*types.Node{
			"laptop": {Name: "laptop", PublicKey: "age1abc", AddedAt: now},
		},
		Excludes: []string{"/etc/myapp/secret.conf"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got types.State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != orig.Version {
		t.Errorf("Version: got %d, want %d", got.Version, orig.Version)
	}
	if len(got.Files) != 1 {
		t.Errorf("Files: got %d, want 1", len(got.Files))
	}
	if len(got.Backups) != 1 {
		t.Errorf("Backups: got %d, want 1", len(got.Backups))
	}
	if len(got.Nodes) != 1 {
		t.Errorf("Nodes: got %d, want 1", len(got.Nodes))
	}
	if len(got.Excludes) != 1 || got.Excludes[0] != "/etc/myapp/secret.conf" {
		t.Errorf("Excludes: got %v, want [/etc/myapp/secret.conf]", got.Excludes)
	}
}

// TestState_OmitEmptyNodes verifies that Nodes and Excludes are omitted
// from JSON when nil/empty (omitempty tag).
func TestState_OmitEmptyNodes(t *testing.T) {
	s := types.State{
		Version: 1,
		Files:   map[string]*types.FileRecord{},
		Backups: map[string][]types.BackupRecord{},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := raw["nodes"]; ok {
		t.Error("nodes must be absent when nil")
	}
	if _, ok := raw["excludes"]; ok {
		t.Error("excludes must be absent when nil")
	}
}
