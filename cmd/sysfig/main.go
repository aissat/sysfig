package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

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
		newTagCmd(),
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


