package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// ── diff ──────────────────────────────────────────────────────────────────────

func newDiffCmd() *cobra.Command {
	var (
		baseDir    string
		sysRoot    string
		colorFlag  bool
		colorSet   bool
		ids        []string
		sideBySide bool
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
		lineNoW    = 4 // digits for line number
		gutterW    = 2 // " │" separator
		padBetween = 2 // gap between left and right panels
	)

	// ANSI codes (no fatih/color — direct codes keep it simple here).
	const (
		reset   = "\033[0m"
		bold    = "\033[1m"
		dim     = "\033[2m"
		red     = "\033[38;2;255;255;255m\033[48;2;139;0;0m" // white text, dark red bg
		green   = "\033[38;2;255;255;255m\033[48;2;0;100;0m" // white text, dark green bg
		dimLine = "\033[2m"
	)

	// Each panel gets half the width minus line-number and gutter columns.
	panelW := (totalWidth-padBetween)/2 - lineNoW - gutterW
	if panelW < 20 {
		panelW = 20
	}

	// Parse the unified diff into side-by-side rows.
	type row struct {
		leftNo   int // 0 = empty
		leftTxt  string
		leftChg  bool // removed line
		rightNo  int
		rightTxt string
		rightChg bool   // added line
		header   string // non-empty for @@ lines
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
			hdr := fitANSI(r.header, totalWidth-2)
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
		leftContent := fitANSI(leftTxt, panelW)
		rightContent := fitANSI(rightTxt, panelW)

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

// fitANSI pads or truncates s to exactly w visible columns while preserving
// ANSI escape sequences used for coloring inline diffs.
func fitANSI(s string, w int) string {
	if w <= 0 {
		return ""
	}

	var b strings.Builder
	visible := 0

	for i := 0; i < len(s) && visible < w; {
		if s[i] == '\x1b' {
			end := i + 1
			if end < len(s) && s[end] == '[' {
				end++
				for end < len(s) {
					c := s[end]
					end++
					if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
						break
					}
				}
				b.WriteString(s[i:end])
				i = end
				continue
			}
		}

		b.WriteByte(s[i])
		i++
		visible++
	}

	if visible < w {
		b.WriteString(strings.Repeat(" ", w-visible))
	}

	return b.String()
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
		hlRed   = "\033[48;2;139;0;0m\033[38;2;255;255;255m" // dark red bg
		hlGreen = "\033[48;2;0;100;0m\033[38;2;255;255;255m" // dark green bg
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
