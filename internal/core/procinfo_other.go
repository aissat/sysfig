//go:build !linux

package core

// newProcWatcher returns the null watcher on non-Linux platforms.
// Process attribution requires Linux-specific interfaces (/proc, fanotify).
func newProcWatcher(_ []string) procWatcher {
	return nullWatcher{}
}
