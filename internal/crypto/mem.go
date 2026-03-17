package crypto

import (
	"fmt"

	"github.com/awnumar/memguard"
)

// SecureKey holds sensitive key bytes in a locked, protected memory region.
// Always call Destroy() when done to wipe and unlock the memory.
type SecureKey struct {
	buf *memguard.LockedBuffer
}

// NewSecureKey copies b into a new locked memory region and returns a SecureKey.
// The caller's slice b is NOT zeroed — the caller is responsible for clearing it.
//
// Note: memguard.NewBufferFromBytes internally moves (and wipes) the source
// slice, so after this call b will have been zeroed by memguard.
func NewSecureKey(b []byte) (*SecureKey, error) {
	buf := memguard.NewBufferFromBytes(b)
	// NewBufferFromBytes returns a null (size-0) buffer on allocation failure.
	if buf.Size() == 0 && len(b) > 0 {
		return nil, fmt.Errorf("crypto: mem: failed to allocate locked memory region")
	}
	return &SecureKey{buf: buf}, nil
}

// Bytes returns a read-only view of the key bytes.
// Do NOT retain the returned slice beyond the lifetime of the SecureKey.
func (sk *SecureKey) Bytes() []byte {
	return sk.buf.Bytes()
}

// Destroy wipes the key bytes and releases the locked memory region.
// Safe to call multiple times (idempotent).
func (sk *SecureKey) Destroy() {
	sk.buf.Destroy()
}

// WithMasterKey loads the master age identity from disk, extracts its raw
// private key bytes into a SecureKey, calls fn with that SecureKey,
// then Destroys the SecureKey when fn returns (success or error).
//
// This is the canonical way to access the master key: load → use → wipe.
//
// keysDir is e.g. ~/.sysfig/keys
func WithMasterKey(keysDir string, fn func(sk *SecureKey) error) error {
	km := NewKeyManager(keysDir)

	identity, err := km.Load()
	if err != nil {
		return fmt.Errorf("crypto: mem: %w", err)
	}

	// Extract the raw 32-byte X25519 scalar from the identity's Bech32 string.
	// masterKeyBytes (defined in keyder.go) decodes identity.String() via the
	// shared decodeBech32Payload helper — no duplication needed here.
	raw, err := masterKeyBytes(identity)
	if err != nil {
		return fmt.Errorf("crypto: mem: %w", err)
	}

	// Move the raw bytes into locked memory. memguard.NewBufferFromBytes zeroes
	// raw as part of the Move operation, so the plaintext scalar does not linger
	// in regular heap memory after this point.
	sk, err := NewSecureKey(raw)
	if err != nil {
		// raw may not have been zeroed yet (NewSecureKey failed before Move);
		// wipe it explicitly as a best-effort defence.
		for i := range raw {
			raw[i] = 0
		}
		return fmt.Errorf("crypto: mem: %w", err)
	}
	defer sk.Destroy()

	return fn(sk)
}
