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

	// Pre-compute STATUS column width so TYPE/TAGS stay aligned.
	statusW := len("STATUS")
	for _, d := range dirOrder {
		files := groups[d]
		dCounts := map[string]int{}
		for _, r := range files {
			dCounts[string(r.Status)]++
		}
		var rawSummary string
		if len(files) == 1 {
			rawSummary = statusLabel(files[0].Status)
		} else {
			var parts []string
			for _, st := range []core.FileStatusLabel{core.StatusSynced, core.StatusEncrypted, core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew} {
				if n := dCounts[string(st)]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", n, strings.ToLower(string(st))))
				}
			}
			rawSummary = strings.Join(parts, "  ·  ")
		}
		if l := len(rawSummary); l > statusW {
			statusW = l
		}
	}
	statusW += 2

	if showIDs {
		fmt.Printf("%s  %s  %s  %s  %s  %s\n", clrBold.Sprint(pad("PATH", dirW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint(pad("SLUG", idW)), clrBold.Sprint(pad("STATUS", statusW)), clrBold.Sprint(pad("TYPE", 6)), clrBold.Sprint("TAGS"))
	} else {
		fmt.Printf("%s  %s  %s  %s  %s\n", clrBold.Sprint(pad("PATH", dirW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint(pad("STATUS", statusW)), clrBold.Sprint(pad("TYPE", 6)), clrBold.Sprint("TAGS"))
	}
	divider()

	implicitTags := core.DetectPlatformTags()

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
		// TYPE column: how the file was tracked.
		isGroup := files[0].Group != ""
		typeStr := "file"
		switch {
		case files[0].HashOnly:
			typeStr = "hash"
		case files[0].LocalOnly:
			typeStr = "local"
		case isGroup:
			typeStr = "group"
		}
		typeCol := clrDim.Sprint(pad(typeStr, 6))

		// TAGS column: user-defined labels only.
		seen := make(map[string]bool)
		var rowTags []string
		for _, f := range files {
			for _, t := range f.Tags {
				if !seen[t] {
					seen[t] = true
					rowTags = append(rowTags, t)
				}
			}
		}
		displayTags := rowTags
		if len(displayTags) == 0 {
			displayTags = implicitTags
		}
		tagsCol := clrInfo.Sprint(strings.Join(displayTags, ","))

		pathCol := pad(rowLabel, dirW)
		hashCol := clrDim.Sprint(pad(rowHash, hashW))
		if dirDirty {
			if showIDs {
				fmt.Printf("%s  %s  %s  %s  %s  %s\n", clrDirty.Sprint(pathCol), hashCol, clrDim.Sprint(pad(rowSlug, idW)), padVisible(summary, statusW), typeCol, tagsCol)
			} else {
				fmt.Printf("%s  %s  %s  %s  %s\n", clrDirty.Sprint(pathCol), hashCol, padVisible(summary, statusW), typeCol, tagsCol)
			}
		} else {
			if showIDs {
				fmt.Printf("%s  %s  %s  %s  %s  %s\n", clrBold.Sprint(pathCol), hashCol, clrDim.Sprint(pad(rowSlug, idW)), padVisible(summary, statusW), typeCol, tagsCol)
			} else {
				fmt.Printf("%s  %s  %s  %s  %s\n", clrBold.Sprint(pathCol), hashCol, padVisible(summary, statusW), typeCol, tagsCol)
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

				formatMetaDrift(&r, "    ")
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

// flatTypeRank returns a sort key for TYPE so file < group < local < hash.
func flatTypeRank(r core.FileStatusResult) int {
	switch {
	case r.HashOnly:
		return 3
	case r.LocalOnly:
		return 2
	case r.Group != "":
		return 1
	default:
		return 0
	}
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
		fmt.Printf("%s  %s  %s  %s  %s  %s\n", clrBold.Sprint(pad("PATH", pathW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint(pad("SLUG", slugW)), clrBold.Sprint(pad("STATUS", stW)), clrBold.Sprint(pad("TYPE", 6)), clrBold.Sprint("TAGS"))
	} else {
		fmt.Printf("%s  %s  %s  %s  %s\n", clrBold.Sprint(pad("PATH", pathW)), clrBold.Sprint(pad("HASH", hashW)), clrBold.Sprint(pad("STATUS", stW)), clrBold.Sprint(pad("TYPE", 6)), clrBold.Sprint("TAGS"))
	}
	divider()

	implicitTags := core.DetectPlatformTags()
	totals := map[string]int{}
	for _, r := range results {
		label := statusLabel(r.Status)
		switch r.Status {
		case core.StatusDirty, core.StatusPending, core.StatusMissing, core.StatusNew:
			hasDiff = true
		}
		totals[string(r.Status)]++

		typeStr := "file"
		switch {
		case r.HashOnly:
			typeStr = "hash"
		case r.LocalOnly:
			typeStr = "local"
		case r.Group != "":
			typeStr = "group"
		}
		typeCol := clrDim.Sprint(pad(typeStr, 6))

		tags := r.Tags
		if len(tags) == 0 {
			tags = implicitTags
		}
		tagsCol := clrInfo.Sprint(strings.Join(tags, ","))

		if showIDs {
			fmt.Printf("%s  %s  %s  %s  %s  %s\n", pad(r.SystemPath, pathW), clrDim.Sprint(pad(r.ID, hashW)), clrDim.Sprint(pad(r.Slug, slugW)), statusColored(r.Status, pad(label, stW)), typeCol, tagsCol)
		} else {
			fmt.Printf("%s  %s  %s  %s  %s\n", pad(r.SystemPath, pathW), clrDim.Sprint(pad(r.ID, hashW)), statusColored(r.Status, pad(label, stW)), typeCol, tagsCol)
		}

		formatMetaDrift(&r, "   ")
	}

	divider()
	var sp []string
	sp = append(sp, clrBold.Sprintf("%d files", len(results)))
	if n := totals[string(core.StatusSynced)]; n > 0 {
		sp = append(sp, clrSynced.Sprintf("%d synced", n))
	}
	if n := totals[string(core.StatusDirty)]; n > 0 {
		sp = append(sp, clrDirty.Sprintf("%d dirty", n))
	}
	if n := totals[string(core.StatusPending)]; n > 0 {
		sp = append(sp, clrPending.Sprintf("%d pending", n))
	}
	if n := totals[string(core.StatusMissing)]; n > 0 {
		sp = append(sp, clrMissing.Sprintf("%d missing", n))
	}
	if n := totals[string(core.StatusEncrypted)]; n > 0 {
		sp = append(sp, clrEncrypted.Sprintf("%d encrypted", n))
	}
	if n := totals[string(core.StatusNew)]; n > 0 {
		sp = append(sp, clrNew.Sprintf("%d new", n))
	}
	fmt.Printf("  %s\n", strings.Join(sp, clrDim.Sprint("  ·  ")))
	return hasDiff
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
			results = filterByTags(results, tags)
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

