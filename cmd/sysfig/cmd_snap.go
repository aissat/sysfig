package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
)

// ── snap ───────────────────────────────────────────────────────────────────────

func newSnapCmd() *cobra.Command {
	var baseDir string
	var sysRoot string

	snap := &cobra.Command{
		Use:   "snap <subcommand>",
		Short: "Manage local snapshots of tracked files",
		Long: `Snapshots capture the current on-disk content of tracked files without
making a git commit. Use them to save a safe checkpoint before testing
config changes, then restore with 'snap restore' if something breaks.`,
	}

	// ── snap take ─────────────────────────────────────────────────────────────
	var (
		snapLabel string
		snapIDs   []string
	)
	takeCmd := &cobra.Command{
		Use:   "take",
		Short: "Capture a snapshot of tracked files",
		Example: `  sysfig snap take
  sysfig snap take --label "before nginx tuning"
  sysfig snap take --id nginx_main --id sshd_config`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			result, err := core.SnapTake(core.SnapTakeOptions{
				BaseDir: baseDir,
				SysRoot: resolveSysRoot(sysRoot),
				Label:   snapLabel,
				IDs:     snapIDs,
			})
			if err != nil {
				return err
			}
			clrBold.Println("\n  Snapshot taken")
			fmt.Println()
			ok("Hash:    %s", clrWarn.Sprint(result.ShortID))
			ok("ID:      %s", clrInfo.Sprint(result.ID))
			if result.Label != "" {
				ok("Label:   %s", result.Label)
			}
			ok("Files:   %d", len(result.Files))
			ok("Path:    %s", clrDim.Sprint(result.SnapPath))
			fmt.Println()
			clrDim.Println("  Restore with: sysfig snap restore " + result.ShortID)
			fmt.Println()
			return nil
		},
	}
	takeCmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	takeCmd.Flags().StringVar(&sysRoot, "sys-root", "", "strip this prefix from system paths")
	takeCmd.Flags().StringVar(&snapLabel, "label", "", "human-readable description for this snapshot")
	takeCmd.Flags().StringArrayVar(&snapIDs, "id", nil, "limit snapshot to these file IDs (repeatable)")

	// ── snap list / snap ls ───────────────────────────────────────────────────
	var (
		listAll     bool
		listSysRoot string
	)
	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List snapshots (scoped to CWD by default; use -a for all)",
		Long: `Lists snapshots that contain files under the current working directory.
Use --all / -a to show every snapshot regardless of path.`,
		Example: `  sysfig snap list           # snapshots touching CWD
  sysfig snap ls             # same (alias)
  sysfig snap list -a        # all snapshots
  sysfig snap list --path /etc/nginx   # specific directory`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			all, err := core.SnapList(baseDir)
			if err != nil {
				return err
			}

			filterDir := ""
			if !listAll {
				cwd, err := os.Getwd()
				if err == nil {
					filterDir = cwd
					if listSysRoot != "" {
						if rel, err := filepath.Rel(listSysRoot, cwd); err == nil {
							filterDir = "/" + rel
						}
					}
				}
			}

			snaps := core.SnapFilterByDir(all, filterDir)

			if len(snaps) == 0 {
				if filterDir != "" {
					info("No snapshots for %s — run: sysfig snap take  (use -a to see all)", filterDir)
				} else {
					info("No snapshots found. Run: sysfig snap take")
				}
				return nil
			}

			scope := filterDir
			if scope == "" {
				scope = "all"
			}
			clrBold.Printf("\n  Snapshots (%d)  [scope: %s]\n\n", len(snaps), clrInfo.Sprint(scope))
			for _, s := range snaps {
				label := s.Label
				if label == "" {
					label = clrDim.Sprint("(no label)")
				}
				// Count files visible under the filter dir.
				visible := core.SnapFilesUnderDir(s, filterDir)
				fmt.Printf("  %s  %s  %s  %s  %s\n",
					clrWarn.Sprint(s.ShortID),
					clrInfo.Sprint(s.ID),
					clrDim.Sprint(s.CreatedAt.Format("2006-01-02 15:04:05")),
					label,
					clrDim.Sprintf("[%d/%d files]", len(visible), len(s.Files)),
				)
			}
			fmt.Println()
			return nil
		},
	}
	listCmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	listCmd.Flags().StringVar(&listSysRoot, "sys-root", "", "strip this prefix from CWD when matching paths")
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "show all snapshots regardless of current directory")

	// ── snap restore ──────────────────────────────────────────────────────────
	var (
		restoreIDs    []string
		restoreDryRun bool
	)
	restoreCmd := &cobra.Command{
		Use:   "restore <snap-id>",
		Short: "Restore files from a snapshot",
		Args:  cobra.ExactArgs(1),
		Example: `  sysfig snap restore 20260318-153042-before-nginx-tuning
  sysfig snap restore 20260318-153042 --dry-run
  sysfig snap restore 20260318-153042 --id nginx_main`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			result, err := core.SnapRestore(core.SnapRestoreOptions{
				BaseDir: baseDir,
				SysRoot: resolveSysRoot(sysRoot),
				SnapID:  args[0],
				IDs:     restoreIDs,
				DryRun:  restoreDryRun,
			})
			if err != nil {
				return err
			}

			prefix := ""
			if result.DryRun {
				prefix = clrDim.Sprint("[dry-run] ")
			}

			idW := 0
			for _, f := range result.Restored {
				if len(f.ID) > idW { idW = len(f.ID) }
			}
			for _, f := range result.Skipped {
				if len(f.ID) > idW { idW = len(f.ID) }
			}
			idW += 2

			clrBold.Printf("\n  Restoring snapshot: %s\n\n", args[0])
			for _, f := range result.Restored {
				fmt.Printf("  %s✓ %s → %s\n", prefix, clrInfo.Sprint(pad(f.ID, idW)), f.SystemPath)
			}
			for _, f := range result.Skipped {
				fmt.Printf("  %s  %s   %s\n", clrDim.Sprint("―"), clrDim.Sprint(pad(f.ID, idW)), clrDim.Sprint("skipped"))
			}
			divider()
			if result.DryRun {
				fmt.Printf("  [dry-run] would restore: %d  ·  skipped: %d\n\n",
					len(result.Restored), len(result.Skipped))
			} else {
				fmt.Printf("  Restored: %d  ·  Skipped: %d\n\n",
					len(result.Restored), len(result.Skipped))
			}
			return nil
		},
	}
	restoreCmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	restoreCmd.Flags().StringVar(&sysRoot, "sys-root", "", "strip this prefix from system paths")
	restoreCmd.Flags().StringArrayVar(&restoreIDs, "id", nil, "limit restore to these file IDs (repeatable)")
	restoreCmd.Flags().BoolVar(&restoreDryRun, "dry-run", false, "show what would be restored without writing")

	// ── snap drop ─────────────────────────────────────────────────────────────
	dropCmd := &cobra.Command{
		Use:   "drop <snap-id>",
		Short: "Delete a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			if err := core.SnapDrop(baseDir, args[0]); err != nil {
				return err
			}
			ok("Deleted snapshot: %s", clrInfo.Sprint(args[0]))
			return nil
		},
	}
	dropCmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")

	// ── snap undo ─────────────────────────────────────────────────────────────
	var (
		undoIDs    []string
		undoDryRun bool
		undoAll    bool
	)
	undoCmd := &cobra.Command{
		Use:   "undo",
		Short: "Restore the most recent snapshot scoped to CWD (use -a for global)",
		Long: `Restores files from the most recent snapshot that touches the current
working directory. Only files under CWD are restored.

Use --all / -a to restore the most recent snapshot globally (all files).`,
		Example: `  sysfig snap undo              # undo last snap for CWD
  sysfig snap undo -a           # undo last snap globally (all files)
  sysfig snap undo --dry-run    # preview without writing
  sysfig snap undo --id nginx_main`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			dir := ""
			if !undoAll {
				cwd, err := os.Getwd()
				if err == nil {
					dir = cwd
					if sysRoot != "" {
						if rel, err := filepath.Rel(sysRoot, cwd); err == nil {
							dir = "/" + rel
						}
					}
				}
			}
			result, snapID, err := core.SnapUndo(core.SnapUndoOptions{
				BaseDir: baseDir,
				SysRoot: resolveSysRoot(sysRoot),
				Dir:     dir,
				IDs:     undoIDs,
				DryRun:  undoDryRun,
			})
			if err != nil {
				return err
			}

			prefix := ""
			if result.DryRun {
				prefix = clrDim.Sprint("[dry-run] ")
			}

			scope := "all files"
			if dir != "" {
				scope = dir
			}
			idW2 := 0
			for _, f := range result.Restored {
				if len(f.ID) > idW2 { idW2 = len(f.ID) }
			}
			for _, f := range result.Skipped {
				if len(f.ID) > idW2 { idW2 = len(f.ID) }
			}
			idW2 += 2

			clrBold.Printf("\n  Undo → restoring snapshot: %s  [scope: %s]\n\n",
				clrInfo.Sprint(snapID), clrDim.Sprint(scope))
			for _, f := range result.Restored {
				fmt.Printf("  %s✓ %s → %s\n", prefix, clrInfo.Sprint(pad(f.ID, idW2)), f.SystemPath)
			}
			for _, f := range result.Skipped {
				fmt.Printf("  %s  %s   %s\n", clrDim.Sprint("―"), clrDim.Sprint(pad(f.ID, idW2)), clrDim.Sprint("skipped"))
			}
			divider()
			if result.DryRun {
				fmt.Printf("  [dry-run] would restore: %d  ·  skipped: %d\n\n",
					len(result.Restored), len(result.Skipped))
			} else {
				fmt.Printf("  Restored: %d  ·  Skipped: %d\n\n",
					len(result.Restored), len(result.Skipped))
			}
			return nil
		},
	}
	undoCmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	undoCmd.Flags().StringVar(&sysRoot, "sys-root", "", "strip this prefix from system paths")
	undoCmd.Flags().StringArrayVar(&undoIDs, "id", nil, "limit restore to these file IDs (repeatable)")
	undoCmd.Flags().BoolVar(&undoDryRun, "dry-run", false, "show what would be restored without writing")
	undoCmd.Flags().BoolVarP(&undoAll, "all", "a", false, "undo across all tracked files, not just CWD")

	snap.AddCommand(takeCmd, listCmd, restoreCmd, dropCmd, undoCmd)
	return snap
}

