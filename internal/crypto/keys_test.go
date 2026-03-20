package crypto_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/aissat/sysfig/internal/crypto"
)

// TestGenerate_CreatesFile verifies that Generate writes master.key at the
// correct path with mode 0600.
func TestGenerate_CreatesFile(t *testing.T) {
	keysDir := t.TempDir()
	km := crypto.NewKeyManager(keysDir)

	if _, err := km.Generate(); err != nil {
		t.Fatalf("Generate returned unexpected error: %v", err)
	}

	wantPath := crypto.MasterKeyPath(keysDir)
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("master.key not found at %q: %v", wantPath, err)
	}

	// Verify exact path component.
	if filepath.Base(wantPath) != "master.key" {
		t.Errorf("expected file name %q, got %q", "master.key", filepath.Base(wantPath))
	}

	// Verify permissions are exactly 0600.
	gotPerm := info.Mode().Perm()
	if gotPerm != 0o600 {
		t.Errorf("expected file mode 0600, got %04o", gotPerm)
	}
}

// TestGenerate_FileContent verifies that the written file can be parsed back
// as a valid X25519 identity and that the public key is non-empty.
func TestGenerate_FileContent(t *testing.T) {
	keysDir := t.TempDir()
	km := crypto.NewKeyManager(keysDir)

	generated, err := km.Generate()
	if err != nil {
		t.Fatalf("Generate returned unexpected error: %v", err)
	}

	// Read the raw file content and parse it independently.
	raw, err := os.ReadFile(crypto.MasterKeyPath(keysDir))
	if err != nil {
		t.Fatalf("failed to read master.key: %v", err)
	}

	parsed, err := age.ParseX25519Identity(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("age.ParseX25519Identity failed on written content: %v", err)
	}

	// Public key must be non-empty.
	pubKey := crypto.PublicKey(parsed)
	if pubKey == "" {
		t.Error("public key is empty after parsing the written file")
	}

	// Public key from parsed file must match the one from the returned identity.
	if got, want := pubKey, crypto.PublicKey(generated); got != want {
		t.Errorf("public key mismatch: parsed file %q, returned identity %q", got, want)
	}
}

// TestGenerate_NoDuplicate verifies that a second Generate call returns an
// error containing "already exists".
func TestGenerate_NoDuplicate(t *testing.T) {
	keysDir := t.TempDir()
	km := crypto.NewKeyManager(keysDir)

	if _, err := km.Generate(); err != nil {
		t.Fatalf("first Generate returned unexpected error: %v", err)
	}

	_, err := km.Generate()
	if err == nil {
		t.Fatal("second Generate expected an error, got nil")
	}

	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected error to contain %q, got: %v", "already exists", err)
	}
}

// TestLoad_Success verifies that Load after Generate returns an identity with
// the same public key as the generated one.
func TestLoad_Success(t *testing.T) {
	keysDir := t.TempDir()
	km := crypto.NewKeyManager(keysDir)

	generated, err := km.Generate()
	if err != nil {
		t.Fatalf("Generate returned unexpected error: %v", err)
	}

	loaded, err := km.Load()
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if got, want := crypto.PublicKey(loaded), crypto.PublicKey(generated); got != want {
		t.Errorf("public key mismatch: loaded %q, generated %q", got, want)
	}
}

// TestLoad_Missing verifies that Load on a non-existent keys dir returns a
// wrapped error.
func TestLoad_Missing(t *testing.T) {
	// Use a path that is guaranteed not to exist.
	keysDir := filepath.Join(t.TempDir(), "nonexistent", "keys")
	km := crypto.NewKeyManager(keysDir)

	_, err := km.Load()
	if err == nil {
		t.Fatal("Load expected an error for missing keys dir, got nil")
	}

	// The error must be wrapped (i.e. non-empty and descriptive).
	if err.Error() == "" {
		t.Error("expected a non-empty error message")
	}
}

// TestPublicKey_Format verifies that the public key string begins with the
// Bech32 age prefix "age1".
func TestPublicKey_Format(t *testing.T) {
	keysDir := t.TempDir()
	km := crypto.NewKeyManager(keysDir)

	identity, err := km.Generate()
	if err != nil {
		t.Fatalf("Generate returned unexpected error: %v", err)
	}

	pubKey := crypto.PublicKey(identity)
	if !strings.HasPrefix(pubKey, "age1") {
		t.Errorf("expected public key to start with %q, got %q", "age1", pubKey)
	}
}
