package crypto

import (
	"bytes"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMasterCipher(t *testing.T) *age.X25519Identity {
	t.Helper()
	master, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return master
}

// TestEncryptDecrypt_RoundTrip verifies that encrypting plaintext with a
// recipient and then decrypting with the corresponding identity returns the
// original plaintext unchanged.
func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	master := newTestMasterCipher(t)
	fileID := "roundtrip-file-id"

	recipient, err := DeriveRecipient(master, fileID)
	require.NoError(t, err)

	identity, err := DeriveIdentity(master, fileID)
	require.NoError(t, err)

	plaintext := []byte("the quick brown fox jumps over the lazy dog")

	ciphertext, err := EncryptFile(plaintext, recipient)
	require.NoError(t, err)
	assert.NotEmpty(t, ciphertext, "ciphertext must not be empty")

	got, err := DecryptFile(ciphertext, identity)
	require.NoError(t, err)

	assert.Equal(t, plaintext, got, "decrypted plaintext must match original")
}

// TestEncryptForFile_RoundTrip verifies that EncryptForFile followed by
// DecryptForFile with the same master identity and fileID returns the original
// plaintext.
func TestEncryptForFile_RoundTrip(t *testing.T) {
	master := newTestMasterCipher(t)
	fileID := "convenience-wrapper-id"
	plaintext := []byte("hello, sysfig encryption!")

	ciphertext, err := EncryptForFile(plaintext, master, fileID)
	require.NoError(t, err)
	assert.NotEmpty(t, ciphertext, "ciphertext must not be empty")

	got, err := DecryptForFile(ciphertext, master, fileID)
	require.NoError(t, err)

	assert.Equal(t, plaintext, got, "decrypted plaintext must match original after EncryptForFile/DecryptForFile round-trip")
}

// TestDecryptForFile_WrongFileID verifies that ciphertext encrypted under
// fileID "a" cannot be decrypted under fileID "b" — the derived keys differ,
// so decryption must return an error.
func TestDecryptForFile_WrongFileID(t *testing.T) {
	master := newTestMasterCipher(t)
	plaintext := []byte("secret content that must not leak")

	ciphertext, err := EncryptForFile(plaintext, master, "file-id-a")
	require.NoError(t, err)

	_, err = DecryptForFile(ciphertext, master, "file-id-b")
	assert.Error(t, err, "decrypting with a different fileID must return an error")
}

// TestEncryptFile_OutputIsNotPlaintext verifies that the ciphertext does not
// contain the plaintext as a verbatim substring, i.e. the output is actually
// encrypted and not just a copy of the input.
func TestEncryptFile_OutputIsNotPlaintext(t *testing.T) {
	master := newTestMasterCipher(t)
	fileID := "opacity-check-id"

	recipient, err := DeriveRecipient(master, fileID)
	require.NoError(t, err)

	plaintext := []byte("this text must not appear verbatim in the ciphertext output")

	ciphertext, err := EncryptFile(plaintext, recipient)
	require.NoError(t, err)

	assert.False(t, bytes.Contains(ciphertext, plaintext),
		"ciphertext must not contain plaintext as a substring")
}

// TestEncryptFile_EmptyPlaintext verifies that encrypting and decrypting an
// empty byte slice works correctly.
func TestEncryptFile_EmptyPlaintext(t *testing.T) {
	master := newTestMasterCipher(t)
	fileID := "empty-plaintext-id"

	ciphertext, err := EncryptForFile([]byte{}, master, fileID)
	require.NoError(t, err)
	assert.NotEmpty(t, ciphertext, "ciphertext for empty plaintext must still contain age header overhead")

	got, err := DecryptForFile(ciphertext, master, fileID)
	require.NoError(t, err)

	assert.Equal(t, []byte{}, got, "decrypting empty plaintext must return empty slice")
}

// TestEncryptForFile_DifferentFileIDs verifies that encrypting the same
// plaintext under two different fileIDs produces different ciphertexts
// (because the derived recipients differ).
func TestEncryptForFile_DifferentFileIDs(t *testing.T) {
	master := newTestMasterCipher(t)
	plaintext := []byte("same plaintext, different file keys")

	ct1, err := EncryptForFile(plaintext, master, "file-id-one")
	require.NoError(t, err)

	ct2, err := EncryptForFile(plaintext, master, "file-id-two")
	require.NoError(t, err)

	assert.False(t, bytes.Equal(ct1, ct2),
		"ciphertexts encrypted under different fileIDs must differ")
}

// TestDecryptFile_CorruptedCiphertext verifies that decrypting corrupted data
// returns an error rather than silently producing garbage plaintext.
func TestDecryptFile_CorruptedCiphertext(t *testing.T) {
	master := newTestMasterCipher(t)
	fileID := "corruption-test-id"

	plaintext := []byte("data that will be encrypted then corrupted")

	ciphertext, err := EncryptForFile(plaintext, master, fileID)
	require.NoError(t, err)

	// Flip a byte in the payload area (skip the first 32 bytes of age header).
	corrupted := make([]byte, len(ciphertext))
	copy(corrupted, ciphertext)
	if len(corrupted) > 50 {
		corrupted[50] ^= 0xFF
	}

	_, err = DecryptForFile(corrupted, master, fileID)
	assert.Error(t, err, "decrypting corrupted ciphertext must return an error")
}
