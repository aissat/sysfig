package core

import "time"

// ChangeSource identifies how process attribution was determined.
type ChangeSource uint8

const (
	ChangeSourceUnknown  ChangeSource = iota // no attribution available
	ChangeSourceFanotify                     // kernel-exact via fanotify (requires CAP_SYS_ADMIN)
	ChangeSourceProcScan                     // best-effort /proc/*/fd scan (no privileges needed)
)

// ChangeInfo captures best-effort context for a file change event.
// All fields are populated when the OS provides the information.
// UID is -1 when unknown (0 is a valid UID for root).
// ChangedAt is "when sysfig observed the filesystem event", not the kernel write time.
type ChangeInfo struct {
	ProcName  string       // short command name e.g. "vim", "" if unknown
	PID       int          // process ID, 0 if unknown
	UID       int          // real UID, -1 if unknown
	UserName  string       // resolved from /etc/passwd, "" if unavailable
	ChangedAt time.Time    // when sysfig observed the event
	Source    ChangeSource // how attribution was determined
	Reliable  bool         // true only for fanotify-sourced attribution
}

// procWatcher identifies the process responsible for a file change.
type procWatcher interface {
	// Lookup returns the best-effort ChangeInfo for path.
	// ChangedAt is always populated; other fields depend on availability.
	Lookup(path string) ChangeInfo
	Close()
}

// nullWatcher is the no-op fallback used on non-Linux platforms.
type nullWatcher struct{}

func (nullWatcher) Lookup(string) ChangeInfo { return ChangeInfo{ChangedAt: time.Now()} }
func (nullWatcher) Close()                   {}
