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
// remoteDisplayPath returns "remote-spec:/path" for remote files, plain path for local.
func remoteDisplayPath(r core.FileStatusResult) string {
	if r.Remote == "" {
		return r.SystemPath
	}
	return r.Remote + ":" + r.SystemPath
}

func groupResultsByDir(results []core.FileStatusResult) (order []string, groups map[string][]core.FileStatusResult) {
	groups = map[string][]core.FileStatusResult{}
	for _, r := range results {
		var dir string
		if r.Group != "" {
			// Files tracked via `sysfig track /dir/` fold under the group root.
			// Remote groups are namespaced by host so two hosts tracking the same
			// directory path never collapse into the same table row.
			if r.Remote != "" {
				dir = r.Remote + ":" + r.Group
			} else {
				dir = r.Group
			}
		} else if r.Remote != "" {
			// Remote files group by "remote-spec:/dir" so files from different
			// users or hosts never merge into the same table row.
			dir = r.Remote + ":" + filepath.Dir(r.SystemPath)
		} else {
			dir = filepath.Dir(r.SystemPath)
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
func printStatusTable(results []core.FileStatusResult) (hasDiff bool) {
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

	// Column widths — same as original layout + HASH column (always 8 chars).
	const hashW = 10 // "HASH" + 2 padding, 8-char ID fits comfortably
	dirW := len("PATH")
	for _, d := range dirOrder {
		files := groups[d]
		var label string
		if len(files) == 1 {
			label = remoteDisplayPath(files[0])
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
	for _, d := range dirOrder {
		files := groups[d]
		if l := len(healthSummary(groupCounts(files))) + 2; l > stateW {
			stateW = l
		}
		if l := len(groupTrackType(files)) + 2; l > trackW {
			trackW = l
		}
		if l := len(displayTags(compactTags(groupTags(files), 3), implicitTags)) + 2; l > tagsW {
			tagsW = l
		}
	}

	// Header: PATH  HASH  STATE  TRACK  TAGS  DETAILS
	fmt.Printf("%s  %s  %s  %s  %s  %s\n",
		clrBold.Sprint(pad("PATH", dirW)),
		clrBold.Sprint(pad("HASH", hashW)),
		clrBold.Sprint(pad("STATE", stateW)),
		clrBold.Sprint(pad("TRACK", trackW)),
		clrBold.Sprint(pad("TAGS", tagsW)),
		clrBold.Sprint("DETAILS"))
	divider()

	for _, dir := range dirOrder {
		files := groups[dir]
		dCounts := groupCounts(files)
		dirDirty := isGroupDegraded(dCounts)

		rowLabel := dir + "/"
		if len(files) == 1 {
			rowLabel = remoteDisplayPath(files[0])
		}

		// Hash: file ID for single-file rows, dir-derived ID for groups.
		rowHash := core.DeriveID(dir)
		if len(files) == 1 {
			rowHash = files[0].ID
		}

		stateCol := padVisible(groupStateColored(dCounts), stateW)
		trackCol := clrDim.Sprint(pad(groupTrackType(files), trackW))
		tagsCol := clrInfo.Sprint(pad(displayTags(compactTags(groupTags(files), 3), implicitTags), tagsW))
		detailsCol := groupDetails(files)

		if dirDirty {
			fmt.Printf("%s  %s  %s  %s  %s  %s\n",
				clrBold.Sprint(pad(rowLabel, dirW)),
				clrDim.Sprint(pad(rowHash, hashW)),
				stateCol, trackCol, tagsCol,
				clrDirty.Sprint(detailsCol))
		} else {
			fmt.Printf("%s  %s  %s  %s  %s  %s\n",
				clrBold.Sprint(pad(rowLabel, dirW)),
				clrDim.Sprint(pad(rowHash, hashW)),
				stateCol, trackCol, tagsCol,
				clrDim.Sprint(detailsCol))
		}

		// Expand changed sub-files (multi-file groups only) — original style.
		// Sub-filename is padded so the hash ID aligns under the HASH column.
		if dirDirty && len(files) > 1 {
			fmt.Printf("  %s\n", clrDim.Sprint("changed:"))
			subNameW := dirW - 4 // 4 = len("    ") indent
			for _, r := range files {
				if !isDegraded(r.Status) {
					continue
				}
				subName := filepath.Base(r.SystemPath)
				if r.Status == core.StatusNew {
					fmt.Printf("    %s\n", clrNew.Sprint(subName+"  → new, run sync to commit"))
					continue
				}
				fmt.Printf("    %s  %s\n",
					statusColored(r.Status, pad(subName, subNameW)),
					clrDim.Sprint(r.ID))
				formatMetaDrift(&r, "    ")
			}
		}
	}

	divider()
	printSummaryFooter(totals, len(results))
	return hasDiff
}

// isDegraded returns true when a file status needs attention.
func isDegraded(s core.FileStatusLabel) bool {
	switch s {
	case core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew, core.StatusTampered:
		return true
	}
	return false
}

// fileTypeStr returns the TYPE label for a file result.
func fileTypeStr(r core.FileStatusResult) string {
	switch {
	case r.Remote != "":
		return "remote"
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
	if n := totals[string(core.StatusStale)]; n > 0 {
		parts = append(parts, clrDim.Sprintf("%d stale", n))
	}
	fmt.Printf("  %s\n", strings.Join(parts, clrDim.Sprint("  ·  ")))
}

// printStatusFlat renders every tracked file as its own row.
func printStatusFlat(results []core.FileStatusResult) (hasDiff bool) {
	// Sort by type rank first, then alphabetically by system path.
	sort.Slice(results, func(i, j int) bool {
		ri, rj := flatTypeRank(results[i]), flatTypeRank(results[j])
		if ri != rj {
			return ri < rj
		}
		return results[i].SystemPath < results[j].SystemPath
	})

	pathW := len("PATH")
	hashW := len("HASH") + 2
	statusW := len("STATUS") + 2
	typeW := len("TYPE") + 2
	tagsW := len("TAGS") + 2
	implicitTags := core.DetectPlatformTags()
	for _, r := range results {
		if l := len(shortenHomePath(remoteDisplayPath(r))) + 2; l > pathW {
			pathW = l
		}
		if l := len(statusLabel(r.Status)) + 2; l > statusW {
			statusW = l
		}
		if l := len(fileTypeStr(r)) + 2; l > typeW {
			typeW = l
		}
		if l := len(displayTags(compactTags(r.Tags, 3), implicitTags)) + 2; l > tagsW {
			tagsW = l
		}
	}

	fmt.Printf("%s  %s  %s  %s  %s\n",
		clrBold.Sprint(pad("PATH", pathW)),
		clrBold.Sprint(pad("HASH", hashW)),
		clrBold.Sprint(pad("STATUS", statusW)),
		clrBold.Sprint(pad("TYPE", typeW)),
		clrBold.Sprint("TAGS"))
	divider()

	totals := map[string]int{}
	for _, r := range results {
		label := statusLabel(r.Status)
		switch r.Status {
		case core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew:
			hasDiff = true
		}
		totals[string(r.Status)]++

		typeCol := clrDim.Sprint(pad(fileTypeStr(r), typeW))
		tagsCol := clrInfo.Sprint(pad(displayTags(compactTags(r.Tags, 3), implicitTags), tagsW))
		var sourceCol string
		if r.Remote != "" {
			sourceCol = "  " + clrDim.Sprint("→ "+r.Remote)
		}
		fmt.Printf("%s  %s  %s  %s  %s%s\n",
			pad(shortenHomePath(remoteDisplayPath(r)), pathW),
			clrDim.Sprint(pad(r.ID, hashW)),
			statusColored(r.Status, pad(label, statusW)),
			typeCol,
			tagsCol,
			sourceCol)

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
		{core.StatusStale, "stale"},
	} {
		if n := counts[item.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, item.label))
		}
	}
	detail := strings.Join(parts, ", ")
	// Append remote source for single-file remote rows.
	if len(files) == 1 && files[0].Remote != "" {
		detail += "  → " + files[0].Remote
	}
	return detail
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
		baseDir     string
		sysRoot     string
		ids         []string
		tags        []string
		watchMode   bool
		interval    time.Duration
		flatFiles   bool
		fetchRemote bool
		showAll     bool
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

			results, err := core.StatusWithOptions(core.StatusOptions{
				BaseDir:     baseDir,
				IDs:         ids,
				SysRoot:     sysRoot,
				FetchRemote: fetchRemote,
			})
			if err != nil {
				return err
			}
			results = filterByTags(results, tags)
			if len(args) == 1 {
				results = filterByArg(results, args[0])
			}
			if !showAll {
				results = filterBySysfigHost(results)
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
	f.StringArrayVar(&tags, "tag", nil, "show only files with this tag (repeatable)")
	f.BoolVarP(&watchMode, "watch", "w", false, "continuously refresh status (Ctrl-C to stop)")
	f.BoolVarP(&flatFiles, "files", "f", false, "show every tracked file individually instead of grouping by directory")
	f.DurationVar(&interval, "interval", 3*time.Second, "refresh interval when --watch is set")
	f.BoolVar(&fetchRemote, "fetch", false, "re-fetch remote-tracked files via SSH to show live DIRTY/SYNCED status")
	f.BoolVarP(&showAll, "all", "a", false, "show all files regardless of $SYSFIG_HOST")
	return cmd
}

// filterBySysfigHost filters results based on SYSFIG_HOST:
//   - SYSFIG_HOST set → show only files tracked from that host
//   - SYSFIG_HOST not set → show everything (local + all remotes)
//   - --all / -a → bypass this filter entirely (same as no SYSFIG_HOST)
func filterBySysfigHost(results []core.FileStatusResult) []core.FileStatusResult {
	host := os.Getenv("SYSFIG_HOST")
	if host == "" {
		return results
	}
	filtered := results[:0]
	for _, r := range results {
		if r.Remote == host {
			filtered = append(filtered, r)
		}
	}
	return filtered
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
