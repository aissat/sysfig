package core

import (
	"testing"
	"time"
)

// TestPendingAttributionUpdate covers the merge rule used in Watch's event
// loop: always take the latest event unless it would downgrade a reliable
// (fanotify) entry to an unreliable (proc-scan) one.
func TestPendingAttributionUpdate(t *testing.T) {
	reliable := ChangeInfo{ProcName: "reliable-proc", Reliable: true, ChangedAt: time.Now()}
	unreliableA := ChangeInfo{ProcName: "proc-A", Reliable: false, ChangedAt: time.Now()}
	unreliableB := ChangeInfo{ProcName: "proc-B", Reliable: false, ChangedAt: time.Now()}

	cases := []struct {
		desc        string
		existing    ChangeInfo
		hasExisting bool
		incoming    ChangeInfo
		wantName    string // expected ProcName after merge
	}{
		{
			desc:        "no existing entry: always take incoming",
			hasExisting: false,
			incoming:    unreliableA,
			wantName:    "proc-A",
		},
		{
			desc:        "unreliable->unreliable: take latest (B wrote the final content)",
			hasExisting: true,
			existing:    unreliableA,
			incoming:    unreliableB,
			wantName:    "proc-B",
		},
		{
			desc:        "unreliable->reliable: upgrade to fanotify attribution",
			hasExisting: true,
			existing:    unreliableA,
			incoming:    reliable,
			wantName:    "reliable-proc",
		},
		{
			desc:        "reliable->reliable: take latest reliable",
			hasExisting: true,
			existing:    reliable,
			incoming:    ChangeInfo{ProcName: "reliable-proc-2", Reliable: true},
			wantName:    "reliable-proc-2",
		},
		{
			desc:        "reliable->unreliable: keep existing (don't downgrade fanotify)",
			hasExisting: true,
			existing:    reliable,
			incoming:    unreliableB,
			wantName:    "reliable-proc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := mergeAttribution(tc.existing, tc.hasExisting, tc.incoming)
			if got.ProcName != tc.wantName {
				t.Errorf("ProcName = %q, want %q", got.ProcName, tc.wantName)
			}
		})
	}
}

// TestFanotifyWatcherCloseDoesNotHang verifies that Close() returns promptly
// after a clean tryFanotify setup. If the stop pipe were not registered in
// epoll, Close() would write to the pipe and block forever on w.stopped.
// Skipped when fanotify is unavailable (no CAP_SYS_ADMIN).
func TestFanotifyWatcherCloseDoesNotHang(t *testing.T) {
	w := tryFanotify(nil)
	if w == nil {
		t.Skip("fanotify unavailable (no CAP_SYS_ADMIN) — skipping epoll registration test")
	}

	done := make(chan struct{})
	go func() {
		w.Close()
		close(done)
	}()

	select {
	case <-done:
		// pass
	case <-time.After(3 * time.Second):
		t.Fatal("Close() hung — stop pipe likely not registered in epoll")
	}
}
