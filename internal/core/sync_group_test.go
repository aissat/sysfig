package core_test

// sync_group_test.go — tests for group-tracked directory lifecycle:
//
//   - CREATE: new file added to group dir is committed on the shared branch
//   - UPDATE: existing group file content change is committed
//   - DELETE: group file deleted from disk is removed from git tree and
//             untracked from state.json in the same commit
//   - TWO GROUPS: two separate group dirs each land on their own branch in one
//     Sync call, with no cross-contamination between branches
//   - NO-OP: a second Sync with no content change produces no commit; a third
//     Sync after modifying one file commits again
//
// All tests that exercise group directories use t.TempDir() for both the
// "system root" (files on disk) and the bare git repo, so they are fully
// hermetic and leave no state in /tmp or the real filesystem.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	logOut, err := exec.Command("git", "--git-dir="+repoDir,
		"log", "--oneline", groupBranch).Output()
	require.NoError(t, err, "git log must succeed")
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
	logOut, err := exec.Command("git", "--git-dir="+repoDir,
		"log", "--oneline", groupBranch).Output()
	require.NoError(t, err, "git log must succeed")
	lines := splitLines(string(logOut))
	assert.Equal(t, 2, len(lines), "update+delete must produce exactly one new commit on the group branch")
}

// ── TestSync_TwoGroupsInOneSync ───────────────────────────────────────────────

// TestSync_TwoGroupsInOneSync verifies that two separate group directories
// tracked in the same state.json are each committed on their own branch in a
// single Sync call, and that files from one group never appear on the other
// group's branch.
//
// Real-world scenario: a user tracks both /etc/svc-a and /etc/svc-b as group
// directories (e.g. two different services).  Running `sysfig sync` once must
// create track/etc/svc-a and track/etc/svc-b as independent branches — files
// from svc-a must never leak into the svc-b branch and vice versa.
//
// All paths are under t.TempDir() so the test is fully hermetic.
func TestSync_TwoGroupsInOneSync(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	sysRoot := t.TempDir()

	// Group A: /etc/svc-a
	svcADir := filepath.Join(sysRoot, "etc", "svc-a")
	require.NoError(t, os.MkdirAll(svcADir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(svcADir, "a1.conf"), []byte("a1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(svcADir, "a2.conf"), []byte("a2\n"), 0o644))

	// Group B: /etc/svc-b
	svcBDir := filepath.Join(sysRoot, "etc", "svc-b")
	require.NoError(t, os.MkdirAll(svcBDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(svcBDir, "b1.conf"), []byte("b1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(svcBDir, "b2.conf"), []byte("b2\n"), 0o644))

	idA1 := core.DeriveID("/etc/svc-a/a1.conf")
	idA2 := core.DeriveID("/etc/svc-a/a2.conf")
	idB1 := core.DeriveID("/etc/svc-b/b1.conf")
	idB2 := core.DeriveID("/etc/svc-b/b2.conf")
	writeStateMulti(t, baseDir, [][4]string{
		{idA1, "/etc/svc-a/a1.conf", "etc/svc-a/a1.conf", "/etc/svc-a"},
		{idA2, "/etc/svc-a/a2.conf", "etc/svc-a/a2.conf", "/etc/svc-a"},
		{idB1, "/etc/svc-b/b1.conf", "etc/svc-b/b1.conf", "/etc/svc-b"},
		{idB2, "/etc/svc-b/b2.conf", "etc/svc-b/b2.conf", "/etc/svc-b"},
	})

	result, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "test: two groups",
		SysRoot: sysRoot,
	})
	require.NoError(t, err)
	require.True(t, result.Committed, "sync must commit when two groups have new files")

	repoDir := filepath.Join(baseDir, "repo.git")
	branchA := "track/etc/svc-a"
	branchB := "track/etc/svc-b"

	// Both group branches must be created.
	assert.True(t, gitBranchExists(t, repoDir, branchA), "group A branch must be created")
	assert.True(t, gitBranchExists(t, repoDir, branchB), "group B branch must be created")

	// Branch A must contain only A's files.
	assert.True(t, gitFileExistsOnBranch(t, repoDir, branchA, "etc/svc-a/a1.conf"))
	assert.True(t, gitFileExistsOnBranch(t, repoDir, branchA, "etc/svc-a/a2.conf"))
	assert.False(t, gitFileExistsOnBranch(t, repoDir, branchA, "etc/svc-b/b1.conf"),
		"branch A must not contain group B files")

	// Branch B must contain only B's files.
	assert.True(t, gitFileExistsOnBranch(t, repoDir, branchB, "etc/svc-b/b1.conf"))
	assert.True(t, gitFileExistsOnBranch(t, repoDir, branchB, "etc/svc-b/b2.conf"))
	assert.False(t, gitFileExistsOnBranch(t, repoDir, branchB, "etc/svc-a/a1.conf"),
		"branch B must not contain group A files")
}

// ── TestSync_GroupNoOp ────────────────────────────────────────────────────────

// TestSync_GroupNoOp verifies that syncing a group directory twice without
// changing any file content does NOT produce a second commit.  A third sync
// after modifying one file must commit again.
//
// Real-world scenario: `sysfig sync` is run on a schedule (e.g. via a systemd
// timer).  When nothing has changed on disk the command must be a no-op — no
// spurious commits, no git history noise.  When a config file is edited
// between two timer ticks the next sync must pick up the change.
//
// Uses core.Track (rather than writeStateMulti) so that rec.Branch is
// populated; the content-equal guard in Sync uses rec.Branch to look up the
// last committed content and must short-circuit when nothing changed.
// The group directory is a t.TempDir() — fully hermetic, no writes to /etc.
func TestSync_GroupNoOp(t *testing.T) {
	requireGitSync(t)
	baseDir := initBareLocalRepo(t)
	repoDir := filepath.Join(baseDir, "repo.git")

	// Use a real tempdir as the group directory (no SysRoot, same as
	// TestSync_AutoTrackNewInGroupDir) so Track sets SystemPath correctly.
	groupDir := t.TempDir()
	file1 := filepath.Join(groupDir, "f1.conf")
	file2 := filepath.Join(groupDir, "f2.conf")
	require.NoError(t, os.WriteFile(file1, []byte("content1\n"), 0o644))
	require.NoError(t, os.WriteFile(file2, []byte("content2\n"), 0o644))

	// Track both files — this sets rec.Branch on each record.
	_, err := core.Track(core.TrackOptions{
		SystemPath: file1,
		RepoDir:    repoDir,
		StateDir:   baseDir,
		Group:      groupDir,
	})
	require.NoError(t, err)
	_, err = core.Track(core.TrackOptions{
		SystemPath: file2,
		RepoDir:    repoDir,
		StateDir:   baseDir,
		Group:      groupDir,
	})
	require.NoError(t, err)

	// First sync — files are new → must commit.
	r1, err := core.Sync(core.SyncOptions{BaseDir: baseDir, Message: "test: initial"})
	require.NoError(t, err)
	assert.True(t, r1.Committed, "first sync must commit new group files")

	// Second sync — content unchanged → must NOT produce a new commit.
	r2, err := core.Sync(core.SyncOptions{BaseDir: baseDir, Message: "test: no-op"})
	require.NoError(t, err)
	assert.False(t, r2.Committed, "unchanged group files must not produce a second commit")

	// Modify one file, then sync — must commit again.
	require.NoError(t, os.WriteFile(file1, []byte("modified\n"), 0o644))
	r3, err := core.Sync(core.SyncOptions{BaseDir: baseDir, Message: "test: after change"})
	require.NoError(t, err)
	assert.True(t, r3.Committed, "changed group file must trigger a new commit")
}

// ── helpers ───────────────────────────────────────────────────────────────────

// splitLines splits s on newlines and drops empty lines.
func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
