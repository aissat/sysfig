package core_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aissat/sysfig/internal/core"
)

// buildTagFixture writes a state.json containing several tracked files with
// various tag configurations. Returns the baseDir.
//
// Files in the fixture:
//   - id1: /etc/nginx.conf - tags ["linux", "arch"]
//   - id2: /etc/pacman.conf - tags ["linux", "arch"]
//   - id3: /etc/fstab - tags ["linux"]
//   - id4: /home/user/.bashrc - no tags
func buildTagFixture(t *testing.T) string {
	t.Helper()
	baseDir := t.TempDir()

	id1 := core.DeriveID("/etc/nginx.conf")
	id2 := core.DeriveID("/etc/pacman.conf")
	id3 := core.DeriveID("/etc/fstab")
	id4 := core.DeriveID("/home/user/.bashrc")

	s := map[string]interface{}{
		"version": 1,
		"files": map[string]interface{}{
			id1: map[string]interface{}{
				"id": id1, "system_path": "/etc/nginx.conf",
				"repo_path": "etc/nginx.conf", "current_hash": "aaaa", "status": "tracked",
				"tags": []string{"linux", "arch"},
			},
			id2: map[string]interface{}{
				"id": id2, "system_path": "/etc/pacman.conf",
				"repo_path": "etc/pacman.conf", "current_hash": "bbbb", "status": "tracked",
				"tags": []string{"linux", "arch"},
			},
			id3: map[string]interface{}{
				"id": id3, "system_path": "/etc/fstab",
				"repo_path": "etc/fstab", "current_hash": "cccc", "status": "tracked",
				"tags": []string{"linux"},
			},
			id4: map[string]interface{}{
				"id": id4, "system_path": "/home/user/.bashrc",
				"repo_path": "home/user/dot-bashrc", "current_hash": "dddd", "status": "tracked",
			},
		},
		"backups": map[string]interface{}{},
	}
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600))
	return baseDir
}

// ---------------------------------------------------------------------------
// TagList
// ---------------------------------------------------------------------------

func TestTagList_EmptyBaseDir(t *testing.T) {
	_, err := core.TagList(core.TagListOptions{BaseDir: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseDir")
}

func TestTagList_TagCounts(t *testing.T) {
	baseDir := buildTagFixture(t)

	result, err := core.TagList(core.TagListOptions{BaseDir: baseDir})
	require.NoError(t, err)
	require.NotNil(t, result)

	// 1 file (/home/user/.bashrc) has no tags.
	assert.Equal(t, 1, result.Untagged, "untagged count must be 1")

	// Build a map for easy lookup.
	counts := map[string]int{}
	for _, e := range result.Entries {
		counts[e.Tag] = e.Count
	}

	// "linux" appears on id1, id2, id3 → count 3.
	assert.Equal(t, 3, counts["linux"], "linux tag count")
	// "arch" appears on id1, id2 → count 2.
	assert.Equal(t, 2, counts["arch"], "arch tag count")
}

func TestTagList_SortedAlphabetically(t *testing.T) {
	baseDir := buildTagFixture(t)

	result, err := core.TagList(core.TagListOptions{BaseDir: baseDir})
	require.NoError(t, err)

	tags := make([]string, len(result.Entries))
	for i, e := range result.Entries {
		tags[i] = e.Tag
	}
	sorted := make([]string, len(tags))
	copy(sorted, tags)
	sort.Strings(sorted)
	assert.Equal(t, sorted, tags, "entries must be sorted alphabetically by tag name")
}

// ---------------------------------------------------------------------------
// TagAuto
// ---------------------------------------------------------------------------

func TestTagAuto_EmptyBaseDir(t *testing.T) {
	_, err := core.TagAuto(core.TagAutoOptions{BaseDir: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseDir")
}

func TestTagAuto_UpdatesUntaggedOnly(t *testing.T) {
	baseDir := buildTagFixture(t)

	result, err := core.TagAuto(core.TagAutoOptions{BaseDir: baseDir, Overwrite: false})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Only id4 (/home/user/.bashrc) has no tags → should be updated.
	assert.Equal(t, 1, result.Updated, "only untagged files should be updated")
	// id1, id2, id3 are already tagged → skipped.
	assert.Equal(t, 3, result.Skipped, "tagged files must be skipped")
}

func TestTagAuto_OverwriteAll(t *testing.T) {
	baseDir := buildTagFixture(t)

	result, err := core.TagAuto(core.TagAutoOptions{BaseDir: baseDir, Overwrite: true})
	require.NoError(t, err)
	require.NotNil(t, result)

	// All 4 files should be updated.
	assert.Equal(t, 4, result.Updated)
	assert.Equal(t, 0, result.Skipped)
}

// ---------------------------------------------------------------------------
// TagSet
// ---------------------------------------------------------------------------

func TestTagSet_EmptyBaseDir(t *testing.T) {
	_, err := core.TagSet(core.TagSetOptions{BaseDir: "", PathOrID: "/etc/nginx.conf"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseDir")
}

func TestTagSet_EmptyPathOrID(t *testing.T) {
	_, err := core.TagSet(core.TagSetOptions{BaseDir: t.TempDir(), PathOrID: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path or ID")
}

func TestTagSet_BySystemPath(t *testing.T) {
	baseDir := buildTagFixture(t)

	result, err := core.TagSet(core.TagSetOptions{
		BaseDir:  baseDir,
		PathOrID: "/etc/fstab",
		Tags:     []string{"linux", "ubuntu"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "/etc/fstab", result.SystemPath)
	assert.Equal(t, []string{"linux"}, result.OldTags)
	assert.Equal(t, []string{"linux", "ubuntu"}, result.NewTags)
}

func TestTagSet_ByID(t *testing.T) {
	baseDir := buildTagFixture(t)
	id := core.DeriveID("/etc/nginx.conf")

	result, err := core.TagSet(core.TagSetOptions{
		BaseDir:  baseDir,
		PathOrID: id,
		Tags:     []string{"darwin"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "/etc/nginx.conf", result.SystemPath)
	assert.Equal(t, []string{"darwin"}, result.NewTags)
}

func TestTagSet_ByIDPrefix(t *testing.T) {
	baseDir := buildTagFixture(t)
	id := core.DeriveID("/etc/nginx.conf")
	// Use an 8-char prefix (at least 4 chars required).
	prefix := id[:8]

	result, err := core.TagSet(core.TagSetOptions{
		BaseDir:  baseDir,
		PathOrID: prefix,
		Tags:     []string{"freebsd"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "/etc/nginx.conf", result.SystemPath)
}

func TestTagSet_NotFound(t *testing.T) {
	baseDir := buildTagFixture(t)

	_, err := core.TagSet(core.TagSetOptions{
		BaseDir:  baseDir,
		PathOrID: "/nonexistent/path",
		Tags:     []string{"linux"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tracked file found")
}

func TestTagSet_ClearTags(t *testing.T) {
	baseDir := buildTagFixture(t)

	result, err := core.TagSet(core.TagSetOptions{
		BaseDir:  baseDir,
		PathOrID: "/etc/fstab",
		Tags:     nil, // clears all tags
	})
	require.NoError(t, err)
	assert.Empty(t, result.NewTags, "tags must be cleared when nil is passed")
}

// ---------------------------------------------------------------------------
// TagRename
// ---------------------------------------------------------------------------

func TestTagRename_EmptyBaseDir(t *testing.T) {
	_, err := core.TagRename(core.TagRenameOptions{BaseDir: "", OldTag: "a", NewTag: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseDir")
}

func TestTagRename_EmptyTags(t *testing.T) {
	dir := t.TempDir()
	_, err := core.TagRename(core.TagRenameOptions{BaseDir: dir, OldTag: "", NewTag: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	_, err = core.TagRename(core.TagRenameOptions{BaseDir: dir, OldTag: "a", NewTag: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestTagRename_RenamesAcrossFiles(t *testing.T) {
	baseDir := buildTagFixture(t)

	// Rename "arch" → "archlinux" — id1 and id2 both carry "arch".
	result, err := core.TagRename(core.TagRenameOptions{
		BaseDir: baseDir,
		OldTag:  "arch",
		NewTag:  "archlinux",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Two files (id1, id2) had "arch" → both should be updated.
	assert.Equal(t, 2, result.Updated)
}

func TestTagRename_NoMatchingTag(t *testing.T) {
	baseDir := buildTagFixture(t)

	// "fedora" is not used by any file — Updated must be 0.
	result, err := core.TagRename(core.TagRenameOptions{
		BaseDir: baseDir,
		OldTag:  "fedora",
		NewTag:  "rhel",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Updated)
}

func TestTagRename_DeduplicatesIfNewTagAlreadyPresent(t *testing.T) {
	// Set up a file that has both "linux" and "arch" tags.
	// Renaming "arch" → "linux" should leave only one "linux" (dedup).
	baseDir := t.TempDir()
	id := core.DeriveID("/etc/test.conf")
	s := map[string]interface{}{
		"version": 1,
		"files": map[string]interface{}{
			id: map[string]interface{}{
				"id": id, "system_path": "/etc/test.conf",
				"repo_path": "etc/test.conf", "current_hash": "aaaa", "status": "tracked",
				"tags": []string{"linux", "arch"},
			},
		},
		"backups": map[string]interface{}{},
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(baseDir, "state.json"), data, 0o600)

	result, err := core.TagRename(core.TagRenameOptions{
		BaseDir: baseDir,
		OldTag:  "arch",
		NewTag:  "linux", // already present
	})
	require.NoError(t, err)
	// The rename happened, but the final list should be deduplicated.
	assert.Equal(t, 1, result.Updated)
}
