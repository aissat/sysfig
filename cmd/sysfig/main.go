package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
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

const usage = `Usage: sysfig <command> [options]

Commands:
  setup   Bootstrap sysfig on a new machine from a remote config repo
  init    Initialise a fresh sysfig environment (no remote)
  track   Start tracking a config file
  keys    Manage the master encryption key
  apply   Deploy tracked configs from the repo to the system
  status  Show sync status of all tracked files
  diff    Show unified diff between system and repo copies
  sync    Capture local changes and commit (offline-safe)
  push    Push commits to the remote (requires network)
  pull    Pull remote changes into the local repo (requires network)
  log     Show commit history as a tree

Run 'sysfig <command> -help' for command-specific options.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "setup":
		runSetup(os.Args[2:])
	case "clone": // hidden alias for backward-compat
		runSetup(os.Args[2:])
	case "init":
		runInit(os.Args[2:])
	case "track":
		runTrack(os.Args[2:])
	case "keys":
		runKeys(os.Args[2:])
	case "apply":
		runApply(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "diff":
		runDiff(os.Args[2:])
	case "sync":
		runSync(os.Args[2:])
	case "push":
		runPush(os.Args[2:])
	case "pull":
		runPull(os.Args[2:])
	case "log":
		runLog(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown subcommand %q\n\n", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

// defaultBaseDir returns ~/.sysfig, falling back to ".sysfig" if the home
// directory cannot be determined.
func defaultBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sysfig"
	}
	return filepath.Join(home, ".sysfig")
}

// runInit parses flags for the 'init' subcommand and calls core.Init.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	encrypt := fs.Bool("encrypt", false, "enable encryption-by-default in the generated sysfig.yaml")

	if err := fs.Parse(args); err != nil {
		// flag already printed the error
		os.Exit(1)
	}

	result, err := core.Init(core.InitOptions{
		BaseDir: *baseDir,
		Encrypt: *encrypt,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	if result.AlreadyExisted {
		info("sysfig already initialised in %s", clrDim.Sprint(result.BaseDir))
		return
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
}

// runTrack parses flags for the 'track' subcommand and calls core.Track (single
// file) or core.TrackDir (directory, when --recursive is set).
func runTrack(args []string) {
	fs := flag.NewFlagSet("track", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	id := fs.String("id", "", "explicit tracking ID (derived from path if omitted, ignored with --recursive)")
	encrypt := fs.Bool("encrypt", false, "mark the file for encryption at rest in the repo")
	template := fs.Bool("template", false, "mark the file as a template with {{variable}} expansions")
	recursive := fs.Bool("recursive", false, "recursively track all files under a directory")
	sysRoot := fs.String("sys-root", "", "strip this prefix from paths before storing in repo and state (sandbox/testing override)")

	// --tag may appear multiple times; we collect them manually after parsing.
	var tags multiFlag
	fs.Var(&tags, "tag", "label to attach to the tracked file (repeatable)")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "Error: track requires a <path> argument")
		fmt.Fprintln(os.Stderr, "Usage: sysfig track [--recursive] <path> [--id <id>] [--tag <tag>]... [--encrypt] [--template]")
		os.Exit(1)
	}

	targetPath := remaining[0]
	repoDir := filepath.Join(*baseDir, "repo.git")

	// ── Recursive directory mode ────────────────────────────────────────────
	if *recursive {
		summary, err := core.TrackDir(core.TrackDirOptions{
			DirPath:  targetPath,
			RepoDir:  repoDir,
			StateDir: *baseDir,
			Tags:     []string(tags),
			Encrypt:  *encrypt,
			Template: *template,
			SysRoot:  *sysRoot,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
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
				if *encrypt {
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
		return
	}

	// ── Single file mode ────────────────────────────────────────────────────
	result, err := core.Track(core.TrackOptions{
		SystemPath: targetPath,
		StateDir:   *baseDir,
		RepoDir:    repoDir,
		ID:         *id,
		Tags:       []string(tags),
		Encrypt:    *encrypt,
		Template:   *template,
		SysRoot:    *sysRoot,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	clrBold.Printf("Tracking %s\n", targetPath)
	fmt.Println()
	ok("ID:   %s", clrInfo.Sprint(result.ID))
	ok("Repo: %s", clrDim.Sprint(result.RepoPath))
	ok("Hash: %s", clrDim.Sprint(result.Hash))
	if *encrypt {
		ok("%s", clrEncrypted.Sprint("Encrypted: yes (age + HKDF-SHA256 per-file key)"))
	}
}

// runKeys handles the 'keys' subcommand with sub-sub-commands: info, generate.
func runKeys(args []string) {
	const keysUsage = `Usage: sysfig keys <subcommand> [options]

Subcommands:
  info      Show master key public key and path
  generate  Generate a new master key (fails if one already exists)

`
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, keysUsage)
		os.Exit(1)
	}

	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(1)
	}

	keysDir := filepath.Join(*baseDir, "keys")
	km := crypto.NewKeyManager(keysDir)

	switch args[0] {
	case "info":
		identity, err := km.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			fmt.Fprintf(os.Stderr, "Hint: run 'sysfig init --encrypt' to generate a master key.\n")
			os.Exit(1)
		}
		clrBold.Println("Master key")
		fmt.Println()
		ok("Path:       %s", clrDim.Sprint(crypto.MasterKeyPath(keysDir)))
		ok("Public key: %s", clrEncrypted.Sprint(crypto.PublicKey(identity)))

	case "generate":
		identity, err := km.Generate()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
		}
		clrBold.Println("Generated new master key")
		fmt.Println()
		ok("Path:       %s", clrDim.Sprint(crypto.MasterKeyPath(keysDir)))
		ok("Public key: %s", clrEncrypted.Sprint(crypto.PublicKey(identity)))
		fmt.Println()
		warn("%s Back up this key immediately!", clrErr.Sprint("IMPORTANT:"))
		fmt.Printf("     Location: %s\n", crypto.MasterKeyPath(keysDir))

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown keys subcommand %q\n\n", args[0])
		fmt.Fprint(os.Stderr, keysUsage)
		os.Exit(1)
	}
}

// runSetup bootstraps sysfig on a new machine from a remote config repo.
// It is the primary onboarding command — "clone" is kept as a hidden alias.
//
// Accepts the remote URL as an optional positional argument (before or after flags).
// If omitted and stdin is a TTY, prompts interactively.
func runSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	configsOnly := fs.Bool("configs-only", false, "skip package installation, deploy configs only")
	skipEncrypted := fs.Bool("skip-encrypted", false, "skip encrypted files when master key is absent")
	yes := fs.Bool("yes", false, "non-interactive: skip all prompts")

	// Split positional URL from flags so both orderings work.
	// Value-taking flags: their next token is a value, not the URL.
	valueTakingFlags := map[string]bool{
		"-base-dir": true, "--base-dir": true,
	}
	var remoteURL string
	var flagArgs []string
	skipNext := false
	for _, arg := range args {
		if skipNext {
			flagArgs = append(flagArgs, arg)
			skipNext = false
			continue
		}
		if len(arg) > 0 && arg[0] == '-' {
			flagArgs = append(flagArgs, arg)
			// If flag doesn't use = syntax, next token is its value.
			bare := strings.SplitN(arg, "=", 2)[0]
			if valueTakingFlags[bare] {
				skipNext = true
			}
		} else if remoteURL == "" {
			remoteURL = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		os.Exit(1)
	}

	// ── Header ────────────────────────────────────────────────────────────
	fmt.Println()
	clrBold.Println("  sysfig setup — bootstrapping your environment")
	fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
	fmt.Println()

	// ── Already set up? ───────────────────────────────────────────────────
	repoDir := filepath.Join(*baseDir, "repo.git")
	if fi, err := os.Stat(repoDir); err == nil && fi.IsDir() {
		info("This machine is already set up.")
		fmt.Printf("     %s %s\n", clrDim.Sprint("config repo:"), clrDim.Sprint(repoDir))
		fmt.Printf("     %s %s\n", clrDim.Sprint("base dir:   "), clrDim.Sprint(*baseDir))
		fmt.Println()
		info("Run %s to check for remote updates.", clrBold.Sprint("sysfig pull"))
		info("Run %s to see current state.", clrBold.Sprint("sysfig status"))
		fmt.Println()
		return
	}

	// ── Step 1: remote URL ────────────────────────────────────────────────
	step(1, "Remote config repository")
	if remoteURL == "" {
		if *yes || !isatty() {
			fmt.Fprintf(os.Stderr, "%s remote URL is required (use: sysfig setup <url>)\n", clrErr.Sprint("Error:"))
			os.Exit(1)
		}
		fmt.Printf("     %s ", clrBold.Sprint("Remote URL:"))
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			remoteURL = strings.TrimSpace(scanner.Text())
		}
		if remoteURL == "" {
			fmt.Fprintf(os.Stderr, "%s no URL provided\n", clrErr.Sprint("Error:"))
			os.Exit(1)
		}
	} else {
		fmt.Printf("     %s %s\n", clrDim.Sprint("url:"), remoteURL)
	}

	// ── Step 2: fetch config ───────────────────────────────────────────────
	fmt.Println()
	step(2, "Fetching your config repo")
	result, err := core.Clone(core.CloneOptions{
		RemoteURL:     remoteURL,
		BaseDir:       *baseDir,
		ConfigsOnly:   *configsOnly,
		SkipEncrypted: *skipEncrypted,
		Yes:           *yes,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%s %s\n", clrErr.Sprint("Error:"), err)
		os.Exit(1)
	}
	ok("Config repo ready")
	fmt.Printf("     %s %s\n", clrDim.Sprint("location:"), clrDim.Sprint(result.RepoDir))

	// ── Step 3: manifest ──────────────────────────────────────────────────
	fmt.Println()
	step(3, "Reading manifest")
	if result.Seeded > 0 {
		ok("Found %s tracked file(s) in your manifest", clrBold.Sprintf("%d", result.Seeded))
	} else {
		info("Manifest has no tracked files yet — use %s to start tracking.", clrBold.Sprint("sysfig track"))
	}

	// ── Step 4: hooks ────────────────────────────────────────────────────
	if result.HooksWarning != "" {
		fmt.Println()
		step(4, "Hooks")
		warn("hooks.yaml created from template — review before using:")
		fmt.Printf("     %s\n", clrDim.Sprint(filepath.Join(*baseDir, "hooks.yaml")))
	}

	// ── Done ─────────────────────────────────────────────────────────────
	fmt.Println()
	divider()
	clrOK.Println("  ✓ Setup complete!")
	fmt.Println()
	fmt.Printf("  %s\n", clrBold.Sprint("What to do next:"))
	fmt.Printf("   %s  Deploy your config files to this machine\n", clrInfo.Sprint("sysfig apply"))
	fmt.Printf("   %s  Check sync status at any time\n", clrInfo.Sprint("sysfig status"))
	fmt.Printf("   %s  See your commit history\n", clrInfo.Sprint("sysfig log    "))
	fmt.Println()
}

// step prints a numbered step header used by runSetup.
func step(n int, label string) {
	fmt.Printf("  %s %s\n", clrBold.Sprintf("[%d]", n), clrBold.Sprint(label))
}

// runApply parses flags for the 'apply' subcommand and calls core.Apply.
func runApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	sysRoot := fs.String("sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	dryRun := fs.Bool("dry-run", false, "print what would happen without writing anything")
	noBackup := fs.Bool("no-backup", false, "skip pre-apply backup (dangerous)")

	var ids multiFlag
	fs.Var(&ids, "id", "apply only this ID (repeatable)")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	results, err := core.Apply(core.ApplyOptions{
		BaseDir:  *baseDir,
		IDs:      []string(ids),
		DryRun:   *dryRun,
		NoBackup: *noBackup,
		SysRoot:  *sysRoot,
	})

	// Print results even if there was a partial error.
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
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		info("Nothing to apply (no tracked files found).")
		return
	}

	fmt.Println()
	divider()
	if skipped > 0 {
		fmt.Printf("  %s  ·  %s\n", clrOK.Sprintf("Applied: %d", applied), clrDim.Sprintf("Dry-run: %d", skipped))
	} else {
		fmt.Printf("  %s\n", clrOK.Sprintf("Applied: %d", applied))
	}
}

// runStatus parses flags for the 'status' subcommand and calls core.Status.
func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	sysRoot := fs.String("sys-root", "", "prepend this path to all system paths (sandbox/testing override)")

	var ids multiFlag
	fs.Var(&ids, "id", "check only this ID (repeatable)")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	results, err := core.Status(*baseDir, []string(ids), *sysRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		info("No tracked files.")
		return
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

		// Show metadata drift detail inline.
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
	// Summary line: e.g. "5 files  ·  3 synced  ·  1 dirty  ·  1 missing"
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
}

// runDiff parses flags for the 'diff' subcommand and calls core.Diff.
// Exit code 0 = all synced (no diff), 1 = differences found, 2 = error.
func runDiff(args []string) {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	sysRoot := fs.String("sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	color := fs.Bool("color", isatty(), "colorize diff output (default: true when stdout is a TTY)")

	var ids multiFlag
	fs.Var(&ids, "id", "diff only this ID (repeatable)")

	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if err := core.CheckDiffPrereqs(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(2)
	}

	results, err := core.Diff(core.DiffOptions{
		BaseDir: *baseDir,
		IDs:     []string(ids),
		SysRoot: *sysRoot,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(2)
	}

	if len(results) == 0 {
		info("No tracked files.")
		os.Exit(0)
	}

	hasDiff := false
	for _, r := range results {
		// Header line: colored by status.
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
			if *color {
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
}

// diffStatusLabel returns the human-readable label used in diff headers.
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

// colorize adds ANSI color codes to a unified diff:
//   - lines starting with '+' → green
//   - lines starting with '-' → red
//   - hunk headers (@@ ... @@) → cyan
func colorize(diff string) string {
	const (
		red    = "\033[31m"
		green  = "\033[32m"
		cyan   = "\033[36m"
		reset  = "\033[0m"
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

// isatty returns true if stdout is connected to a terminal.
func isatty() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// runSync parses flags for the 'sync' subcommand and calls core.Sync.
// Commit is always local (offline-safe). Pass --push to also push to remote.
func runSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	message := fs.String("message", "", "commit message (default: sysfig: sync <timestamp>)")
	push := fs.Bool("push", false, "also push to remote after committing (requires network)")
	sysRoot := fs.String("sys-root", "", "prefix all system paths (sandbox/testing override)")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	result, err := core.Sync(core.SyncOptions{
		BaseDir: *baseDir,
		Message: *message,
		Push:    *push,
		SysRoot: *sysRoot,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	if !result.Committed {
		info("Nothing to commit — shadow repo is clean.")
		return
	}

	ok("Committed: %s", clrBold.Sprint(result.Message))
	ok("Repo:      %s", clrDim.Sprint(result.RepoDir))
	if result.Pushed {
		ok("Pushed to remote.")
	} else {
		info("Not pushed. Run %s when online.", clrBold.Sprint("sysfig push"))
	}
}

// runPush parses flags for the 'push' subcommand and calls core.Push.
func runPush(args []string) {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if err := core.Push(core.PushOptions{BaseDir: *baseDir}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	ok("Pushed to remote.")
}

// runLog shows the shadow repo commit history as a graph tree.
func runLog(args []string) {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")
	file := fs.String("file", "", "show only commits that touched this repo-relative path (e.g. etc/app/config.conf)")
	n := fs.Int("n", 0, "limit to last N commits (0 = unlimited)")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	repoDir := filepath.Join(*baseDir, "repo.git")
	if repoDir == "" {
		fmt.Fprintln(os.Stderr, "Error: could not determine repo directory")
		os.Exit(1)
	}

	gitArgs := []string{
		"--git-dir=" + repoDir,
		"log", "--graph",
		"--pretty=format:%C(yellow)%h%Creset %C(cyan)%ad%Creset %s%C(green)%d%Creset",
		"--date=short",
		"--all",
	}
	if *n > 0 {
		gitArgs = append(gitArgs, fmt.Sprintf("-n%d", *n))
	}
	if *file != "" {
		gitArgs = append(gitArgs, "--", *file)
	}

	cmd := exec.Command("git", gitArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}

// runPull parses flags for the 'pull' subcommand and calls core.Pull.
func runPull(args []string) {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	baseDir := fs.String("base-dir", defaultBaseDir(), "directory where sysfig stores its data")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	result, err := core.Pull(core.PullOptions{BaseDir: *baseDir})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %s\n", clrErr.Sprint("Error:"), err)
		fmt.Fprintf(os.Stderr, "%s check your network connection and remote configuration.\n", clrWarn.Sprint("Hint:"))
		os.Exit(1)
	}

	if result.AlreadyUpToDate {
		info("Already up to date.")
	} else {
		ok("Pulled latest changes from remote.")
		info("Run %s to deploy updated config files.", clrBold.Sprint("sysfig apply"))
	}
}

// multiFlag implements flag.Value to allow a flag to be specified multiple
// times, accumulating each value into a string slice.
type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil || len(*m) == 0 {
		return ""
	}
	result := ""
	for i, v := range *m {
		if i > 0 {
			result += ","
		}
		result += v
	}
	return result
}

func (m *multiFlag) Set(val string) error {
	*m = append(*m, val)
	return nil
}
