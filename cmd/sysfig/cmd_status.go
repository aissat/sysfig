package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
)

// formatMetaDrift prints owner/group/mode drift lines for a FileStatusResult.
// The indent parameter is the leading whitespace (differs between table and flat views).
func formatMetaDrift(r *core.FileStatusResult, indent string) {
	if !r.MetaDrift || r.RecordedMeta == nil || r.CurrentMeta == nil {
		return
	}
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
		fmt.Printf("%s%s owner: %s → %s\n",
			indent,
			clrWarn.Sprint("⚠"),
			clrDim.Sprintf("%s:%s", recOwner, recGroup),
			clrDirty.Sprintf("%s:%s", curOwner, curGroup))
	}
	if rec.Mode != cur.Mode {
		fmt.Printf("%s%s mode:  %s → %s\n",
			indent,
			clrWarn.Sprint("⚠"),
			clrDim.Sprintf("%04o", rec.Mode),
			clrDirty.Sprintf("%04o", cur.Mode))
	}
}

// ── status ────────────────────────────────────────────────────────────────────

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
	implicitTags := core.DetectPlatformTags()

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

	stateW := len("STATE") + 2
	trackW := len("TRACK") + 2
	tagsW := len("TAGS") + 2
	detailsW := len("DETAILS")
	for _, d := range dirOrder {
		files := groups[d]
		if l := len(healthSummary(groupCounts(files))); l+2 > stateW {
			stateW = l + 2
		}
		if l := len(groupTrackType(files)); l+2 > trackW {
			trackW = l + 2
		}
		if l := len(displayTags(compactTags(groupTags(files), 3), implicitTags)); l+2 > tagsW {
			tagsW = l + 2
		}
		if l := len(groupDetails(files)); l > detailsW {
			detailsW = l
		}
	}

	fmt.Printf("%s  %s  %s  %s  %s\n",
		clrBold.Sprint(pad("PATH", dirW)),
		clrBold.Sprint(pad("STATE", stateW)),
		clrBold.Sprint(pad("TRACK", trackW)),
		clrBold.Sprint(pad("TAGS", tagsW)),
		clrBold.Sprint("DETAILS"))
	divider()

	for _, dir := range dirOrder {
		files := groups[dir]
		dirDisplay := dir + "/"
		dCounts := groupCounts(files)
		dirDirty := isGroupDegraded(dCounts)

		// Single file in this dir: show the full path instead of the folder.
		rowLabel := dirDisplay
		if len(files) == 1 {
			rowLabel = files[0].SystemPath
		}

		stateCol := padVisible(groupStateColored(dCounts), stateW)
		trackCol := clrDim.Sprint(pad(groupTrackType(files), trackW))
		tagsCol := clrInfo.Sprint(pad(displayTags(compactTags(groupTags(files), 3), implicitTags), tagsW))
		detailsCol := groupDetails(files)
		pathCol := pad(rowLabel, dirW)

		if dirDirty {
			fmt.Printf("%s  %s  %s  %s  %s\n",
				clrBold.Sprint(pathCol),
				stateCol,
				trackCol,
				tagsCol,
				clrDirty.Sprint(detailsCol))
		} else {
			fmt.Printf("%s  %s  %s  %s  %s\n",
				clrBold.Sprint(pathCol),
				stateCol,
				trackCol,
				tagsCol,
				clrDim.Sprint(detailsCol))
		}

		// Expand changed files under the dir (skip if single-file row — it's already shown inline).
		if dirDirty && len(files) > 1 {
			fmt.Printf("  %s\n", clrDim.Sprint("changed:"))
			for _, r := range files {
				switch r.Status {
				case core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew:
				default:
					continue
				}
				subName := filepath.Base(r.SystemPath)
				if r.Status == core.StatusNew {
					fmt.Printf("    %s\n", clrNew.Sprint(subName+"  → track required"))
					continue
				}
				fmt.Printf("    %s\n", statusColored(r.Status, subName))

				formatMetaDrift(&r, "    ")
			}
		}
	}

	divider()
	printSummaryFooter(totals, len(results))
	return hasDiff
}

// fileTypeStr returns the TYPE label for a file result.
func fileTypeStr(r core.FileStatusResult) string {
	switch {
	case r.HashOnly:
		return "hash"
	case r.LocalOnly:
		return "local"
	case r.Group != "":
		return "group"
	default:
		return "file"
	}
}

// flatTypeRank returns a sort key for TYPE so file < group < local < hash.
func flatTypeRank(r core.FileStatusResult) int {
	switch fileTypeStr(r) {
	case "group":
		return 1
	case "local":
		return 2
	case "hash":
		return 3
	default:
		return 0
	}
}

// filterByArg filters results by a positional argument that is either:
//   - a path prefix (starts with "/") → keeps files whose SystemPath has that prefix
//   - a hash prefix (hex chars only)  → keeps files whose ID starts with the arg
func filterByArg(results []core.FileStatusResult, arg string) []core.FileStatusResult {
	if arg == "" {
		return results
	}
	isPath := strings.HasPrefix(arg, "/")
	var out []core.FileStatusResult
	for _, r := range results {
		if isPath {
			if strings.HasPrefix(r.SystemPath, arg) {
				out = append(out, r)
			}
		} else {
			// Match file ID or the group/dir hash shown in the grouped table.
			groupKey := r.Group
			if groupKey == "" {
				groupKey = filepath.Dir(r.SystemPath)
			}
			groupHash := core.DeriveID(groupKey)
			if strings.HasPrefix(r.ID, arg) || strings.HasPrefix(groupHash, arg) {
				out = append(out, r)
			}
		}
	}
	return out
}

// displayTags returns user-defined tags falling back to platform tags.
func displayTags(tags []string, implicit []string) string {
	t := tags
	if len(t) == 0 {
		t = implicit
	}
	return strings.Join(t, ",")
}

// printSummaryFooter prints the "N files · N synced · …" summary line.
func printSummaryFooter(totals map[string]int, total int) {
	parts := []string{clrBold.Sprintf("%d files", total)}
	if n := totals[string(core.StatusSynced)]; n > 0 {
		parts = append(parts, clrSynced.Sprintf("%d synced", n))
	}
	if n := totals[string(core.StatusEncrypted)]; n > 0 {
		parts = append(parts, clrEncrypted.Sprintf("%d encrypted", n))
	}
	if n := totals[string(core.StatusDirty)]; n > 0 {
		parts = append(parts, clrDirty.Sprintf("%d dirty", n))
	}
	if n := totals[string(core.StatusPending)]; n > 0 {
		parts = append(parts, clrPending.Sprintf("%d pending", n))
	}
	if n := totals[string(core.StatusMissing)]; n > 0 {
		parts = append(parts, clrMissing.Sprintf("%d missing", n))
	}
	if n := totals[string(core.StatusNew)]; n > 0 {
		parts = append(parts, clrNew.Sprintf("%d new", n))
	}
	fmt.Printf("  %s\n", strings.Join(parts, clrDim.Sprint("  ·  ")))
}

// printStatusFlat renders every tracked file as its own row.
func printStatusFlat(results []core.FileStatusResult, showIDs bool) (hasDiff bool) {
	// Sort by type rank first, then alphabetically by system path.
	sort.Slice(results, func(i, j int) bool {
		ri, rj := flatTypeRank(results[i]), flatTypeRank(results[j])
		if ri != rj {
			return ri < rj
		}
		return results[i].SystemPath < results[j].SystemPath
	})

	pathW := len("PATH")
	stW := len("STATE")
	trackW := len("TRACK") + 2
	tagsW := len("TAGS") + 2
	implicitTags := core.DetectPlatformTags()
	for _, r := range results {
		shortPath := shortenHomePath(r.SystemPath)
		if len(shortPath) > pathW {
			pathW = len(shortPath)
		}
		if len(statusLabel(r.Status)) > stW {
			stW = len(statusLabel(r.Status))
		}
		if l := len(displayTags(compactTags(r.Tags, 3), implicitTags)); l+2 > tagsW {
			tagsW = l + 2
		}
	}
	pathW += 2
	stW += 2

	fmt.Printf("%s  %s  %s  %s  %s\n",
		clrBold.Sprint(pad("PATH", pathW)),
		clrBold.Sprint(pad("STATE", stW)),
		clrBold.Sprint(pad("TRACK", trackW)),
		clrBold.Sprint(pad("TAGS", tagsW)),
		clrBold.Sprint("DETAILS"))
	divider()

	totals := map[string]int{}
	for _, r := range results {
		label := statusLabel(r.Status)
		switch r.Status {
		case core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew:
			hasDiff = true
		}
		totals[string(r.Status)]++

		trackCol := clrDim.Sprint(pad(fileTypeStr(r), trackW))
		tagsCol := clrInfo.Sprint(pad(displayTags(compactTags(r.Tags, 3), implicitTags), tagsW))
		details := strings.ToLower(label)
		if r.Status == core.StatusDirty || r.Status == core.StatusPending || r.Status == core.StatusMissing || r.Status == core.StatusTampered {
			details = "needs attention"
		}
		fmt.Printf("%s  %s  %s  %s  %s\n",
			pad(shortenHomePath(r.SystemPath), pathW),
			statusColored(r.Status, pad(label, stW)),
			trackCol,
			tagsCol,
			clrDim.Sprint(details))

		formatMetaDrift(&r, "   ")
	}

	divider()
	printSummaryFooter(totals, len(results))
	return hasDiff
}

func groupCounts(files []core.FileStatusResult) map[core.FileStatusLabel]int {
	counts := make(map[core.FileStatusLabel]int)
	for _, r := range files {
		counts[r.Status]++
	}
	return counts
}

func isGroupDegraded(counts map[core.FileStatusLabel]int) bool {
	return counts[core.StatusDirty]+counts[core.StatusPending]+counts[core.StatusMissing]+counts[core.StatusNew]+counts[core.StatusTampered] > 0
}

func healthSummary(counts map[core.FileStatusLabel]int) string {
	if isGroupDegraded(counts) {
		return "DEGRADED"
	}
	return "HEALTHY"
}

func groupStateColored(counts map[core.FileStatusLabel]int) string {
	label := healthSummary(counts)
	if label == "DEGRADED" {
		return clrDirty.Sprint(label)
	}
	return clrSynced.Sprint(label)
}

func groupTrackType(files []core.FileStatusResult) string {
	groupType := fileTypeStr(files[0])
	for _, f := range files[1:] {
		if fileTypeStr(f) != groupType {
			return "mixed"
		}
	}
	return groupType
}

func groupTags(files []core.FileStatusResult) []string {
	seen := make(map[string]bool)
	var tags []string
	for _, f := range files {
		for _, t := range f.Tags {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	return tags
}

func compactTags(tags []string, keep int) []string {
	if len(tags) <= keep || keep <= 0 {
		return tags
	}
	out := append([]string{}, tags[:keep]...)
	out = append(out, fmt.Sprintf("+%d", len(tags)-keep))
	return out
}

func groupDetails(files []core.FileStatusResult) string {
	counts := groupCounts(files)
	var parts []string
	for _, item := range []struct {
		status core.FileStatusLabel
		label  string
	}{
		{core.StatusDirty, "dirty"},
		{core.StatusPending, "pending"},
		{core.StatusMissing, "missing"},
		{core.StatusTampered, "tampered"},
		{core.StatusNew, "new"},
		{core.StatusSynced, "synced"},
		{core.StatusEncrypted, "encrypted"},
	} {
		if n := counts[item.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, item.label))
		}
	}
	return strings.Join(parts, ", ")
}

func shortenHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~/" + strings.TrimPrefix(path, home+"/")
	}
	return path
}

func newStatusCmd() *cobra.Command {
	var (
		baseDir   string
		sysRoot   string
		ids       []string
		tags      []string
		watchMode bool
		interval  time.Duration
		flatFiles bool
		showIDs   bool
	)

	cmd := &cobra.Command{
		Use:   "status [path|hash]",
		Short: "Show sync status of all tracked files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			if watchMode {
				return runStatusWatch(baseDir, sysRoot, ids, interval)
			}

			results, err := core.Status(baseDir, ids, sysRoot)
			if err != nil {
				return err
			}
			results = filterByTags(results, tags)
			if len(args) == 1 {
				results = filterByArg(results, args[0])
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
	f.StringArrayVar(&tags, "tag", nil, "show only files with this tag (repeatable)")
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
