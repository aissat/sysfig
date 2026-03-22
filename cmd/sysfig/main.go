package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/internal/crypto"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
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
	clrNew       = color.New(color.FgCyan)
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
	case core.StatusNew:
		return clrNew.Sprint(label)
	case core.StatusTampered:
		return clrErr.Sprint(label)
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
// resolveSyncTarget resolves a sync target (path, hash, or CWD) to a list of
// file IDs. Returns nil (= all files) if target is empty and CWD doesn't match.
// autoTrackNewInTarget stages any untracked files found in group directories
// that fall under the given target path. Called before sync so new files are
// included in the commit without requiring a separate `sysfig track`.
func autoTrackNewInTarget(baseDir, target string) {
	statePath := filepath.Join(baseDir, "state.json")
	sm := state.NewManager(statePath)
	s, err := sm.Load()
	if err != nil {
		return
	}
	repoDir := filepath.Join(baseDir, "repo.git")

	absTarget := target
	if abs, err := filepath.Abs(target); err == nil {
		absTarget = abs
	}

	tracked := make(map[string]bool, len(s.Files))
	for _, rec := range s.Files {
		tracked[rec.SystemPath] = true
	}
	excluded := make(map[string]bool, len(s.Excludes))
	for _, ex := range s.Excludes {
		excluded[ex] = true
	}

	for _, rec := range s.Files {
		if rec.Group == "" {
			continue
		}
		// Only scan group dirs under the target.
		if !strings.HasPrefix(rec.Group, absTarget) && rec.Group != absTarget {
			continue
		}
		filepath.WalkDir(rec.Group, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if excluded[path] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if tracked[path] {
				return nil
			}
			core.Track(core.TrackOptions{ //nolint:errcheck
				SystemPath: path,
				StateDir:   baseDir,
				RepoDir:    repoDir,
				Group:      rec.Group,
			})
			tracked[path] = true
			return nil
		})
	}
}

func resolveSyncTarget(baseDir, target string) []string {
	statePath := filepath.Join(baseDir, "state.json")
	sm := state.NewManager(statePath)
	s, err := sm.Load()
	if err != nil {
		return nil
	}

	absTarget := target
	if target != "" {
		if abs, err := filepath.Abs(target); err == nil {
			absTarget = abs
		}
	}

	var ids []string
	for id, rec := range s.Files {
		match := false
		switch {
		case target == "":
			// CWD mode: match files whose path starts with absTarget (CWD).
			match = strings.HasPrefix(rec.SystemPath, absTarget+"/") ||
				rec.SystemPath == absTarget ||
				rec.Group == absTarget ||
				strings.HasPrefix(rec.Group, absTarget+"/")
		case id == target || id[:min8(len(id))] == target:
			// Hash/ID match (full or short 8-char prefix).
			match = true
		case rec.SystemPath == absTarget ||
			strings.HasPrefix(rec.SystemPath, absTarget+"/") ||
			rec.Group == absTarget ||
			strings.HasPrefix(rec.Group, absTarget+"/"):
			// Path match.
			match = true
		}
		if match {
			ids = append(ids, id)
		}
	}
	return ids
}

func min8(n int) int {
	if n < 8 {
		return n
	}
	return 8
}

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
ownership and permissions, and stay fully offline-capable.

Quick start (local, no remote):
  sysfig init                        initialise on this machine
  sysfig track /etc/nginx/nginx.conf start tracking a file
  sysfig sync                        commit all changes

Coming from another machine?
  sysfig bootstrap <remote-url>      clone your config repo and apply`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Commands that are valid before init — don't gate these.
			skip := map[string]bool{
				"init": true, "bootstrap": true, "version": true,
				"help": true, "doctor": true, "completion": true,
				"__complete": true, "__completeNoDesc": true,
			}
			if skip[cmd.Name()] {
				return nil
			}
			baseDir := resolveBaseDir("")
			if _, err := os.Stat(filepath.Join(baseDir, "repo.git")); os.IsNotExist(err) {
				return fmt.Errorf(
					"sysfig is not initialised in %s\n\n"+
						"  To start fresh:              sysfig init\n"+
						"  To restore from remote repo: sysfig bootstrap <url>",
					baseDir,
				)
			}
			return nil
		},
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
		newBootstrapCmd(),
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
		newShowCmd(),
		newUndoCmd(),
		newDoctorCmd(),
		newSnapCmd(),
		newWatchCmd(),
		newProfileCmd(),
		newNodeCmd(),
		newSourceCmd(),
		newAuditCmd(),
	)

	// Force cobra to register the built-in completion subcommands now so we
	// can attach our own subcommands to it.
	root.InitDefaultCompletionCmd()
	if compCmd, _, err := root.Find([]string{"completion"}); err == nil && compCmd != nil {
		compCmd.AddCommand(newCompletionInstallCmd(root))

		// Replace cobra's zsh subcommand with one that moves `compdef` to
		// the end of the script. This makes `. <(sysfig completion zsh)` work
		// reliably even with process substitution.
		if zshCmd, _, err := compCmd.Find([]string{"zsh"}); err == nil && zshCmd != nil {
			zshCmd.RunE = func(cmd *cobra.Command, args []string) error {
				var buf bytes.Buffer
				if err := root.GenZshCompletion(&buf); err != nil {
					return err
				}
				script := buf.String()
				// Move the early `compdef _sysfig sysfig` line to the end so
				// it runs after the _sysfig function is fully defined —
				// required when sourced via process substitution.
				const earlyCompdef = "compdef _sysfig sysfig\n"
				script = strings.Replace(script, earlyCompdef, "", 1)
				script += "\ncompdef _sysfig sysfig\n"
				fmt.Print(script)
				return nil
			}
		}
	}

	return root
}

// newCompletionInstallCmd returns a `completion install` subcommand that
// detects the current shell and writes the completion script to the right place.
func newCompletionInstallCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "install [bash|zsh|fish]",
		Short:     "Install shell completion (auto-detects shell, or pass one explicitly)",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Explicit shell arg takes priority over $SHELL.
			var shell string
			if len(args) == 1 {
				shell = args[0]
			} else {
				shell = os.Getenv("SHELL")
			}
			switch {
			case shell == "zsh" || strings.Contains(shell, "zsh"):
				return installZshCompletion(root)
			case shell == "bash" || strings.Contains(shell, "bash"):
				return installBashCompletion(root)
			case shell == "fish" || strings.Contains(shell, "fish"):
				return installFishCompletion(root)
			default:
				return fmt.Errorf("unknown shell %q — pass one explicitly: sysfig completion install [bash|zsh|fish]", shell)
			}
		},
	}
}

func installZshCompletion(root *cobra.Command) error {
	// Prefer the first writable dir in $fpath, fall back to ~/.zfunc.
	dir := filepath.Join(os.Getenv("HOME"), ".zfunc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(dir, "_sysfig")
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := root.GenZshCompletion(f); err != nil {
		return err
	}
	ok("Written %s", dest)
	info("Add this to ~/.zshrc if not already present:")
	fmt.Println(`    fpath=(~/.zfunc $fpath)`)
	fmt.Println(`    autoload -Uz compinit && compinit`)
	info("Then restart your shell or run: exec zsh")
	return nil
}

func installBashCompletion(root *cobra.Command) error {
	// Try /etc/bash_completion.d first (system-wide), fall back to ~/.bash_completion.d.
	dirs := []string{"/etc/bash_completion.d", filepath.Join(os.Getenv("HOME"), ".bash_completion.d")}
	var dir string
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err == nil {
			dir = d
			break
		}
	}
	if dir == "" {
		return fmt.Errorf("could not find a writable bash completion directory")
	}
	dest := filepath.Join(dir, "sysfig")
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := root.GenBashCompletion(f); err != nil {
		return err
	}
	ok("Written %s", dest)
	if dir == filepath.Join(os.Getenv("HOME"), ".bash_completion.d") {
		info("Add this to ~/.bashrc if not already present:")
		fmt.Println(`    for f in ~/.bash_completion.d/*; do source "$f"; done`)
	}
	info("Then restart your shell or run: source %s", dest)
	return nil
}

func installFishCompletion(root *cobra.Command) error {
	dir := filepath.Join(os.Getenv("HOME"), ".config", "fish", "completions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(dir, "sysfig.fish")
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := root.GenFishCompletion(f, true); err != nil {
		return err
	}
	ok("Written %s", dest)
	info("Fish picks it up automatically — restart your shell or run: exec fish")
	return nil
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
		Short: "Pull latest configs from remote and apply them (ongoing use)",
		Long: `deploy pulls the latest configs from your remote repo and applies them.
Use this for routine updates on machines already set up with sysfig bootstrap.
Idempotent: safe to re-run as many times as needed.

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

// ── bootstrap ─────────────────────────────────────────────────────────────────

func newBootstrapCmd() *cobra.Command {
	var (
		baseDir       string
		configsOnly   bool
		skipEncrypted bool
		yes           bool
	)

	cmd := &cobra.Command{
		Use:   "bootstrap [remote-url]",
		Short: "First-time setup: clone a remote config repo and apply configs on this machine",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			remoteURL := ""
			if len(args) > 0 {
				remoteURL = args[0]
			}

			fmt.Println()
			clrBold.Println("  sysfig bootstrap — first-time setup")
			fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
			fmt.Println()

			repoDir := filepath.Join(baseDir, "repo.git")
			if fi, err := os.Stat(repoDir); err == nil && fi.IsDir() {
				// Repo exists — but check if state.json was never seeded
				// (can happen if bootstrap was interrupted before seeding).
				// If empty, fall through to core.Clone which will re-seed.
				stateEmpty := true
				sm := state.NewManager(filepath.Join(baseDir, "state.json"))
				if s, err := sm.Load(); err == nil && len(s.Files) > 0 {
					stateEmpty = false
				}
				if !stateEmpty {
					info("This machine is already set up.")
					fmt.Printf("     %s %s\n", clrDim.Sprint("config repo:"), clrDim.Sprint(repoDir))
					fmt.Printf("     %s %s\n", clrDim.Sprint("base dir:   "), clrDim.Sprint(baseDir))
					fmt.Println()
					info("Run %s to check for remote updates.", clrBold.Sprint("sysfig pull"))
					info("Run %s to see current state.", clrBold.Sprint("sysfig status"))
					fmt.Println()
					return nil
				}
				info("Repo exists but state is empty — re-seeding from manifest...")
				fmt.Println()
			}

			step(1, "Remote config repository")
			if remoteURL == "" {
				if yes || !isatty() {
					return fmt.Errorf("remote URL is required (use: sysfig bootstrap <url>)")
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

// autoSyncTracked commits only the given file IDs to their track branches.
// Called automatically after `sysfig track` so newly tracked files appear in
// `sysfig log` immediately without requiring a separate `sysfig sync`.
func autoSyncTracked(baseDir string, ids []string) {
	_, err := core.Sync(core.SyncOptions{
		BaseDir: baseDir,
		Message: "sysfig: track",
		FileIDs: ids,
	})
	if err != nil {
		fmt.Printf("  %s auto-sync: %v\n", clrWarn.Sprint("warn:"), err)
	}
}

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
		localOnly bool
		hashOnly  bool
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

			if localOnly && hashOnly {
				return fmt.Errorf("--local and --hash-only are mutually exclusive")
			}

			if recursive {
				summary, err := core.TrackDir(core.TrackDirOptions{
					DirPath:   targetPath,
					RepoDir:   repoDir,
					StateDir:  baseDir,
					Tags:      tags,
					Encrypt:   encrypt,
					Template:  template,
					SysRoot:   resolveSysRoot(sysRoot),
					Excludes:  excludes,
					LocalOnly: localOnly,
					HashOnly:  hashOnly,
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
				// Print each sub-error on its own line for readability.
				errStr := err.Error()
				for _, line := range strings.Split(errStr, "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Fprintf(os.Stderr, "  %s %s\n", clrErr.Sprint("error:"), line)
					}
				}
				// If any error was permission denied, hint at sudo.
				if strings.Contains(errStr, "permission denied") {
					fmt.Println()
					warn("Some files require elevated privileges.")
					fmt.Printf("     %s\n", clrDim.Sprint("Re-run as root:  sudo sysfig apply"))
					if len(ids) > 0 {
						fmt.Printf("     %s\n", clrDim.Sprint("Or apply only the failed files: sudo sysfig apply --id <id>"))
					}
				}
				os.Exit(1)
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

// groupResultsByDir groups FileStatusResults by their display directory,
// preserving first-seen order.  Files tracked via a group directory
// (Group != "") are folded under that group root; individually-tracked
// files use filepath.Dir(SystemPath) as the key.
func groupResultsByDir(results []core.FileStatusResult) (order []string, groups map[string][]core.FileStatusResult) {
	groups = map[string][]core.FileStatusResult{}
	for _, r := range results {
		dir := filepath.Dir(r.SystemPath)
		// Files tracked via `sysfig track /dir/` carry rec.Group (the root
		// tracked directory).  Use that as the grouping key so sub-directory
		// files (e.g. /etc/pacman.d/hooks/foo) fold under the group row
		// (e.g. /etc/pacman.d/) instead of appearing in their own row.
		if r.Group != "" {
			dir = r.Group
		}
		if _, seen := groups[dir]; !seen {
			order = append(order, dir)
		}
		groups[dir] = append(groups[dir], r)
	}
	return
}

// printStatusTable renders the status table grouped by directory.
// Folders where all files are clean show one summary line.
// Folders with any changed files expand to list those files.
// Returns true if any file needs attention.
func printStatusTable(results []core.FileStatusResult, showIDs bool) (hasDiff bool) {
	type dirGroup struct {
		dir     string
		results []core.FileStatusResult
	}

	// Group results by directory, preserving first-seen order.
	dirOrder, groups := groupResultsByDir(results)

	// Count totals.
	totals := map[string]int{}
	for _, r := range results {
		totals[string(r.Status)]++
		switch r.Status {
		case core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew:
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

	// Hash column is always 8 chars (fixed). Slug column only with showIDs.
	const hashW = 10 // "HASH" + 2 padding
	idW := 0
	if showIDs {
		idW = len("SLUG")
		for _, r := range results {
			if len(r.Slug) > idW {
				idW = len(r.Slug)
			}
		}
		idW += 2
	}

	if showIDs {
		fmt.Printf("%s  %s  %s  %s\n", clrBold.Sprint(pad("PATH", dirW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint(pad("SLUG", idW)), clrBold.Sprint("STATUS"))
	} else {
		fmt.Printf("%s  %s  %s\n", clrBold.Sprint(pad("PATH", dirW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint("STATUS"))
	}
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
			if n := dCounts[string(core.StatusNew)]; n > 0 {
				parts = append(parts, clrNew.Sprintf("%d new", n))
			}
			summary = strings.Join(parts, clrDim.Sprint("  ·  "))
		}

		// Determine if this dir has any non-clean files.
		dirDirty := dCounts[string(core.StatusDirty)]+
			dCounts[string(core.StatusPending)]+
			dCounts[string(core.StatusMissing)]+
			dCounts[string(core.StatusNew)] > 0

		// Single file in this dir: show the full path instead of the folder.
		rowLabel := dirDisplay
		if len(files) == 1 {
			rowLabel = files[0].SystemPath
		}

		rowHash := core.DeriveID(dir)
		rowSlug := ""
		if len(files) == 1 {
			rowHash = files[0].ID
			rowSlug = files[0].Slug
		}
		// grp = any file in this row was tracked via `sysfig track /dir/` (Group != "").
		// local = tracked with --local (never pushed to remote).
		// hash = tracked with --hash-only (integrity monitoring only).
		// no tag = tracked individually via `sysfig track /path/to/file`.
		isGroup := files[0].Group != ""
		typeTag := ""
		switch {
		case files[0].HashOnly:
			typeTag = "  " + clrDim.Sprint("hash")
		case files[0].LocalOnly:
			typeTag = "  " + clrDim.Sprint("local")
		case isGroup:
			typeTag = "  " + clrDim.Sprint("grp")
		}

		pathCol := pad(rowLabel, dirW)
		hashCol := clrDim.Sprint(pad(rowHash, hashW))
		if dirDirty {
			if showIDs {
				fmt.Printf("%s  %s  %s  %s%s\n", clrDirty.Sprint(pathCol), hashCol, clrDim.Sprint(pad(rowSlug, idW)), summary, typeTag)
			} else {
				fmt.Printf("%s  %s  %s%s\n", clrDirty.Sprint(pathCol), hashCol, summary, typeTag)
			}
		} else {
			if showIDs {
				fmt.Printf("%s  %s  %s  %s%s\n", clrBold.Sprint(pathCol), hashCol, clrDim.Sprint(pad(rowSlug, idW)), summary, typeTag)
			} else {
				fmt.Printf("%s  %s  %s%s\n", clrBold.Sprint(pathCol), hashCol, summary, typeTag)
			}
		}

		// Expand changed files under the dir (skip if single-file row — it's already shown inline).
		if dirDirty && len(files) > 1 {
			for _, r := range files {
				switch r.Status {
				case core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew:
				default:
					continue
				}
				// Sub-row: align PATH/HASH/STATUS columns with parent rows.
				// "  └ " is 4 display columns but 6 bytes (└ is 3 bytes in UTF-8).
				// Pad only the filename to dirW-4 so total display width equals dirW.
				subName := pad(filepath.Base(r.SystemPath), dirW-4)
				if r.Status == core.StatusNew {
					fmt.Printf("  └ %s  %s  %s\n",
						clrDim.Sprint(subName),
						clrDim.Sprint(pad("", hashW)),
						clrNew.Sprint("NEW  → sysfig track "+filepath.Dir(r.SystemPath)))
					continue
				}
				label := statusLabel(r.Status)
				coloredLabel := statusColored(r.Status, label)
				fmt.Printf("  └ %s  %s  %s\n",
					clrDirty.Sprint(subName),
					clrDim.Sprint(pad(r.ID, hashW)),
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
	if n := totals[string(core.StatusNew)]; n > 0 {
		summaryParts = append(summaryParts, clrNew.Sprintf("%d new", n))
	}
	fmt.Printf("  %s\n", strings.Join(summaryParts, clrDim.Sprint("  ·  ")))
	return hasDiff
}

// printStatusFlat renders every tracked file as its own row.
func printStatusFlat(results []core.FileStatusResult, showIDs bool) (hasDiff bool) {
	pathW := len("PATH")
	stW := len("STATUS")
	slugW := 0
	if showIDs {
		slugW = len("SLUG")
		for _, r := range results {
			if len(r.Slug) > slugW {
				slugW = len(r.Slug)
			}
		}
		slugW += 2
	}
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

	const hashW = 10 // "HASH" + 2 padding
	if showIDs {
		fmt.Printf("%s  %s  %s  %s\n", clrBold.Sprint(pad("PATH", pathW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint(pad("SLUG", slugW)), clrBold.Sprint("STATUS"))
	} else {
		fmt.Printf("%s  %s  %s\n", clrBold.Sprint(pad("PATH", pathW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint("STATUS"))
	}
	divider()

	totals := map[string]int{}
	for _, r := range results {
		label := statusLabel(r.Status)
		switch r.Status {
		case core.StatusDirty, core.StatusPending, core.StatusMissing:
			hasDiff = true
		}
		totals[string(r.Status)]++
		if showIDs {
			fmt.Printf("%s  %s  %s  %s\n", pad(r.SystemPath, pathW), clrDim.Sprint(pad(r.ID, hashW)), clrDim.Sprint(pad(r.Slug, slugW)), statusColored(r.Status, pad(label, stW)))
		} else {
			fmt.Printf("%s  %s  %s\n", pad(r.SystemPath, pathW), clrDim.Sprint(pad(r.ID, hashW)), statusColored(r.Status, pad(label, stW)))
		}

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
		showIDs   bool
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
				dirty = printStatusFlat(results, showIDs)
			} else {
				dirty = printStatusTable(results, showIDs)
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
	f.BoolVarP(&showIDs, "show-ids", "i", false, "show tracking ID column")
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
			printStatusTable(results, false)
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
		baseDir      string
		sysRoot      string
		colorFlag    bool
		colorSet     bool
		ids          []string
		sideBySide   bool
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
			termW := termWidth()
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
				if sideBySide {
					fmt.Print(renderSideBySide(r.Diff, termW))
				} else if useColor {
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
	f.BoolVarP(&sideBySide, "side-by-side", "y", false, "show diff in side-by-side view")
	f.StringArrayVar(&ids, "id", nil, "diff only this ID (repeatable)")
	return cmd
}

// termWidth returns the current terminal column width, defaulting to 160.
func termWidth() int {
	// Try to read the terminal size via ioctl.
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 160
}

// renderSideBySide formats a unified diff as a side-by-side view with line
// numbers, red-highlighted removed lines on the left and blue-highlighted
// added lines on the right.
func renderSideBySide(diff string, totalWidth int) string {
	const (
		lineNoW  = 4  // digits for line number
		gutterW  = 2  // " │" separator
		padBetween = 2 // gap between left and right panels
	)

	// ANSI codes (no fatih/color — direct codes keep it simple here).
	const (
		reset    = "\033[0m"
		bold     = "\033[1m"
		dim      = "\033[2m"
		red      = "\033[38;2;255;255;255m\033[48;2;139;0;0m"   // white text, dark red bg
		green    = "\033[38;2;255;255;255m\033[48;2;0;100;0m"   // white text, dark green bg
		dimLine  = "\033[2m"
	)

	// Each panel gets half the width minus line-number and gutter columns.
	panelW := (totalWidth-padBetween)/2 - lineNoW - gutterW
	if panelW < 20 {
		panelW = 20
	}

	// truncate or pad a string to exactly w visible chars (no ANSI).
	fit := func(s string, w int) string {
		if len(s) >= w {
			return s[:w]
		}
		return s + strings.Repeat(" ", w-len(s))
	}

	// Parse the unified diff into side-by-side rows.
	type row struct {
		leftNo  int    // 0 = empty
		leftTxt string
		leftChg bool   // removed line
		rightNo int
		rightTxt string
		rightChg bool  // added line
		header  string // non-empty for @@ lines
	}

	var rows []row
	leftLine, rightLine := 0, 0

	// Pre-parse: group each hunk's - and + lines, pair them up.
	lines := strings.Split(diff, "\n")
	i := 0
	for i < len(lines) {
		l := lines[i]
		if strings.HasPrefix(l, "@@") {
			// Parse line numbers from @@ -a,b +c,d @@
			var la, lc int
			fmt.Sscanf(l, "@@ -%d", &la)
			fmt.Sscanf(l[strings.Index(l, "+"):], "+%d", &lc)
			leftLine = la
			rightLine = lc
			rows = append(rows, row{header: l})
			i++
			continue
		}
		if strings.HasPrefix(l, "---") || strings.HasPrefix(l, "+++") {
			i++
			continue
		}
		if strings.HasPrefix(l, " ") || l == "" {
			txt := ""
			if len(l) > 0 {
				txt = l[1:]
			}
			rows = append(rows, row{
				leftNo: leftLine, leftTxt: txt,
				rightNo: rightLine, rightTxt: txt,
			})
			leftLine++
			rightLine++
			i++
			continue
		}
		// Collect a block of - and + lines together, pair them.
		var removed, added []string
		for i < len(lines) && strings.HasPrefix(lines[i], "-") {
			removed = append(removed, lines[i][1:])
			i++
		}
		for i < len(lines) && strings.HasPrefix(lines[i], "+") {
			added = append(added, lines[i][1:])
			i++
		}
		maxLen := len(removed)
		if len(added) > maxLen {
			maxLen = len(added)
		}
		// Unrecognized line (e.g. "diff --git", "index ...") — skip it.
		if maxLen == 0 {
			i++
			continue
		}
		for j := 0; j < maxLen; j++ {
			r := row{}
			if j < len(removed) {
				r.leftNo = leftLine
				r.leftTxt = removed[j]
				r.leftChg = true
				leftLine++
			}
			if j < len(added) {
				r.rightNo = rightLine
				r.rightTxt = added[j]
				r.rightChg = true
				rightLine++
			}
			rows = append(rows, r)
		}
	}

	// Render rows.
	var out strings.Builder
	sep := dim + " │ " + reset
	for _, r := range rows {
		if r.header != "" {
			hdr := fit(r.header, totalWidth-2)
			out.WriteString(bold + dim + hdr + reset + "\n")
			continue
		}

		// Left panel.
		var leftNum, rightNum string
		if r.leftNo > 0 {
			leftNum = fmt.Sprintf("%*d", lineNoW, r.leftNo)
		} else {
			leftNum = strings.Repeat(" ", lineNoW)
		}
		if r.rightNo > 0 {
			rightNum = fmt.Sprintf("%*d", lineNoW, r.rightNo)
		} else {
			rightNum = strings.Repeat(" ", lineNoW)
		}

		leftTxt := r.leftTxt
		rightTxt := r.rightTxt
		if r.leftChg && r.rightChg {
			leftTxt, rightTxt = inlineHighlight(leftTxt, rightTxt)
		}
		leftContent := fit(leftTxt, panelW)
		rightContent := fit(rightTxt, panelW)

		var leftFmt, rightFmt string
		if r.leftChg {
			leftFmt = red + leftNum + " " + leftContent + reset
		} else {
			leftFmt = dimLine + leftNum + reset + " " + leftContent
		}
		if r.rightChg {
			rightFmt = green + rightNum + " " + rightContent + reset
		} else {
			rightFmt = dimLine + rightNum + reset + " " + rightContent
		}

		out.WriteString(leftFmt + sep + rightFmt + "\n")
	}

	return out.String()
}

// wordTokens splits s into a slice of word/non-word tokens for word-level diff.
func wordTokens(s string) []string {
	var tokens []string
	start := -1
	for i, c := range s {
		isWord := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_'
		if isWord {
			if start < 0 {
				start = i
			}
		} else {
			if start >= 0 {
				tokens = append(tokens, s[start:i])
				start = -1
			}
			tokens = append(tokens, string(c))
		}
	}
	if start >= 0 {
		tokens = append(tokens, s[start:])
	}
	return tokens
}

// inlineHighlight computes word-level differences between oldLine and newLine
// and returns both lines with changed tokens highlighted using ANSI bg colors.
func inlineHighlight(oldLine, newLine string) (oldHL, newHL string) {
	const (
		hlRed   = "\033[48;2;139;0;0m\033[38;2;255;255;255m"   // dark red bg
		hlGreen = "\033[48;2;0;100;0m\033[38;2;255;255;255m"   // dark green bg
		hlReset = "\033[0m"
	)

	a := wordTokens(oldLine)
	b := wordTokens(newLine)

	// LCS DP table.
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] > dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	// Trace back to build highlighted strings.
	var leftB, rightB strings.Builder
	i, j := 0, 0
	for i < m || j < n {
		if i < m && j < n && a[i] == b[j] {
			leftB.WriteString(a[i])
			rightB.WriteString(b[j])
			i++
			j++
		} else if j < n && (i >= m || dp[i][j+1] >= dp[i+1][j]) {
			rightB.WriteString(hlGreen + b[j] + hlReset)
			j++
		} else {
			leftB.WriteString(hlRed + a[i] + hlReset)
			i++
		}
	}
	return leftB.String(), rightB.String()
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
		dim   = "\033[2m"
		reset = "\033[0m"
	)

	lines := strings.SplitAfter(diff, "\n")

	var out bytes.Buffer
	oldLine, newLine := 0, 0

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		isRemoved := len(line) > 0 && line[0] == '-' && (len(line) < 4 || line[:3] != "---")
		isAdded := len(line) > 0 && line[0] == '+' && (len(line) < 4 || line[:3] != "+++")
		isHunk := len(line) > 2 && line[:2] == "@@"
		isHeader := (len(line) >= 3 && line[:3] == "---") || (len(line) >= 3 && line[:3] == "+++")

		if isHunk {
			// Parse @@ -oldStart,.. +newStart,.. @@ to reset line counters.
			var o, n int
			fmt.Sscanf(line, "@@ -%d", &o)
			if idx := strings.Index(line, " +"); idx >= 0 {
				fmt.Sscanf(line[idx+1:], "+%d", &n)
			}
			oldLine, newLine = o, n
			out.WriteString(cyan + line + reset)
			continue
		}

		if isHeader {
			out.WriteString(dim + line + reset)
			continue
		}

		if isRemoved {
			numStr := fmt.Sprintf("%s%4d%s ", dim, oldLine, reset)
			oldLine++
			// Look ahead for inline highlight.
			if i+1 < len(lines) {
				next := lines[i+1]
				nextIsAdded := len(next) > 0 && next[0] == '+' && (len(next) < 4 || next[:3] != "+++")
				if nextIsAdded {
					oldTxt := strings.TrimRight(line[1:], "\n")
					newTxt := strings.TrimRight(next[1:], "\n")
					oldHL, newHL := inlineHighlight(oldTxt, newTxt)
					newNumStr := fmt.Sprintf("%s%4d%s ", dim, newLine, reset)
					newLine++
					out.WriteString(red + "-" + numStr + oldHL + reset + "\n")
					out.WriteString(green + "+" + newNumStr + newHL + reset + "\n")
					i++
					continue
				}
			}
			out.WriteString(red + "-" + numStr + strings.TrimRight(line[1:], "\n") + reset + "\n")
		} else if isAdded {
			numStr := fmt.Sprintf("%s%4d%s ", dim, newLine, reset)
			newLine++
			out.WriteString(green + "+" + numStr + strings.TrimRight(line[1:], "\n") + reset + "\n")
		} else if line != "" && line != "\n" {
			// Context line — show both line numbers.
			numStr := fmt.Sprintf("%s%4d%s ", dim, oldLine, reset)
			oldLine++
			newLine++
			out.WriteString(numStr + line)
		} else {
			out.WriteString(line)
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

// ── log ───────────────────────────────────────────────────────────────────────

func newLogCmd() *cobra.Command {
	var (
		baseDir     string
		filePath    string
		id          string
		n           int
		noRollbacks bool
		graphMode   bool
	)

	cmd := &cobra.Command{
		Use:   "log [system-path]",
		Short: "Show commit history with changed paths",
		Long: `Show git commit history. Each commit is expanded to one line per top-level
directory that changed, so you can see at a glance what was touched.

Filter to a specific path or ID:
  sysfig log /etc/pacman.d
  sysfig log --id 7734be1e`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			repoDir := filepath.Join(baseDir, "repo.git")

			// Resolve positional system path or --id to a repo-relative filter path.
			var filterPath string // if set, only show commits touching this path
			if len(args) == 1 {
				sysArg := strings.TrimSuffix(args[0], "/")
				// Convert absolute system path to repo-relative path by stripping
				// the leading "/". Works for both files and directories deterministically.
				filterPath = strings.TrimPrefix(sysArg, "/")
			}
			if id != "" && filterPath == "" {
				sm := state.NewManager(filepath.Join(baseDir, "state.json"))
				s, err := sm.Load()
				if err == nil {
					for _, rec := range s.Files {
						if core.DeriveID(rec.SystemPath) == id {
							filterPath = rec.RepoPath
							break
						}
					}
				}
				if filterPath == "" {
					return fmt.Errorf("no tracked file found for id %q", id)
				}
			}
			if filePath != "" {
				filterPath = filePath
			}

			// Step 1: get list of commits.
			// When filtering by path/id, use that file's dedicated branch for
			// clean per-file history. Otherwise show all track/* branches.
			listArgs := []string{
				"--no-pager", "--git-dir=" + repoDir,
				"log", "--pretty=format:%h\t%ad\t%s\t%D",
				"--date=format:%Y-%m-%d %H:%M",
			}
			// Note: we build our own lane graph — do NOT pass --graph to git.
			if filterPath != "" {
				// Find branch(es) for this path from state.
				var trackBranches []string
				seenBranch := map[string]bool{}
				sm2 := state.NewManager(filepath.Join(baseDir, "state.json"))
				if s2, err2 := sm2.Load(); err2 == nil {
					for _, rec := range s2.Files {
						sp := filepath.Clean(rec.SystemPath)
						rp := rec.RepoPath
						if sp == filepath.Clean("/"+filterPath) || rp == filterPath ||
							strings.HasPrefix(rp, strings.TrimSuffix(filterPath, "/")+"/") {
							if rec.Branch != "" && !seenBranch[rec.Branch] {
								trackBranches = append(trackBranches, rec.Branch)
								seenBranch[rec.Branch] = true
							}
						}
					}
				}
				if len(trackBranches) > 0 {
					listArgs = append(listArgs, trackBranches...)
				} else {
					listArgs = append(listArgs, "--all", "--", filterPath)
				}
			} else {
				// Show all track/* branches merged into one timeline.
				listArgs = append(listArgs, "--all")
			}
			if n > 0 {
				listArgs = append(listArgs, fmt.Sprintf("-n%d", n))
			}
			out, err := exec.Command("git", listArgs...).Output()
			if err != nil {
				os.Exit(1)
			}

			// ANSI helpers (no fatih/color — keep it inline).
			const (
				yellow  = "\033[33m"
				cyan    = "\033[36m"
				magenta = "\033[35m"
				green   = "\033[32m"
				reset   = "\033[0m"
				dim     = "\033[2m"
			)

			// Lane colors cycle through distinct ANSI colors.
			laneColors := []string{
				"\033[32m", "\033[33m", "\033[36m", "\033[35m",
				"\033[34m", "\033[31m", "\033[37m",
			}

			// Pre-build hash→branch map by walking every track/* and manifest branch.
			// This lets us assign a lane to every commit, not just branch tips.
			hashToBranch := map[string]string{}
			if graphMode {
				branchListOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"for-each-ref", "--format=%(refname:short)", "refs/heads/").Output()
				for _, b := range strings.Split(strings.TrimSpace(string(branchListOut)), "\n") {
					b = strings.TrimSpace(b)
					if !strings.HasPrefix(b, "track/") && b != "manifest" {
						continue
					}
					commitListOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
						"log", b, "--format=%H").Output()
					for _, h := range strings.Split(strings.TrimSpace(string(commitListOut)), "\n") {
						h = strings.TrimSpace(h)
						if h != "" {
							if hashToBranch[h] == "" { hashToBranch[h] = b }
							if len(h) >= 7 && hashToBranch[h[:7]] == "" { hashToBranch[h[:7]] = b }
						}
					}
				}
			}

			type logRow struct {
				hash       string
				date       string
				pathLabel  string
				subject    string
				statLabel  string
				decoration string
				branchName string // for lane graph
				connector  string // non-commit graph line (e.g. "|", "|/")
			}

			lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			rows := make([]logRow, 0, len(lines))
			maxPathLen := 0

			for _, line := range lines {
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, "\t", 4)
				// Graph-only connector lines (e.g. "|", "|/", "|\") have no tabs.
				if len(parts) < 3 {
					if graphMode {
						rows = append(rows, logRow{connector: line})
					}
					continue
				}

				hash := strings.TrimSpace(parts[0])

				date, subject := parts[1], parts[2]
				if noRollbacks && strings.HasPrefix(subject, "sysfig: rollback ") {
					continue
				}
				decoration := ""
				if len(parts) == 4 && parts[3] != "" {
					decoration = parts[3]
				}

				// Get files changed in this commit.
				dtOut, _ := exec.Command("git",
					"--no-pager", "--git-dir="+repoDir,
					"diff-tree", "--no-commit-id", "-r", "--name-only", hash,
				).Output()
				if len(strings.TrimSpace(string(dtOut))) == 0 {
					dtOut, _ = exec.Command("git",
						"--no-pager", "--git-dir="+repoDir,
						"ls-tree", "-r", "--name-only", hash,
					).Output()
				}

				var paths []string
				for _, f := range strings.Split(strings.TrimSpace(string(dtOut)), "\n") {
					if f == "" || f == "sysfig.yaml" {
						continue
					}
					if filterPath != "" && f != filterPath &&
						!strings.HasPrefix(f, strings.TrimSuffix(filterPath, "/")+"/") {
						continue
					}
					paths = append(paths, f)
				}

				pathLabel := ""
				if len(paths) == 1 {
					pathLabel = paths[0]
				} else if len(paths) > 1 {
					pathLabel = fmt.Sprintf("%s +%d", paths[0], len(paths)-1)
				}
				if len(pathLabel) > maxPathLen {
					maxPathLen = len(pathLabel)
				}

				// Diff stat.
				statLabel := ""
				nsOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"diff-tree", "--numstat", "--no-commit-id", hash).Output()
				if len(strings.TrimSpace(string(nsOut))) == 0 {
					nsOut, _ = exec.Command("git", "--no-pager", "--git-dir="+repoDir,
						"show", "--numstat", "--format=", hash).Output()
				}
				for _, sl := range strings.Split(strings.TrimSpace(string(nsOut)), "\n") {
					fields := strings.Fields(sl)
					if len(fields) >= 2 && (fields[0] != "0" || fields[1] != "0") {
						statLabel = dim + "[+" + fields[0] + "/-" + fields[1] + "]" + reset
						break
					}
				}

				rows = append(rows, logRow{
					hash:       hash,
					date:       date,
					pathLabel:  pathLabel,
					subject:    subject,
					statLabel:  statLabel,
					decoration: decoration,
					branchName: hashToBranch[hash],
				})
			}

			// Second pass: render with aligned path column + optional lane graph.
			// Lane graph state: lanes[i] holds the branch name active in column i.
			lanes := []string{}
			laneColor := map[string]string{}
			laneIdx := map[string]int{}
			// Pre-compute last occurrence index per branch for lane closing.
			lastOcc := map[string]int{}
			for i, r := range rows {
				if r.branchName != "" {
					lastOcc[r.branchName] = i
				}
			}
			getLane := func(branch string) int {
				if idx, ok := laneIdx[branch]; ok {
					return idx
				}
				for i, l := range lanes {
					if l == "" {
						lanes[i] = branch
						laneIdx[branch] = i
						return i
					}
				}
				i := len(lanes)
				lanes = append(lanes, branch)
				laneIdx[branch] = i
				laneColor[branch] = laneColors[i%len(laneColors)]
				return i
			}
			closeLane := func(branch string) {
				if idx, ok := laneIdx[branch]; ok {
					lanes[idx] = ""
					delete(laneIdx, branch)
				}
			}
			graphPfx := func(active int) string {
				// find rightmost active column
				width := active + 1
				for i := len(lanes) - 1; i > active; i-- {
					if lanes[i] != "" {
						width = i + 1
						break
					}
				}
				var sb strings.Builder
				for i := 0; i < width; i++ {
					if i == active {
						sb.WriteString(laneColor[lanes[i]] + "* " + reset)
					} else if lanes[i] != "" {
						sb.WriteString(laneColor[lanes[i]] + "| " + reset)
					} else {
						sb.WriteString("  ")
					}
				}
				return sb.String()
			}
			for i, r := range rows {
				if r.connector != "" {
					fmt.Println(r.connector)
					continue
				}
				paddedPath := r.pathLabel
				if maxPathLen > 0 {
					paddedPath = r.pathLabel + strings.Repeat(" ", maxPathLen-len(r.pathLabel))
				}
				decStr := ""
				if r.decoration != "" {
					decStr = "  " + green + "(" + r.decoration + ")" + reset
				}
				prefix := "* "
				if graphMode && r.branchName != "" {
					activeLane := getLane(r.branchName)
					prefix = graphPfx(activeLane)
					if lastOcc[r.branchName] == i {
						closeLane(r.branchName)
					}
				}
				if r.pathLabel == "" {
					fmt.Printf("%s%s%s%s %s%s%s  %s %s%s\n",
						prefix,
						yellow, r.hash, reset,
						cyan, r.date, reset,
						r.subject, r.statLabel, decStr)
				} else {
					fmt.Printf("%s%s%s%s %s%s%s  %s%s%s  %s %s%s\n",
						prefix,
						yellow, r.hash, reset,
						cyan, r.date, reset,
						magenta, paddedPath, reset,
						r.subject, r.statLabel, decStr)
				}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&filePath, "path", "", "filter by repo-relative path")
	f.StringVar(&id, "id", "", "filter by tracking ID")
	f.IntVarP(&n, "number", "n", 0, "limit to last N commits (0 = unlimited)")
	f.BoolVar(&noRollbacks, "no-rollbacks", false, "hide rollback commits from output")
	f.BoolVarP(&graphMode, "graph", "g", false, "show branch/merge graph (like git log --graph)")
	return cmd
}

// ── show ──────────────────────────────────────────────────────────────────────

func newShowCmd() *cobra.Command {
	var (
		baseDir    string
		sideBySide bool
	)
	cmd := &cobra.Command{
		Use:   "show <commit>",
		Short: "Show what changed in a commit, diff-style",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			repoDir := filepath.Join(baseDir, "repo.git")
			hash := args[0]

			out, err := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
				"show", "--format=", "-p", hash).Output()
			if err != nil {
				return fmt.Errorf("commit %q not found", hash)
			}
			diff := strings.TrimPrefix(string(out), "\n")
			if diff == "" {
				fmt.Println("(no changes)")
				return nil
			}

			const maxLines = 500
			if strings.Count(diff, "\n") > maxLines {
				warn("diff too large (%d lines) — showing stat only", strings.Count(diff, "\n"))
				stat, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"show", "--format=", "--stat", hash).Output()
				fmt.Print(strings.TrimPrefix(string(stat), "\n"))
				return nil
			}

			if sideBySide {
				fmt.Print(renderSideBySide(diff, termWidth()))
			} else {
				fmt.Print(colorize(diff))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&sideBySide, "side-by-side", "y", false, "Side-by-side view")
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig base directory")
	return cmd
}


// ── undo ──────────────────────────────────────────────────────────────────────

func newUndoCmd() *cobra.Command {
	var (
		baseDir string
		all     bool
		force   bool
		dryRun  bool
	)

	isHash := func(s string) bool {
		if len(s) < 7 || len(s) > 40 {
			return false
		}
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}

	cmd := &cobra.Command{
		Use:   "undo [<commit>] <path>",
		Short: "Undo changes to a tracked file",
		Long: `Undo changes to a tracked file — two modes:

  sysfig undo <path>           discard unsaved edits, restore from last sync
  sysfig undo <commit> <path>  rewind the file's history to a specific commit

The first form is non-destructive (no history change).
The second form resets the file's track branch — commits after <commit> are lost.

Examples:
  sysfig undo /home/aye7/.zshrc             # discard uncommitted edits
  sysfig undo 2b8e60e /home/aye7/.zshrc     # rewind to that commit
  sysfig undo 9b43458 --all                 # rewind all tracked files`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing argument — provide a path or a commit hash\n\n%s", cmd.UsageString())
			}
			if len(args) > 2 {
				return fmt.Errorf("too many arguments (expected 1 or 2, got %d)", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			repoDir := filepath.Join(baseDir, "repo.git")

			sm := state.NewManager(filepath.Join(baseDir, "state.json"))
			s, err := sm.Load()
			if err != nil {
				return fmt.Errorf("undo: load state: %w", err)
			}

			// Disambiguate: undo <path|id>  vs  undo <commit> <path|id>  vs  undo <commit> --all
			//
			// A single hex-looking arg could be either a commit hash or a tracking ID.
			// We check the state for an ID match first — if found, treat it as an ID
			// (non-destructive restore). Only fall back to commit-hash semantics when
			// no file in state carries that ID.
			idByValue := func(id string) bool {
				for _, rec := range s.Files {
					if rec.ID == id {
						return true
					}
				}
				return false
			}

			var commitHash, pathArg, idArg string
			if len(args) == 2 {
				commitHash = args[0]
				second := args[1]
				if filepath.IsAbs(second) {
					pathArg = filepath.Clean(second)
				} else {
					idArg = second
				}
			} else if isHash(args[0]) && !idByValue(args[0]) {
				// Hex-looking arg that is NOT a known tracking ID → commit hash.
				commitHash = args[0]
				if !all {
					return fmt.Errorf("provide a path or ID after the commit hash, or use --all\n\nexample: sysfig undo %s <path|id>", args[0])
				}
			} else if filepath.IsAbs(args[0]) {
				pathArg = filepath.Clean(args[0])
			} else {
				// Non-absolute arg (tracking ID or relative path treated as ID).
				idArg = args[0]
			}

			type target struct{ repoPath, systemPath, branch string }
			var targets []target
			for _, rec := range s.Files {
				sp := filepath.Clean(rec.SystemPath)
				if idArg != "" {
					if rec.ID != idArg {
						continue
					}
				} else if pathArg != "" {
					if sp != pathArg && !strings.HasPrefix(sp, pathArg+string(filepath.Separator)) {
						continue
					}
				}
				branch := rec.Branch
				if branch == "" {
					branch = "track/" + core.SanitizeBranchName(rec.RepoPath)
				}
				targets = append(targets, target{rec.RepoPath, rec.SystemPath, branch})
			}

			if len(targets) == 0 {
				ref := pathArg
				if idArg != "" {
					ref = idArg
				}
				return fmt.Errorf("no tracked files found matching %q", ref)
			}

			// ── Mode 1: restore from last sync (no commit hash) ──────────────
			if commitHash == "" {
				for _, t := range targets {
					if dryRun {
						fmt.Printf("  %s  would restore %s from last sync\n", clrDim.Sprint("[dry-run]"), t.systemPath)
						continue
					}
					content, err := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
						"show", t.branch+":"+t.repoPath).Output()
					if err != nil {
						fail("%s not in repo yet — never synced?", t.repoPath)
						continue
					}
					var perm os.FileMode = 0o644
					if fi, err := os.Stat(t.systemPath); err == nil {
						perm = fi.Mode().Perm()
					}
					if err := os.MkdirAll(filepath.Dir(t.systemPath), 0o755); err != nil {
						return err
					}
					if err := os.WriteFile(t.systemPath, content, perm); err != nil {
						fail("write %s: %v", t.systemPath, err)
						continue
					}
					if sysHash, err := hash.File(t.systemPath); err == nil {
						_ = sm.WithLock(func(st *types.State) error {
							if r, ok := st.Files[core.DeriveID(t.systemPath)]; ok {
								r.CurrentHash = sysHash
							}
							return nil
						})
					}
					ok("Restored: %s", clrBold.Sprint(t.systemPath))
				}
				return nil
			}

			// ── Mode 2: rewind track branch to commit ────────────────────────
			fullHash, err := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
				"rev-parse", "--verify", commitHash).Output()
			if err != nil {
				return fmt.Errorf("commit %q not found in repo", commitHash)
			}
			fullCommit := strings.TrimSpace(string(fullHash))

			if !force {
				clrWarn.Printf("  ⚠  This will reset %d file(s) to %s and discard later commits on their branches.\n", len(targets), commitHash[:7])
				fmt.Print("  Continue? [y/N] ")
				var answer string
				fmt.Scanln(&answer)
				if strings.ToLower(strings.TrimSpace(answer)) != "y" {
					info("Aborted.")
					return nil
				}
			}

			for _, t := range targets {
				if dryRun {
					fmt.Printf("  %s  would reset %s → %s\n", clrDim.Sprint("[dry-run]"), t.systemPath, commitHash[:7])
					continue
				}
				ref := "refs/heads/" + t.branch
				if out, err := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"update-ref", ref, fullCommit).CombinedOutput(); err != nil {
					fail("git update-ref %s: %s", t.branch, strings.TrimSpace(string(out)))
					continue
				}
				content, err := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"show", fullCommit+":"+t.repoPath).Output()
				if err != nil {
					fail("commit %s does not contain %s — skipping", commitHash[:7], t.repoPath)
					continue
				}
				var perm os.FileMode = 0o644
				if fi, err := os.Stat(t.systemPath); err == nil {
					perm = fi.Mode().Perm()
				}
				if err := os.MkdirAll(filepath.Dir(t.systemPath), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(t.systemPath, content, perm); err != nil {
					fail("write %s: %v", t.systemPath, err)
					continue
				}
				if sysHash, err := hash.File(t.systemPath); err == nil {
					_ = sm.WithLock(func(st *types.State) error {
						if r, ok := st.Files[core.DeriveID(t.systemPath)]; ok {
							r.CurrentHash = sysHash
						}
						return nil
					})
				}
				ok("Rewound: %s", clrBold.Sprint(t.systemPath))
				fmt.Printf("     %s %s\n", clrDim.Sprint("branch reset to:"), clrInfo.Sprint(commitHash[:7]))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.BoolVar(&all, "all", false, "apply to all tracked files (use with a commit hash)")
	f.BoolVar(&force, "force", false, "skip confirmation prompt")
	f.BoolVar(&dryRun, "dry-run", false, "show what would happen without making changes")
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
func watchRun(baseDir, sysRoot string, debounce time.Duration, dryRun, push bool) error {
	clrBold.Println("Watching tracked files for changes  (Ctrl-C to stop)")
	fmt.Printf("  %s %s\n", clrDim.Sprint("base-dir:"), clrDim.Sprint(baseDir))
	fmt.Printf("  %s %v\n", clrDim.Sprint("debounce:"), debounce)
	if push {
		fmt.Printf("  %s %s\n", clrDim.Sprint("push:"), clrOK.Sprint("enabled"))
	}
	divider()

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
			fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrErr.Sprint("error"), err)
			return
		}
		if result == nil || !result.Committed {
			return
		}
		fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrWarn.Sprint("changed"), path)
		if len(result.CommittedFiles) > 0 {
			for _, f := range result.CommittedFiles {
				fmt.Printf("            %s %s\n", clrOK.Sprint("committed"), clrDim.Sprint(f))
			}
		}
		if result.Message != "" {
			fmt.Printf("            %s\n", clrDim.Sprint(result.Message))
		}
	}

	return core.Watch(core.WatchOptions{
		BaseDir:  baseDir,
		SysRoot:  resolveSysRoot(sysRoot),
		Debounce: debounce,
		DryRun:   dryRun,
		Push:     push,
		OnEvent:  onEvent,
	}, stop)
}

func newWatchCmd() *cobra.Command {
	var (
		baseDir  string
		sysRoot  string
		debounce time.Duration
		dryRun   bool
		push     bool
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
			return watchRun(baseDir, sysRoot, debounce, dryRun, push)
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "wait this long after last change before syncing")
	f.BoolVar(&dryRun, "dry-run", false, "print detected changes without syncing")
	f.BoolVar(&push, "push", false, "push to remote after each successful sync")

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
		push     bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the file watcher in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			return watchRun(baseDir, sysRoot, debounce, dryRun, push)
		},
	}
	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "wait this long after last change before syncing")
	f.BoolVar(&dryRun, "dry-run", false, "print detected changes without syncing")
	f.BoolVar(&push, "push", false, "push to remote after each successful sync")
	return cmd
}

// ── watch install ─────────────────────────────────────────────────────────────

const watchServiceTemplate = `[Unit]
Description=sysfig config watcher — auto-sync tracked files
Documentation=https://github.com/aissat/sysfig
After=default.target

[Service]
Type=simple
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=%s watch --base-dir %s --debounce %s%s
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
		push     bool
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
			extraFlags := ""
			if push {
				extraFlags = " --push"
			}
			content := fmt.Sprintf(watchServiceTemplate, binPath, baseDir, debounce, extraFlags)

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
	f.BoolVar(&push, "push", false, "push to remote after each successful sync")
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

// ── source command ────────────────────────────────────────────────────────────

func newSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Manage Config Source template catalogs",
		Long: `Config Sources let you consume shared config templates from a remote bundle,
inject per-machine variables, and commit the rendered output as ordinary tracked files.

Workflow:
  sysfig source add corporate bundle+local:///mnt/nfs/corp-configs.bundle
  sysfig source list corporate
  sysfig source use corporate/system-proxy
  sysfig source render
  sysfig diff && sysfig apply`,
	}
	cmd.AddCommand(
		newSourceAddCmd(),
		newSourceListCmd(),
		newSourceUseCmd(),
		newSourceRenderCmd(),
		newSourcePullCmd(),
	)
	return cmd
}

func newSourceAddCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Register a new config source bundle",
		Args:  cobra.ExactArgs(2),
		Example: `  sysfig source add corporate bundle+local:///mnt/corp-nfs/corp-configs.bundle
  sysfig source add community bundle+ssh://backup@fileserver/srv/sysfig/community.bundle`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			name, sourceURL := args[0], args[1]
			if err := core.SourceAdd(baseDir, name, sourceURL); err != nil {
				return err
			}
			ok("Source %q registered", name)
			info("Run 'sysfig source list %s' to see available profiles.", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newSourceListCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "list <source>",
		Short: "List profiles available in a source bundle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			sourceName := args[0]
			entries, err := core.SourceList(baseDir, sourceName)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				info("No profiles found in source %q", sourceName)
				return nil
			}
			divider()
			fmt.Printf("  %-28s  %-5s  %s\n",
				clrBold.Sprint("PROFILE"), clrBold.Sprint("FILES"), clrBold.Sprint("DESCRIPTION"))
			divider()
			for _, e := range entries {
				fmt.Printf("  %-28s  %-5d  %s\n", e.Name, e.Files, e.Description)
			}
			divider()
			fmt.Printf("\n  To activate a profile: sysfig source use %s/<profile>\n\n", sourceName)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newSourceUseCmd() *cobra.Command {
	var baseDir string
	var varFlags []string
	var valuesFile string
	cmd := &cobra.Command{
		Use:   "use <source/profile>",
		Short: "Activate a profile with per-machine variables",
		Long: `Activate a profile from a registered source.

Supply variables with --values values.yaml (a flat YAML map of key: value
pairs) and/or --var key=value flags. --var takes precedence over --values.
Any variable not supplied is prompted interactively when stdin is a TTY, or
read line-by-line in alphabetical order when piped.

After activation, run 'sysfig source render' to commit the rendered files.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			sourceProfile := args[0]

			parts := strings.SplitN(sourceProfile, "/", 2)

			if len(parts) != 2 {
				return fmt.Errorf("invalid source reference %q — expected <source>/<profile>", sourceProfile)
			}
			sourceName, profileName := parts[0], parts[1]

			// Ensure source is pulled so we can read profile.yaml.
			if err := core.SourcePull(baseDir, sourceName); err != nil {
				return fmt.Errorf("pull source %q: %w", sourceName, err)
			}

			profile, err := core.ReadProfileYAML(baseDir, sourceName, profileName)
			if err != nil {
				return err
			}

			if valuesFile != "" && len(varFlags) > 0 {
				return fmt.Errorf("--values and --var are mutually exclusive — use one or the other")
			}

			// Seed variables from --values file or --var flags.
			variables := make(map[string]string, len(profile.Variables))
			if valuesFile != "" {
				data, err := os.ReadFile(valuesFile)
				if err != nil {
					return fmt.Errorf("--values: %w", err)
				}
				var fileVars map[string]string
				if err := yaml.Unmarshal(data, &fileVars); err != nil {
					return fmt.Errorf("--values %q: %w", valuesFile, err)
				}
				for k, v := range fileVars {
					variables[k] = v
				}
			}

			// --var flags override values from file.
			for _, kv := range varFlags {
				idx := strings.IndexByte(kv, '=')
				if idx < 1 {
					return fmt.Errorf("--var %q: expected key=value", kv)
				}
				variables[kv[:idx]] = kv[idx+1:]
			}

			// Collect variables still needed (not provided via --var).
			varNames := make([]string, 0, len(profile.Variables))
			for k := range profile.Variables {
				varNames = append(varNames, k)
			}
			sort.Strings(varNames)

			// Build the list of variables that still need a value.
			var needPrompt []string
			for _, varName := range varNames {
				if _, provided := variables[varName]; !provided {
					needPrompt = append(needPrompt, varName)
				}
			}

			// Prompt / read from stdin only for variables not already set.
			if len(needPrompt) > 0 {
				scanner := bufio.NewScanner(os.Stdin)
				isTTY := term.IsTerminal(int(os.Stdin.Fd()))

				for _, varName := range needPrompt {
					decl := profile.Variables[varName]
					prompt := "  " + varName
					if decl.Required {
						prompt += " (required)"
					}
					if decl.Default != "" {
						prompt += " [" + decl.Default + "]"
					}
					prompt += ": "

					if isTTY {
						fmt.Print(prompt)
					}
					if scanner.Scan() {
						val := strings.TrimSpace(scanner.Text())
						if val == "" && decl.Default != "" {
							val = decl.Default
						}
						if val == "" && decl.Required {
							return fmt.Errorf("variable %q is required", varName)
						}
						if val != "" {
							variables[varName] = val
						}
					}
				}
			}

			if err := core.SourceUse(core.SourceUseOptions{
				BaseDir:       baseDir,
				SourceProfile: sourceProfile,
				Variables:     variables,
			}); err != nil {
				return err
			}

			ok("Profile %q added to sources.yaml", sourceProfile)
			info("Run 'sysfig source render' to commit the rendered files.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	cmd.Flags().StringArrayVar(&varFlags, "var", nil, "set a variable (key=value, repeatable)")
	cmd.Flags().StringVar(&valuesFile, "values", "", "YAML file of variable values (key: value)")
	return cmd
}

func newSourceRenderCmd() *cobra.Command {
	var baseDir string
	var force, dryRun bool
	var renderProfile string
	var valuesFile string
	var varFlags []string
	cmd := &cobra.Command{
		Use:   "render [--profile <source/profile>] [--values values.yaml] [--var key=value] [--force] [--dry-run]",
		Short: "Render activated profiles into the local repo",
		Long: `Render all activated profiles (or one specific profile) by:
  1. Fetching each source bundle into the local cache
  2. Reading profile.yaml and validating variables
  3. Rendering each template and committing to track/* branches
  4. Updating state.json with source ownership

--values values.yaml sets variables for multiple profiles at once (mutually
exclusive with --var):

  corp/system-proxy:
    proxy_url: http://proxy.corp.com:3128
    bypass_list: 10.0.0.0/8,localhost
  corp/dns-resolvers:
    primary_dns: 10.0.0.53

--var sets variables inline for a single profile (mutually exclusive with
--values). Format: key=value or source/profile:key=value.

After rendering, run 'sysfig diff' and 'sysfig apply' to write files to disk.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)

			if valuesFile != "" && len(varFlags) > 0 {
				return fmt.Errorf("--values and --var are mutually exclusive — use one or the other")
			}

			// Parse --var flags into per-profile variable maps.
			// Format: "corp/system-proxy:proxy_url=value" or "proxy_url=value" (global).
			profileOverrides := map[string]map[string]string{}
			globalOverrides := map[string]string{}
			for _, kv := range varFlags {
				// Check for scoped format: contains "/" before ":"
				colonIdx := strings.IndexByte(kv, ':')
				eqIdx := strings.IndexByte(kv, '=')
				if eqIdx < 1 {
					return fmt.Errorf("--var %q: expected key=value or profile:key=value", kv)
				}
				if colonIdx > 0 && colonIdx < eqIdx {
					// Scoped: "corp/system-proxy:proxy_url=value"
					profile := kv[:colonIdx]
					rest := kv[colonIdx+1:]
					eqIdx2 := strings.IndexByte(rest, '=')
					if eqIdx2 < 1 {
						return fmt.Errorf("--var %q: expected profile:key=value", kv)
					}
					if profileOverrides[profile] == nil {
						profileOverrides[profile] = map[string]string{}
					}
					profileOverrides[profile][rest[:eqIdx2]] = rest[eqIdx2+1:]
				} else {
					// Global: "proxy_url=value"
					globalOverrides[kv[:eqIdx]] = kv[eqIdx+1:]
				}
			}

			// If --values supplied, build per-profile variable maps, then apply overrides.
			if valuesFile != "" {
				data, err := os.ReadFile(valuesFile)
				if err != nil {
					return fmt.Errorf("--values: %w", err)
				}
				var multiVars map[string]map[string]string
				if err := yaml.Unmarshal(data, &multiVars); err != nil {
					return fmt.Errorf("--values %q: %w", valuesFile, err)
				}
				// Merge global and scoped --var overrides into the file values.
				for profile, vars := range multiVars {
					if vars == nil {
						vars = map[string]string{}
						multiVars[profile] = vars
					}
					for k, v := range globalOverrides {
						vars[k] = v
					}
					for k, v := range profileOverrides[profile] {
						vars[k] = v
					}
				}
				// Activate profiles in sorted order for deterministic output.
				profiles := make([]string, 0, len(multiVars))
				for p := range multiVars {
					profiles = append(profiles, p)
				}
				sort.Strings(profiles)
				for _, sourceProfile := range profiles {
					vars := multiVars[sourceProfile]
					if err := core.SourceUse(core.SourceUseOptions{
						BaseDir:       baseDir,
						SourceProfile: sourceProfile,
						Variables:     vars,
					}); err != nil {
						return fmt.Errorf("values: activate %q: %w", sourceProfile, err)
					}
					ok("Activated: %s", sourceProfile)
				}
				if len(profiles) > 0 {
					fmt.Println()
				}
			}

			result, err := core.SourceRender(core.RenderOptions{
				BaseDir: baseDir,
				Profile: renderProfile,
				Force:   force,
				DryRun:  dryRun,
			})

			if result != nil {
				for _, p := range result.Rendered {
					if dryRun {
						info("[dry-run] Would render: %s", p)
					} else {
						ok("Rendered: %s", p)
					}
				}
				for _, p := range result.Skipped {
					clrDim.Printf("  · Unchanged: %s\n", p)
				}
				for _, c := range result.Conflicts {
					fail("Conflict: %s", c.SystemPath)
					fmt.Printf("      currently owned by: %s\n", c.CurrentOwner)
					fmt.Printf("      requested by:        %s\n", c.RequestedBy)
				}
				if len(result.Conflicts) > 0 {
					fmt.Println()
					warn("Re-run with --force to transfer ownership.")
				}
				for _, hookErr := range result.HookErrors {
					warn("Hook error (non-fatal): %v", hookErr)
				}
			}

			if err != nil {
				return err
			}

			if result != nil && len(result.Rendered) > 0 && !dryRun {
				fmt.Println()
				info("Run 'sysfig diff' to review, then 'sysfig apply' to write files to disk.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	cmd.Flags().StringVar(&renderProfile, "profile", "", "limit render to one profile (e.g. corporate/system-proxy)")
	cmd.Flags().StringVar(&valuesFile, "values", "", "YAML file with per-profile variables (profile: {key: value})")
	cmd.Flags().StringArrayVar(&varFlags, "var", nil, "override a variable: key=value or profile:key=value (repeatable)")
	cmd.Flags().BoolVar(&force, "force", false, "transfer ownership of conflicting files")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be rendered without writing")
	return cmd
}

func newSourcePullCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "pull <source>",
		Short: "Fetch the latest source bundle into the local cache",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			name := args[0]
			if err := core.SourcePull(baseDir, name); err != nil {
				return err
			}
			ok("Source %q updated", name)
			info("Run 'sysfig source render' to re-render with the updated templates.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

// ── audit ─────────────────────────────────────────────────────────────────────

// newAuditCmd returns the `sysfig audit` command.
//
// Exit codes:
//
//	0  all audited files are clean (SYNCED)
//	1  one or more files are TAMPERED or DIRTY
//	2  an error prevented the audit from completing
func newAuditCmd() *cobra.Command {
	var (
		baseDir  string
		hashOnly bool
		local    bool
		all      bool
		quiet    bool
	)

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Check integrity of local-only and hash-only tracked files",
		Long: `audit checks tracked files that are flagged as --local or --hash-only
and reports any that have drifted from their recorded hash.

Exit codes:
  0  all checked files are clean
  1  one or more files are TAMPERED or DIRTY
  2  error (could not read state or hash a file)

Designed for use in systemd timers or cron jobs:
  sysfig audit --hash-only  # exits 1 if any hash-only file is TAMPERED`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)

			results, err := core.Status(baseDir, nil, "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "audit: %v\n", err)
				os.Exit(2)
			}

			var checked, drifted int
			for _, r := range results {
				// Determine whether this result is in scope.
				inScope := all ||
					(hashOnly && r.HashOnly) ||
					(local && r.LocalOnly) ||
					(!all && !hashOnly && !local && (r.HashOnly || r.LocalOnly))

				if !inScope {
					continue
				}
				checked++

				clean := r.Status == core.StatusSynced
				if !clean {
					drifted++
					if !quiet {
						label := clrErr.Sprint(string(r.Status))
						fmt.Printf("  %s  %s\n", label, r.SystemPath)
					}
				} else if !quiet {
					fmt.Printf("  %s  %s\n", clrOK.Sprint("OK     "), r.SystemPath)
				}
			}

			if !quiet {
				fmt.Println()
				if drifted > 0 {
					fmt.Printf("  %s\n", clrErr.Sprintf("Audit: %d/%d file(s) drifted", drifted, checked))
				} else {
					fmt.Printf("  %s\n", clrOK.Sprintf("Audit: %d/%d file(s) clean", checked, checked))
				}
			}

			if drifted > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	f.BoolVar(&hashOnly, "hash-only", false, "audit only hash-only tracked files")
	f.BoolVar(&local, "local", false, "audit only local-only tracked files")
	f.BoolVar(&all, "all", false, "audit all tracked files (not just local/hash-only)")
	f.BoolVar(&quiet, "quiet", false, "suppress per-file output; exit code still reflects drift")
	return cmd
}

// ── friendly error messages ───────────────────────────────────────────────────

// friendlyErr translates raw core errors into actionable user-facing messages.
// It checks error strings for known patterns and returns a cleaner error; if no
// pattern matches the original error is returned unchanged.
func friendlyErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "resolve path"):
		return fmt.Errorf("file not found — check the path and try again\n\n  hint: use an absolute path, e.g. /etc/nginx/nginx.conf")
	case strings.Contains(msg, "is in the system denylist"):
		return fmt.Errorf("that path is in the protected denylist and cannot be tracked\n\n  hint: edit ~/.sysfig/denylist to allow it, or choose a different file")
	case strings.Contains(msg, "is not a regular file"):
		return fmt.Errorf("the path exists but is not a regular file (it may be a directory or device)\n\n  hint: to track a directory use:  sysfig track --recursive <dir>")
	case strings.Contains(msg, "already tracked"):
		return fmt.Errorf("this file is already being tracked\n\n  hint: run sysfig status to see all tracked files")
	case strings.Contains(msg, "master key not found"):
		return fmt.Errorf("no encryption key found — generate one first\n\n  hint: run sysfig keys generate")
	case strings.Contains(msg, "managed by source profile"):
		// pass through — message is already clear and includes --force hint
		return err
	case strings.Contains(msg, "no remote configured") || strings.Contains(msg, "remote URL"):
		return fmt.Errorf("no remote repository configured\n\n  hint: run sysfig remote add <url>  or  sysfig bootstrap <url>")
	default:
		return err
	}
}
