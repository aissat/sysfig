package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/internal/state"
	"github.com/spf13/cobra"
)

// ── deploy ────────────────────────────────────────────────────────────────────

func newDeployCmd() *cobra.Command {
	var (
		baseDir       string
		fromURL       string
		profile       string
		vars          []string
		dryRun        bool
		noBackup      bool
		skipEncrypted bool
		noPull        bool
		yes           bool
		sysRoot       string
		ids           []string
		tags          []string
		paths         []string
		allFiles      bool
		host          string
		sshKey        string
		sshPort       int
		sshSudo       bool
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

			// ── --from + --profile: render template repo → push to host ──
			if fromURL != "" && profile != "" {
				if host == "" {
					return fmt.Errorf("deploy --from --profile requires --host")
				}
				fmt.Println()
				clrBold.Printf("  sysfig deploy (source) → %s\n", host)
				fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
				fmt.Println()

				tmpDir, err := os.MkdirTemp("", "sysfig-source-*")
				if err != nil {
					return fmt.Errorf("deploy --from: create temp dir: %w", err)
				}
				defer os.RemoveAll(tmpDir)

				info("Fetching %s …", clrBold.Sprint(fromURL))
				repoDir := filepath.Join(tmpDir, "repo.git")
				if err := core.FetchProfileRepo(fromURL, repoDir); err != nil {
					return fmt.Errorf("deploy --from: fetch: %w", err)
				}

				// Parse --var key=value pairs.
				varMap := make(map[string]string, len(vars))
				for _, v := range vars {
					k, val, ok := strings.Cut(v, "=")
					if !ok {
						return fmt.Errorf("deploy --var: invalid format %q (expected key=value)", v)
					}
					varMap[k] = val
				}

				info("Rendering profile %s …", clrBold.Sprint(profile))
				rendered, err := core.RenderProfileFromRepo(repoDir, profile, varMap)
				if err != nil {
					return err
				}

				applied, failed, err := core.RemoteDeployRendered(core.RemoteRenderedOptions{
					Host:    host,
					SSHKey:  sshKey,
					SSHPort: sshPort,
					Files:   rendered,
					Sudo:    sshSudo,
					DryRun:  dryRun,
					Progress: func(path string, writeErr error) {
						if dryRun {
							fmt.Printf("  %s %s → %s\n", clrDim.Sprint("[dry-run]"), clrInfo.Sprint(profile), clrDim.Sprint(path))
						} else if writeErr != nil {
							fail("%s  %s", path, writeErr)
						} else {
							ok("Applied: %s → %s", clrBold.Sprint(profile), path)
						}
					},
				})
				if err != nil {
					return err
				}

				fmt.Println()
				divider()
				clrOK.Printf("  ✓ Source deploy complete! (%s)\n\n", host)
				fmt.Printf("  Applied: %d", applied)
				if failed > 0 {
					fmt.Printf("  ·  %s", clrErr.Sprintf("Failed: %d", failed))
				}
				fmt.Println()
				if failed > 0 {
					os.Exit(1)
				}
				return nil
			}

			// ── --from: clone a sysfig repo into a temp dir ───────────
			if fromURL != "" {
				tmpDir, err := os.MkdirTemp("", "sysfig-from-*")
				if err != nil {
					return fmt.Errorf("deploy --from: create temp dir: %w", err)
				}
				defer os.RemoveAll(tmpDir)

				info("Fetching from %s …", clrBold.Sprint(fromURL))
				if _, err := core.Clone(core.CloneOptions{
					RemoteURL:     fromURL,
					BaseDir:       tmpDir,
					SkipEncrypted: skipEncrypted,
					Yes:           true,
				}); err != nil {
					return fmt.Errorf("deploy --from: fetch: %w", err)
				}
				baseDir = tmpDir
			}

			// ── Remote host mode ──────────────────────────────────────
			if host != "" {
				fmt.Println()
				clrBold.Printf("  sysfig deploy → %s\n", host)
				fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
				fmt.Println()

				hasSudoHint := false
				result, err := core.RemoteDeploy(core.RemoteDeployOptions{
					Host:          host,
					SSHKey:        sshKey,
					SSHPort:       sshPort,
					BaseDir:       baseDir,
					IDs:           ids,
					Tags:          tags,
					Paths:         paths,
					All:           allFiles,
					DryRun:        dryRun,
					SkipEncrypted: skipEncrypted,
					Sudo:          sshSudo,
					Progress: func(r core.RemoteFileResult) {
						switch {
						case r.Err != nil:
							fail("%s  %s", clrErr.Sprint(r.ID), clrErr.Sprint(r.Err.Error()))
							if !hasSudoHint && strings.Contains(r.Err.Error(), "permission denied") && !sshSudo {
								hasSudoHint = true
							}
						case r.Skipped && r.SkipReason == "dry-run":
							fmt.Printf("  %s %s → %s\n", clrDim.Sprint("[dry-run]"), clrInfo.Sprint(r.ID), clrDim.Sprint(r.SystemPath))
						case r.Skipped:
							fmt.Printf("  %s %s  %s\n", clrDim.Sprint("―"), clrDim.Sprint(r.ID), clrDim.Sprintf("(%s)", r.SkipReason))
						default:
							ok("%s → %s", clrBold.Sprint(r.ID), r.SystemPath)
						}
					},
				})
				if err != nil {
					return err
				}

				if len(result.Results) == 0 {
					info("Nothing to deploy (no tracked files).")
				}
				if hasSudoHint {
					fmt.Printf("  %s\n", clrDim.Sprint("Hint: /etc/ files need root — re-run with --sudo to use sudo on the remote."))
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
				Tags:          tags,
				Paths:         paths,
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
	f.StringVar(&fromURL, "from", "", "fetch configs from this git remote or bundle URL instead of local repo")
	f.StringVar(&profile, "profile", "", "render this profile from a config-template repo (requires --from and --host)")
	f.StringArrayVar(&vars, "var", nil, "profile variable value: key=value (repeatable, used with --profile)")
	f.StringArrayVar(&ids, "id", nil, "deploy only this ID or 8-char prefix (repeatable)")
	f.StringArrayVar(&tags, "tag", nil, "deploy only files with this tag (repeatable) — e.g. --tag arch")
	f.StringArrayVar(&paths, "path", nil, "deploy only the file at this system path (repeatable)")
	f.BoolVar(&allFiles, "all", false, "deploy all tracked files (required when no --tag, --id, or --path is given)")
	f.BoolVar(&dryRun, "dry-run", false, "print what would happen without writing anything")
	f.BoolVar(&noBackup, "no-backup", false, "skip pre-apply backup")
	f.BoolVar(&skipEncrypted, "skip-encrypted", false, "skip encrypted files when master key is absent")
	f.BoolVar(&noPull, "no-pull", false, "skip pull — apply from local repo only (offline mode)")
	f.BoolVar(&yes, "yes", false, "non-interactive: skip all prompts")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.StringVar(&host, "host", "", "SSH target (user@hostname) — push files to remote without sysfig installed there")
	f.StringVar(&sshKey, "ssh-key", "", "path to SSH identity file (default: use ssh-agent)")
	f.IntVar(&sshPort, "ssh-port", 22, "SSH port on the remote host")
	f.BoolVar(&sshSudo, "sudo", false, "wrap remote writes with sudo (required for /etc/ and root-owned paths)")
	return cmd
}

// ── bootstrap ─────────────────────────────────────────────────────────────────

func newBootstrapCmd() *cobra.Command {
	var (
		baseDir       string
		configsOnly   bool
		skipEncrypted bool
		yes           bool
		noApply       bool
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

			if noApply {
				fmt.Printf("  %s\n", clrBold.Sprint("What to do next:"))
				fmt.Printf("   %s  Deploy your config files to this machine\n", clrInfo.Sprint("sysfig apply"))
				fmt.Printf("   %s  Check sync status at any time\n", clrInfo.Sprint("sysfig status"))
				fmt.Println()
				return nil
			}

			// Auto-apply after bootstrap.
			fmt.Printf("  %s\n\n", clrBold.Sprint("Applying config files…"))
			applyResults, applyErr := core.Apply(core.ApplyOptions{
				BaseDir: baseDir,
			})
			applied, permDenied := 0, 0
			for _, r := range applyResults {
				if r.Skipped {
					continue
				}
				ok("Applied: %s", clrBold.Sprint(r.ID))
				fmt.Printf("     %s %s\n", clrDim.Sprint("→"), r.SystemPath)
				if r.BackupPath != "" {
					fmt.Printf("     %s %s\n", clrDim.Sprint("backup:"), clrDim.Sprint(r.BackupPath))
				}
				applied++
			}
			if applyErr != nil {
				errStr := applyErr.Error()
				for _, line := range strings.Split(errStr, "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Fprintf(os.Stderr, "  %s %s\n", clrErr.Sprint("error:"), line)
						if strings.Contains(line, "permission denied") {
							permDenied++
						}
					}
				}
			}
			fmt.Println()
			divider()
			fmt.Printf("  Applied: %s\n", clrBold.Sprintf("%d", applied))
			if permDenied > 0 {
				fmt.Println()
				warn("Some files require elevated privileges.")
				fmt.Printf("     %s\n", clrDim.Sprint("Re-run: sudo sysfig apply"))
			} else {
				fmt.Printf("   %s  Check sync status at any time\n", clrInfo.Sprint("sysfig status"))
			}
			fmt.Println()
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.BoolVar(&configsOnly, "configs-only", false, "skip package installation, deploy configs only")
	f.BoolVar(&skipEncrypted, "skip-encrypted", false, "skip encrypted files when master key is absent")
	f.BoolVar(&yes, "yes", false, "non-interactive: skip all prompts")
	f.BoolVar(&noApply, "no-apply", false, "skip applying configs after cloning (apply manually later)")
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

			// Run post_apply hooks for source-managed files after they are on disk.
			if !dryRun {
				var sourceProfiles []string
				for _, r := range results {
					if r.SourceProfileApplied != "" {
						sourceProfiles = append(sourceProfiles, r.SourceProfileApplied)
					}
				}
				for _, hookErr := range core.RunSourceHooks(baseDir, sourceProfiles) {
					warn("Source hook error (non-fatal): %v", hookErr)
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

