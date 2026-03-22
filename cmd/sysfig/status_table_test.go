package main

import (
	"testing"

	"github.com/aissat/sysfig/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// groupResultsByDir
// ---------------------------------------------------------------------------

// TestGroupResultsByDir_Individual verifies that individually-tracked files
// (Group == "") are keyed by filepath.Dir(SystemPath).
func TestGroupResultsByDir_Individual(t *testing.T) {
	results := []core.FileStatusResult{
		{SystemPath: "/home/user/.zshrc", Status: core.StatusSynced},
		{SystemPath: "/home/user/.vimrc", Status: core.StatusSynced},
		{SystemPath: "/etc/pacman.conf", Status: core.StatusSynced},
	}

	order, groups := groupResultsByDir(results)

	require.Len(t, order, 2, "two distinct parent directories")
	assert.Equal(t, "/home/user", order[0])
	assert.Equal(t, "/etc", order[1])

	assert.Len(t, groups["/home/user"], 2)
	assert.Len(t, groups["/etc"], 1)
}

// TestGroupResultsByDir_GroupFoldsSubdirs verifies that files tracked via a
// directory group (Group != "") are all folded under the group root, even when
// their immediate parent directory differs.
//
// Scenario mirrors /etc/pacman.d/ group containing:
//   - /etc/pacman.d/mirrorlist          (parent = /etc/pacman.d)
//   - /etc/pacman.d/hooks/blackarch.hook (parent = /etc/pacman.d/hooks)
//
// Both must appear under the /etc/pacman.d group key.
func TestGroupResultsByDir_GroupFoldsSubdirs(t *testing.T) {
	results := []core.FileStatusResult{
		{
			SystemPath: "/etc/pacman.d/mirrorlist",
			Group:      "/etc/pacman.d",
			Status:     core.StatusSynced,
		},
		{
			SystemPath: "/etc/pacman.d/hooks/blackarch.hook",
			Group:      "/etc/pacman.d",
			Status:     core.StatusSynced,
		},
	}

	order, groups := groupResultsByDir(results)

	require.Len(t, order, 1, "both files must fold into one group key")
	assert.Equal(t, "/etc/pacman.d", order[0])
	assert.Len(t, groups["/etc/pacman.d"], 2)
}

// TestGroupResultsByDir_MixedGroupAndIndividual verifies that group-tracked
// files and individually-tracked files coexist correctly without cross-
// contamination between group keys and directory keys.
func TestGroupResultsByDir_MixedGroupAndIndividual(t *testing.T) {
	results := []core.FileStatusResult{
		// group-tracked under /etc/pacman.d
		{SystemPath: "/etc/pacman.d/mirrorlist", Group: "/etc/pacman.d", Status: core.StatusSynced},
		{SystemPath: "/etc/pacman.d/hooks/foo.hook", Group: "/etc/pacman.d", Status: core.StatusSynced},
		// individually tracked
		{SystemPath: "/etc/pacman.conf", Status: core.StatusSynced},
		{SystemPath: "/home/user/.zshrc", Status: core.StatusSynced},
	}

	order, groups := groupResultsByDir(results)

	require.Len(t, order, 3)
	assert.Equal(t, "/etc/pacman.d", order[0])
	assert.Equal(t, "/etc", order[1])
	assert.Equal(t, "/home/user", order[2])

	assert.Len(t, groups["/etc/pacman.d"], 2)
	assert.Len(t, groups["/etc"], 1)
	assert.Len(t, groups["/home/user"], 1)
}

// TestGroupResultsByDir_OrderPreserved verifies that the returned order slice
// reflects first-seen insertion order, not alphabetical or any other order.
func TestGroupResultsByDir_OrderPreserved(t *testing.T) {
	results := []core.FileStatusResult{
		{SystemPath: "/var/log/app.log", Status: core.StatusSynced},
		{SystemPath: "/etc/app.conf", Status: core.StatusSynced},
		{SystemPath: "/home/user/.bashrc", Status: core.StatusSynced},
	}

	order, _ := groupResultsByDir(results)

	require.Len(t, order, 3)
	assert.Equal(t, "/var/log", order[0])
	assert.Equal(t, "/etc", order[1])
	assert.Equal(t, "/home/user", order[2])
}

// TestGroupResultsByDir_Empty verifies that an empty input yields empty output.
func TestGroupResultsByDir_Empty(t *testing.T) {
	order, groups := groupResultsByDir(nil)
	assert.Empty(t, order)
	assert.Empty(t, groups)
}
