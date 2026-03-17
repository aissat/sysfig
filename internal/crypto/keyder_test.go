package crypto

import (
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMaster(t *testing.T) *age.X25519Identity {
	t.Helper()
	master, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return master
}

// TestDeriveRecipient_Deterministic verifies that calling DeriveRecipient with
// the same master identity and the same fileID always produces the same public
// key string.
func TestDeriveRecipient_Deterministic(t *testing.T) {
	master := newTestMaster(t)
	fileID := "file-uuid-abc123"

	r1, err := DeriveRecipient(master, fileID)
	require.NoError(t, err)

	r2, err := DeriveRecipient(master, fileID)
	require.NoError(t, err)

	assert.Equal(t, r1.String(), r2.String(),
		"DeriveRecipient must be deterministic: same master + same fileID → same public key")
}

// TestDeriveRecipient_DifferentFileIDs verifies that different fileIDs produce
// different public keys even when the master identity is identical.
func TestDeriveRecipient_DifferentFileIDs(t *testing.T) {
	master := newTestMaster(t)

	r1, err := DeriveRecipient(master, "file-id-one")
	require.NoError(t, err)

	r2, err := DeriveRecipient(master, "file-id-two")
	require.NoError(t, err)

	assert.NotEqual(t, r1.String(), r2.String(),
		"DeriveRecipient must produce distinct keys for distinct fileIDs")
}

// TestDeriveRecipient_DifferentMasters verifies that different master identities
// produce different public keys for the same fileID.
func TestDeriveRecipient_DifferentMasters(t *testing.T) {
	master1 := newTestMaster(t)
	master2 := newTestMaster(t)
	fileID := "shared-file-id"

	r1, err := DeriveRecipient(master1, fileID)
	require.NoError(t, err)

	r2, err := DeriveRecipient(master2, fileID)
	require.NoError(t, err)

	assert.NotEqual(t, r1.String(), r2.String(),
		"DeriveRecipient must produce distinct keys for distinct master identities")
}

// TestDeriveIdentity_MatchesRecipient verifies that DeriveIdentity and
// DeriveRecipient are consistent: the public key derived from the identity
// must equal the recipient derived directly.
func TestDeriveIdentity_MatchesRecipient(t *testing.T) {
	master := newTestMaster(t)
	fileID := "consistency-check-id"

	identity, err := DeriveIdentity(master, fileID)
	require.NoError(t, err)

	recipient, err := DeriveRecipient(master, fileID)
	require.NoError(t, err)

	assert.Equal(t, identity.Recipient().String(), recipient.String(),
		"DeriveIdentity(...).Recipient() must equal DeriveRecipient(...) for the same inputs")
}

// TestDeriveIdentity_Deterministic verifies that DeriveIdentity is deterministic
// across two independent calls with the same inputs.
func TestDeriveIdentity_Deterministic(t *testing.T) {
	master := newTestMaster(t)
	fileID := "deterministic-identity-id"

	id1, err := DeriveIdentity(master, fileID)
	require.NoError(t, err)

	id2, err := DeriveIdentity(master, fileID)
	require.NoError(t, err)

	assert.Equal(t, id1.String(), id2.String(),
		"DeriveIdentity must be deterministic: same master + same fileID → same private key string")
	assert.Equal(t, id1.Recipient().String(), id2.Recipient().String(),
		"DeriveIdentity must be deterministic: same master + same fileID → same public key string")
}

// TestDeriveIdentity_DifferentFileIDs verifies that different fileIDs produce
// different identities.
func TestDeriveIdentity_DifferentFileIDs(t *testing.T) {
	master := newTestMaster(t)

	id1, err := DeriveIdentity(master, "alpha")
	require.NoError(t, err)

	id2, err := DeriveIdentity(master, "beta")
	require.NoError(t, err)

	assert.NotEqual(t, id1.String(), id2.String(),
		"DeriveIdentity must produce distinct identities for distinct fileIDs")
}

// TestMasterKeyBytes_RoundTrip verifies that masterKeyBytes correctly extracts
// exactly 32 bytes from any freshly generated identity.
func TestMasterKeyBytes_RoundTrip(t *testing.T) {
	for i := 0; i < 5; i++ {
		master := newTestMaster(t)
		raw, err := masterKeyBytes(master)
		require.NoError(t, err, "masterKeyBytes must not error on a valid identity")
		assert.Len(t, raw, 32, "masterKeyBytes must return exactly 32 bytes")
	}
}

// TestEncodeBech32AgeSecretKey_RoundTrip verifies that our Bech32 encoder
// produces strings that age.ParseX25519Identity can successfully parse, and
// that re-encoding the decoded identity gives back the same string.
func TestEncodeBech32AgeSecretKey_RoundTrip(t *testing.T) {
	master := newTestMaster(t)

	raw, err := masterKeyBytes(master)
	require.NoError(t, err)

	encoded, err := encodeBech32AgeSecretKey(raw)
	require.NoError(t, err)

	reparsed, err := age.ParseX25519Identity(encoded)
	require.NoError(t, err, "encoded string must be parseable by age.ParseX25519Identity")

	assert.Equal(t, master.String(), reparsed.String(),
		"round-tripped identity must match original")
}
