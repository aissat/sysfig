package main

import (
	"fmt"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/fatih/color"
)

// ── UI helpers ────────────────────────────────────────────────────────────────

var (
	clrOK   = color.New(color.FgGreen, color.Bold)
	clrWarn = color.New(color.FgYellow, color.Bold)
	clrErr  = color.New(color.FgRed, color.Bold)
	clrInfo = color.New(color.FgCyan)
	clrDim  = color.New(color.Faint)
	clrBold = color.New(color.Bold)

	clrSynced    = color.New(color.FgGreen)
	clrDirty     = color.New(color.FgYellow)
	clrPending   = color.New(color.FgBlue)
	clrMissing   = color.New(color.FgRed)
	clrEncrypted = color.New(color.FgMagenta)
	clrNew       = color.New(color.FgCyan)
)

func ok(format string, a ...interface{}) { fmt.Printf("  "+clrOK.Sprint("✓")+" "+format+"\n", a...) }
func warn(format string, a ...interface{}) {
	fmt.Printf("  "+clrWarn.Sprint("⚠")+"  "+format+"\n", a...)
}
func info(format string, a ...interface{}) {
	fmt.Printf("  "+clrInfo.Sprint("ℹ")+" "+format+"\n", a...)
}
func fail(format string, a ...interface{}) {
	fmt.Printf("  "+clrErr.Sprint("✗")+" "+format+"\n", a...)
}

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
	case core.StatusNew:
		return clrNew.Sprint(label)
	case core.StatusTampered:
		return clrErr.Sprint(label)
	case core.StatusStale:
		return clrDim.Sprint(label)
	default:
		return label
	}
}

func divider() { fmt.Println(clrDim.Sprint(strings.Repeat("─", 76))) }

func step(n int, label string) {
	fmt.Printf("  %s %s\n", clrBold.Sprintf("[%d]", n), clrBold.Sprint(label))
}

// pad returns s left-padded with spaces to at least width visible characters.
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// visibleLen returns the number of printable characters in s, stripping ANSI
// escape sequences (e.g. "\x1b[32m...\x1b[0m").
func visibleLen(s string) int {
	n := 0
	inEsc := false
	for i := 0; i < len(s); i++ {
		if inEsc {
			if s[i] == 'm' {
				inEsc = false
			}
			continue
		}
		if s[i] == '\x1b' {
			inEsc = true
			continue
		}
		n++
	}
	return n
}

// padVisible pads s to visible width w, ignoring ANSI escape sequences.
func padVisible(s string, w int) string {
	vl := visibleLen(s)
	if vl >= w {
		return s
	}
	return s + strings.Repeat(" ", w-vl)
}

// filterByTags returns the subset of results that carry at least one of the
// requested tags. Falls back to DetectPlatformTags() for untagged files.
// Returns the full slice unchanged when tags is empty.
func filterByTags(results []core.FileStatusResult, tags []string) []core.FileStatusResult {
	if len(tags) == 0 {
		return results
	}
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}
	implicit := core.DetectPlatformTags()
	var out []core.FileStatusResult
	for _, r := range results {
		effective := r.Tags
		if len(effective) == 0 {
			effective = implicit
		}
		for _, t := range effective {
			if want[t] {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// statusLabel returns the display label for a status.
func statusLabel(s core.FileStatusLabel) string {
	switch s {
	case core.StatusDirty:
		return "DIRTY"
	case core.StatusPending:
		return "PENDING"
	default:
		return string(s)
	}
}

// friendlyErr translates raw core errors into actionable user-facing messages.
// It checks error strings for known patterns and returns a cleaner error; if no
// pattern matches the original error is returned unchanged.
func friendlyErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "resolve path"):
		return fmt.Errorf("file not found — check the path and try again\n\n  hint: use an absolute path, e.g. /etc/nginx/nginx.conf")
	case strings.Contains(msg, "is in the system denylist"):
		return fmt.Errorf("that path is in the protected denylist and cannot be tracked\n\n  hint: edit ~/.sysfig/denylist to allow it, or choose a different file")
	case strings.Contains(msg, "is not a regular file"):
		return fmt.Errorf("the path exists but is not a regular file (it may be a directory or device)\n\n  hint: to track a directory use:  sysfig track --recursive <dir>")
	case strings.Contains(msg, "already tracked"):
		return fmt.Errorf("this file is already being tracked\n\n  hint: run sysfig status to see all tracked files")
	case strings.Contains(msg, "master key not found"):
		return fmt.Errorf("no encryption key found — generate one first\n\n  hint: run sysfig keys generate")
	case strings.Contains(msg, "managed by source profile"):
		// pass through — message is already clear and includes --force hint
		return err
	case strings.Contains(msg, "no remote configured") || strings.Contains(msg, "remote URL"):
		return fmt.Errorf("no remote repository configured\n\n  hint: run sysfig remote add <url>  or  sysfig bootstrap <url>")
	default:
		return err
	}
}
