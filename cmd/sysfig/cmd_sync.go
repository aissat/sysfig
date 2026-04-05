package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
)

// ── remote ────────────────────────────────────────────────────────────────────

func newRemoteCmd() *cobra.Command {
	var baseDir string

	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage the git remote for the sysfig repo",
	}
	cmd.PersistentFlags().StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")

	runGit := func(baseDir string, args ...string) error {
		repoDir := filepath.Join(baseDir, "repo.git")
		gitArgs := append([]string{"--git-dir=" + repoDir}, args...)
		c := exec.Command("git", gitArgs...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "set <url>",
		Short: "Set (or replace) the origin remote URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			url := args[0]
			// Check if origin exists; add or update accordingly.
			repoDir := filepath.Join(baseDir, "repo.git")
			checkCmd := exec.Command("git", "--git-dir="+repoDir, "remote", "get-url", "origin")
			if checkCmd.Run() == nil {
				if err := runGit(baseDir, "remote", "set-url", "origin", url); err != nil {
					return err
				}
			} else {
				if err := runGit(baseDir, "remote", "add", "origin", url); err != nil {
					return err
				}
			}
			ok("Remote origin set to %s", clrInfo.Sprint(url))
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the current remote URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			return runGit(baseDir, "remote", "-v")
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "Remove the origin remote",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			return runGit(baseDir, "remote", "remove", "origin")
		},
	})

	return cmd
}

// ── sync ──────────────────────────────────────────────────────────────────────

func newSyncCmd() *cobra.Command {
	var (
		baseDir string
		message string
		auto    bool
		all     bool
		push    bool
		pull    bool
		force   bool
		sysRoot string
	)

	cmd := &cobra.Command{
		Use:   "sync [target]",
		Short: "Commit local changes, optionally pull first and/or push after",
		Long: `Stages all modified tracked files and creates a local git commit (offline-safe).

A message is required: use -m for a custom message or --auto for the default.

Optional [target] limits the sync to a specific file or directory:
  sysfig sync /etc/pacman.conf -m "updated mirrors"   # by path
  sysfig sync 63d01e28 -m "updated mirrors"           # by hash/ID
  sysfig sync -m "updated mirrors"                    # CWD-aware: auto-detect

Use --pull to fetch remote changes first (full round-trip with --push):
  sysfig sync --pull --push`,
		Example: `  sysfig sync -m "tuned nginx"              # all changed files
  sysfig sync /etc/nginx -m "tuned nginx"   # only files under /etc/nginx
  sysfig sync --auto                        # auto-generated message
  sysfig sync --auto --push                 # commit + push`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if message == "" && !auto {
				return fmt.Errorf("message required: use -m \"your message\" or --auto for the default message")
			}
			baseDir = resolveBaseDir(baseDir)

			// Determine the target scope (path, hash, or CWD).
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			// CWD-aware: if no target given and not --all, scope to CWD.
			if target == "" && !all {
				if cwd, err := os.Getwd(); err == nil {
					target = cwd
				}
			}

			// Auto-track new files in group dirs under the target before resolving IDs.
			autoTrackNewInTarget(baseDir, target)

			// Resolve target → FileIDs filter (nil = all files).
			var fileIDs []string
			if target != "" {
				fileIDs = resolveSyncTarget(baseDir, target)
			}
			// If target was given explicitly and nothing matched, error out.
			if len(args) > 0 && len(fileIDs) == 0 {
				return fmt.Errorf("sync: no tracked files match %q", args[0])
			}
			// When SYSFIG_HOST is set and --all is not, restrict sync to that
			// host's remote files only (avoids re-fetching unrelated hosts).
			if !all {
				fileIDs = filterIDsByHost(baseDir, fileIDs)
			}

			result, err := core.Sync(core.SyncOptions{
				BaseDir: baseDir,
				Message: message,
				Pull:    pull,
				Push:    push,
				Force:   force,
				SysRoot: resolveSysRoot(sysRoot),
				FileIDs: fileIDs,
			})
			if err != nil {
				return err
			}
			fixSudoOwnership(baseDir)

			// Pull status.
			if pull {
				if result.PullErr != nil {
					warn("Pull failed: %s", result.PullErr)
					info("Continuing with local repo.")
				} else if result.Pulled {
					ok("Pulled latest changes from remote.")
				} else {
					info("Already up to date.")
				}
			}

			for path, fetchErr := range result.RemoteFetchErrors {
				fail("Remote fetch failed: %s — %s", clrBold.Sprint(path), clrErr.Sprint(fetchErr))
			}
			if !result.Committed {
				info("Nothing to commit — shadow repo is clean.")
				if push {
					if err := core.Push(core.PushOptions{BaseDir: baseDir, Force: force}); err != nil {
						return err
					}
					ok("Pushed to remote.")
				}
				return nil
			}
			for _, f := range result.CommittedFiles {
				ok("Committed: %s", clrBold.Sprint(f))
			}
			ok("Repo:      %s", clrDim.Sprint(result.RepoDir))
			if result.Pushed {
				ok("Pushed to remote.")
			} else if !push {
				info("Not pushed. Run %s when online.", clrBold.Sprint("sysfig sync --push"))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVarP(&message, "message", "m", "", "commit message")
	f.BoolVar(&auto, "auto", false, "use auto-generated commit message (sysfig: update <path>)")
	f.BoolVar(&all, "all", false, "sync all tracked files, ignoring CWD scope")
	f.BoolVar(&pull, "pull", false, "pull from remote before committing (requires network)")
	f.BoolVar(&push, "push", false, "push to remote after committing (requires network)")
	f.BoolVar(&force, "force", false, "force push (use for first push to a non-empty remote)")
	f.StringVar(&sysRoot, "sys-root", "", "prefix all system paths (sandbox/testing override)")
	return cmd
}

// ── push (hidden alias → sysfig sync --push) ─────────────────────────────────

func newPushCmd() *cobra.Command {
	var baseDir string

	cmd := &cobra.Command{
		Use:        "push",
		Short:      "Push local commits to the remote (alias: sysfig sync --push)",
		Hidden:     true,
		Deprecated: "use 'sysfig sync --push' instead",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			if err := core.Push(core.PushOptions{BaseDir: baseDir}); err != nil {
				return err
			}
			ok("Pushed to remote.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	return cmd
}

// ── pull (hidden alias → sysfig sync --pull) ─────────────────────────────────

func newPullCmd() *cobra.Command {
	var baseDir string

	cmd := &cobra.Command{
		Use:        "pull",
		Short:      "Pull remote changes into the local repo (alias: sysfig sync --pull)",
		Hidden:     true,
		Deprecated: "use 'sysfig sync --pull' instead",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			result, err := core.Pull(core.PullOptions{BaseDir: baseDir})
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s check your network connection and remote configuration.\n", clrWarn.Sprint("Hint:"))
				return err
			}
			if result.AlreadyUpToDate {
				info("Already up to date.")
			} else {
				ok("Pulled latest changes from remote.")
				info("Run %s to deploy updated config files.", clrBold.Sprint("sysfig apply"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	return cmd
}

