package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFitANSI_PadsVisibleWidth(t *testing.T) {
	s := "\x1b[31mred\x1b[0m"
	fitted := fitANSI(s, 8)
	assert.Equal(t, 8, visibleWidth(fitted))
	assert.True(t, strings.HasPrefix(fitted, "\x1b[31mred\x1b[0m"))
}

func TestRenderSideBySide_SeparatorStaysAlignedWithInlineANSI(t *testing.T) {
	diff := strings.Join([]string{
		"@@ -1,2 +1,2 @@",
		"-# When:      2026-03-22 14",
		"+# When:      2026-03-24",
		"-# Retrieved: 2026-03-22 14",
		"+# Retrieved: 2026-03-24",
		"",
	}, "\n")

	out := renderSideBySide(diff, 120)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	var sepCol int
	found := false
	for _, line := range lines {
		idx := strings.Index(line, " │ ")
		if idx < 0 {
			continue
		}
		col := visibleWidth(line[:idx])
		if !found {
			sepCol = col
			found = true
			continue
		}
		assert.Equal(t, sepCol, col, "separator column drifted: %q", line)
	}

	require.True(t, found, "expected at least one rendered side-by-side row")
}

func visibleWidth(s string) int {
	width := 0
	for i := 0; i < len(s); {
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
				i = end
				continue
			}
		}
		i++
		width++
	}
	return width
}
