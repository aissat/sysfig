package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
		treeMode    bool
		dirtyOnly   bool
	)

	cmd := &cobra.Command{
		Use:   "log [system-path]",
		Short: "Show commit history with changed paths",
		Long: `Show git commit history. Each commit is expanded to one line per top-level
directory that changed, so you can see at a glance what was touched.

Filter to a specific path or ID:
  sysfig log /etc/pacman.d
  sysfig log --id 7734be1e
  sysfig log --dirty        show only files with uncommitted changes`,
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

			// --dirty: run a status check and restrict to branches of dirty files.
			// Any file whose status is DIRTY, TAMPERED, MISSING, or PENDING counts.
			var dirtyBranches []string
			if dirtyOnly {
				statResults, serr := core.Status(baseDir, nil, "")
				if serr != nil {
					return fmt.Errorf("log --dirty: status check: %w", serr)
				}
				seenDB := map[string]bool{}
				sm0 := state.NewManager(filepath.Join(baseDir, "state.json"))
				s0, _ := sm0.Load()
				for _, r := range statResults {
					switch r.Status {
					case core.StatusDirty, core.StatusTampered, core.StatusMissing, core.StatusPending:
					default:
						continue
					}
					if s0 != nil {
						if rec, ok := s0.Files[r.ID]; ok && rec.Branch != "" && !seenDB[rec.Branch] {
							dirtyBranches = append(dirtyBranches, rec.Branch)
							seenDB[rec.Branch] = true
						}
					}
				}
				if len(dirtyBranches) == 0 {
					info("No dirty files — everything is in sync.")
					return nil
				}
			}

			// ── Tree mode ─────────────────────────────────────────────────────────
			if treeMode {
				const (
					yellow = "\033[33m"
					cyan   = "\033[36m"
					reset  = "\033[0m"
					dim    = "\033[2m"
				)
				tw := termWidth()

				smT := state.NewManager(filepath.Join(baseDir, "state.json"))
				sT, errT := smT.Load()
				if errT != nil || sT == nil || len(sT.Files) == 0 {
					info("No tracked files.")
					return nil
				}

				type pathNode struct {
					name       string
					isDir      bool
					children   []*pathNode
					branch     string // for leaf nodes
					repoPath   string // empty for group-dir leaves
					isGroupDir bool   // true = group-tracked directory leaf
					groupPath  string // repo-relative group dir (e.g. "etc/pacman.d"), for subject shortening
				}

				root := &pathNode{name: "", isDir: true}

				var insertNode func(*pathNode, []string, *pathNode)
				insertNode = func(parent *pathNode, parts []string, leaf *pathNode) {
					if len(parts) == 1 {
						leaf.name = parts[0]
						parent.children = append(parent.children, leaf)
						return
					}
					dirName := parts[0]
					var dir *pathNode
					for _, c := range parent.children {
						if c.isDir && c.name == dirName {
							dir = c
							break
						}
					}
					if dir == nil {
						dir = &pathNode{name: dirName, isDir: true}
						parent.children = append(parent.children, dir)
					}
					insertNode(dir, parts[1:], leaf)
				}

				dirtySet := map[string]bool{}
				for _, b := range dirtyBranches {
					dirtySet[b] = true
				}

				// seenGroups deduplicates group-tracked directories so each group
				// branch appears exactly once in the tree (not once per file in it).
				seenGroups := map[string]bool{}

				for _, rec := range sT.Files {
					if rec.Branch == "" {
						continue
					}
					if filterPath != "" {
						sysFiltered := "/" + strings.TrimPrefix(filterPath, "/")
						base := strings.TrimSuffix(sysFiltered, "/")
						if !strings.HasPrefix(rec.SystemPath, base+"/") && rec.SystemPath != base {
							continue
						}
					}
					if dirtyOnly && !dirtySet[rec.Branch] {
						continue
					}

					if rec.Group != "" {
						// Group-tracked: insert ONE leaf for the directory, not per file.
						groupPath := strings.TrimPrefix(rec.Group, "/")
						if seenGroups[groupPath] {
							continue
						}
						seenGroups[groupPath] = true
						parts := strings.Split(groupPath, "/")
						leaf := &pathNode{
							isGroupDir: true,
							branch:     rec.Branch,
							groupPath:  groupPath,
						}
						insertNode(root, parts, leaf)
					} else {
						// Individually tracked file.
						parts := strings.Split(strings.TrimPrefix(rec.SystemPath, "/"), "/")
						leaf := &pathNode{
							branch:   rec.Branch,
							repoPath: rec.RepoPath,
						}
						insertNode(root, parts, leaf)
					}
				}

				if len(root.children) == 0 {
					info("No tracked files.")
					return nil
				}

				// Sort: dirs before files, alphabetical within each type.
				var sortTree func(*pathNode)
				sortTree = func(nd *pathNode) {
					sort.Slice(nd.children, func(i, j int) bool {
						ci, cj := nd.children[i], nd.children[j]
						if ci.isDir != cj.isDir {
							return ci.isDir
						}
						return ci.name < cj.name
					})
					for _, c := range nd.children {
						if c.isDir {
							sortTree(c)
						}
					}
				}
				sortTree(root)

				type commitInfo struct {
					hash    string
					date    string
					subject string
					adds    int
					dels    int
					paths   []string // files touched (from --numstat)
				}

				fetchCommits := func(branch, path string) []commitInfo {
					args := []string{"--no-pager", "--git-dir=" + repoDir,
						"log", branch, "--pretty=tformat:%h\t%ad\t%s",
						"--numstat", "--date=format:%Y-%m-%d",
					}
					if n > 0 {
						args = append(args, fmt.Sprintf("-n%d", n))
					}
					if path != "" {
						args = append(args, "--", path)
					}
					raw, _ := exec.Command("git", args...).Output()

					// isHexStr detects a git short-hash (all lowercase hex, ≥4 chars).
					// Used to distinguish commit-header lines from numstat lines.
					isHexStr := func(s string) bool {
						if len(s) < 4 {
							return false
						}
						for _, c := range s {
							if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
								return false
							}
						}
						return true
					}

					var out []commitInfo
					var cur *commitInfo
					for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
						if line == "" {
							continue
						}
						ps := strings.SplitN(line, "\t", 3)
						if len(ps) >= 3 && isHexStr(ps[0]) {
							// New commit header: hash \t date \t subject
							if cur != nil {
								out = append(out, *cur)
							}
							cur = &commitInfo{hash: ps[0], date: ps[1], subject: ps[2]}
						} else if cur != nil && len(ps) >= 3 {
							// Numstat line: adds \t dels \t path
							a, _ := strconv.Atoi(ps[0])
							d, _ := strconv.Atoi(ps[1])
							cur.adds += a
							cur.dels += d
							if ps[2] != "" {
								cur.paths = append(cur.paths, ps[2])
							}
						}
					}
					if cur != nil {
						out = append(out, *cur)
					}
					return out
				}

				var renderNode func(*pathNode, string, bool)

				renderLeaf := func(fn *pathNode, prefix string, isLast bool) {
					connector := "├── "
					cont := "│   "
					if isLast {
						connector = "└── "
						cont = "    "
					}
					// Group-dir leaves show a trailing "/" to look like directories.
					nameSuffix := ""
					if fn.isGroupDir {
						nameSuffix = "/"
					}
					commits := fetchCommits(fn.branch, fn.repoPath)
					tipStr := ""
					if len(commits) > 0 {
						tipStr = " @ " + yellow + commits[0].hash + reset
					}
					fmt.Printf("%s%s%s%s  %s%s\n",
						prefix, connector, fn.name, nameSuffix,
						dim+"["+fn.branch+"]"+reset,
						tipStr)

					// shortenSubject rebuilds a cleaner subject for group-dir commits.
					// It strips the group path prefix from files listed in numstat and
					// shows the basenames (abbreviated when >1 file changed).
					//
					// "sysfig: update etc/pacman.d" + numstat [mirrorlist]
					//   → "sysfig: update mirrorlist"
					// "sysfig: update etc/pacman.d" + numstat [mirrorlist, mirrorlist.bak, ...]
					//   → "sysfig: update (mirrorlist, +4 others...)"
					shortenSubject := func(c commitInfo) string {
						if fn.groupPath == "" || len(c.paths) == 0 {
							return c.subject
						}
						gpfx := fn.groupPath + "/"
						var names []string
						for _, p := range c.paths {
							if after := strings.TrimPrefix(p, gpfx); after != p && after != "" {
								names = append(names, after)
							}
						}
						if len(names) == 0 {
							return c.subject
						}
						// Extract verb: everything in the subject before the group path
						verb := c.subject
						if idx := strings.Index(c.subject, fn.groupPath); idx >= 0 {
							verb = strings.TrimRight(c.subject[:idx], " ,")
						}
						if len(names) == 1 {
							return verb + " " + names[0]
						}
						return verb + " (" + names[0] + ", +" + strconv.Itoa(len(names)-1) + " others...)"
					}

					for i, c := range commits {
						bullet := "●"
						if i == 0 {
							bullet = "◉"
						}
						bar := "┃"
						if i == len(commits)-1 {
							bar = "┗"
						}
						statsStr := ""
						if c.adds > 0 || c.dels > 0 {
							statsStr = " " + dim + "[+" + strconv.Itoa(c.adds) + "/-" + strconv.Itoa(c.dels) + "]" + reset
						}
						line := fmt.Sprintf("%s%s %s %s%s%s (%s%s%s) %s%s",
							prefix+cont, bar,
							bullet,
							yellow, c.hash, reset,
							dim, c.date, reset,
							shortenSubject(c),
							statsStr)
						fmt.Println(fitANSI(line, tw))
					}
					if !isLast {
						fmt.Println(prefix + cont)
					}
				}

				renderNode = func(pn *pathNode, prefix string, isLast bool) {
					if !pn.isDir {
						renderLeaf(pn, prefix, isLast)
						return
					}
					connector := "├── "
					cont := "│   "
					if isLast {
						connector = "└── "
						cont = "    "
					}
					fmt.Printf("%s%s%s/\n", prefix, connector, pn.name)
					for i, child := range pn.children {
						renderNode(child, prefix+cont, i == len(pn.children)-1)
					}
				}

				fmt.Println("/ (sysfig root)")
				for i, child := range root.children {
					renderNode(child, "", i == len(root.children)-1)
				}
				return nil
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
			} else if dirtyOnly {
				listArgs = append(listArgs, dirtyBranches...)
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
				// Show only branches of currently-tracked files so stale/orphan
				// branches from previously-untracked files don't appear.
				smAll := state.NewManager(filepath.Join(baseDir, "state.json"))
				var allBranches []string
				seenAll := map[string]bool{}
				if sAll, err := smAll.Load(); err == nil {
					for _, rec := range sAll.Files {
						if rec.Branch != "" && !seenAll[rec.Branch] {
							allBranches = append(allBranches, rec.Branch)
							seenAll[rec.Branch] = true
						}
					}
				}
				if len(allBranches) > 0 {
					listArgs = append(listArgs, allBranches...)
				} else {
					// Empty state — nothing to show.
					info("No tracked files.")
					return nil
				}
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

			// branchLaneKey maps a branch ref to a path lane.
			// Every distinct file/group gets its own lane; parent directories
			// serve as virtual trunk lanes computed by laneParent().
			//
			// Examples:
			//   track/etc/hostname              -> "etc/hostname"
			//   track/etc/pacman.d              -> "etc/pacman.d"
			//   track/home/aye7/dot-zshrc       -> "home/aye7"
			//   track/tmp/sysfig-test/app       -> "tmp/sysfig-test/app"
			//   track/tmp/sysfig-indiv/one.conf -> "tmp/sysfig-indiv/one.conf"
			branchLaneKey := func(branch string) string {
				if branch == "manifest" {
					return "manifest"
				}

				kind := ""
				rel := branch
				switch {
				case strings.HasPrefix(branch, "track/"):
					kind = "track"
					rel = strings.TrimPrefix(branch, "track/")
				case strings.HasPrefix(branch, "local/"):
					kind = "local"
					rel = strings.TrimPrefix(branch, "local/")
				case strings.HasPrefix(branch, "remote/"):
					kind = "remote"
					rel = strings.TrimPrefix(branch, "remote/")
				default:
					return branch
				}

				parts := strings.Split(rel, "/")
				for i, p := range parts {
					if strings.HasPrefix(p, "dot-") {
						parts[i] = "." + p[4:]
					}
				}
				if len(parts) == 0 {
					return rel
				}

				if kind == "remote" {
					// remote/<host>/<path...>
					if len(parts) <= 1 {
						return "remote"
					}
					host := parts[0]
					pathParts := parts[1:]
					switch {
					case len(pathParts) >= 2 && pathParts[0] == "home":
						return "remote/" + host + "/home/" + pathParts[1]
					case len(pathParts) >= 3 && pathParts[0] == "tmp":
						// tmp/<suite>/<child>
						// If there is only one leaf file under tmp/<suite>, keep it on
						// the suite lane; only branch when there is a child subtree.
						if len(pathParts) >= 4 {
							return "remote/" + host + "/tmp/" + pathParts[1] + "/" + pathParts[2]
						}
						return "remote/" + host + "/tmp/" + pathParts[1]
					case len(pathParts) >= 3:
						// /etc/pacman.d, /var/lib, /usr/local ...
						return "remote/" + host + "/" + pathParts[0] + "/" + pathParts[1]
					case len(pathParts) >= 1:
						// plain file under root -> stay on root lane
						return "remote/" + host + "/" + pathParts[0]
					default:
						return "remote/" + host
					}
				}

				switch parts[0] {
				case "home":
					if len(parts) >= 2 {
						return "home/" + parts[1]
					}
				case "tmp":
					if len(parts) >= 3 {
						return "tmp/" + parts[1] + "/" + parts[2]
					}
					if len(parts) >= 2 {
						return "tmp/" + parts[1]
					}
				default:
					// Each file/group gets its own lane so it branches from the
					// root trunk (e.g. "etc/hostname" branches from "etc" trunk).
					if len(parts) >= 2 {
						return parts[0] + "/" + parts[1]
					}
				}
				return parts[0]
			}

			laneParent := func(lk string) string {
				if lk == "" || lk == "manifest" {
					return ""
				}
				parts := strings.Split(lk, "/")
				if len(parts) <= 1 {
					return ""
				}
				if parts[0] == "remote" {
					switch len(parts) {
					case 2:
						return ""
					case 3:
						return "remote/" + parts[1]
					case 4:
						return "remote/" + parts[1] + "/" + parts[2]
					default:
						return strings.Join(parts[:len(parts)-1], "/")
					}
				}
				switch len(parts) {
				case 2:
					return parts[0]
				default:
					return strings.Join(parts[:len(parts)-1], "/")
				}
			}

			// Pre-build hash→lane map by walking every relevant branch.
			hashToLane := map[string]string{}
			if graphMode {
				branchListOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
					"for-each-ref", "--format=%(refname:short)", "refs/heads/").Output()
				for b := range strings.SplitSeq(strings.TrimSpace(string(branchListOut)), "\n") {
					b = strings.TrimSpace(b)
					if !strings.HasPrefix(b, "track/") &&
						!strings.HasPrefix(b, "local/") &&
						!strings.HasPrefix(b, "remote/") &&
						b != "manifest" {
						continue
					}
					lane := branchLaneKey(b)
					commitListOut, _ := exec.Command("git", "--no-pager", "--git-dir="+repoDir,
						"log", b, "--format=%H").Output()
					for h := range strings.SplitSeq(strings.TrimSpace(string(commitListOut)), "\n") {
						h = strings.TrimSpace(h)
						if h != "" {
							if hashToLane[h] == "" {
								hashToLane[h] = lane
							}
							if len(h) >= 7 && hashToLane[h[:7]] == "" {
								hashToLane[h[:7]] = lane
							}
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
					branchName: hashToLane[hash],
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
				// Build a forest of lanes in first-seen order so the graph stays
				// chronological while still showing parent/child path branches.
				children := map[string][]string{}
				seenLane := map[string]bool{}
				var rootOrder []string
				var addLane func(string)
				addLane = func(lk string) {
					if lk == "" || seenLane[lk] {
						return
					}
					parent := laneParent(lk)
					if parent != "" {
						addLane(parent)
						children[parent] = append(children[parent], lk)
					} else {
						rootOrder = append(rootOrder, lk)
					}
					seenLane[lk] = true
				}
				for _, r := range rows {
					if r.branchName != "" {
						addLane(r.branchName)
					}
				}

				var sortedLanes []string
				var walk func(string)
				walk = func(lk string) {
					sortedLanes = append(sortedLanes, lk)
					for _, child := range children[lk] {
						walk(child)
					}
				}
				for _, root := range rootOrder {
					walk(root)
				}

				laneIdx := map[string]int{}
				for i, lk := range sortedLanes {
					laneIdx[lk] = i
				}

				// Lane activity: a lane shows │ strictly between its OWN first
				// and last commit rows. Parent spans are NOT extended by children —
				// that would make the parent │ column persist after its own commits
				// finish, which looks wrong.
				firstRowOf := map[string]int{}
				lastRowOf := map[string]int{}
				tipOf := map[string]bool{}
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

				// fillSpan returns the row span of a lane including all its
				// descendant lanes. Used both for the legend and for trunk rendering.
				var fillSpan func(string) (int, int, bool)
				fillSpan = func(lk string) (int, int, bool) {
					first, last, ok := firstRowOf[lk], lastRowOf[lk], false
					if _, has := firstRowOf[lk]; has {
						ok = true
					}
					for _, child := range children[lk] {
						cFirst, cLast, cOK := fillSpan(child)
						if !cOK {
							continue
						}
						if !ok || cFirst < first {
							first = cFirst
						}
						if !ok || cLast > last {
							last = cLast
						}
						ok = true
					}
					return first, last, ok
				}
				// tFirst/tLast: row span of each root trunk (encompassing all children).
				tFirst := map[string]int{}
				tLast := map[string]int{}
				for _, root := range rootOrder {
					f, l, ok := fillSpan(root)
					if ok {
						tFirst[root] = f
						tLast[root] = l
					}
				}

				// legend: one entry per lane, two columns wide.
				{
					sepW := min(tw-1, 72)
					sep := dim + strings.Repeat("─", sepW) + reset
					fmt.Println(sep)
					// Derive colW from the separator width so that both columns
					// together fit within the separator line and the legend doesn't
					// sprawl across the full (possibly very wide) terminal.
					colW := (sepW - 2) / 2
					col := 0
					for i, lk := range sortedLanes {
						color := laneColors[i%len(laneColors)]
						indent := strings.Repeat("  ", len(strings.Split(lk, "/"))-1)
						label := "/" + strings.ReplaceAll(lk, "remote/", "@")
						entry := color + "●" + reset + " " + indent + label
						if col == 0 {
							fmt.Print("  " + padVisible(entry, colW))
							col = 1
						} else {
							fmt.Println(entry)
							col = 0
						}
					}
					if col != 0 {
						fmt.Println()
					}
					fmt.Println(sep)
				}

				// buildPfx — compact trunk-only graph.
				//
				// One 2-char column per ROOT lane (home, etc, manifest, …).
				// The commit dot appears AFTER all root columns at a fixed position:
				//
				//   │   root active, no commit from this root this row
				//       root not yet started or already finished
				//   ╭─  root's first row in its span
				//   ├─  root's middle row in its span
				//   ╰─  root's last row in its span
				//
				// Then the dot: ◉ for the tip commit of a file lane, ● otherwise.
				// This keeps the dot at column (2 × numActiveRoots) regardless of
				// which root is committing — hashes always start at the same column.
				buildPfx := func(rowIdx int, commitLane string) string {
					var sb strings.Builder

					// Walk up from commit lane to find its root trunk.
					commitRoot := commitLane
					if commitRoot != "" {
						for laneParent(commitRoot) != "" {
							commitRoot = laneParent(commitRoot)
						}
					}

					for _, root := range rootOrder {
						rFirst, ok := tFirst[root]
						if !ok || rowIdx < rFirst || rowIdx > tLast[root] {
							sb.WriteString("  ")
							continue
						}
						rIdx := laneIdx[root]
						rColor := laneColors[rIdx%len(laneColors)]

						if root != commitRoot {
							sb.WriteString(rColor + "│ " + reset)
							continue
						}

						// Committing root — show the arc connector (joint + dash).
						isFirst := rFirst == rowIdx
						isLast := tLast[root] == rowIdx

						var joint string
						switch {
						case isFirst:
							joint = "╭"
						case isLast:
							joint = "╰"
						default:
							joint = "├"
						}
						sb.WriteString(rColor + joint + "─" + reset)
					}

					// Dot always after all root columns, colored by file lane.
					if commitLane != "" {
						cIdx := laneIdx[commitLane]
						cColor := laneColors[cIdx%len(laneColors)]
						dot := "●"
						if tipOf[commitLane] {
							dot = "◉"
							tipOf[commitLane] = false
						}
						sb.WriteString(cColor + dot + reset)
					}

					sb.WriteString(" ")
					return sb.String()
				}

				for i, r := range rows {
					if r.connector != "" {
						continue
					}
					printRow(buildPfx(i, r.branchName), r.hash, r.date, r.pathLabel, r.subject, r.statLabel, r.decoration)
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
	f.BoolVarP(&treeMode, "tree", "t", false, "show file-centric tree view with per-file commit history")
	f.BoolVarP(&dirtyOnly, "dirty", "d", false, "show only files with uncommitted changes")
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
