package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

// defaultBaseDir returns ~/.sysfig.
func defaultBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sysfig"
	}
	return filepath.Join(home, ".sysfig")
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

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "sysfig",
		Short: "Config management that thinks like a sysadmin, not a git wrapper",
		Long: `sysfig — security-first configuration management for Linux.

Version-control your config files in a bare git repo, deploy them across
machines with a single command, encrypt secrets with age, track file
ownership and permissions, and stay fully offline-capable.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newDeployCmd(),
		newSetupCmd(),
		newInitCmd(),
		newTrackCmd(),
		newKeysCmd(),
		newApplyCmd(),
		newStatusCmd(),
		newDiffCmd(),
		newSyncCmd(),
		newPushCmd(),
		newPullCmd(),
		newLogCmd(),
		newDoctorCmd(),
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

				for _, r := range result.Results {
					switch {
					case r.Err != nil:
						fail("%s  %s", clrErr.Sprint(r.ID), clrErr.Sprint(r.Err.Error()))
					case r.Skipped && r.SkipReason == "dry-run":
						fmt.Printf("  %s %s %s\n",
							clrDim.Sprint("[dry-run]"),
							clrInfo.Sprint(r.ID),
							clrDim.Sprint("→ "+r.SystemPath))
					case r.Skipped:
						fmt.Printf("  %s %s  %s\n",
							clrDim.Sprint("―"),
							clrDim.Sprint(r.ID),
							clrDim.Sprintf("(%s)", r.SkipReason))
					default:
						ok("%-36s → %s", clrBold.Sprint(r.ID), r.SystemPath)
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
				SysRoot:       sysRoot,
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
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
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
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
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
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
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
	)

	cmd := &cobra.Command{
		Use:   "track <path>",
		Short: "Start tracking a config file (or directory with --recursive)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetPath := args[0]
			repoDir := filepath.Join(baseDir, "repo.git")

			if recursive {
				summary, err := core.TrackDir(core.TrackDirOptions{
					DirPath:  targetPath,
					RepoDir:  repoDir,
					StateDir: baseDir,
					Tags:     tags,
					Encrypt:  encrypt,
					Template: template,
					SysRoot:  sysRoot,
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
				SysRoot:    sysRoot,
			})
			if err != nil {
				return err
			}

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
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	f.StringVar(&id, "id", "", "explicit tracking ID (derived from path if omitted)")
	f.BoolVar(&encrypt, "encrypt", false, "mark the file for encryption at rest in the repo")
	f.BoolVar(&template, "template", false, "mark the file as a template with {{variable}} expansions")
	f.BoolVar(&recursive, "recursive", false, "recursively track all files under a directory")
	f.StringVar(&sysRoot, "sys-root", "", "strip this prefix from paths before storing in repo and state")
	f.StringArrayVar(&tags, "tag", nil, "label to attach (repeatable)")
	return cmd
}

// ── keys ──────────────────────────────────────────────────────────────────────

func newKeysCmd() *cobra.Command {
	var baseDir string

	keysCmd := &cobra.Command{
		Use:   "keys <subcommand>",
		Short: "Manage the master encryption key",
	}
	keysCmd.PersistentFlags().StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")

	keysCmd.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Show the master key path and its age public key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			keysDir := filepath.Join(baseDir, "keys")
			km := crypto.NewKeyManager(keysDir)
			identity, err := km.Generate()
			if err != nil {
				return err
			}
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
		ids      []string
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Deploy tracked configs from the repo to the system",
		RunE: func(cmd *cobra.Command, args []string) error {
			results, err := core.Apply(core.ApplyOptions{
				BaseDir:  baseDir,
				IDs:      ids,
				DryRun:   dryRun,
				NoBackup: noBackup,
				SysRoot:  sysRoot,
			})

			applied, skipped := 0, 0
			for _, r := range results {
				if r.Skipped {
					fmt.Printf("  %s %s %s\n", clrDim.Sprint("[dry-run]"), clrInfo.Sprint(r.ID), clrDim.Sprint("→ "+r.SystemPath))
					skipped++
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
				applied++
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
			if skipped > 0 {
				fmt.Printf("  %s  ·  %s\n", clrOK.Sprintf("Applied: %d", applied), clrDim.Sprintf("Dry-run: %d", skipped))
			} else {
				fmt.Printf("  %s\n", clrOK.Sprintf("Applied: %d", applied))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.BoolVar(&dryRun, "dry-run", false, "print what would happen without writing anything")
	f.BoolVar(&noBackup, "no-backup", false, "skip pre-apply backup (dangerous)")
	f.StringArrayVar(&ids, "id", nil, "apply only this ID (repeatable)")
	return cmd
}

// ── status ────────────────────────────────────────────────────────────────────

func newStatusCmd() *cobra.Command {
	var (
		baseDir string
		sysRoot string
		ids     []string
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sync status of all tracked files",
		RunE: func(cmd *cobra.Command, args []string) error {
			results, err := core.Status(baseDir, ids, sysRoot)
			if err != nil {
				return err
			}

			if len(results) == 0 {
				info("No tracked files.")
				return nil
			}

			hasDiff := false
			counts := map[string]int{}

			fmt.Printf("%-40s %-20s %s\n", clrBold.Sprint("ID"), clrBold.Sprint("STATUS"), clrBold.Sprint("SYSTEM PATH"))
			divider()

			for _, r := range results {
				label := string(r.Status)
				switch r.Status {
				case core.StatusDirty:
					label = "DIRTY/MODIFIED"
					hasDiff = true
				case core.StatusPending:
					label = "PENDING/APPLY"
					hasDiff = true
				case core.StatusMissing:
					hasDiff = true
				}
				counts[string(r.Status)]++
				coloredLabel := statusColored(r.Status, fmt.Sprintf("%-20s", label))
				fmt.Printf("%-40s %s %s\n", r.ID, coloredLabel, clrDim.Sprint(r.SystemPath))

				if r.MetaDrift && r.RecordedMeta != nil && r.CurrentMeta != nil {
					rec := r.RecordedMeta
					cur := r.CurrentMeta
					if rec.UID != cur.UID || rec.GID != cur.GID {
						recOwner := rec.Owner
						if recOwner == "" {
							recOwner = fmt.Sprintf("%d", rec.UID)
						}
						recGroup := rec.Group
						if recGroup == "" {
							recGroup = fmt.Sprintf("%d", rec.GID)
						}
						curOwner := cur.Owner
						if curOwner == "" {
							curOwner = fmt.Sprintf("%d", cur.UID)
						}
						curGroup := cur.Group
						if curGroup == "" {
							curGroup = fmt.Sprintf("%d", cur.GID)
						}
						fmt.Printf("   %s owner:  %s → %s\n",
							clrWarn.Sprint("⚠"),
							clrDim.Sprintf("%s:%s", recOwner, recGroup),
							clrDirty.Sprintf("%s:%s", curOwner, curGroup))
					}
					if rec.Mode != cur.Mode {
						fmt.Printf("   %s mode:   %s → %s\n",
							clrWarn.Sprint("⚠"),
							clrDim.Sprintf("%04o", rec.Mode),
							clrDirty.Sprintf("%04o", cur.Mode))
					}
				}
			}

			divider()
			parts := []string{clrBold.Sprintf("%d files", len(results))}
			if n := counts[string(core.StatusSynced)]; n > 0 {
				parts = append(parts, clrSynced.Sprintf("%d synced", n))
			}
			if n := counts[string(core.StatusDirty)]; n > 0 {
				parts = append(parts, clrDirty.Sprintf("%d dirty", n))
			}
			if n := counts[string(core.StatusPending)]; n > 0 {
				parts = append(parts, clrPending.Sprintf("%d pending", n))
			}
			if n := counts[string(core.StatusMissing)]; n > 0 {
				parts = append(parts, clrMissing.Sprintf("%d missing", n))
			}
			if n := counts[string(core.StatusEncrypted)]; n > 0 {
				parts = append(parts, clrEncrypted.Sprintf("%d encrypted", n))
			}
			fmt.Printf("  %s\n", strings.Join(parts, clrDim.Sprint("  ·  ")))

			if hasDiff {
				os.Exit(1)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.StringArrayVar(&ids, "id", nil, "check only this ID (repeatable)")
	return cmd
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
				SysRoot: sysRoot,
			})
			if err != nil {
				os.Exit(2)
			}

			if len(results) == 0 {
				info("No tracked files.")
				os.Exit(0)
			}

			hasDiff := false
			for _, r := range results {
				statusTag := clrDim.Sprintf("[%s]", diffStatusLabel(r.Status))
				switch r.Status {
				case core.StatusDirty:
					statusTag = clrDirty.Sprintf("[%s]", diffStatusLabel(r.Status))
				case core.StatusPending:
					statusTag = clrPending.Sprintf("[%s]", diffStatusLabel(r.Status))
				case core.StatusMissing:
					statusTag = clrMissing.Sprintf("[%s]", diffStatusLabel(r.Status))
				case core.StatusSynced:
					statusTag = clrSynced.Sprintf("[%s]", diffStatusLabel(r.Status))
				}
				fmt.Printf("%s %s  %s\n", clrBold.Sprint("──"), clrBold.Sprint(r.ID), statusTag)
				fmt.Printf("   %s %s\n", clrDim.Sprint("system:"), r.SystemPath)
				fmt.Printf("   %s %s\n", clrDim.Sprint("repo:  "), r.RepoPath)

				switch {
				case r.Skipped:
					fmt.Printf("   %s\n\n", clrDim.Sprintf("(skipped: %s)", r.SkipReason))
				case r.Diff == "":
					fmt.Printf("   %s\n\n", clrSynced.Sprint("(identical)"))
				default:
					hasDiff = true
					fmt.Println()
					if useColor {
						fmt.Print(colorize(r.Diff))
					} else {
						fmt.Print(r.Diff)
					}
					fmt.Println()
				}
			}

			if hasDiff {
				os.Exit(1)
			}
			os.Exit(0)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
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

// ── sync ──────────────────────────────────────────────────────────────────────

func newSyncCmd() *cobra.Command {
	var (
		baseDir string
		message string
		push    bool
		sysRoot string
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Capture local changes and commit (offline-safe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := core.Sync(core.SyncOptions{
				BaseDir: baseDir,
				Message: message,
				Push:    push,
				SysRoot: sysRoot,
			})
			if err != nil {
				return err
			}
			if !result.Committed {
				info("Nothing to commit — shadow repo is clean.")
				return nil
			}
			ok("Committed: %s", clrBold.Sprint(result.Message))
			ok("Repo:      %s", clrDim.Sprint(result.RepoDir))
			if result.Pushed {
				ok("Pushed to remote.")
			} else {
				info("Not pushed. Run %s when online.", clrBold.Sprint("sysfig push"))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	f.StringVar(&message, "message", "", "commit message (default: sysfig: sync <timestamp>)")
	f.BoolVar(&push, "push", false, "also push to remote after committing (requires network)")
	f.StringVar(&sysRoot, "sys-root", "", "prefix all system paths (sandbox/testing override)")
	return cmd
}

// ── push ──────────────────────────────────────────────────────────────────────

func newPushCmd() *cobra.Command {
	var baseDir string

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push local commits to the remote git repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := core.Push(core.PushOptions{BaseDir: baseDir}); err != nil {
				return err
			}
			ok("Pushed to remote.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	return cmd
}

// ── pull ──────────────────────────────────────────────────────────────────────

func newPullCmd() *cobra.Command {
	var baseDir string

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull remote changes into the local repo (requires network)",
		RunE: func(cmd *cobra.Command, args []string) error {
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
	cmd.Flags().StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
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
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
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
			result := core.Doctor(core.DoctorOptions{
				BaseDir: baseDir,
				Network: network,
			})

			fmt.Println()
			clrBold.Println("  sysfig doctor — environment health check")
			fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
			fmt.Println()

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
					label = clrOK.Sprintf("%-30s", f.Label)
				case core.SeverityWarn:
					icon = clrWarn.Sprint("⚠")
					label = clrWarn.Sprintf("%-30s", f.Label)
				case core.SeverityFail:
					icon = clrErr.Sprint("✗")
					label = clrErr.Sprintf("%-30s", f.Label)
				case core.SeverityInfo:
					icon = clrInfo.Sprint("ℹ")
					label = clrDim.Sprintf("%-30s", f.Label)
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
	f.StringVar(&baseDir, "base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	f.BoolVar(&network, "network", false, "also probe the configured remote (requires network)")
	return cmd
}
