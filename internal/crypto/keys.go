package crypto

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	sysfigfs "github.com/sysfig-dev/sysfig/internal/fs"
)

const masterKeyFile = "master.key"

// KeyManager handles the master age identity lifecycle.
type KeyManager struct {
	keysDir string // e.g. ~/.sysfig/keys
}

// NewKeyManager returns a KeyManager rooted at keysDir.
func NewKeyManager(keysDir string) *KeyManager {
	return &KeyManager{keysDir: keysDir}
}

// MasterKeyPath returns the full path of master.key given a keysDir.
func MasterKeyPath(keysDir string) string {
	return filepath.Join(keysDir, masterKeyFile)
}

// Generate creates a new random X25519 age identity, writes the private key
// to <keysDir>/master.key (mode 0600) using WriteFileAtomic, and returns the
// identity. Returns an error if a key already exists (use Load instead).
func (km *KeyManager) Generate() (*age.X25519Identity, error) {
	keyPath := MasterKeyPath(km.keysDir)

	// Fail fast if the key file is already present — never overwrite silently.
	if _, err := os.Stat(keyPath); err == nil {
		return nil, fmt.Errorf("crypto: keys: master key already exists at %q (use Load instead)", keyPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("crypto: keys: %w", err)
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("crypto: keys: generate identity: %w", err)
	}

	// Write the private key string followed by a newline.
	data := []byte(identity.String() + "\n")
	if err := sysfigfs.WriteFileAtomic(keyPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("crypto: keys: %w", err)
	}

	return identity, nil
}

// Load reads and parses the identity from <keysDir>/master.key.
// Returns a wrapped error if the file does not exist or is malformed.
func (km *KeyManager) Load() (*age.X25519Identity, error) {
	keyPath := MasterKeyPath(km.keysDir)

	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("crypto: keys: %w", err)
	}

	// age.ParseX25519Identity expects the raw Bech32 private key string.
	raw := strings.TrimSpace(string(data))
	identity, err := age.ParseX25519Identity(raw)
	if err != nil {
		return nil, fmt.Errorf("crypto: keys: parse identity: %w", err)
	}

	return identity, nil
}

// PublicKey returns the Bech32 public key string for the given identity.
func PublicKey(id *age.X25519Identity) string {
	return id.Recipient().String()
}
