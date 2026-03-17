package crypto

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"golang.org/x/crypto/hkdf"
)

// bech32Charset is the standard BIP173 alphabet used by the age Bech32 implementation.
const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// bech32CharsetRev maps each byte value to its 5-bit index in bech32Charset,
// or -1 if the byte is not a valid Bech32 character.
var bech32CharsetRev [256]int8

func init() {
	for i := range bech32CharsetRev {
		bech32CharsetRev[i] = -1
	}
	for i, c := range bech32Charset {
		bech32CharsetRev[c] = int8(i)
	}
}

// decodeBech32Payload decodes the data portion of a Bech32-encoded string and
// returns the raw bytes (without the checksum). The input is expected to be
// the full Bech32 string (e.g. "AGE-SECRET-KEY-1..."). It finds the last '1'
// separator, converts the base-32 (5-bit) characters that follow into bytes,
// and strips the 6-character checksum appended by the encoder.
func decodeBech32Payload(s string) ([]byte, error) {
	s = strings.ToLower(s)
	sep := strings.LastIndexByte(s, '1')
	if sep < 0 {
		return nil, fmt.Errorf("missing bech32 separator")
	}
	data := s[sep+1:]
	if len(data) < 6 {
		return nil, fmt.Errorf("bech32 data too short")
	}
	// Strip the 6-character checksum.
	data = data[:len(data)-6]

	// Collect the 5-bit values.
	vals := make([]byte, len(data))
	for i, c := range data {
		v := bech32CharsetRev[byte(c)]
		if v < 0 {
			return nil, fmt.Errorf("invalid bech32 character %q", c)
		}
		vals[i] = byte(v)
	}

	// Convert 5-bit groups to 8-bit bytes.
	var bits uint32
	var nbits int
	var out []byte
	for _, v := range vals {
		bits = (bits << 5) | uint32(v)
		nbits += 5
		for nbits >= 8 {
			nbits -= 8
			out = append(out, byte(bits>>uint(nbits)))
		}
	}
	return out, nil
}

// masterKeyBytes extracts the raw 32-byte scalar from a master age X25519Identity.
// age does not expose the scalar directly, so we decode it from the identity's
// Bech32 string representation (the String() method returns the canonical
// "AGE-SECRET-KEY-1…" encoding of the scalar).
func masterKeyBytes(master *age.X25519Identity) ([]byte, error) {
	raw, err := decodeBech32Payload(master.String())
	if err != nil {
		return nil, fmt.Errorf("crypto: keyder: extract master key: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("crypto: keyder: expected 32 key bytes, got %d", len(raw))
	}
	return raw, nil
}

// deriveScalar performs the shared HKDF derivation used by both DeriveRecipient
// and DeriveIdentity. It returns a 32-byte derived scalar.
//
// HKDF parameters:
//   - Hash:   SHA-256
//   - IKM:    master identity's raw 32-byte scalar
//   - Salt:   []byte("sysfig-file-key")
//   - Info:   []byte(fileID)
func deriveScalar(master *age.X25519Identity, fileID string) ([]byte, error) {
	ikm, err := masterKeyBytes(master)
	if err != nil {
		return nil, err
	}

	h := hkdf.New(sha256.New, ikm, []byte("sysfig-file-key"), []byte(fileID))
	derived := make([]byte, 32)
	if _, err := io.ReadFull(h, derived); err != nil {
		return nil, fmt.Errorf("crypto: keyder: hkdf read: %w", err)
	}
	return derived, nil
}

// DeriveRecipient derives a deterministic age X25519 recipient for the given
// fileID from the master identity's private key bytes using HKDF-SHA256.
//
// Derivation process:
//  1. Extract the raw private key bytes from the master identity
//     (decoded from master.String() which is the Bech32 encoding of the 32-byte scalar)
//  2. Create an HKDF reader:
//     hkdf.New(sha256.New, masterKeyBytes, []byte("sysfig-file-key"), []byte(fileID))
//  3. Read exactly 32 bytes from the HKDF reader → perFileKeyBytes
//  4. Create a new age X25519 identity from those bytes via age.ParseX25519Identity
//  5. Return the identity's recipient (public key)
//
// The derived key is EPHEMERAL — never stored on disk.
// Returns an error if HKDF read or identity creation fails.
func DeriveRecipient(master *age.X25519Identity, fileID string) (*age.X25519Recipient, error) {
	id, err := DeriveIdentity(master, fileID)
	if err != nil {
		return nil, err
	}
	return id.Recipient(), nil
}

// DeriveIdentity derives a full per-file X25519 identity (private+public) for
// the given fileID from the master identity using HKDF-SHA256.
//
// This is the decryption counterpart to DeriveRecipient; because we derive a
// complete identity the caller has both the public key (for encryption) and the
// private key (for decryption) without any additional storage.
//
// The derived identity is EPHEMERAL — never stored on disk.
// Returns an error if HKDF read or identity creation fails.
func DeriveIdentity(master *age.X25519Identity, fileID string) (*age.X25519Identity, error) {
	scalar, err := deriveScalar(master, fileID)
	if err != nil {
		return nil, err
	}

	// age.ParseX25519Identity expects the canonical "AGE-SECRET-KEY-1…" Bech32
	// string. We re-encode the derived scalar by generating a temporary identity
	// with the same scalar via the round-trip: encode to Bech32 → parse.
	//
	// We build the Bech32 string ourselves using the same encoding that age uses
	// internally (bech32 with HRP "AGE-SECRET-KEY-" then upper-cased). Rather
	// than reimplementing the encoder we create a throwaway identity from the
	// known master key, replace its scalar via HKDF, then encode using the
	// same pathway.  The cleanest zero-dependency approach is to encode the
	// derived scalar to a Bech32 string and hand it to ParseX25519Identity.
	encoded, err := encodeBech32AgeSecretKey(scalar)
	if err != nil {
		return nil, fmt.Errorf("crypto: keyder: encode derived key: %w", err)
	}

	id, err := age.ParseX25519Identity(encoded)
	if err != nil {
		return nil, fmt.Errorf("crypto: keyder: parse derived identity: %w", err)
	}
	return id, nil
}

// encodeBech32AgeSecretKey encodes a 32-byte X25519 scalar as an age secret key
// string ("AGE-SECRET-KEY-1…") using standard Bech32 encoding.
//
// This mirrors what age's internal bech32.Encode("AGE-SECRET-KEY-", scalar) does,
// followed by strings.ToUpper — which is exactly what age.X25519Identity.String()
// returns.
func encodeBech32AgeSecretKey(scalar []byte) (string, error) {
	if len(scalar) != 32 {
		return "", fmt.Errorf("scalar must be 32 bytes")
	}
	const hrp = "age-secret-key-"

	// Convert 8-bit bytes to 5-bit groups.
	vals := convertBits(scalar, 8, 5, true)

	// Build the data string.
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, v := range vals {
		sb.WriteByte(bech32Charset[v])
	}

	// Compute and append checksum.
	checksum := bech32Checksum(hrp, vals)
	for _, c := range checksum {
		sb.WriteByte(bech32Charset[c])
	}

	return strings.ToUpper(sb.String()), nil
}

// convertBits converts a byte slice from one bit-group size to another.
// pad controls whether padding is added when there are leftover bits.
func convertBits(data []byte, fromBits, toBits uint, pad bool) []byte {
	acc := 0
	bits := uint(0)
	var result []byte
	maxv := (1 << toBits) - 1
	for _, b := range data {
		acc = (acc << fromBits) | int(b)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte((acc>>bits)&maxv))
		}
	}
	if pad && bits > 0 {
		result = append(result, byte((acc<<(toBits-bits))&maxv))
	}
	return result
}

// bech32Checksum computes the 6-byte Bech32 checksum for the given HRP and data.
func bech32Checksum(hrp string, data []byte) []byte {
	values := bech32HRPExpand(hrp)
	values = append(values, data...)
	values = append(values, 0, 0, 0, 0, 0, 0)
	polymod := bech32Polymod(values) ^ 1
	result := make([]byte, 6)
	for i := range result {
		result[i] = byte((polymod >> (5 * (5 - i))) & 31)
	}
	return result
}

// bech32HRPExpand expands the human-readable part for checksum computation.
func bech32HRPExpand(hrp string) []byte {
	result := make([]byte, len(hrp)*2+1)
	for i, c := range hrp {
		result[i] = byte(c >> 5)
		result[i+len(hrp)+1] = byte(c & 31)
	}
	result[len(hrp)] = 0
	return result
}

// bech32Polymod computes the Bech32 polynomial checksum.
func bech32Polymod(values []byte) uint32 {
	generator := []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i, g := range generator {
			if (top>>uint(i))&1 != 0 {
				chk ^= g
			}
		}
	}
	return chk
}
