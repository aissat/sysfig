package state

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/sysfig-dev/sysfig/pkg/types"
)

// TestLoad_Empty verifies that loading from a non-existent path returns a
// valid, empty state rather than an error.
func TestLoad_Empty(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir + "/state.json")

	s, err := m.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("Load() returned nil state")
	}
	if s.Version != 1 {
		t.Errorf("Version = %d, want 1", s.Version)
	}
	if s.Files == nil {
		t.Error("Files map is nil")
	}
	if s.Backups == nil {
		t.Error("Backups map is nil")
	}
	if len(s.Files) != 0 {
		t.Errorf("Files len = %d, want 0", len(s.Files))
	}
	if len(s.Backups) != 0 {
		t.Errorf("Backups len = %d, want 0", len(s.Backups))
	}
}

// TestWithLock_Write verifies that a FileRecord added inside WithLock is
// durably persisted and readable via a subsequent Load call.
func TestWithLock_Write(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir + "/state.json")

	want := &types.FileRecord{
		ID:          "test-id",
		SystemPath:  "/etc/nginx/nginx.conf",
		RepoPath:    "nginx/nginx.conf",
		CurrentHash: "abc123",
		Status:      types.StatusTracked,
	}

	err := m.WithLock(func(s *types.State) error {
		s.Files[want.ID] = want
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock() unexpected error: %v", err)
	}

	// Read the state back without a lock.
	s, err := m.Load()
	if err != nil {
		t.Fatalf("Load() after write unexpected error: %v", err)
	}

	got, ok := s.Files[want.ID]
	if !ok {
		t.Fatalf("Files[%q] not found after write", want.ID)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.SystemPath != want.SystemPath {
		t.Errorf("SystemPath = %q, want %q", got.SystemPath, want.SystemPath)
	}
	if got.RepoPath != want.RepoPath {
		t.Errorf("RepoPath = %q, want %q", got.RepoPath, want.RepoPath)
	}
	if got.CurrentHash != want.CurrentHash {
		t.Errorf("CurrentHash = %q, want %q", got.CurrentHash, want.CurrentHash)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
}

// TestWithLock_Concurrent launches 10 goroutines that each add a unique
// FileRecord under the exclusive lock. After all goroutines finish every
// record must be present — no writes should be lost to races.
func TestWithLock_Concurrent(t *testing.T) {
	const numGoroutines = 10

	dir := t.TempDir()
	m := NewManager(dir + "/state.json")

	var wg sync.WaitGroup
	errs := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		i := i // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("file-%d", i)
			err := m.WithLock(func(s *types.State) error {
				s.Files[id] = &types.FileRecord{
					ID:         id,
					SystemPath: fmt.Sprintf("/etc/app/config-%d.conf", i),
					RepoPath:   fmt.Sprintf("app/config-%d.conf", i),
					Status:     types.StatusTracked,
				}
				return nil
			})
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: %w", i, err)
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent WithLock error: %v", err)
	}

	s, err := m.Load()
	if err != nil {
		t.Fatalf("Load() after concurrent writes: %v", err)
	}

	if len(s.Files) != numGoroutines {
		t.Errorf("Files count = %d, want %d", len(s.Files), numGoroutines)
	}
	for i := 0; i < numGoroutines; i++ {
		id := fmt.Sprintf("file-%d", i)
		if _, ok := s.Files[id]; !ok {
			t.Errorf("Files[%q] missing after concurrent writes", id)
		}
	}
}

// TestWithLock_ErrorRollback verifies that when fn returns an error the state
// file is NOT modified — a subsequent Load returns the original state.
func TestWithLock_ErrorRollback(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir + "/state.json")

	// Seed an initial record so the state file exists on disk.
	initial := &types.FileRecord{
		ID:         "original",
		SystemPath: "/etc/ssh/sshd_config",
		RepoPath:   "ssh/sshd_config",
		Status:     types.StatusTracked,
	}
	if err := m.WithLock(func(s *types.State) error {
		s.Files[initial.ID] = initial
		return nil
	}); err != nil {
		t.Fatalf("setup WithLock() unexpected error: %v", err)
	}

	// Now attempt a mutation that fails partway through.
	sentinelErr := errors.New("something went wrong")
	err := m.WithLock(func(s *types.State) error {
		// Mutate in-memory …
		s.Files["should-not-persist"] = &types.FileRecord{
			ID:     "should-not-persist",
			Status: types.StatusModified,
		}
		// … then signal failure so the write must be aborted.
		return sentinelErr
	})

	if !errors.Is(err, sentinelErr) {
		t.Fatalf("WithLock() error = %v, want %v", err, sentinelErr)
	}

	// The state on disk must still match the initial seed.
	s, err := m.Load()
	if err != nil {
		t.Fatalf("Load() after failed WithLock: %v", err)
	}

	if _, ok := s.Files["should-not-persist"]; ok {
		t.Error("rolled-back record was unexpectedly persisted")
	}
	if _, ok := s.Files[initial.ID]; !ok {
		t.Errorf("Files[%q] missing — original state was lost", initial.ID)
	}
	if len(s.Files) != 1 {
		t.Errorf("Files count = %d, want 1", len(s.Files))
	}
}
