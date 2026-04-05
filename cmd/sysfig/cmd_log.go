package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
	"github.com/spf13/cobra"
)

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
			} else if host := os.Getenv("SYSFIG_HOST"); host != "" {
				// SYSFIG_HOST is set and no explicit filter: show only branches
				// for files tracked from that host (remote/<hostname>/*).
				hostname := core.RemoteHostname(host)
				sm3 := state.NewManager(filepath.Join(baseDir, "state.json"))
				var hostBranches []string
				if s3, err3 := sm3.Load(); err3 == nil {
					seen := map[string]bool{}
					for _, rec := range s3.Files {
						if rec.Remote == host && rec.Branch != "" && !seen[rec.Branch] {
							hostBranches = append(hostBranches, rec.Branch)
							seen[rec.Branch] = true
						}
					}
				}
				if len(hostBranches) > 0 {
					listArgs = append(listArgs, hostBranches...)
				} else {
					// Fallback: ref prefix for the host.
					listArgs = append(listArgs, "--branches=remote/"+hostname+"/*")
				}
			} else {
				// Show all branches merged into one timeline.
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

			// Lane colors: 12 distinct ANSI colors (normal + bright variants)
			// so up to 12 branches have unique colors before cycling.
			laneColors := []string{
				"\033[32m",  // green
				"\033[33m",  // yellow
				"\033[36m",  // cyan
				"\033[35m",  // magenta
				"\033[34m",  // blue
				"\033[31m",  // red
				"\033[92m",  // bright green
				"\033[93m",  // bright yellow
				"\033[96m",  // bright cyan
				"\033[95m",  // bright magenta
				"\033[94m",  // bright blue
				"\033[91m",  // bright red
			}

			// Pre-build hash→branch map by walking every track/* and manifest branch.
			// This lets us assign a lane to every commit, not just branch tips.
			hashToBranch := map[string]string{}
			if graphMode {
				branchListOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"for-each-ref", "--format=%(refname:short)", "refs/heads/").Output()
				for b := range strings.SplitSeq(strings.TrimSpace(string(branchListOut)), "\n") {
					b = strings.TrimSpace(b)
					if !strings.HasPrefix(b, "track/") && b != "manifest" {
						continue
					}
					commitListOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
						"log", b, "--format=%H").Output()
					for h := range strings.SplitSeq(strings.TrimSpace(string(commitListOut)), "\n") {
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
				for f := range strings.SplitSeq(strings.TrimSpace(string(dtOut)), "\n") {
					if f == "" || f == "sysfig.yaml" {
						continue
					}
					if filterPath != "" && f != filterPath &&
						!strings.HasPrefix(f, strings.TrimSuffix(filterPath, "/")+"/") {
						continue
					}
					paths = append(paths, f)
				}

				const maxPath = 35
				pathLabel := ""
				if len(paths) == 1 {
					pathLabel = paths[0]
				} else if len(paths) > 1 {
					pathLabel = fmt.Sprintf("%s +%d", paths[0], len(paths)-1)
				}
				if len(pathLabel) > maxPath {
					pathLabel = pathLabel[:maxPath-1] + "…"
				}
				if vl := visibleLen(pathLabel); vl > maxPathLen {
					maxPathLen = vl
				}
				if maxPathLen > maxPath {
					maxPathLen = maxPath
				}

				// Diff stat.
				statLabel := ""
				nsOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"diff-tree", "--numstat", "--no-commit-id", hash).Output()
				if len(strings.TrimSpace(string(nsOut))) == 0 {
					nsOut, _ = exec.Command("git", "--no-pager", "--git-dir="+repoDir,
						"show", "--numstat", "--format=", hash).Output()
				}
				for sl := range strings.SplitSeq(strings.TrimSpace(string(nsOut)), "\n") {
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

			// ── Second pass: render ──────────────────────────────────────────

			// Shared row printer used by both modes.
			// Full line is truncated to terminal width to prevent wrapping.
			tw := termWidth()
			printRow := func(prefix, h, date, pathLabel, subject, statLabel, decoration string) {
				paddedPath := pathLabel
				if maxPathLen > 0 {
					if pad := maxPathLen - visibleLen(pathLabel); pad > 0 {
						paddedPath = pathLabel + strings.Repeat(" ", pad)
					}
				}
				decStr := ""
				if decoration != "" {
					decStr = "  " + green + "(" + decoration + ")" + reset
				}
				var line string
				if pathLabel == "" {
					line = fmt.Sprintf("%s%s%s%s %s%s%s  %s %s%s",
						prefix,
						yellow, h, reset,
						cyan, date, reset,
						subject, statLabel, decStr)
				} else {
					line = fmt.Sprintf("%s%s%s%s %s%s%s  %s%s%s  %s %s%s",
						prefix,
						yellow, h, reset,
						cyan, date, reset,
						magenta, paddedPath, reset,
						subject, statLabel, decStr)
				}
				fmt.Println(fitANSI(line, tw))
			}

			if graphMode {
				// Assign each branch a fixed lane (column), sorted for stability.
				branchSet := map[string]bool{}
				for _, r := range rows {
					if r.branchName != "" {
						branchSet[r.branchName] = true
					}
				}
				sortedBranches := make([]string, 0, len(branchSet))
				for b := range branchSet {
					sortedBranches = append(sortedBranches, b)
				}
				sort.Strings(sortedBranches)

				branchLane := map[string]int{}
				for li, b := range sortedBranches {
					branchLane[b] = li
				}

				// shortTrack converts a branch ref to a human-readable file path.
				// "track/home/aye7/dot-zshrc" → "home/aye7/.zshrc"
				// "manifest" or "local/etc/fstab" → kept as-is
				shortTrack := func(b string) string {
					b = strings.TrimPrefix(b, "track/")
					parts := strings.Split(b, "/")
					for i, p := range parts {
						if strings.HasPrefix(p, "dot-") {
							parts[i] = "." + p[4:]
						}
					}
					return strings.Join(parts, "/")
				}

				// Print legend: one row per lane, two columns side by side.
				colW := tw/2 - 2
				sep := dim + strings.Repeat("─", tw-1) + reset
				fmt.Println(sep)
				for i, b := range sortedBranches {
					color := laneColors[i%len(laneColors)]
					entry := color + "●" + reset + " " + shortTrack(b)
					if i%2 == 0 {
						fmt.Print("  " + padVisible(entry, colW))
					} else {
						fmt.Println(entry)
					}
				}
				if len(sortedBranches)%2 != 0 {
					fmt.Println()
				}
				fmt.Println(sep)

				// Lane activity: a lane shows │ between its first and last commit row.
				firstRowOf := map[string]int{}
				lastRowOf := map[string]int{}
				tipOf := map[string]bool{} // branch → true for the first (newest) commit
				for i, r := range rows {
					if r.branchName == "" {
						continue
					}
					if _, seen := firstRowOf[r.branchName]; !seen {
						firstRowOf[r.branchName] = i
						tipOf[r.branchName] = true
					}
					lastRowOf[r.branchName] = i
				}

				// buildPfx builds the lane prefix for a given row index and commit lane.
				// Each lane is 1 char wide (no trailing space) — minimises graph width.
				// A single space is appended at the end as separator from the commit info.
				buildPfx := func(rowIdx, commitLane int) string {
					var sb strings.Builder
					for li, branch := range sortedBranches {
						color := laneColors[li%len(laneColors)]
						fst, seen := firstRowOf[branch]
						lst := lastRowOf[branch]
						switch {
						case li == commitLane:
							sym := "●"
							if tipOf[branch] {
								sym = "◉"
								tipOf[branch] = false // only first occurrence is tip
							}
							sb.WriteString(color + sym + reset)
						case seen && rowIdx >= fst && rowIdx <= lst:
							sb.WriteString(color + "│" + reset)
						default:
							sb.WriteString(" ")
						}
					}
					sb.WriteString(" ") // separator
					return sb.String()
				}

				for i, r := range rows {
					if r.connector != "" {
						continue
					}
					commitLane := -1
					if r.branchName != "" {
						commitLane = branchLane[r.branchName]
					}
					printRow(buildPfx(i, commitLane), r.hash, r.date, r.pathLabel, r.subject, r.statLabel, r.decoration)
				}
			} else {
				for _, r := range rows {
					if r.connector != "" {
						continue
					}
					printRow("  ", r.hash, r.date, r.pathLabel, r.subject, r.statLabel, r.decoration)
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

