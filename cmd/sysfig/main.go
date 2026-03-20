package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"syscall"
	"time"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/sysfig-dev/sysfig/internal/core"
	"github.com/sysfig-dev/sysfig/internal/crypto"
)

// ── UI helpers ────────────────────────────────────────────────────────────────

var (
	clrOK      = color.New(color.FgGreen, color.Bold)
	clrWarn    = color.New(color.FgYellow, color.Bold)
	clrErr     = color.New(color.FgRed, color.Bold)
	clrInfo    = color.New(color.FgCyan)
	clrDim     = color.New(color.Faint)
	clrBold    = color.New(color.Bold)

	clrSynced    = color.New(color.FgGreen)
	clrDirty     = color.New(color.FgYellow)
	clrPending   = color.New(color.FgBlue)
	clrMissing   = color.New(color.FgRed)
	clrEncrypted = color.New(color.FgMagenta)
)

func ok(format string, a ...interface{})   { fmt.Printf("  "+clrOK.Sprint("✓")+" "+format+"\n", a...) }
func warn(format string, a ...interface{}) { fmt.Printf("  "+clrWarn.Sprint("⚠")+"  "+format+"\n", a...) }
func info(format string, a ...interface{}) { fmt.Printf("  "+clrInfo.Sprint("ℹ")+" "+format+"\n", a...) }
func fail(format string, a ...interface{}) { fmt.Printf("  "+clrErr.Sprint("✗")+" "+format+"\n", a...) }

func statusColored(s core.FileStatusLabel, label string) string {
	switch s {
	case core.StatusSynced:
		return clrSynced.Sprint(label)
	case core.StatusDirty:
		return clrDirty.Sprint(label)
	case core.StatusPending:
		return clrPending.Sprint(label)
	case core.StatusMissing:
		return clrMissing.Sprint(label)
	case core.StatusEncrypted:
		return clrEncrypted.Sprint(label)
	default:
		return label
	}
}

func divider() { fmt.Println(clrDim.Sprint(strings.Repeat("─", 76))) }

func step(n int, label string) {
	fmt.Printf("  %s %s\n", clrBold.Sprintf("[%d]", n), clrBold.Sprint(label))
}

// globalProfile holds the value of the --profile persistent flag.
// Set by newRootCmd() before any subcommand runs.
var globalProfile string

// sysfigHome returns ~/.sysfig for the real user running the process.
//
// Uses user.Current() which reads /etc/passwd by UID — immune to a stale
// $HOME env var (e.g. after `su <user>` without a login shell). Falls back
// to os.UserHomeDir() if user.Current() fails.
//
// When a non-root user runs `sudo sysfig`, the process UID is 0 (root) so
// this correctly returns /root/.sysfig — system configs land in root's repo.
// When ali runs `sysfig` (no sudo), UID is ali's → /home/ali/.sysfig.
func sysfigHome() string {
	// When running under sudo, use the invoking user's home so that
	// "sudo sysfig" stores data in ~/.sysfig of the real user, not root.
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil && u.HomeDir != "" {
			return filepath.Join(u.HomeDir, ".sysfig")
		}
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return filepath.Join(u.HomeDir, ".sysfig")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sysfig"
	}
	return filepath.Join(home, ".sysfig")
}

// fixSudoOwnership re-chowns baseDir to the invoking user when running under
// sudo, so that files written by root land with the correct owner.
func fixSudoOwnership(baseDir string) {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	_ = filepath.WalkDir(baseDir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		uid := toInt(u.Uid)
		gid := toInt(u.Gid)
		_ = os.Lchown(path, uid, gid)
		return nil
	})
}

func toInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// profilesDir returns ~/.sysfig/profiles.
func profilesDir() string { return filepath.Join(sysfigHome(), "profiles") }

// defaultBaseDir returns the active base directory.
// Priority: --profile flag > SYSFIG_PROFILE env var > SYSFIG_BASE_DIR env var > ~/.sysfig
func defaultBaseDir() string {
	profile := globalProfile
	if profile == "" {
		profile = os.Getenv("SYSFIG_PROFILE")
	}
	if profile != "" {
		return filepath.Join(profilesDir(), profile)
	}
	if dir := os.Getenv("SYSFIG_BASE_DIR"); dir != "" {
		return dir
	}
	return sysfigHome()
}

// resolveBaseDir returns baseDir if non-empty, otherwise calls defaultBaseDir().
// Every command should call this at the top of RunE so that --profile is
// honoured even though flag defaults are evaluated at command-creation time.
func resolveBaseDir(baseDir string) string {
	if baseDir != "" {
		return baseDir
	}
	return defaultBaseDir()
}

// resolveSysRoot returns sysRoot if non-empty, then SYSFIG_SYS_ROOT env var,
// then "" (meaning real system root — no prefix stripping).
// This allows labs and CI to export SYSFIG_SYS_ROOT=/sysroot once instead of
// passing --sys-root on every command.
func resolveSysRoot(sysRoot string) string {
	if sysRoot != "" {
		return sysRoot
	}
	return os.Getenv("SYSFIG_SYS_ROOT")
}

// isatty returns true if stdout is connected to a terminal.
func isatty() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ── Root command ──────────────────────────────────────────────────────────────

// Build-time variables injected via -ldflags.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "sysfig",
		Short:   "Config management that thinks like a sysadmin, not a git wrapper",
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate),
		Long: `sysfig — security-first configuration management for Linux.

Version-control your config files in a bare git repo, deploy them across
machines with a single command, encrypt secrets with age, track file
ownership and permissions, and stay fully offline-capable.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// --profile is a persistent flag: it applies to every subcommand and
	// overrides the base directory resolution in defaultBaseDir().
	root.PersistentFlags().StringVar(&globalProfile, "profile", "",
		"use named profile (~/.sysfig/profiles/<name>); overrides --base-dir default")

	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Show version information",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Printf("sysfig %s\n", version)
				fmt.Printf("  commit:  %s\n", commit)
				fmt.Printf("  built:   %s\n", buildDate)
			},
		},
		newDeployCmd(),
		newSetupCmd(),
		newInitCmd(),
		newTrackCmd(),
		newUntrackCmd(),
		newKeysCmd(),
		newApplyCmd(),
		newStatusCmd(),
		newDiffCmd(),
		newRemoteCmd(),
		newSyncCmd(),
		newPushCmd(),
		newPullCmd(),
		newLogCmd(),
		newDoctorCmd(),
		newSnapCmd(),
		newWatchCmd(),
		newProfileCmd(),
		newNodeCmd(),
	)

	// Hidden alias kept for backward-compat.
	cloneAlias := *newSetupCmd()
	cloneAlias.Use = "clone"
	cloneAlias.Hidden = true
	root.AddCommand(&cloneAlias)

	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}

// ── deploy ────────────────────────────────────────────────────────────────────

func newDeployCmd() *cobra.Command {
	var (
		baseDir       string
		dryRun        bool
		noBackup      bool
		skipEncrypted bool
		noPull        bool
		yes           bool
		sysRoot       string
		ids           []string
		host          string
		sshKey        string
		sshPort       int
	)

	cmd := &cobra.Command{
		Use:   "deploy [remote-url]",
		Short: "Pull from remote and apply configs — one command for everything",
		Long: `deploy is the recommended entry point for both first-time machines and
routine updates. Idempotent: safe to re-run as many times as needed.

With --host it SSHes into the target and pushes tracked files directly.
No sysfig installation is required on the remote host.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			remoteURL := ""
			if len(args) > 0 {
				remoteURL = args[0]
			}

			// ── Remote host mode ──────────────────────────────────────
			if host != "" {
				fmt.Println()
				clrBold.Printf("  sysfig deploy → %s\n", host)
				fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
				fmt.Println()

				result, err := core.RemoteDeploy(core.RemoteDeployOptions{
					Host:          host,
					SSHKey:        sshKey,
					SSHPort:       sshPort,
					BaseDir:       baseDir,
					IDs:           ids,
					DryRun:        dryRun,
					SkipEncrypted: skipEncrypted,
				})
				if err != nil {
					return err
				}

				deployIDW := 0
				for _, r := range result.Results {
					if len(r.ID) > deployIDW { deployIDW = len(r.ID) }
				}
				deployIDW += 2

				for _, r := range result.Results {
					switch {
					case r.Err != nil:
						fail("%s  %s", clrErr.Sprint(r.ID), clrErr.Sprint(r.Err.Error()))
					case r.Skipped && r.SkipReason == "dry-run":
						fmt.Printf("  %s %s %s\n",
							clrDim.Sprint("[dry-run]"),
							clrInfo.Sprint(pad(r.ID, deployIDW)),
							clrDim.Sprint("→ "+r.SystemPath))
					case r.Skipped:
						fmt.Printf("  %s %s  %s\n",
							clrDim.Sprint("―"),
							clrDim.Sprint(pad(r.ID, deployIDW)),
							clrDim.Sprintf("(%s)", r.SkipReason))
					default:
						ok("%s → %s", clrBold.Sprint(pad(r.ID, deployIDW)), r.SystemPath)
					}
				}

				if len(result.Results) == 0 {
					info("Nothing to deploy (no tracked files).")
				}

				fmt.Println()
				divider()
				clrOK.Printf("  ✓ Remote deploy complete! (%s)\n", host)
				fmt.Println()
				parts := []string{clrOK.Sprintf("Applied: %d", result.Applied)}
				if result.Skipped > 0 {
					parts = append(parts, clrDim.Sprintf("Skipped: %d", result.Skipped))
				}
				if result.Failed > 0 {
					parts = append(parts, clrErr.Sprintf("Failed: %d", result.Failed))
				}
				fmt.Printf("  %s\n\n", strings.Join(parts, clrDim.Sprint("  ·  ")))

				if result.Failed > 0 {
					os.Exit(1)
				}
				return nil
			}

			// ── Local deploy mode ──────────────────────────────────────
			fmt.Println()
			clrBold.Println("  sysfig deploy — syncing your environment")
			fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
			fmt.Println()

			repoDir := filepath.Join(baseDir, "repo.git")
			_, repoErr := os.Stat(repoDir)
			alreadySetUp := repoErr == nil

			if !alreadySetUp && remoteURL == "" {
				if yes || !isatty() {
					return fmt.Errorf("remote URL required on first run (no local repo found)")
				}
				fmt.Printf("     %s ", clrBold.Sprint("Remote URL:"))
				scanner := bufio.NewScanner(os.Stdin)
				if scanner.Scan() {
					remoteURL = strings.TrimSpace(scanner.Text())
				}
				if remoteURL == "" {
					return fmt.Errorf("no URL provided")
				}
			}

			result, err := core.Deploy(core.DeployOptions{
				RemoteURL:     remoteURL,
				BaseDir:       baseDir,
				IDs:           ids,
				DryRun:        dryRun,
				NoBackup:      noBackup,
				SkipEncrypted: skipEncrypted,
				NoPull:        noPull,
				Yes:           yes,
				SysRoot:       resolveSysRoot(sysRoot),
			})
			if err != nil {
				return err
			}

			switch result.Phase {
			case core.DeployPhaseSetup:
				cr := result.CloneResult
				ok("Config repo cloned")
				fmt.Printf("     %s %s\n", clrDim.Sprint("location:"), clrDim.Sprint(cr.RepoDir))
				if cr.Seeded > 0 {
					ok("Manifest seeded: %s tracked file(s)", clrBold.Sprintf("%d", cr.Seeded))
				} else {
					info("Manifest has no tracked files yet.")
				}
				if cr.HooksWarning != "" {
					warn("hooks.yaml created from template — review before using")
				}
			case core.DeployPhasePull:
				pr := result.PullResult
				if pr.AlreadyUpToDate {
					info("Already up to date — no remote changes.")
				} else {
					ok("Pulled latest changes from remote.")
				}
			case core.DeployPhaseSkipped:
				info("Pull skipped — applying from local repo.")
			}

			fmt.Println()
			if len(result.ApplyResults) == 0 {
				info("Nothing to apply.")
			} else {
				for _, r := range result.ApplyResults {
					if r.Skipped {
						fmt.Printf("  %s %s %s\n",
							clrDim.Sprint("[dry-run]"),
							clrInfo.Sprint(r.ID),
							clrDim.Sprint("→ "+r.SystemPath))
						continue
					}
					ok("Applied: %s", clrBold.Sprint(r.ID))
					fmt.Printf("     %s %s\n", clrDim.Sprint("→"), r.SystemPath)
					if r.BackupPath != "" {
						fmt.Printf("     %s %s\n", clrDim.Sprint("backup:"), clrDim.Sprint(r.BackupPath))
					}
					if r.Encrypted {
						fmt.Printf("     %s\n", clrEncrypted.Sprint("decrypted from age ciphertext"))
					}
					if r.ChownWarning != "" {
						warn("%s", r.ChownWarning)
					}
				}
			}

			fmt.Println()
			divider()
			clrOK.Println("  ✓ Deploy complete!")
			fmt.Println()
			parts := []string{clrOK.Sprintf("Applied: %d", result.Applied)}
			if result.Skipped > 0 {
				parts = append(parts, clrDim.Sprintf("Dry-run: %d", result.Skipped))
			}
			fmt.Printf("  %s\n", strings.Join(parts, clrDim.Sprint("  ·  ")))
			fmt.Println()
			fmt.Printf("  %s\n", clrBold.Sprint("What to do next:"))
			fmt.Printf("   %s  See current sync state\n", clrInfo.Sprint("sysfig status"))
			fmt.Printf("   %s  Check environment health\n", clrInfo.Sprint("sysfig doctor"))
			fmt.Printf("   %s  See commit history\n", clrInfo.Sprint("sysfig log   "))
			fmt.Println()
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringArrayVar(&ids, "id", nil, "apply only this ID (repeatable)")
	f.BoolVar(&dryRun, "dry-run", false, "print what would happen without writing anything")
	f.BoolVar(&noBackup, "no-backup", false, "skip pre-apply backup")
	f.BoolVar(&skipEncrypted, "skip-encrypted", false, "skip encrypted files when master key is absent")
	f.BoolVar(&noPull, "no-pull", false, "skip pull — apply from local repo only (offline mode)")
	f.BoolVar(&yes, "yes", false, "non-interactive: skip all prompts")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.StringVar(&host, "host", "", "SSH target (user@hostname) — push files to remote without sysfig installed there")
	f.StringVar(&sshKey, "ssh-key", "", "path to SSH identity file (default: use ssh-agent)")
	f.IntVar(&sshPort, "ssh-port", 22, "SSH port on the remote host")
	return cmd
}

// ── setup ─────────────────────────────────────────────────────────────────────

func newSetupCmd() *cobra.Command {
	var (
		baseDir       string
		configsOnly   bool
		skipEncrypted bool
		yes           bool
	)

	cmd := &cobra.Command{
		Use:   "setup [remote-url]",
		Short: "Bootstrap sysfig on a new machine from a remote config repo",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			remoteURL := ""
			if len(args) > 0 {
				remoteURL = args[0]
			}

			fmt.Println()
			clrBold.Println("  sysfig setup — bootstrapping your environment")
			fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
			fmt.Println()

			repoDir := filepath.Join(baseDir, "repo.git")
			if fi, err := os.Stat(repoDir); err == nil && fi.IsDir() {
				info("This machine is already set up.")
				fmt.Printf("     %s %s\n", clrDim.Sprint("config repo:"), clrDim.Sprint(repoDir))
				fmt.Printf("     %s %s\n", clrDim.Sprint("base dir:   "), clrDim.Sprint(baseDir))
				fmt.Println()
				info("Run %s to check for remote updates.", clrBold.Sprint("sysfig pull"))
				info("Run %s to see current state.", clrBold.Sprint("sysfig status"))
				fmt.Println()
				return nil
			}

			step(1, "Remote config repository")
			if remoteURL == "" {
				if yes || !isatty() {
					return fmt.Errorf("remote URL is required (use: sysfig setup <url>)")
				}
				fmt.Printf("     %s ", clrBold.Sprint("Remote URL:"))
				scanner := bufio.NewScanner(os.Stdin)
				if scanner.Scan() {
					remoteURL = strings.TrimSpace(scanner.Text())
				}
				if remoteURL == "" {
					return fmt.Errorf("no URL provided")
				}
			} else {
				fmt.Printf("     %s %s\n", clrDim.Sprint("url:"), remoteURL)
			}

			fmt.Println()
			step(2, "Fetching your config repo")
			result, err := core.Clone(core.CloneOptions{
				RemoteURL:     remoteURL,
				BaseDir:       baseDir,
				ConfigsOnly:   configsOnly,
				SkipEncrypted: skipEncrypted,
				Yes:           yes,
			})
			if err != nil {
				return err
			}
			ok("Config repo ready")
			fmt.Printf("     %s %s\n", clrDim.Sprint("location:"), clrDim.Sprint(result.RepoDir))

			fmt.Println()
			step(3, "Reading manifest")
			if result.Seeded > 0 {
				ok("Found %s tracked file(s) in your manifest", clrBold.Sprintf("%d", result.Seeded))
			} else {
				info("Manifest has no tracked files yet — use %s to start tracking.", clrBold.Sprint("sysfig track"))
			}

			if result.HooksWarning != "" {
				fmt.Println()
				step(4, "Hooks")
				warn("hooks.yaml created from template — review before using:")
				fmt.Printf("     %s\n", clrDim.Sprint(filepath.Join(baseDir, "hooks.yaml")))
			}

			fmt.Println()
			divider()
			clrOK.Println("  ✓ Setup complete!")
			fmt.Println()
			fmt.Printf("  %s\n", clrBold.Sprint("What to do next:"))
			fmt.Printf("   %s  Deploy your config files to this machine\n", clrInfo.Sprint("sysfig apply"))
			fmt.Printf("   %s  Check sync status at any time\n", clrInfo.Sprint("sysfig status"))
			fmt.Printf("   %s  See your commit history\n", clrInfo.Sprint("sysfig log    "))
			fmt.Println()
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.BoolVar(&configsOnly, "configs-only", false, "skip package installation, deploy configs only")
	f.BoolVar(&skipEncrypted, "skip-encrypted", false, "skip encrypted files when master key is absent")
	f.BoolVar(&yes, "yes", false, "non-interactive: skip all prompts")
	return cmd
}

// ── init ──────────────────────────────────────────────────────────────────────

func newInitCmd() *cobra.Command {
	var (
		baseDir string
		encrypt bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialise a fresh sysfig environment (no remote)",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			result, err := core.Init(core.InitOptions{
				BaseDir: baseDir,
				Encrypt: encrypt,
			})
			if err != nil {
				return err
			}

			if result.AlreadyExisted {
				info("sysfig already initialised in %s", clrDim.Sprint(result.BaseDir))
				return nil
			}

			clrBold.Printf("Initialising sysfig in %s\n", result.BaseDir)
			fmt.Println()
			ok("Shadow repo:   %s", clrDim.Sprint(result.RepoDir))
			ok("Backups dir:   %s", clrDim.Sprint(result.BackupsDir))
			ok("Keys dir:      %s", clrDim.Sprint(result.KeysDir))
			ok("State file:    %s", clrDim.Sprint(result.StateFile))
			ok("sysfig.yaml:   %s", clrDim.Sprint(result.ManifestFile))
			ok("Hooks example: %s", clrDim.Sprint(result.HooksExample))
			if result.MasterKeyPath != "" {
				ok("Master key:    %s", clrDim.Sprint(result.MasterKeyPath))
				fmt.Println()
				warn("%s Back up your master key immediately!", clrErr.Sprint("IMPORTANT:"))
				fmt.Printf("     Loss of this key means loss of all encrypted files.\n")
				fmt.Printf("     Location: %s\n", result.MasterKeyPath)
			}
			fmt.Println()
			clrBold.Println("Next steps:")
			fmt.Printf("  1. Edit %s\n", result.ManifestFile)
			fmt.Println("  2. Run 'sysfig track <path>' to start tracking files")
			if result.MasterKeyPath != "" {
				fmt.Println("  3. Run 'sysfig track --encrypt <path>' to track encrypted files")
				fmt.Println("  4. Run 'sysfig sync' to commit changes")
			} else {
				fmt.Println("  3. Run 'sysfig sync' to commit changes")
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.BoolVar(&encrypt, "encrypt", false, "also generate a master key for encryption")
	return cmd
}

// ── track ─────────────────────────────────────────────────────────────────────

func newTrackCmd() *cobra.Command {
	var (
		baseDir   string
		id        string
		encrypt   bool
		template  bool
		recursive bool
		sysRoot   string
		tags      []string
		excludes  []string
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

			// Auto-detect directories so the user doesn't need --recursive.
			if !recursive {
				if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
					recursive = true
				}
			}

			if recursive {
				summary, err := core.TrackDir(core.TrackDirOptions{
					DirPath:  targetPath,
					RepoDir:  repoDir,
					StateDir: baseDir,
					Tags:     tags,
					Encrypt:  encrypt,
					Template: template,
					SysRoot:  resolveSysRoot(sysRoot),
					Excludes: excludes,
				})
				if err != nil {
					return err
				}

				clrBold.Printf("Tracking (recursive): %s\n", targetPath)
				divider()

				for _, e := range summary.Entries {
					switch {
					case e.Err != nil:
						fail("%s  %s", clrErr.Sprint("ERROR  "), e.Path)
						fmt.Printf("             %s\n", clrErr.Sprint(e.Err.Error()))
					case e.Skipped:
						fmt.Printf("  %s %s  %s\n", clrDim.Sprint("―"), clrDim.Sprint("SKIPPED"), clrDim.Sprint(e.Path))
						fmt.Printf("             %s\n", clrDim.Sprint(e.Reason))
					default:
						ok("%s  %s", clrOK.Sprint("TRACKED"), e.Path)
						fmt.Printf("             id:   %s\n", clrInfo.Sprint(e.ID))
						fmt.Printf("             hash: %s\n", clrDim.Sprint(e.Result.Hash))
						if encrypt {
							fmt.Printf("             %s\n", clrEncrypted.Sprint("encrypted: yes"))
						}
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
				fmt.Printf("  %s  ·  %s  ·  %s\n", tracked, skipped, errStr)

				if summary.Errors > 0 {
					os.Exit(1)
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
			})
			if err != nil {
				return err
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

			// Resolve to an absolute path if it looks like a file path.
			if !strings.Contains(arg, "/") {
				// Looks like a bare ID — use as-is.
			} else {
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

// ── keys ──────────────────────────────────────────────────────────────────────

func newKeysCmd() *cobra.Command {
	var baseDir string

	keysCmd := &cobra.Command{
		Use:   "keys <subcommand>",
		Short: "Manage the master encryption key",
	}
	keysCmd.PersistentFlags().StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")

	keysCmd.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Show the master key path and its age public key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			keysDir := filepath.Join(baseDir, "keys")
			km := crypto.NewKeyManager(keysDir)
			identity, err := km.Load()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Hint: run 'sysfig init --encrypt' to generate a master key.\n")
				return err
			}
			clrBold.Println("Master key")
			fmt.Println()
			ok("Path:       %s", clrDim.Sprint(crypto.MasterKeyPath(keysDir)))
			ok("Public key: %s", clrEncrypted.Sprint(crypto.PublicKey(identity)))
			return nil
		},
	})

	keysCmd.AddCommand(&cobra.Command{
		Use:   "generate",
		Short: "Generate a new master key (fails if one already exists)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			keysDir := filepath.Join(baseDir, "keys")
			km := crypto.NewKeyManager(keysDir)
			identity, err := km.Generate()
			if err != nil {
				return err
			}
			fixSudoOwnership(baseDir)
			clrBold.Println("Generated new master key")
			fmt.Println()
			ok("Path:       %s", clrDim.Sprint(crypto.MasterKeyPath(keysDir)))
			ok("Public key: %s", clrEncrypted.Sprint(crypto.PublicKey(identity)))
			fmt.Println()
			warn("%s Back up this key immediately!", clrErr.Sprint("IMPORTANT:"))
			fmt.Printf("     Location: %s\n", crypto.MasterKeyPath(keysDir))
			return nil
		},
	})

	return keysCmd
}

// ── apply ─────────────────────────────────────────────────────────────────────

func newApplyCmd() *cobra.Command {
	var (
		baseDir  string
		sysRoot  string
		dryRun   bool
		noBackup bool
		force    bool
		ids      []string
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Deploy tracked configs from the repo to the system",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			defer fixSudoOwnership(baseDir)
			results, err := core.Apply(core.ApplyOptions{
				BaseDir:  baseDir,
				IDs:      ids,
				DryRun:   dryRun,
				NoBackup: noBackup,
				Force:    force,
				SysRoot:  resolveSysRoot(sysRoot),
			})

			applied, skipped, dirty, hookFailed := 0, 0, 0, 0
			for _, r := range results {
				if r.Skipped {
					fmt.Printf("  %s %s %s\n", clrDim.Sprint("[dry-run]"), clrInfo.Sprint(r.ID), clrDim.Sprint("→ "+r.SystemPath))
					skipped++
					continue
				}
				if r.DirtySkipped {
					warn("Skipped %s — file has local changes (DIRTY). Use --force to overwrite.", clrBold.Sprint(r.ID))
					fmt.Printf("     %s %s\n", clrDim.Sprint("→"), r.SystemPath)
					fmt.Printf("     %s\n", clrDim.Sprint("Tip: run 'sysfig sync' first to commit local changes, or 'sysfig snap take' to snapshot them."))
					dirty++
					continue
				}
				if r.HookFailed {
					fail("Applied (hook failed): %s", clrBold.Sprint(r.ID))
				} else {
					ok("Applied: %s", clrBold.Sprint(r.ID))
				}
				fmt.Printf("     %s %s\n", clrDim.Sprint("→"), r.SystemPath)
				if r.BackupPath != "" {
					fmt.Printf("     %s %s\n", clrDim.Sprint("backup:"), clrDim.Sprint(r.BackupPath))
				}
				if r.Encrypted {
					fmt.Printf("     %s\n", clrEncrypted.Sprint("decrypted from age ciphertext"))
				}
				if r.TemplateRendered {
					fmt.Printf("     %s\n", clrInfo.Sprint("template variables rendered"))
				}
				if r.ChownWarning != "" {
					warn("%s", r.ChownWarning)
				}
				for _, hr := range r.Hooks {
					if hr.Err != nil {
						fmt.Printf("     %s %s: %s\n", clrErr.Sprint("hook failed:"), clrBold.Sprint(hr.Name), clrErr.Sprint(hr.Err))
						if hr.Output != "" {
							fmt.Printf("     %s\n", clrDim.Sprint(hr.Output))
						}
					} else {
						fmt.Printf("     %s %s\n", clrOK.Sprint("hook ok:"), clrDim.Sprint(hr.Name))
						if hr.Output != "" {
							fmt.Printf("     %s\n", clrDim.Sprint(hr.Output))
						}
					}
				}
				if r.HookFailed {
					hookFailed++
				} else {
					applied++
				}
			}

			if err != nil {
				return err
			}
			if len(results) == 0 {
				info("Nothing to apply (no tracked files found).")
				return nil
			}

			fmt.Println()
			divider()
			parts := []string{clrOK.Sprintf("Applied: %d", applied)}
			if skipped > 0 {
				parts = append(parts, clrDim.Sprintf("Dry-run: %d", skipped))
			}
			if dirty > 0 {
				parts = append(parts, clrWarn.Sprintf("Skipped (dirty): %d", dirty))
			}
			if hookFailed > 0 {
				parts = append(parts, clrErr.Sprintf("Hook failed: %d", hookFailed))
			}
			fmt.Printf("  %s\n", strings.Join(parts, "  ·  "))
			if hookFailed > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.BoolVar(&dryRun, "dry-run", false, "print what would happen without writing anything")
	f.BoolVar(&noBackup, "no-backup", false, "skip pre-apply backup (dangerous)")
	f.BoolVar(&force, "force", false, "overwrite DIRTY (locally-modified) files without prompting")
	f.StringArrayVar(&ids, "id", nil, "apply only this ID (repeatable)")
	return cmd
}

// ── status ────────────────────────────────────────────────────────────────────

// pad returns s left-padded with spaces to at least width visible characters.
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// statusLabel returns the display label for a status.
func statusLabel(s core.FileStatusLabel) string {
	switch s {
	case core.StatusDirty:
		return "DIRTY"
	case core.StatusPending:
		return "PENDING"
	default:
		return string(s)
	}
}

// printStatusTable renders the status table grouped by directory.
// Folders where all files are clean show one summary line.
// Folders with any changed files expand to list those files.
// Returns true if any file needs attention.
func printStatusTable(results []core.FileStatusResult) (hasDiff bool) {
	type dirGroup struct {
		dir     string
		results []core.FileStatusResult
	}

	// Group results by directory, preserving first-seen order.
	dirOrder := []string{}
	groups := map[string][]core.FileStatusResult{}
	for _, r := range results {
		dir := filepath.Dir(r.SystemPath)
		if _, seen := groups[dir]; !seen {
			dirOrder = append(dirOrder, dir)
		}
		groups[dir] = append(groups[dir], r)
	}

	// Count totals.
	totals := map[string]int{}
	for _, r := range results {
		totals[string(r.Status)]++
		switch r.Status {
		case core.StatusDirty, core.StatusPending, core.StatusMissing:
			hasDiff = true
		}
	}

	// Compute max row label width — must account for single-file rows that
	// show the full path, not just the directory name.
	dirW := len("PATH")
	for _, d := range dirOrder {
		files := groups[d]
		var label string
		if len(files) == 1 {
			label = files[0].SystemPath
		} else {
			label = d + "/"
		}
		if len(label) > dirW {
			dirW = len(label)
		}
	}
	dirW += 2

	fmt.Printf("%s  %s\n", clrBold.Sprint(pad("PATH", dirW)), clrBold.Sprint("STATUS"))
	divider()

	for _, dir := range dirOrder {
		files := groups[dir]
		dirDisplay := dir + "/"

		// Tally this dir's statuses.
		dCounts := map[string]int{}
		for _, r := range files {
			dCounts[string(r.Status)]++
		}

		// Build summary. Single-file rows: plain status word. Multi-file: "N status" counts.
		var summary string
		if len(files) == 1 {
			r := files[0]
			summary = statusColored(r.Status, statusLabel(r.Status))
		} else {
			var parts []string
			if n := dCounts[string(core.StatusSynced)]; n > 0 {
				parts = append(parts, clrSynced.Sprintf("%d synced", n))
			}
			if n := dCounts[string(core.StatusEncrypted)]; n > 0 {
				parts = append(parts, clrEncrypted.Sprintf("%d encrypted", n))
			}
			if n := dCounts[string(core.StatusDirty)]; n > 0 {
				parts = append(parts, clrDirty.Sprintf("%d dirty", n))
			}
			if n := dCounts[string(core.StatusPending)]; n > 0 {
				parts = append(parts, clrPending.Sprintf("%d pending", n))
			}
			if n := dCounts[string(core.StatusMissing)]; n > 0 {
				parts = append(parts, clrMissing.Sprintf("%d missing", n))
			}
			summary = strings.Join(parts, clrDim.Sprint("  ·  "))
		}

		// Determine if this dir has any non-clean files.
		dirDirty := dCounts[string(core.StatusDirty)]+
			dCounts[string(core.StatusPending)]+
			dCounts[string(core.StatusMissing)] > 0

		// Single file in this dir: show the full path instead of the folder.
		rowLabel := dirDisplay
		if len(files) == 1 {
			rowLabel = files[0].SystemPath
		}

		if dirDirty {
			fmt.Printf("%s  %s\n", clrDirty.Sprint(pad(rowLabel, dirW)), summary)
		} else {
			fmt.Printf("%s  %s\n", clrBold.Sprint(pad(rowLabel, dirW)), summary)
		}

		// Expand changed files under the dir (skip if single-file row — it's already shown inline).
		if dirDirty && len(files) > 1 {
			// Compute filename column width for alignment within this dir.
			nameW := 0
			for _, r := range files {
				switch r.Status {
				case core.StatusDirty, core.StatusPending, core.StatusMissing:
					name := filepath.Base(r.SystemPath)
					if len(name) > nameW {
						nameW = len(name)
					}
				}
			}
			nameW += 2

			for _, r := range files {
				switch r.Status {
				case core.StatusDirty, core.StatusPending, core.StatusMissing:
				default:
					continue
				}
				name := filepath.Base(r.SystemPath)
				label := statusLabel(r.Status)
				coloredLabel := statusColored(r.Status, label)
				fmt.Printf("  %s %s  %s\n",
					clrDim.Sprint("└"),
					clrDirty.Sprint(pad(name, nameW)),
					coloredLabel)

				// Meta drift detail.
				if r.MetaDrift && r.RecordedMeta != nil && r.CurrentMeta != nil {
					rec := r.RecordedMeta
					cur := r.CurrentMeta
					if rec.UID != cur.UID || rec.GID != cur.GID {
						recOwner := rec.Owner
						if recOwner == "" { recOwner = fmt.Sprintf("%d", rec.UID) }
						recGroup := rec.Group
						if recGroup == "" { recGroup = fmt.Sprintf("%d", rec.GID) }
						curOwner := cur.Owner
						if curOwner == "" { curOwner = fmt.Sprintf("%d", cur.UID) }
						curGroup := cur.Group
						if curGroup == "" { curGroup = fmt.Sprintf("%d", cur.GID) }
						fmt.Printf("    %s owner: %s → %s\n",
							clrWarn.Sprint("⚠"),
							clrDim.Sprintf("%s:%s", recOwner, recGroup),
							clrDirty.Sprintf("%s:%s", curOwner, curGroup))
					}
					if rec.Mode != cur.Mode {
						fmt.Printf("    %s mode:  %s → %s\n",
							clrWarn.Sprint("⚠"),
							clrDim.Sprintf("%04o", rec.Mode),
							clrDirty.Sprintf("%04o", cur.Mode))
					}
				}
			}
		}
	}

	divider()
	summaryParts := []string{clrBold.Sprintf("%d files", len(results))}
	if n := totals[string(core.StatusSynced)]; n > 0 {
		summaryParts = append(summaryParts, clrSynced.Sprintf("%d synced", n))
	}
	if n := totals[string(core.StatusDirty)]; n > 0 {
		summaryParts = append(summaryParts, clrDirty.Sprintf("%d dirty", n))
	}
	if n := totals[string(core.StatusPending)]; n > 0 {
		summaryParts = append(summaryParts, clrPending.Sprintf("%d pending", n))
	}
	if n := totals[string(core.StatusMissing)]; n > 0 {
		summaryParts = append(summaryParts, clrMissing.Sprintf("%d missing", n))
	}
	if n := totals[string(core.StatusEncrypted)]; n > 0 {
		summaryParts = append(summaryParts, clrEncrypted.Sprintf("%d encrypted", n))
	}
	fmt.Printf("  %s\n", strings.Join(summaryParts, clrDim.Sprint("  ·  ")))
	return hasDiff
}

// printStatusFlat renders every tracked file as its own row.
func printStatusFlat(results []core.FileStatusResult) (hasDiff bool) {
	pathW := len("PATH")
	stW := len("STATUS")
	for _, r := range results {
		if len(r.SystemPath) > pathW {
			pathW = len(r.SystemPath)
		}
		if len(statusLabel(r.Status)) > stW {
			stW = len(statusLabel(r.Status))
		}
	}
	pathW += 2
	stW += 2

	fmt.Printf("%s  %s\n", clrBold.Sprint(pad("PATH", pathW)), clrBold.Sprint("STATUS"))
	divider()

	totals := map[string]int{}
	for _, r := range results {
		label := statusLabel(r.Status)
		switch r.Status {
		case core.StatusDirty, core.StatusPending, core.StatusMissing:
			hasDiff = true
		}
		totals[string(r.Status)]++
		fmt.Printf("%s  %s\n", pad(r.SystemPath, pathW), statusColored(r.Status, pad(label, stW)))

		if r.MetaDrift && r.RecordedMeta != nil && r.CurrentMeta != nil {
			rec, cur := r.RecordedMeta, r.CurrentMeta
			if rec.UID != cur.UID || rec.GID != cur.GID {
				recOwner := rec.Owner
				if recOwner == "" { recOwner = fmt.Sprintf("%d", rec.UID) }
				recGroup := rec.Group
				if recGroup == "" { recGroup = fmt.Sprintf("%d", rec.GID) }
				curOwner := cur.Owner
				if curOwner == "" { curOwner = fmt.Sprintf("%d", cur.UID) }
				curGroup := cur.Group
				if curGroup == "" { curGroup = fmt.Sprintf("%d", cur.GID) }
				fmt.Printf("   %s owner: %s → %s\n",
					clrWarn.Sprint("⚠"),
					clrDim.Sprintf("%s:%s", recOwner, recGroup),
					clrDirty.Sprintf("%s:%s", curOwner, curGroup))
			}
			if rec.Mode != cur.Mode {
				fmt.Printf("   %s mode:  %s → %s\n",
					clrWarn.Sprint("⚠"),
					clrDim.Sprintf("%04o", rec.Mode),
					clrDirty.Sprintf("%04o", cur.Mode))
			}
		}
	}

	divider()
	var sp []string
	sp = append(sp, clrBold.Sprintf("%d files", len(results)))
	if n := totals[string(core.StatusSynced)]; n > 0 { sp = append(sp, clrSynced.Sprintf("%d synced", n)) }
	if n := totals[string(core.StatusDirty)]; n > 0 { sp = append(sp, clrDirty.Sprintf("%d dirty", n)) }
	if n := totals[string(core.StatusPending)]; n > 0 { sp = append(sp, clrPending.Sprintf("%d pending", n)) }
	if n := totals[string(core.StatusMissing)]; n > 0 { sp = append(sp, clrMissing.Sprintf("%d missing", n)) }
	if n := totals[string(core.StatusEncrypted)]; n > 0 { sp = append(sp, clrEncrypted.Sprintf("%d encrypted", n)) }
	fmt.Printf("  %s\n", strings.Join(sp, clrDim.Sprint("  ·  ")))
	return hasDiff
}

func newStatusCmd() *cobra.Command {
	var (
		baseDir   string
		sysRoot   string
		ids       []string
		watchMode bool
		interval  time.Duration
		flatFiles bool
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sync status of all tracked files",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			if watchMode {
				return runStatusWatch(baseDir, sysRoot, ids, interval)
			}

			results, err := core.Status(baseDir, ids, sysRoot)
			if err != nil {
				return err
			}
			if len(results) == 0 {
				info("No tracked files.")
				return nil
			}
			var dirty bool
			if flatFiles {
				dirty = printStatusFlat(results)
			} else {
				dirty = printStatusTable(results)
			}
			if dirty {
				os.Exit(1)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.StringArrayVar(&ids, "id", nil, "check only this ID (repeatable)")
	f.BoolVarP(&watchMode, "watch", "w", false, "continuously refresh status (Ctrl-C to stop)")
	f.BoolVarP(&flatFiles, "files", "f", false, "show every tracked file individually instead of grouping by directory")
	f.DurationVar(&interval, "interval", 3*time.Second, "refresh interval when --watch is set")
	return cmd
}

// runStatusWatch clears the screen and re-renders the status table every
// interval until the user sends SIGINT/SIGTERM.
func runStatusWatch(baseDir, sysRoot string, ids []string, interval time.Duration) error {
	stop := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		close(stop)
	}()

	// ANSI: move cursor to top-left and clear to end of screen.
	const clearScreen = "\033[H\033[2J"
	// ANSI: move cursor to top-left without clearing (flicker-free redraw).
	const cursorHome = "\033[H"

	first := true
	for {
		results, err := core.Status(baseDir, ids, sysRoot)

		// On the very first frame clear the terminal fully.
		// On subsequent frames jump to top-left and overwrite in-place to
		// avoid flickering.
		if first {
			fmt.Print(clearScreen)
			first = false
		} else {
			fmt.Print(cursorHome)
		}

		ts := time.Now().Format("2006-01-02 15:04:05")
		fmt.Printf("%s  %s  %s\n\n",
			clrBold.Sprint("sysfig status"),
			clrDim.Sprint("--watch"),
			clrDim.Sprint(ts))

		if err != nil {
			fmt.Printf("  %s  %s\n", clrErr.Sprint("ERROR"), err)
		} else if len(results) == 0 {
			info("No tracked files.")
		} else {
			printStatusTable(results)
		}

		// Print a blank footer so old content below is visually separated.
		fmt.Printf("\n  %s\n", clrDim.Sprintf("Refreshing every %v — Ctrl-C to stop", interval))

		select {
		case <-stop:
			fmt.Println()
			return nil
		case <-time.After(interval):
		}
	}
}

// ── diff ──────────────────────────────────────────────────────────────────────

func newDiffCmd() *cobra.Command {
	var (
		baseDir    string
		sysRoot    string
		colorFlag  bool
		colorSet   bool
		ids        []string
	)

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show unified diff between system files and repo versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			if err := core.CheckDiffPrereqs(); err != nil {
				os.Exit(2)
			}

			useColor := isatty()
			if cmd.Flags().Changed("color") {
				useColor = colorFlag
				_ = colorSet
			}

			results, err := core.Diff(core.DiffOptions{
				BaseDir: baseDir,
				IDs:     ids,
				SysRoot: resolveSysRoot(sysRoot),
			})
			if err != nil {
				os.Exit(2)
			}

			if len(results) == 0 {
				info("No tracked files.")
				os.Exit(0)
			}

			// Separate changed vs clean.
			var changed, clean []core.DiffResult
			for _, r := range results {
				if r.Diff != "" {
					changed = append(changed, r)
				} else {
					clean = append(clean, r)
				}
			}

			if len(changed) == 0 {
				info("All %d tracked files are identical to the repo.", len(results))
				os.Exit(0)
			}

			// Print only the changed files.
			for i, r := range changed {
				if i > 0 {
					fmt.Println()
				}
				var statusTag string
				switch r.Status {
				case core.StatusDirty:
					statusTag = clrDirty.Sprint("DIRTY")
				case core.StatusPending:
					statusTag = clrPending.Sprint("PENDING")
				case core.StatusMissing:
					statusTag = clrMissing.Sprint("MISSING")
				default:
					statusTag = clrDim.Sprint(string(r.Status))
				}
				fmt.Printf("%s %s  %s\n",
					clrBold.Sprint("──"),
					clrBold.Sprint(r.SystemPath),
					statusTag)
				if useColor {
					fmt.Print(colorize(r.Diff))
				} else {
					fmt.Print(r.Diff)
				}
			}

			// One-line summary of clean files.
			divider()
			if len(clean) > 0 {
				fmt.Printf("  %s  ·  %s\n",
					clrDirty.Sprintf("%d changed", len(changed)),
					clrDim.Sprintf("%d identical", len(clean)))
			} else {
				fmt.Printf("  %s\n", clrDirty.Sprintf("%d changed", len(changed)))
			}

			os.Exit(1)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.BoolVar(&colorFlag, "color", true, "colorize diff output (default: true when stdout is a TTY)")
	f.StringArrayVar(&ids, "id", nil, "diff only this ID (repeatable)")
	return cmd
}

func diffStatusLabel(s core.FileStatusLabel) string {
	switch s {
	case core.StatusDirty:
		return "DIRTY — run: sysfig sync"
	case core.StatusPending:
		return "PENDING — run: sysfig apply"
	case core.StatusSynced:
		return "SYNCED"
	case core.StatusMissing:
		return "MISSING"
	case core.StatusEncrypted:
		return "ENCRYPTED"
	default:
		return string(s)
	}
}

func colorize(diff string) string {
	const (
		red   = "\033[31m"
		green = "\033[32m"
		cyan  = "\033[36m"
		reset = "\033[0m"
	)
	var out bytes.Buffer
	start := 0
	for i := 0; i < len(diff); i++ {
		if diff[i] == '\n' || i == len(diff)-1 {
			end := i + 1
			line := diff[start:end]
			switch {
			case len(line) > 0 && line[0] == '+' && (len(line) < 4 || line[:3] != "+++"):
				out.WriteString(green + line + reset)
			case len(line) > 0 && line[0] == '-' && (len(line) < 4 || line[:3] != "---"):
				out.WriteString(red + line + reset)
			case len(line) > 2 && line[:2] == "@@":
				out.WriteString(cyan + line + reset)
			default:
				out.WriteString(line)
			}
			start = end
		}
	}
	return out.String()
}

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
		push    bool
		pull    bool
		force   bool
		sysRoot string
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Commit local changes, optionally pull first and/or push after",
		Long: `Stages all modified tracked files and creates a local git commit (offline-safe).

Use --pull to fetch remote changes first (full round-trip with --push):
  sysfig sync --pull --push

This replaces the standalone 'push' and 'pull' commands.`,
		Example: `  sysfig sync                        # local commit only
  sysfig sync --message "tuned nginx" # with custom message
  sysfig sync --push                  # commit + push
  sysfig sync --pull                  # pull first, then commit
  sysfig sync --pull --push           # full round-trip`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			result, err := core.Sync(core.SyncOptions{
				BaseDir: baseDir,
				Message: message,
				Pull:    pull,
				Push:    push,
				Force:   force,
				SysRoot: resolveSysRoot(sysRoot),
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

			if !result.Committed {
				info("Nothing to commit — shadow repo is clean.")
				return nil
			}
			ok("Committed: %s", clrBold.Sprint(result.Message))
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
	f.StringVar(&message, "message", "", "commit message (default: sysfig: sync <timestamp>)")
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

// ── log ───────────────────────────────────────────────────────────────────────

func newLogCmd() *cobra.Command {
	var (
		baseDir string
		file    string
		n       int
	)

	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show commit history as a tree",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			repoDir := filepath.Join(baseDir, "repo.git")
			gitArgs := []string{
				"--git-dir=" + repoDir,
				"log", "--graph",
				"--pretty=format:%C(yellow)%h%Creset %C(cyan)%ad%Creset %s%C(green)%d%Creset",
				"--date=short",
				"--all",
			}
			if n > 0 {
				gitArgs = append(gitArgs, fmt.Sprintf("-n%d", n))
			}
			if file != "" {
				gitArgs = append(gitArgs, "--", file)
			}

			c := exec.Command("git", gitArgs...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			c.Env = os.Environ()
			if err := c.Run(); err != nil {
				os.Exit(1)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&file, "file", "", "show only commits touching this repo-relative path")
	f.IntVarP(&n, "number", "n", 0, "limit to last N commits (0 = unlimited)")
	return cmd
}

// ── doctor ────────────────────────────────────────────────────────────────────

func newDoctorCmd() *cobra.Command {
	var (
		baseDir string
		network bool
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run a full health check of your sysfig environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			result := core.Doctor(core.DoctorOptions{
				BaseDir: baseDir,
				Network: network,
			})

			fmt.Println()
			clrBold.Println("  sysfig doctor — environment health check")
			fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
			fmt.Println()

			labelW := 0
			for _, f := range result.Findings {
				if len(f.Label) > labelW {
					labelW = len(f.Label)
				}
			}
			labelW += 2

			lastCategory := ""
			for _, f := range result.Findings {
				if f.Category != lastCategory {
					if lastCategory != "" {
						fmt.Println()
					}
					fmt.Printf("  %s\n", clrBold.Sprint(f.Category))
					lastCategory = f.Category
				}

				var icon, label string
				switch f.Severity {
				case core.SeverityOK:
					icon = clrOK.Sprint("✓")
					label = clrOK.Sprint(pad(f.Label, labelW))
				case core.SeverityWarn:
					icon = clrWarn.Sprint("⚠")
					label = clrWarn.Sprint(pad(f.Label, labelW))
				case core.SeverityFail:
					icon = clrErr.Sprint("✗")
					label = clrErr.Sprint(pad(f.Label, labelW))
				case core.SeverityInfo:
					icon = clrInfo.Sprint("ℹ")
					label = clrDim.Sprint(pad(f.Label, labelW))
				}

				fmt.Printf("    %s  %s  %s\n", icon, label, clrDim.Sprint(f.Detail))
				if f.Hint != "" {
					fmt.Printf("       %s %s\n", clrDim.Sprint("→"), clrInfo.Sprint(f.Hint))
				}
			}

			fmt.Println()
			divider()
			okStr := clrOK.Sprintf("✓ %d passed", result.OK)
			parts := []string{okStr}
			if result.Warn > 0 {
				parts = append(parts, clrWarn.Sprintf("⚠ %d warnings", result.Warn))
			}
			if result.Fail > 0 {
				parts = append(parts, clrErr.Sprintf("✗ %d failed", result.Fail))
			}
			fmt.Printf("  %s\n", strings.Join(parts, clrDim.Sprint("  ·  ")))
			fmt.Println()

			if result.Fail > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.BoolVar(&network, "network", false, "also probe the configured remote (requires network)")
	return cmd
}

// ── watch ─────────────────────────────────────────────────────────────────────

// watchRun is the shared foreground-watcher logic used by both
// `sysfig watch` (bare) and `sysfig watch run`.
func watchRun(baseDir, sysRoot string, debounce time.Duration, dryRun bool) error {
	clrBold.Println("Watching tracked files for changes  (Ctrl-C to stop)")
	fmt.Printf("  %s %s\n", clrDim.Sprint("base-dir:"), clrDim.Sprint(baseDir))
	fmt.Printf("  %s %v\n\n", clrDim.Sprint("debounce:"), debounce)

	stop := make(chan struct{})

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println()
		info("Stopping watch.")
		close(stop)
	}()

	onEvent := func(path string, result *core.SyncResult, err error) {
		ts := time.Now().Format("15:04:05")
		if dryRun {
			fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrInfo.Sprint("[dry-run]"), path)
			return
		}
		if err != nil {
			fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrErr.Sprint("ERROR"), err)
			return
		}
		if result != nil && !result.Committed {
			return
		}
		fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrOK.Sprint("synced"), path)
		if result != nil && result.Message != "" {
			fmt.Printf("            %s\n", clrDim.Sprint(result.Message))
		}
	}

	return core.Watch(core.WatchOptions{
		BaseDir:  baseDir,
		SysRoot:  resolveSysRoot(sysRoot),
		Debounce: debounce,
		DryRun:   dryRun,
		OnEvent:  onEvent,
	}, stop)
}

func newWatchCmd() *cobra.Command {
	var (
		baseDir  string
		sysRoot  string
		debounce time.Duration
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Auto-sync tracked files when they change on disk",
		Long: `Watch monitors every tracked config file for changes and runs
'sysfig sync' automatically after a short debounce window.

Sub-commands:
  run       Run the watcher in the foreground (default)
  install   Install as a systemd user service
  uninstall Remove the systemd user service
  status    Show systemd service status

Press Ctrl-C to stop the foreground watcher.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			return watchRun(baseDir, sysRoot, debounce, dryRun)
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "wait this long after last change before syncing")
	f.BoolVar(&dryRun, "dry-run", false, "print detected changes without syncing")

	cmd.AddCommand(
		newWatchRunCmd(),
		newWatchInstallCmd(),
		newWatchUninstallCmd(),
		newWatchStatusCmd(),
	)
	return cmd
}

// ── watch run ─────────────────────────────────────────────────────────────────

func newWatchRunCmd() *cobra.Command {
	var (
		baseDir  string
		sysRoot  string
		debounce time.Duration
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the file watcher in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			return watchRun(baseDir, sysRoot, debounce, dryRun)
		},
	}
	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "wait this long after last change before syncing")
	f.BoolVar(&dryRun, "dry-run", false, "print detected changes without syncing")
	return cmd
}

// ── watch install ─────────────────────────────────────────────────────────────

const watchServiceTemplate = `[Unit]
Description=sysfig config watcher — auto-sync tracked files
Documentation=https://github.com/sysfig-dev/sysfig
After=default.target

[Service]
Type=simple
ExecStart=%s watch --base-dir %s --debounce %s
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
`

func newWatchInstallCmd() *cobra.Command {
	var (
		baseDir  string
		debounce time.Duration
		enable   bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install sysfig-watch as a systemd user service",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			// Resolve binary path.
			binPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cannot resolve binary path: %w", err)
			}

			// Build service file content.
			content := fmt.Sprintf(watchServiceTemplate, binPath, baseDir, debounce)

			// Determine service file path: ~/.config/systemd/user/
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("cannot resolve home directory: %w", err)
			}
			serviceDir := filepath.Join(homeDir, ".config", "systemd", "user")
			servicePath := filepath.Join(serviceDir, "sysfig-watch.service")

			if err := os.MkdirAll(serviceDir, 0o755); err != nil {
				return fmt.Errorf("create systemd user dir: %w", err)
			}
			if err := os.WriteFile(servicePath, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write service file: %w", err)
			}

			ok("Service file written: %s", clrDim.Sprint(servicePath))

			// Reload daemon so systemd picks up the new unit.
			if out, err := runSystemctl("--user", "daemon-reload"); err != nil {
				warn("daemon-reload failed: %s", out)
			} else {
				ok("systemctl --user daemon-reload")
			}

			if enable {
				if out, err := runSystemctl("--user", "enable", "--now", "sysfig-watch"); err != nil {
					return fmt.Errorf("enable service: %s: %w", out, err)
				}
				ok("Service enabled and started")
				fmt.Printf("  %s\n", clrDim.Sprint("systemctl --user status sysfig-watch"))
			} else {
				fmt.Println()
				info("To start the service now:")
				fmt.Printf("  %s\n", clrBold.Sprint("systemctl --user enable --now sysfig-watch"))
				fmt.Printf("  %s\n", clrBold.Sprint("sysfig watch status"))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "debounce window written into the service file")
	f.BoolVar(&enable, "enable", false, "also run 'systemctl --user enable --now sysfig-watch'")
	return cmd
}

// ── watch uninstall ───────────────────────────────────────────────────────────

func newWatchUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the sysfig-watch systemd user service",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("cannot resolve home directory: %w", err)
			}
			servicePath := filepath.Join(homeDir, ".config", "systemd", "user", "sysfig-watch.service")

			// Stop + disable (best-effort).
			if out, err := runSystemctl("--user", "disable", "--now", "sysfig-watch"); err != nil {
				warn("disable failed (service may not be running): %s", out)
			} else {
				ok("Service stopped and disabled")
			}

			if err := os.Remove(servicePath); err != nil {
				if os.IsNotExist(err) {
					info("Service file not found — nothing to remove.")
					return nil
				}
				return fmt.Errorf("remove service file: %w", err)
			}
			ok("Removed: %s", clrDim.Sprint(servicePath))

			if _, err := runSystemctl("--user", "daemon-reload"); err == nil {
				ok("systemctl --user daemon-reload")
			}
			return nil
		},
	}
	return cmd
}

// ── watch status ──────────────────────────────────────────────────────────────

func newWatchStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the systemd service status for sysfig-watch",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := runSystemctl("--user", "status", "sysfig-watch")
			fmt.Print(out)
			return err
		},
	}
	return cmd
}

// runSystemctl runs a systemctl command and returns combined output + error.
func runSystemctl(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ── profile ────────────────────────────────────────────────────────────────────

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile <subcommand>",
		Short: "Manage sysfig profiles (isolated config sets)",
		Long: `Profiles let you maintain separate sets of tracked files, each with their
own git repo, state, keys, and snapshots.

Examples:
  sysfig profile list
  sysfig profile create work
  sysfig --profile work track /etc/nginx/nginx.conf
  sysfig --profile work sync
  sysfig profile delete personal`,
	}
	cmd.AddCommand(
		newProfileListCmd(),
		newProfileCreateCmd(),
		newProfileDeleteCmd(),
	)
	return cmd
}

func newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := sysfigHome()
			pDir := profilesDir()

			active := globalProfile
			if active == "" {
				active = os.Getenv("SYSFIG_PROFILE")
			}

			// Collect rows first so we can compute column width.
			type profileRow struct{ name, path, marker string }
			rows := []profileRow{}
			defaultMarker := ""
			if active == "" {
				defaultMarker = clrOK.Sprint(" ◀ active")
			}
			rows = append(rows, profileRow{"default", home, defaultMarker})

			entries, err := os.ReadDir(pDir)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				marker := ""
				if name == active {
					marker = clrOK.Sprint(" ◀ active")
				}
				rows = append(rows, profileRow{name, filepath.Join(pDir, name), marker})
			}

			nameW := len("NAME")
			for _, r := range rows {
				if len(r.name) > nameW {
					nameW = len(r.name)
				}
			}
			nameW += 2

			fmt.Printf("  %s  %s\n", clrBold.Sprint(pad("NAME", nameW)), clrBold.Sprint("PATH"))
			divider()
			for _, r := range rows {
				fmt.Printf("  %s  %s%s\n", clrBold.Sprint(pad(r.name, nameW)), clrDim.Sprint(r.path), r.marker)
			}
			return nil
		},
	}
}

func newProfileCreateCmd() *cobra.Command {
	var setupURL string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create and initialise a new profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "default" {
				return fmt.Errorf("'default' is reserved — it refers to ~/.sysfig")
			}

			baseDir := filepath.Join(profilesDir(), name)
			if _, err := os.Stat(baseDir); err == nil {
				return fmt.Errorf("profile %q already exists at %s", name, baseDir)
			}

			clrBold.Printf("Creating profile %q\n\n", name)

			if setupURL != "" {
				// Clone from remote.
				result, err := core.Clone(core.CloneOptions{
					BaseDir:   baseDir,
					RemoteURL: setupURL,
				})
				if err != nil {
					return err
				}
				ok("Cloned:  %s", clrDim.Sprint(result.RepoDir))
			} else {
				// Fresh init.
				result, err := core.Init(core.InitOptions{BaseDir: baseDir})
				if err != nil {
					return err
				}
				ok("Repo:    %s", clrDim.Sprint(result.RepoDir))
			}

			ok("Profile %q ready.", name)
			fmt.Println()
			info("To use this profile:")
			fmt.Printf("  %s\n", clrBold.Sprintf("sysfig --profile %s track <path>", name))
			fmt.Printf("  %s\n", clrBold.Sprintf("sysfig --profile %s sync", name))
			fmt.Printf("\n  or set: %s\n", clrDim.Sprintf("export SYSFIG_PROFILE=%s", name))
			return nil
		},
	}
	cmd.Flags().StringVar(&setupURL, "from", "", "clone from remote git URL instead of creating empty repo")
	return cmd
}

func newProfileDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a named profile and all its data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "default" {
				return fmt.Errorf("cannot delete the default profile")
			}

			baseDir := filepath.Join(profilesDir(), name)
			if _, err := os.Stat(baseDir); os.IsNotExist(err) {
				return fmt.Errorf("profile %q does not exist", name)
			}

			if !force {
				return fmt.Errorf(
					"this will permanently delete %s\nRe-run with --force to confirm", baseDir)
			}

			if err := os.RemoveAll(baseDir); err != nil {
				return fmt.Errorf("delete profile: %w", err)
			}
			ok("Deleted profile %q (%s)", name, baseDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion (required)")
	return cmd
}

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

// ── node ─────────────────────────────────────────────────────────────────────

func newNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node <subcommand>",
		Short: "Manage remote nodes (multi-recipient encryption)",
		Long: `Nodes represent remote machines that should be able to decrypt
encrypted config files. Each node is identified by a name and an age X25519
public key. When you run 'sysfig sync', every encrypted file is re-encrypted
to your local master key AND all registered node public keys — so each machine
can decrypt its own copy independently.

Examples:
  sysfig node add laptop age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p
  sysfig node list
  sysfig node remove laptop`,
	}
	cmd.AddCommand(newNodeAddCmd(), newNodeListCmd(), newNodeRemoveCmd())
	return cmd
}

func newNodeAddCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "add <name> <age-public-key>",
		Short: "Register a remote node by name and age public key",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			fixSudoOwnership(baseDir)
			name, pubkey := args[0], args[1]
			result, err := core.NodeAdd(core.NodeAddOptions{
				BaseDir:   baseDir,
				Name:      name,
				PublicKey: pubkey,
			})
			if err != nil {
				return err
			}
			ok("Node %q registered", result.Node.Name)
			info("Public key: %s", clrDim.Sprint(result.Node.PublicKey))
			info("Re-run 'sysfig sync' to re-encrypt tracked files for this node.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newNodeListCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			fixSudoOwnership(baseDir)
			nodes, err := core.NodeList(core.NodeListOptions{BaseDir: baseDir})
			if err != nil {
				return err
			}
			if len(nodes) == 0 {
				info("No nodes registered. Use 'sysfig node add <name> <age-pubkey>'.")
				return nil
			}
			nameW := len("NAME")
			keyW := len("PUBLIC KEY")
			for _, n := range nodes {
				if len(n.Name) > nameW {
					nameW = len(n.Name)
				}
				if len(n.PublicKey) > keyW {
					keyW = len(n.PublicKey)
				}
			}
			nameW += 2
			keyW += 2

			divider()
			fmt.Printf("  %s  %s  %s\n",
				clrBold.Sprint(pad("NAME", nameW)),
				clrBold.Sprint(pad("PUBLIC KEY", keyW)),
				clrBold.Sprint("ADDED"))
			divider()
			for _, n := range nodes {
				fmt.Printf("  %s  %s  %s\n",
					pad(n.Name, nameW),
					clrDim.Sprint(pad(n.PublicKey, keyW)),
					n.AddedAt.Format("2006-01-02"))
			}
			divider()
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newNodeRemoveCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			fixSudoOwnership(baseDir)
			if err := core.NodeRemove(core.NodeRemoveOptions{
				BaseDir: baseDir,
				Name:    args[0],
			}); err != nil {
				return err
			}
			ok("Node %q removed", args[0])
			info("Re-run 'sysfig sync' to re-encrypt tracked files without this node.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}
