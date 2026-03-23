package core_test

import (
	"testing"

	"github.com/aissat/sysfig/internal/core"
)

// TestSanitizeBranchName verifies that dot-prefixed path components are
// replaced with "dot-" so Git accepts them as ref names.
func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Plain paths — must be unchanged.
		{"etc/nginx/nginx.conf", "etc/nginx/nginx.conf"},
		{"home/user/config", "home/user/config"},
		// Dot-prefixed file at the end.
		{"home/alice/.zshrc", "home/alice/dot-zshrc"},
		// Dot-prefixed directory component.
		{"home/aye7/.config/nvim/init.lua", "home/aye7/dot-config/nvim/init.lua"},
		// Multiple dot-prefixed components.
		{"home/aye7/.config/.nvim", "home/aye7/dot-config/dot-nvim"},
		// Dot-prefixed first component (unusual but possible with a custom SysRoot).
		{".bashrc", "dot-bashrc"},
		// No dots at all.
		{"etc/hosts", "etc/hosts"},
		// Component that starts with "dot-" already — must not be double-escaped.
		{"home/user/dot-zshrc", "home/user/dot-zshrc"},
	}

	for _, tc := range tests {
		got := core.SanitizeBranchName(tc.input)
		if got != tc.want {
			t.Errorf("SanitizeBranchName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
