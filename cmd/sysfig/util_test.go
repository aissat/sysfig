package main

import (
	"testing"

	"github.com/aissat/sysfig/internal/core"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// parseRemotePath
// ---------------------------------------------------------------------------

func TestParseRemotePath(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		wantHost  string
		wantPath  string
		wantMatch bool
	}{
		// ── inline remote without port ───────────────────────────────────────
		{
			name:      "user@host:/path",
			arg:       "admin@web1:/etc/nginx/nginx.conf",
			wantHost:  "admin@web1",
			wantPath:  "/etc/nginx/nginx.conf",
			wantMatch: true,
		},
		{
			name:      "user@host:/single segment",
			arg:       "root@server:/etc/hostname",
			wantHost:  "root@server",
			wantPath:  "/etc/hostname",
			wantMatch: true,
		},
		// ── inline remote with port ──────────────────────────────────────────
		{
			name:      "user@host:port:/path",
			arg:       "admin@web1:2222:/etc/nginx/nginx.conf",
			wantHost:  "admin@web1:2222",
			wantPath:  "/etc/nginx/nginx.conf",
			wantMatch: true,
		},
		{
			name:      "user@host:22:/path (default port explicit)",
			arg:       "aye7@10.0.0.1:22:/etc/ssh/sshd_config",
			wantHost:  "aye7@10.0.0.1:22",
			wantPath:  "/etc/ssh/sshd_config",
			wantMatch: true,
		},
		// ── local paths — must NOT match ────────────────────────────────────
		{
			name:      "absolute local path",
			arg:       "/etc/nginx/nginx.conf",
			wantMatch: false,
		},
		{
			name:      "relative local path",
			arg:       "etc/nginx/nginx.conf",
			wantMatch: false,
		},
		{
			name:      "home dir tilde path",
			arg:       "~/.zshrc",
			wantMatch: false,
		},
		{
			name:      "dotfile relative path",
			arg:       ".zshrc",
			wantMatch: false,
		},
		// ── edge cases ───────────────────────────────────────────────────────
		{
			name:      "no @ sign",
			arg:       "host:/etc/path",
			wantMatch: false,
		},
		{
			name:      "@ but no :/ separator",
			arg:       "user@host",
			wantMatch: false,
		},
		{
			name:      "@ after :/ — not a remote spec",
			arg:       "/path/user@host",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, path, ok := core.ParseInlineRemote(tt.arg)
			assert.Equal(t, tt.wantMatch, ok, "match")
			if tt.wantMatch {
				assert.Equal(t, tt.wantHost, host, "host")
				assert.Equal(t, tt.wantPath, path, "path")
			}
		})
	}
}
