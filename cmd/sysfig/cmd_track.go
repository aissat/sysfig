package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
)

// ── track ─────────────────────────────────────────────────────────────────────

func newTrackCmd() *cobra.Command {
	var (
		baseDir    string
		id         string
		encrypt    bool
		template   bool
		recursive  bool
		sysRoot    string
		tags       []string
		excludes   []string
		localOnly  bool
		hashOnly   bool
		remoteHost string
		sshKey     string
	)

	cmd := &cobra.Command{
		Use:   "track <path>",
		Short: "Start tracking a config file (or directory with --recursive)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			targetPath := args[0]
			repoDir := filepath.Join(baseDir, "repo.git")

			// Auto-init if the repo doesn't exist yet.
			if _, err := os.Stat(repoDir); os.IsNotExist(err) {
				if _, err := core.Init(core.InitOptions{BaseDir: baseDir}); err != nil {
					return fmt.Errorf("auto-init: %w", err)
				}
				fixSudoOwnership(baseDir)
			}

			// Parse inline remote syntax from the positional argument:
			//   user@host:/path           → remote=user@host, path=/path
			//   user@host:2222:/path      → remote=user@host:2222, path=/path
			// The --remote flag takes priority over inline syntax.
			if remoteHost == "" {
				if h, p, ok := parseRemotePath(targetPath); ok {
					remoteHost = h
					targetPath = p
				}
			}

			// Resolve remote host: explicit flag takes priority, then SYSFIG_HOST env var.
			// Must happen before auto-detect so the env-var path is also covered.
			if remoteHost == "" {
				remoteHost = os.Getenv("SYSFIG_HOST")
			}

			// Auto-detect directories so the user doesn't need --recursive.
			// Skip local stat when remoteHost is set: the path lives on the remote host,
			// not the local filesystem, so a coincidental local directory of the same
			// name must not silently trigger a local recursive walk.
			if !recursive && remoteHost == "" {
				if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
					recursive = true
				}
			}

			if localOnly && hashOnly {
				return fmt.Errorf("--local and --hash-only are mutually exclusive")
			}

			// Auto-tag with OS + distro when no tags were provided.
			if len(tags) == 0 {
				tags = core.DetectPlatformTags()
			}

			if recursive {
				summary, err := core.TrackDir(core.TrackDirOptions{
					DirPath:    targetPath,
					RepoDir:    repoDir,
					StateDir:   baseDir,
					Tags:       tags,
					Encrypt:    encrypt,
					Template:   template,
					SysRoot:    resolveSysRoot(sysRoot),
					Excludes:   excludes,
					LocalOnly:  localOnly,
					HashOnly:   hashOnly,
					RemoteHost: remoteHost,
					SSHKey:     sshKey,
				})
				if err != nil {
					return friendlyErr(err)
				}

				clrBold.Printf("Tracking (recursive): %s\n", targetPath)
				divider()

				var trackedIDs []string
				for _, e := range summary.Entries {
					switch {
					case e.Err != nil:
						fail("%s  %s", clrErr.Sprint("ERROR  "), e.Path)
						fmt.Printf("             %s\n", clrErr.Sprint(e.Err.Error()))
					case e.Skipped:
						fmt.Printf("  %s %s  %s\n", clrDim.Sprint("â"), clrDim.Sprint("SKIPPED"), clrDim.Sprint(e.Path))
						fmt.Printf("             %s\n", clrDim.Sprint(e.Reason))
					default:
						ok("%s  %s", clrOK.Sprint("TRACKED"), e.Path)
						fmt.Printf("             id:   %s\n", clrInfo.Sprint(e.ID))
						fmt.Printf("             hash: %s\n", clrDim.Sprint(e.Result.Hash))
						if encrypt {
							fmt.Printf("             %s\n", clrEncrypted.Sprint("encrypted: yes"))
						}
						trackedIDs = append(trackedIDs, e.ID)
					}
				}

				divider()
				tracked := clrOK.Sprintf("Tracked: %d", summary.Tracked)
				skipped := clrDim.Sprintf("Skipped: %d", summary.Skipped)
				var errStr string
				if summary.Errors > 0 {
					errStr = clrErr.Sprintf("Errors: %d", summary.Errors)
				} else {
					errStr = clrDim.Sprintf("Errors: %d", summary.Errors)
				}
				fmt.Printf("  %s  Â·  %s  Â·  %s\n", tracked, skipped, errStr)

				if summary.Errors > 0 {
					os.Exit(1)
				}

				// Auto-sync only the newly tracked files.
				// LocalOnly and HashOnly files have no repo content to sync.
				if len(trackedIDs) > 0 && !localOnly && !hashOnly {
					autoSyncTracked(baseDir, trackedIDs)
				}
				return nil
			}

			result, err := core.Track(core.TrackOptions{
				SystemPath: targetPath,
				StateDir:   baseDir,
				RepoDir:    repoDir,
				ID:         id,
				Tags:       tags,
				Encrypt:    encrypt,
				Template:   template,
				SysRoot:    resolveSysRoot(sysRoot),
				LocalOnly:  localOnly,
				HashOnly:   hashOnly,
				RemoteHost: remoteHost,
				SSHKey:     sshKey,
			})
			if err != nil {
				return friendlyErr(err)
			}
			fixSudoOwnership(baseDir)

			clrBold.Printf("Tracking %s\n", targetPath)
			fmt.Println()
			ok("ID:   %s", clrInfo.Sprint(result.ID))
			ok("Repo: %s", clrDim.Sprint(result.RepoPath))
			ok("Hash: %s", clrDim.Sprint(result.Hash))
			if encrypt {
				ok("%s", clrEncrypted.Sprint("Encrypted: yes (age + HKDF-SHA256 per-file key)"))
			}
			if localOnly {
				ok("%s", clrDim.Sprint("Mode:  local-only (never pushed to remote)"))
			}
			if hashOnly {
				ok("%s", clrDim.Sprint("Mode:  hash-only (integrity monitoring, no content stored)"))
			}
			if remoteHost != "" {
				ok("From:  %s", clrInfo.Sprint(remoteHost))
			}
			// Auto-sync only this newly tracked file.
			// LocalOnly and HashOnly files have no repo content to sync.
			if !localOnly && !hashOnly {
				autoSyncTracked(baseDir, []string{result.ID})
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&id, "id", "", "explicit tracking ID (derived from path if omitted)")
	f.BoolVar(&encrypt, "encrypt", false, "mark the file for encryption at rest in the repo")
	f.BoolVar(&template, "template", false, "mark the file as a template with {{variable}} expansions")
	f.BoolVar(&recursive, "recursive", false, "recursively track all files under a directory")
	f.StringVar(&sysRoot, "sys-root", "", "strip this prefix from paths before storing in repo and state")
	f.StringArrayVar(&tags, "tag", nil, "label to attach (repeatable)")
	f.StringArrayVar(&excludes, "exclude", nil, "path or glob to skip during --recursive walk (repeatable)")
	f.BoolVar(&localOnly, "local", false, "track locally only — never staged in git or pushed to remote")
	f.BoolVar(&hashOnly, "hash-only", false, "record hash only for integrity monitoring — no content stored in repo")
	f.StringVarP(&remoteHost, "remote", "r", "", "fetch file from remote host via SSH (user@host); falls back to $SYSFIG_HOST")
	f.StringVar(&sshKey, "ssh-key", "", "SSH identity file for --remote (default: SSH_AUTH_SOCK agent)")
	return cmd
}

// ── untrack ───────────────────────────────────────────────────────────────────

func newUntrackCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "untrack <path-or-id>",
		Short: "Stop tracking a file (removes from state, leaves system file untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			arg := args[0]

			// Resolve to an absolute path if it looks like a file/dir path.
			// A bare tracking ID is 8 hex chars with no slashes or dots.
			// Anything starting with '.', '/', or containing '/' is a path.
			looksLikePath := strings.Contains(arg, "/") ||
				strings.HasPrefix(arg, ".") ||
				strings.HasPrefix(arg, "~")
			if looksLikePath {
				abs, err := filepath.Abs(arg)
				if err == nil {
					arg = abs
				}
			}

			removed, err := core.Untrack(core.UntrackOptions{BaseDir: baseDir, Arg: arg})
			if err != nil {
				return err
			}
			for _, id := range removed {
				ok("Untracked %s", id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "override base dir")
	return cmd
}
