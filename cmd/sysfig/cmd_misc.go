package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/internal/crypto"
	"github.com/spf13/cobra"
)

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
		tags     []string
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

			// Collect in-scope results.
			type auditRow struct {
				path      string
				id        string
				tags      []string
				trackType string
				status    core.FileStatusLabel
			}
			filteredResults := filterByTags(results, tags)
			var rows []auditRow
			for _, r := range filteredResults {
				inScope := all ||
					(hashOnly && r.HashOnly) ||
					(local && r.LocalOnly) ||
					(!all && !hashOnly && !local && (r.HashOnly || r.LocalOnly))
				if !inScope {
					continue
				}
				tt := "file"
				switch {
				case r.HashOnly:
					tt = "hash"
				case r.LocalOnly:
					tt = "local"
				}
				rows = append(rows, auditRow{r.SystemPath, r.ID, r.Tags, tt, r.Status})
			}

			var drifted int
			if !quiet && len(rows) > 0 {
				pathW := len("PATH")
				stateW := len("STATE") + 2
				trackW := len("TRACK") + 2
				tagsW := len("TAGS") + 2
				detailsW := len("DETAILS")
				implicitTags := core.DetectPlatformTags()
				for _, r := range rows {
					if len(r.path) > pathW {
						pathW = len(r.path)
					}
					if len(healthSummary(map[core.FileStatusLabel]int{r.status: 1}))+2 > stateW {
						stateW = len(healthSummary(map[core.FileStatusLabel]int{r.status: 1})) + 2
					}
					if len(r.trackType)+2 > trackW {
						trackW = len(r.trackType) + 2
					}
					if l := len(displayTags(compactTags(r.tags, 3), implicitTags)); l+2 > tagsW {
						tagsW = l + 2
					}
					details := "verified"
					if r.status != core.StatusSynced {
						details = strings.ToLower(string(r.status))
					}
					if len(details) > detailsW {
						detailsW = len(details)
					}
				}
				pathW += 2

				fmt.Printf("%s  %s  %s  %s  %s\n",
					clrBold.Sprint(pad("PATH", pathW)),
					clrBold.Sprint(pad("STATE", stateW)),
					clrBold.Sprint(pad("TRACK", trackW)),
					clrBold.Sprint(pad("TAGS", tagsW)),
					clrBold.Sprint("DETAILS"))
				divider()
				for _, r := range rows {
					clean := r.status == core.StatusSynced
					stateCol := groupStateColored(map[core.FileStatusLabel]int{r.status: 1})
					trackCol := clrDim.Sprint(pad(r.trackType, trackW))
					tagsCol := clrInfo.Sprint(pad(displayTags(compactTags(r.tags, 3), implicitTags), tagsW))
					details := "verified"
					if !clean {
						drifted++
						details = strings.ToLower(string(r.status))
					}
					detailsCol := clrDim.Sprint(details)
					if !clean {
						detailsCol = clrDirty.Sprint(details)
					}
					fmt.Printf("%s  %s  %s  %s  %s\n",
						clrBold.Sprint(pad(r.path, pathW)),
						padVisible(stateCol, stateW),
						trackCol,
						tagsCol,
						detailsCol)
				}
				divider()
				fmt.Println()
			} else {
				for _, r := range rows {
					if r.status != core.StatusSynced {
						drifted++
					}
				}
			}

			checked := len(rows)
			if !quiet {
				if drifted > 0 {
					fmt.Printf("  %s\n", clrErr.Sprintf("Audit degraded · %d/%d file(s) drifted", drifted, checked))
				} else {
					fmt.Printf("  %s\n", clrOK.Sprintf("Audit clean · %d/%d verified", checked, checked))
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
	f.StringArrayVar(&tags, "tag", nil, "audit only files with this tag (repeatable)")
	return cmd
}

// ── tag ───────────────────────────────────────────────────────────────────────

func newTagCmd() *cobra.Command {
	var (
		baseDir   string
		listFlag  bool
		autoFlag  bool
		overwrite bool
		renameOld string
		renameTo  string
	)

	cmd := &cobra.Command{
		Use:   "tag [path-or-id] [tag...]",
		Short: "Manage tags on tracked files",
		Long: `Manage tags on tracked files.

Tags let you target specific files during deploy:
  sysfig deploy --host user@vm --tag ubuntu --sudo

Usage:
  sysfig tag --list                     show all tags and file counts
  sysfig tag --auto                     write OS+distro tags to untagged files
  sysfig tag --auto --overwrite         rewrite tags on ALL files
  sysfig tag --rename old --to new      rename a tag across all files
  sysfig tag <path-or-id> [tag...]      set tags on a specific file`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)

			switch {
			case listFlag:
				result, err := core.TagList(core.TagListOptions{BaseDir: baseDir})
				if err != nil {
					return err
				}
				fmt.Println()
				clrBold.Println("  sysfig tag — tag list")
				fmt.Println(clrDim.Sprint("  ─────────────────────────────────────────────"))
				fmt.Println()
				if len(result.Entries) == 0 && result.Untagged == 0 {
					info("No tracked files.")
					return nil
				}
				const tagW = 16
				fmt.Printf("  %s  %s\n", clrBold.Sprint(pad("TAG", tagW)), clrBold.Sprint("FILES"))
				divider()
				for _, e := range result.Entries {
					fmt.Printf("  %s  %s\n", clrInfo.Sprint(pad(e.Tag, tagW)), clrBold.Sprintf("%d", e.Count))
				}
				divider()
				if result.Untagged > 0 {
					implicit := strings.Join(core.DetectPlatformTags(), ",")
					fmt.Printf("\n  %s\n\n",
						clrDim.Sprintf("%d file(s) have no explicit tags — platform tags would apply: %s", result.Untagged, implicit))
				}
				fmt.Println()

			case renameOld != "" && renameTo != "":
				result, err := core.TagRename(core.TagRenameOptions{
					BaseDir: baseDir,
					OldTag:  renameOld,
					NewTag:  renameTo,
				})
				if err != nil {
					return err
				}
				if result.Updated == 0 {
					info("Tag %q not found in any tracked file.", renameOld)
				} else {
					ok("Renamed tag %q → %q across %d file(s).", renameOld, renameTo, result.Updated)
				}

			case autoFlag:
				result, err := core.TagAuto(core.TagAutoOptions{
					BaseDir:   baseDir,
					Overwrite: overwrite,
				})
				if err != nil {
					return err
				}
				implicit := strings.Join(core.DetectPlatformTags(), ",")
				ok("Tagged %d file(s) with: %s", result.Updated, clrInfo.Sprint(implicit))
				if result.Skipped > 0 {
					fmt.Printf("  %s\n", clrDim.Sprintf("Skipped %d already-tagged file(s) — use --overwrite to rewrite all.", result.Skipped))
				}

			case len(args) >= 1:
				newTags := args[1:]
				result, err := core.TagSet(core.TagSetOptions{
					BaseDir:  baseDir,
					PathOrID: args[0],
					Tags:     newTags,
				})
				if err != nil {
					return err
				}
				if len(result.NewTags) == 0 {
					ok("Cleared tags on %s", clrBold.Sprint(result.SystemPath))
				} else {
					ok("%s → [%s]", clrBold.Sprint(result.SystemPath), clrInfo.Sprint(strings.Join(result.NewTags, ",")))
				}

			default:
				return cmd.Help()
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	f.BoolVar(&listFlag, "list", false, "show all tags and file counts")
	f.BoolVar(&autoFlag, "auto", false, "write OS+distro tags to untagged files")
	f.BoolVar(&overwrite, "overwrite", false, "with --auto, rewrite tags on all files")
	f.StringVar(&renameOld, "rename", "", "rename this tag (use with --to)")
	f.StringVar(&renameTo, "to", "", "new tag name (use with --rename)")
	return cmd
}
