package crypto

import (
	"bytes"
	"fmt"
	"io"

	"filippo.io/age"
)

// EncryptFile encrypts plaintext using the given age recipient and returns the
// ciphertext bytes. Uses age's binary streaming encryption (armor=false) for
// compact output.
func EncryptFile(plaintext []byte, recipient age.Recipient) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
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
// HKDF-SHA256, then encrypts plaintext with that recipient.
// The derived key is ephemeral and never stored on disk.
func EncryptForFile(plaintext []byte, master *age.X25519Identity, fileID string) ([]byte, error) {
	recipient, err := DeriveRecipient(master, fileID)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: derive recipient: %w", err)
	}
	ciphertext, err := EncryptFile(plaintext, recipient)
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
