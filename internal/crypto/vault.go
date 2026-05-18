package crypto

import (
	"fmt"

	"filippo.io/age"
)

// FileVault wraps key management and per-file encrypt/decrypt behind a single
// type, removing the three-step key-load → cipher pattern that was duplicated
// across track, sync, apply, and remote_deploy.
type FileVault struct {
	km *KeyManager
}

// NewFileVault returns a FileVault backed by the key directory at keysDir.
func NewFileVault(keysDir string) *FileVault {
	return &FileVault{km: NewKeyManager(keysDir)}
}

// Encrypt encrypts plaintext for fileID. extra recipients are added alongside
// the master key (used for node recipients in sync).
func (v *FileVault) Encrypt(fileID string, plaintext []byte, extra ...age.Recipient) ([]byte, error) {
	master, err := v.km.Load()
	if err != nil {
		return nil, fmt.Errorf("file vault: load master key: %w", err)
	}
	return EncryptForFile(plaintext, master, fileID, extra...)
}

// Decrypt decrypts ciphertext for fileID.
func (v *FileVault) Decrypt(fileID string, ciphertext []byte) ([]byte, error) {
	master, err := v.km.Load()
	if err != nil {
		return nil, fmt.Errorf("file vault: load master key: %w", err)
	}
	return DecryptForFile(ciphertext, master, fileID)
}
