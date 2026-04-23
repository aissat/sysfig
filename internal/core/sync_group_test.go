package core_test

// sync_group_test.go — tests for group-tracked directory lifecycle:
//   - CREATE: new file added to group dir is committed on the shared branch
//   - UPDATE: existing group file content change is committed
//   - DELETE: group file deleted from disk is removed from git tree and
//             untracked from state.json in the same commit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aissat/sysfig/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── helpers local to this file ────────────────────────────────────────────────

// gitFileExistsOnBranch reports whether relPath is present in the tree of branch.
func gitFileExistsOnBranch(t *testing.T, repoDir, branch, relPath string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+repoDir, "ls-tree", "--name-only",
		branch, relPath)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}

// ── TestSync_Group_Create ─────────────────────────────────────────────────────

// TestSync_Group_Create verifies that when a new file is present on disk for
// a group-tracked directory it is committed on the group branch and the
// SyncResult.CommittedFiles includes its repo-relative path.
func TestSync_Group_Create(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	sysRoot := t.TempDir()

	groupDir := "/etc/myapp"
	appDir := filepath.Join(sysRoot, "etc", "myapp")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	// Seed group with one existing file and one "new" file that has never been
	// committed (simulates a file created after the last sync).
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "old.conf"), []byte("old\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "new.conf"), []byte("new\n"), 0o644))

	id1 := core.DeriveID("/etc/myapp/old.conf")
	id2 := core.DeriveID("/etc/myapp/new.conf")
	writeStateMulti(t, baseDir, [][4]string{
		{id1, "/etc/myapp/old.conf", "etc/myapp/old.conf", groupDir},
		{id2, "/etc/myapp/new.conf", "etc/myapp/new.conf", groupDir},
	})

	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: group create",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.True(t, result.Committed, "sync must create a commit when new group file exists")

	repoDir := filepath.Join(baseDir, "repo.git")
	groupBranch := "track/etc/myapp"

	// Both files must appear on the group branch.
	for _, relPath := range []string{"etc/myapp/old.conf", "etc/myapp/new.conf"} {
		assert.True(t, gitFileExistsOnBranch(t, repoDir, groupBranch, relPath),
			"file %s must be committed on group branch", relPath)
	}

	// new.conf must be listed in CommittedFiles.
	assert.Contains(t, result.CommittedFiles, "etc/myapp/new.conf",
		"CommittedFiles must include the new file's repo-relative path")
	assert.Empty(t, result.DeletedFiles, "no files should be deleted in create scenario")
}

// ── TestSync_Group_Update ─────────────────────────────────────────────────────

// TestSync_Group_Update verifies that modifying the content of a group-tracked
// file produces a new commit on the group branch with the updated content.
func TestSync_Group_Update(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	sysRoot := t.TempDir()

	groupDir := "/etc/nginx"
	nginxDir := filepath.Join(sysRoot, "etc", "nginx")
	require.NoError(t, os.MkdirAll(nginxDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nginxDir, "nginx.conf"), []byte("v1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(nginxDir, "mime.types"), []byte("types{}\n"), 0o644))

	id1 := core.DeriveID("/etc/nginx/nginx.conf")
	id2 := core.DeriveID("/etc/nginx/mime.types")
	writeStateMulti(t, baseDir, [][4]string{
		{id1, "/etc/nginx/nginx.conf", "etc/nginx/nginx.conf", groupDir},
		{id2, "/etc/nginx/mime.types", "etc/nginx/mime.types", groupDir},
	})

	// First sync — commit initial content.
	_, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: initial nginx",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)

	// Modify nginx.conf content.
	require.NoError(t, os.WriteFile(filepath.Join(nginxDir, "nginx.conf"), []byte("v2\n"), 0o644))

	// Second sync — must commit the update.
	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: update nginx.conf",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.True(t, result.Committed, "sync must create a commit when group file content changes")

	// Updated content must be in the repo.
	repoDir := filepath.Join(baseDir, "repo.git")
	content, err := gitBlobContent(t, repoDir, "track/etc/nginx", "etc/nginx/nginx.conf")
	require.NoError(t, err)
	assert.Equal(t, "v2\n", string(content), "repo must contain updated nginx.conf content")

	// mime.types was not changed — it must still exist on the branch.
	assert.True(t, gitFileExistsOnBranch(t, repoDir, "track/etc/nginx", "etc/nginx/mime.types"),
		"unchanged mime.types must still be present on branch after update commit")

	assert.Empty(t, result.DeletedFiles, "no files should be deleted in update scenario")
}

// ── TestSync_Group_Delete ─────────────────────────────────────────────────────

// TestSync_Group_Delete is the primary regression test for the "missing file
// silently skipped" bug.  When a tracked group file no longer exists on disk
// the sync must:
//
//	(a) remove it from the git tree (git update-index --remove)
//	(b) add a commit on the group branch recording the deletion
//	(c) untrack the file from state.json
//	(d) report the system path in SyncResult.DeletedFiles
func TestSync_Group_Delete(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	sysRoot := t.TempDir()

	groupDir := "/etc/gtk-4.0"
	gtkDir := filepath.Join(sysRoot, "etc", "gtk-4.0")
	require.NoError(t, os.MkdirAll(gtkDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gtkDir, "gtk.css"), []byte(".widget{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(gtkDir, "dark.css"), []byte(".dark{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(gtkDir, "light.css"), []byte(".light{}\n"), 0o644))

	idGtk := core.DeriveID("/etc/gtk-4.0/gtk.css")
	idDark := core.DeriveID("/etc/gtk-4.0/dark.css")
	idLight := core.DeriveID("/etc/gtk-4.0/light.css")
	writeStateMulti(t, baseDir, [][4]string{
		{idGtk, "/etc/gtk-4.0/gtk.css", "etc/gtk-4.0/gtk.css", groupDir},
		{idDark, "/etc/gtk-4.0/dark.css", "etc/gtk-4.0/dark.css", groupDir},
		{idLight, "/etc/gtk-4.0/light.css", "etc/gtk-4.0/light.css", groupDir},
	})

	// First sync — commit all three files.
	_, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: initial gtk",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)

	repoDir := filepath.Join(baseDir, "repo.git")
	groupBranch := "track/etc/gtk-4.0"

	// Confirm all three were committed.
	for _, f := range []string{"etc/gtk-4.0/gtk.css", "etc/gtk-4.0/dark.css", "etc/gtk-4.0/light.css"} {
		assert.True(t, gitFileExistsOnBranch(t, repoDir, groupBranch, f),
			"file %s must be present after initial commit", f)
	}

	// Delete dark.css and light.css from disk (they were removed by the user).
	require.NoError(t, os.Remove(filepath.Join(gtkDir, "dark.css")))
	require.NoError(t, os.Remove(filepath.Join(gtkDir, "light.css")))

	// Second sync — must detect and commit the deletions.
	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: remove dark and light themes",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.True(t, result.Committed, "sync must commit when group files are deleted from disk")

	// (a) Deleted files must no longer exist in the git tree.
	assert.False(t, gitFileExistsOnBranch(t, repoDir, groupBranch, "etc/gtk-4.0/dark.css"),
		"dark.css must be removed from git tree")
	assert.False(t, gitFileExistsOnBranch(t, repoDir, groupBranch, "etc/gtk-4.0/light.css"),
		"light.css must be removed from git tree")

	// gtk.css was not deleted — it must still be in the tree.
	assert.True(t, gitFileExistsOnBranch(t, repoDir, groupBranch, "etc/gtk-4.0/gtk.css"),
		"gtk.css (not deleted) must remain in git tree")

	// (b) There must be at least one commit after the initial one.
	logOut, _ := exec.Command("git", "--git-dir="+repoDir,
		"log", "--oneline", groupBranch).Output()
	lines := splitLines(string(logOut))
	assert.GreaterOrEqual(t, len(lines), 2, "group branch must have a deletion commit")

	// (c) Deleted files must be removed from state.json.
	st := readState(t, baseDir)
	_, darkStillTracked := st.Files[idDark]
	_, lightStillTracked := st.Files[idLight]
	assert.False(t, darkStillTracked, "dark.css must be untracked from state.json after deletion")
	assert.False(t, lightStillTracked, "light.css must be untracked from state.json after deletion")

	// gtk.css must remain in state.json.
	_, gtkStillTracked := st.Files[idGtk]
	assert.True(t, gtkStillTracked, "gtk.css (not deleted) must remain in state.json")

	// (d) DeletedFiles must list the system paths.
	assert.Contains(t, result.DeletedFiles, "/etc/gtk-4.0/dark.css",
		"DeletedFiles must contain dark.css system path")
	assert.Contains(t, result.DeletedFiles, "/etc/gtk-4.0/light.css",
		"DeletedFiles must contain light.css system path")
}

// ── TestSync_Group_DeleteOnly ─────────────────────────────────────────────────

// TestSync_Group_DeleteOnly verifies the edge case where ALL remaining files
// in a group are deleted (nothing to update or create) — sync must still
// produce a commit with the deletions.
func TestSync_Group_DeleteOnly(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	sysRoot := t.TempDir()

	groupDir := "/etc/oldapp"
	appDir := filepath.Join(sysRoot, "etc", "oldapp")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.conf"), []byte("cfg\n"), 0o644))

	idCfg := core.DeriveID("/etc/oldapp/config.conf")
	writeStateMulti(t, baseDir, [][4]string{
		{idCfg, "/etc/oldapp/config.conf", "etc/oldapp/config.conf", groupDir},
	})

	// First sync — commit the file.
	_, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: initial oldapp",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)

	// Delete the only file in the group.
	require.NoError(t, os.Remove(filepath.Join(appDir, "config.conf")))

	// Second sync — deletion-only group must still produce a commit.
	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: remove all oldapp files",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.True(t, result.Committed, "deletion-only group must still produce a commit")

	repoDir := filepath.Join(baseDir, "repo.git")
	groupBranch := "track/etc/oldapp"

	// File must be gone from git tree.
	assert.False(t, gitFileExistsOnBranch(t, repoDir, groupBranch, "etc/oldapp/config.conf"),
		"config.conf must be removed from git tree")

	// File must be untracked.
	st := readState(t, baseDir)
	_, stillTracked := st.Files[idCfg]
	assert.False(t, stillTracked, "deleted file must be untracked from state.json")

	assert.Contains(t, result.DeletedFiles, "/etc/oldapp/config.conf")
}

// ── TestSync_Group_MixedUpdateAndDelete ───────────────────────────────────────

// TestSync_Group_MixedUpdateAndDelete verifies that a single sync handles a
// group where some files are modified and others are deleted — all in one
// commit on the shared group branch.
func TestSync_Group_MixedUpdateAndDelete(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	sysRoot := t.TempDir()

	groupDir := "/etc/mixed"
	mixDir := filepath.Join(sysRoot, "etc", "mixed")
	require.NoError(t, os.MkdirAll(mixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mixDir, "keep.conf"), []byte("v1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mixDir, "drop.conf"), []byte("bye\n"), 0o644))

	idKeep := core.DeriveID("/etc/mixed/keep.conf")
	idDrop := core.DeriveID("/etc/mixed/drop.conf")
	writeStateMulti(t, baseDir, [][4]string{
		{idKeep, "/etc/mixed/keep.conf", "etc/mixed/keep.conf", groupDir},
		{idDrop, "/etc/mixed/drop.conf", "etc/mixed/drop.conf", groupDir},
	})

	// Initial commit.
	_, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: initial mixed",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)

	// Modify keep.conf, delete drop.conf.
	require.NoError(t, os.WriteFile(filepath.Join(mixDir, "keep.conf"), []byte("v2\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(mixDir, "drop.conf")))

	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: mixed update+delete",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.True(t, result.Committed)

	repoDir := filepath.Join(baseDir, "repo.git")
	groupBranch := "track/etc/mixed"

	// keep.conf updated.
	content, err := gitBlobContent(t, repoDir, groupBranch, "etc/mixed/keep.conf")
	require.NoError(t, err)
	assert.Equal(t, "v2\n", string(content), "keep.conf must have updated content")

	// drop.conf removed.
	assert.False(t, gitFileExistsOnBranch(t, repoDir, groupBranch, "etc/mixed/drop.conf"),
		"drop.conf must be gone from git tree")

	// State checks.
	st := readState(t, baseDir)
	_, dropStillTracked := st.Files[idDrop]
	assert.False(t, dropStillTracked, "drop.conf must be untracked")
	_, keepStillTracked := st.Files[idKeep]
	assert.True(t, keepStillTracked, "keep.conf must remain tracked")

	assert.Contains(t, result.CommittedFiles, "etc/mixed/keep.conf")
	assert.Contains(t, result.DeletedFiles, "/etc/mixed/drop.conf")

	// Both changes landed in a single commit (one extra commit on top of initial).
	logOut, _ := exec.Command("git", "--git-dir="+repoDir,
		"log", "--oneline", groupBranch).Output()
	lines := splitLines(string(logOut))
	assert.Equal(t, 2, len(lines), "update+delete must produce exactly one new commit on the group branch")
}

// ── helpers ───────────────────────────────────────────────────────────────────

// splitLines splits s on newlines and drops empty lines.
func splitLines(s string) []string {
	var out []string
	for _, l := range func() []string {
		result := []string{}
		start := 0
		for i, c := range s {
			if c == '\n' {
				result = append(result, s[start:i])
				start = i + 1
			}
		}
		if start < len(s) {
			result = append(result, s[start:])
		}
		return result
	}() {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
