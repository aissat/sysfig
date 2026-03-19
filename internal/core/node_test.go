package core

import (
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/sysfig-dev/sysfig/internal/state"
	"github.com/sysfig-dev/sysfig/pkg/types"
)

// testPubKey generates a fresh age X25519 key pair and returns the public key string.
func testPubKey(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	return id.Recipient().String()
}

func mustInitState(t *testing.T, baseDir string) {
	t.Helper()
	sm := state.NewManager(filepath.Join(baseDir, "state.json"))
	if err := sm.WithLock(func(s *types.State) error { return nil }); err != nil {
		t.Fatalf("init state: %v", err)
	}
}

func TestNodeAdd(t *testing.T) {
	dir := t.TempDir()
	mustInitState(t, dir)
	pubkey := testPubKey(t)

	result, err := NodeAdd(NodeAddOptions{BaseDir: dir, Name: "laptop", PublicKey: pubkey})
	if err != nil {
		t.Fatalf("NodeAdd: %v", err)
	}
	if result.Node.Name != "laptop" {
		t.Errorf("Name = %q, want laptop", result.Node.Name)
	}
	if result.Node.PublicKey != pubkey {
		t.Errorf("PublicKey mismatch")
	}
}

func TestNodeAdd_Duplicate(t *testing.T) {
	dir := t.TempDir()
	mustInitState(t, dir)
	pubkey := testPubKey(t)

	if _, err := NodeAdd(NodeAddOptions{BaseDir: dir, Name: "laptop", PublicKey: pubkey}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := NodeAdd(NodeAddOptions{BaseDir: dir, Name: "laptop", PublicKey: pubkey}); err == nil {
		t.Error("expected error for duplicate node, got nil")
	}
}

func TestNodeAdd_InvalidKey(t *testing.T) {
	dir := t.TempDir()
	mustInitState(t, dir)
	if _, err := NodeAdd(NodeAddOptions{BaseDir: dir, Name: "x", PublicKey: "not-a-key"}); err == nil {
		t.Error("expected error for invalid key, got nil")
	}
}

func TestNodeList(t *testing.T) {
	dir := t.TempDir()
	mustInitState(t, dir)

	for _, name := range []string{"server", "laptop", "pi"} {
		if _, err := NodeAdd(NodeAddOptions{BaseDir: dir, Name: name, PublicKey: testPubKey(t)}); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}

	nodes, err := NodeList(NodeListOptions{BaseDir: dir})
	if err != nil {
		t.Fatalf("NodeList: %v", err)
	}
	if len(nodes) != 3 {
		t.Errorf("len = %d, want 3", len(nodes))
	}
	// Should be sorted alphabetically.
	if nodes[0].Name != "laptop" || nodes[1].Name != "pi" || nodes[2].Name != "server" {
		t.Errorf("unexpected order: %v", nodes)
	}
}

func TestNodeRemove(t *testing.T) {
	dir := t.TempDir()
	mustInitState(t, dir)
	pubkey := testPubKey(t)

	if _, err := NodeAdd(NodeAddOptions{BaseDir: dir, Name: "laptop", PublicKey: pubkey}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := NodeRemove(NodeRemoveOptions{BaseDir: dir, Name: "laptop"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	nodes, _ := NodeList(NodeListOptions{BaseDir: dir})
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes after remove, got %d", len(nodes))
	}
}

func TestNodeRemove_NotFound(t *testing.T) {
	dir := t.TempDir()
	mustInitState(t, dir)
	if err := NodeRemove(NodeRemoveOptions{BaseDir: dir, Name: "ghost"}); err == nil {
		t.Error("expected error for non-existent node, got nil")
	}
}
