package crypto

import (
	"bytes"
	"fmt"
	"io"

	"filippo.io/age"
)

// EncryptFile encrypts plaintext to one or more age recipients and returns
// the ciphertext bytes. age's multi-recipient format means any one recipient
// can independently decrypt the result. Uses binary (non-armored) format.
func EncryptFile(plaintext []byte, recipients ...age.Recipient) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("crypto: cipher: at least one recipient required")
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipients...)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: encrypt init: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("crypto: cipher: encrypt write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("crypto: cipher: encrypt close: %w", err)
	}
	return buf.Bytes(), nil
}

// DecryptFile decrypts ciphertext using the given age identity and returns the
// plaintext bytes.
func DecryptFile(ciphertext []byte, identity age.Identity) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: decrypt init: %w", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: decrypt read: %w", err)
	}
	return plaintext, nil
}

// EncryptForFile derives the per-file recipient from master + fileID using
// HKDF-SHA256, then encrypts plaintext to that recipient plus any additional
// node recipients. Any single recipient can independently decrypt the result.
// The derived key is ephemeral and never stored on disk.
func EncryptForFile(plaintext []byte, master *age.X25519Identity, fileID string, extra ...age.Recipient) ([]byte, error) {
	derived, err := DeriveRecipient(master, fileID)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: derive recipient: %w", err)
	}
	recipients := append([]age.Recipient{derived}, extra...)
	ciphertext, err := EncryptFile(plaintext, recipients...)
	if err != nil {
		return nil, err
	}
	return ciphertext, nil
}

// DecryptForFile derives the per-file identity from master + fileID using
// HKDF-SHA256, then decrypts ciphertext with that identity.
// The derived key is ephemeral and never stored on disk.
func DecryptForFile(ciphertext []byte, master *age.X25519Identity, fileID string) ([]byte, error) {
	identity, err := DeriveIdentity(master, fileID)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: derive identity: %w", err)
	}
	plaintext, err := DecryptFile(ciphertext, identity)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}
