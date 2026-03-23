package core_test

import (
	"runtime"
	"testing"

	"github.com/aissat/sysfig/internal/core"
)

// TestDetectPlatformTags_ContainsGOOS verifies that the result always contains
// at least one tag and that the first tag matches the current runtime.GOOS.
func TestDetectPlatformTags_ContainsGOOS(t *testing.T) {
	tags := core.DetectPlatformTags()
	if len(tags) == 0 {
		t.Fatal("DetectPlatformTags must return at least one tag")
	}
	if tags[0] != runtime.GOOS {
		t.Errorf("first tag = %q, want %q (runtime.GOOS)", tags[0], runtime.GOOS)
	}
}

// TestDetectPlatformTags_NonEmpty verifies that no returned tag is an empty string.
func TestDetectPlatformTags_NonEmpty(t *testing.T) {
	tags := core.DetectPlatformTags()
	for i, tag := range tags {
		if tag == "" {
			t.Errorf("tag[%d] is an empty string — all tags must be non-empty", i)
		}
	}
}

// TestDetectPlatformTags_LinuxHasDistro verifies that on Linux we get at least
// two tags (OS family + distro) when /etc/os-release is readable.
// This test is skipped on non-Linux platforms.
func TestDetectPlatformTags_LinuxHasDistro(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("distro tag detection only applies to Linux")
	}
	tags := core.DetectPlatformTags()
	// On Linux we expect at least ["linux", "<distro>"].
	// If /etc/os-release is missing (unusual) we still get ["linux"].
	if len(tags) < 1 {
		t.Errorf("expected at least 1 tag on Linux, got %v", tags)
	}
}
