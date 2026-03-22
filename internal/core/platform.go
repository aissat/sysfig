package core

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// DetectPlatformTags returns the auto-detected platform tags for the current
// machine. Two tags are returned when possible:
//   - OS family: "linux", "darwin", "freebsd", etc. (from runtime.GOOS)
//   - Distro:    "arch", "ubuntu", "debian", "fedora", etc. (Linux only,
//                from /etc/os-release ID= field)
//
// Example results:
//
//	Linux Arch:   ["linux", "arch"]
//	Linux Ubuntu: ["linux", "ubuntu"]
//	macOS:        ["darwin"]
func DetectPlatformTags() []string {
	tags := []string{runtime.GOOS}
	if distro := detectDistro(); distro != "" {
		tags = append(tags, distro)
	}
	return tags
}

// detectDistro reads /etc/os-release and returns the distro family.
// Prefers ID_LIKE (e.g. "arch" for Archcraft, "debian" for Ubuntu) so that
// derivative distros are grouped with their parent. Falls back to ID= when
// ID_LIKE is absent (e.g. pure Arch, Fedora, Alpine).
func detectDistro() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()

	var id, idLike string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ID_LIKE=") {
			idLike = strings.ToLower(strings.Trim(strings.TrimPrefix(line, "ID_LIKE="), `"'`))
			// ID_LIKE may be space-separated ("rhel fedora") — take the first.
			if i := strings.IndexByte(idLike, ' '); i >= 0 {
				idLike = idLike[:i]
			}
		} else if strings.HasPrefix(line, "ID=") {
			id = strings.ToLower(strings.Trim(strings.TrimPrefix(line, "ID="), `"'`))
		}
	}
	if idLike != "" {
		return idLike
	}
	return id
}
