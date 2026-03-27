//go:build linux

package core

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// newProcWatcher returns the best available procWatcher on Linux.
// Tries fanotify first (accurate, requires CAP_SYS_ADMIN); falls back to a
// /proc fd scan (best-effort, no privileges required).
func newProcWatcher(paths []string) procWatcher {
	if fa := tryFanotify(paths); fa != nil {
		return fa
	}
	return &procScanner{}
}

// ── fanotify watcher ──────────────────────────────────────────────────────────

type fanotifyWatcher struct {
	mu       sync.Mutex
	seen     map[string]ChangeInfo
	faFd     int
	epFd     int
	stopPipe [2]int // [0]=read end (O_NONBLOCK), [1]=write end
	stopped  chan struct{}
	fallback *procScanner
}

// tryFanotify attempts to create a fanotify-based watcher.
// Returns nil when fanotify is unavailable (no CAP_SYS_ADMIN, old kernel, etc.).
//
// Limitation: FAN_CLOSE_WRITE on a specific inode fires only for in-place
// writes. Editors that save atomically (write to a temp file then rename) will
// replace the inode, so no FAN_CLOSE_WRITE fires on the original mark.
// Those cases are handled by the embedded procScanner fallback.
func tryFanotify(paths []string) *fanotifyWatcher {
	faFd, err := unix.FanotifyInit(
		unix.FAN_CLASS_NOTIF|unix.FAN_CLOEXEC|unix.FAN_NONBLOCK,
		unix.O_RDONLY,
	)
	if err != nil {
		return nil
	}

	epFd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		unix.Close(faFd)
		return nil
	}

	var pipeFds [2]int
	if err := unix.Pipe2(pipeFds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		unix.Close(faFd)
		unix.Close(epFd)
		return nil
	}

	// Add both fds to epoll. We do NOT store fd numbers in the EpollEvent.Data
	// field because its layout differs across Linux architectures (packed on
	// amd64, padded on arm64). Instead, after any wakeup we check the stop
	// pipe non-blocking and then drain the fanotify fd.
	//
	// Both registrations must succeed: if the stop pipe is not in epoll,
	// Close() will write to it and block forever on w.stopped.
	cleanup := func() {
		unix.Close(faFd)
		unix.Close(epFd)
		unix.Close(pipeFds[0])
		unix.Close(pipeFds[1])
	}
	if err := unix.EpollCtl(epFd, unix.EPOLL_CTL_ADD, faFd, &unix.EpollEvent{Events: unix.EPOLLIN}); err != nil {
		cleanup()
		return nil
	}
	if err := unix.EpollCtl(epFd, unix.EPOLL_CTL_ADD, pipeFds[0], &unix.EpollEvent{Events: unix.EPOLLIN}); err != nil {
		cleanup()
		return nil
	}

	// Mark each tracked file inode for close-write events only.
	for _, p := range paths {
		_ = unix.FanotifyMark(faFd, unix.FAN_MARK_ADD, unix.FAN_CLOSE_WRITE, unix.AT_FDCWD, p)
	}

	w := &fanotifyWatcher{
		seen:     make(map[string]ChangeInfo),
		faFd:     faFd,
		epFd:     epFd,
		stopPipe: pipeFds,
		stopped:  make(chan struct{}),
		fallback: &procScanner{},
	}
	go w.loop()
	return w
}

func (w *fanotifyWatcher) loop() {
	defer close(w.stopped)
	buf := make([]byte, 4096)
	events := make([]unix.EpollEvent, 4)
	ourPID := os.Getpid()

	for {
		n, err := unix.EpollWait(w.epFd, events, -1) // block until activity
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}

		// Check the stop pipe. Since it is O_NONBLOCK, this returns immediately:
		// - nil error  → stop signal received, exit.
		// - EAGAIN     → wakeup was from fanotify fd, continue.
		var tmp [1]byte
		if _, err := unix.Read(w.stopPipe[0], tmp[:]); err == nil {
			return
		}

		w.drainEvents(buf, ourPID)
	}
}

// sizeofFanotifyEventMetadata is the size of unix.FanotifyEventMetadata in bytes.
// Defined locally because unix.SizeofFanotifyEventMetadata is not exported.
// Layout: Event_len(4) + Vers(1) + Reserved(1) + Metadata_len(2) + Mask(8) + Fd(4) + Pid(4) = 24.
const sizeofFanotifyEventMetadata = 24

// drainEvents reads all immediately available fanotify events from the
// non-blocking fd and updates the seen map.
func (w *fanotifyWatcher) drainEvents(buf []byte, ourPID int) {
	const metadataVersion = 3 // FANOTIFY_METADATA_VERSION
	for {
		n, err := unix.Read(w.faFd, buf)
		if err != nil {
			return // EAGAIN (no more events) or fatal
		}
		if n < sizeofFanotifyEventMetadata {
			return
		}
		offset := 0
		for offset+sizeofFanotifyEventMetadata <= n {
			// Cast buf[offset:] to *FanotifyEventMetadata. The Go heap allocator
			// guarantees at least pointer-size alignment for the buffer start,
			// and Event_len == 24 (a multiple of 8) keeps all subsequent events
			// aligned on amd64/arm64.
			meta := (*unix.FanotifyEventMetadata)(unsafe.Pointer(&buf[offset]))
			evLen := int(meta.Event_len)
			if evLen < sizeofFanotifyEventMetadata || meta.Vers != metadataVersion {
				break
			}

			evFd := int(meta.Fd)
			pid := int(meta.Pid)

			if evFd >= 0 {
				if pid > 0 && pid != ourPID {
					path, _ := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", evFd))
					if path != "" {
						info := buildChangeInfo(pid, ChangeSourceFanotify, true)
						w.mu.Lock()
						w.seen[path] = info
						w.mu.Unlock()
					}
				}
				_ = unix.Close(evFd) // always close the event fd to avoid leaks
			}
			offset += evLen
		}
	}
}

// Lookup returns the most recent ChangeInfo for path.
// Uses the fanotify cache when a recent entry exists (< 10s); falls back to
// a /proc fd scan to cover atomic-rename saves.
func (w *fanotifyWatcher) Lookup(path string) ChangeInfo {
	w.mu.Lock()
	if info, ok := w.seen[path]; ok && time.Since(info.ChangedAt) < 10*time.Second {
		w.mu.Unlock()
		return info
	}
	w.mu.Unlock()
	return w.fallback.Lookup(path)
}

func (w *fanotifyWatcher) Close() {
	unix.Write(w.stopPipe[1], []byte{1}) // signal the loop goroutine to stop
	<-w.stopped                          // wait for clean exit before closing fds
	unix.Close(w.stopPipe[0])
	unix.Close(w.stopPipe[1])
	unix.Close(w.epFd)
	unix.Close(w.faFd)
}

// ── /proc fd scan ─────────────────────────────────────────────────────────────

// procScanner scans /proc/*/fd at lookup time looking for a process that
// currently has the target file open. No privileges required.
//
// Reliability: works for in-place saves (the process still has the fd open
// at the moment fsnotify delivers the event). Misses atomic-rename saves
// where the process has already closed all fds before the event arrives.
// In that case, Lookup returns a zero ChangeInfo (Source == ChangeSourceUnknown).
// We prefer "unknown" over a wrong actor.
type procScanner struct{}

func (s *procScanner) Lookup(path string) ChangeInfo {
	ourPID := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ChangeInfo{ChangedAt: time.Now()}
	}
	for _, e := range entries {
		if !e.IsDir() || !isAllDigits(e.Name()) {
			continue
		}
		pid := parsePID(e.Name())
		if pid == 0 || pid == ourPID {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // process may have exited
		}
		for _, fd := range fds {
			target, err := os.Readlink(fdDir + "/" + fd.Name())
			if err == nil && target == path {
				return buildChangeInfo(pid, ChangeSourceProcScan, false)
			}
		}
	}
	// No open fd found — return zero ChangeInfo so the caller can distinguish
	// "unknown" from a potentially wrong attribution.
	return ChangeInfo{ChangedAt: time.Now()}
}

func (s *procScanner) Close() {}

// ── shared helpers ────────────────────────────────────────────────────────────

func buildChangeInfo(pid int, src ChangeSource, reliable bool) ChangeInfo {
	uid, uname := pidIdentity(pid)
	return ChangeInfo{
		ProcName:  readComm(pid),
		PID:       pid,
		UID:       uid,
		UserName:  uname,
		ChangedAt: time.Now(),
		Source:    src,
		Reliable:  reliable,
	}
}

func readComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// pidIdentity returns the real UID and resolved username for pid.
// Returns uid == -1 when the /proc status file cannot be read.
// When the process is running as root (UID 0) and has SUDO_USER set in its
// environment, the identity of the original user is returned instead.
func pidIdentity(pid int) (uid int, name string) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1, ""
	}
	for _, line := range strings.SplitN(string(b), "\n", 50) {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		// fields[1] is the real UID (first of: real, effective, saved, filesystem).
		u := parseUID(fields[1])
		// When the process runs as root, check whether it was started via sudo
		// so we can attribute the change to the original user.
		if u == 0 {
			if su, sname := resolveSudoUser(pid); su >= 0 {
				return su, sname
			}
		}
		entry, err := user.LookupId(fields[1])
		if err != nil {
			return u, fields[1] // return numeric string as name fallback
		}
		return u, entry.Username
	}
	return -1, ""
}

// resolveSudoUser reads SUDO_USER from /proc/<pid>/environ.
// Returns (uid, username) of the original user when the process was started
// via sudo, or (-1, "") when SUDO_USER is absent or unresolvable.
func resolveSudoUser(pid int) (uid int, name string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return -1, ""
	}
	prefix := []byte("SUDO_USER=")
	for _, entry := range bytes.Split(data, []byte{0}) {
		if !bytes.HasPrefix(entry, prefix) {
			continue
		}
		sudoUsername := string(entry[len(prefix):])
		if sudoUsername == "" {
			return -1, ""
		}
		u, err := user.Lookup(sudoUsername)
		if err != nil {
			return -1, sudoUsername // name only, no UID
		}
		return parseUID(u.Uid), u.Username
	}
	return -1, ""
}

// parseUID converts a decimal UID string to int. Returns -1 on any error.
// Unlike parsePID, UID 0 is valid (root) so a separate sentinel is needed.
func parseUID(s string) int {
	if !isAllDigits(s) {
		return -1
	}
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// parsePID converts a decimal PID string to int. Returns 0 on any error.
// (PID 0 is never a valid userspace process ID.)
func parsePID(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
