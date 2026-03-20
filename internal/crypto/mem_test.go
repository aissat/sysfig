package crypto_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aissat/sysfig/internal/crypto"
)

// TestNewSecureKey_Basic creates a SecureKey from a known byte slice, verifies
// that Bytes() returns the expected content, then calls Destroy() and confirms
// it does not panic.
//
// Note: memguard.NewBufferFromBytes moves (and wipes) the source slice as part
// of its secure-copy semantics, so we snapshot the expected bytes before
// handing the slice to NewSecureKey.
func TestNewSecureKey_Basic(t *testing.T) {
	input := []byte("secret")

	// Snapshot expected values before NewSecureKey wipes the source slice.
	want := make([]byte, len(input))
	copy(want, input)

	sk, err := crypto.NewSecureKey(input)
	if err != nil {
		t.Fatalf("NewSecureKey returned unexpected error: %v", err)
	}

	got := sk.Bytes()
	if len(got) != len(want) {
		t.Fatalf("Bytes() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Bytes()[%d] = %d, want %d", i, got[i], want[i])
		}
	}

	// Destroy must not panic.
	sk.Destroy()
}

// TestNewSecureKey_Destroy_Idempotent verifies that calling Destroy() twice on
// the same SecureKey does not panic.
func TestNewSecureKey_Destroy_Idempotent(t *testing.T) {
	sk, err := crypto.NewSecureKey([]byte("idempotent"))
	if err != nil {
		t.Fatalf("NewSecureKey returned unexpected error: %v", err)
	}

	// First destroy — should succeed silently.
	sk.Destroy()

	// Second destroy — must not panic.
	sk.Destroy()
}

// TestWithMasterKey_Success generates a real age identity in a temp keysDir,
// then calls WithMasterKey and verifies that the SecureKey passed to fn holds
// exactly 32 non-zero bytes (the X25519 scalar).
func TestWithMasterKey_Success(t *testing.T) {
	keysDir := t.TempDir()
	km := crypto.NewKeyManager(keysDir)

	if _, err := km.Generate(); err != nil {
		t.Fatalf("Generate returned unexpected error: %v", err)
	}

	called := false
	err := crypto.WithMasterKey(keysDir, func(sk *crypto.SecureKey) error {
		called = true

		b := sk.Bytes()
		if len(b) != 32 {
			t.Errorf("expected 32 key bytes, got %d", len(b))
			return nil
		}

		// The X25519 scalar should not be all-zero (astronomically unlikely for
		// a randomly generated key, and indicative of a decode error if it were).
		allZero := true
		for _, v := range b {
			if v != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			t.Error("key bytes are all zero — likely a decode error")
		}

		return nil
	})

	if err != nil {
		t.Fatalf("WithMasterKey returned unexpected error: %v", err)
	}
	if !called {
		t.Error("fn was never called")
	}
}

// TestWithMasterKey_MissingKey calls WithMasterKey on a directory that contains
// no master.key and verifies that a wrapped error is returned and fn is never
// invoked.
func TestWithMasterKey_MissingKey(t *testing.T) {
	// Use a path that is guaranteed not to contain a key file.
	keysDir := filepath.Join(t.TempDir(), "no-such-keys")

	called := false
	err := crypto.WithMasterKey(keysDir, func(sk *crypto.SecureKey) error {
		called = true
		return nil
	})

	if err == nil {
		t.Fatal("expected an error for missing key, got nil")
	}

	// The error must be non-empty and descriptive.
	if err.Error() == "" {
		t.Error("expected a non-empty error message")
	}

	// errors.Unwrap chain must be non-nil — the error must be wrapped.
	if errors.Unwrap(err) == nil {
		t.Errorf("expected a wrapped error (errors.Unwrap returned nil), got: %v", err)
	}

	if called {
		t.Error("fn must not be called when the key cannot be loaded")
	}
}
