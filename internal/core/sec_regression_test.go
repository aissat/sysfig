// Security regression tests — white-box (package core) so private functions
// runCmd and loadHostKeyCallback can be reached directly.
package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// ── SEC-002: Hook timeout must be enforced ────────────────────────────────────
//
// Before the fix, runCmd used exec.Command (no context) so the timeout
// parameter was silently ignored — a blocking hook could hang sysfig forever.

func TestSEC002_RunCmdTimeoutEnforced(t *testing.T) {
	// Run sleep(1) directly — avoids spawning a subshell whose children
	// could keep stdout open past the SIGKILL and stall CombinedOutput.
	start := time.Now()
	_, err := runCmd(150*time.Millisecond, "sleep", "60")
	elapsed := time.Since(start)

	assert.Error(t, err, "runCmd must error when the process is killed by the timeout")
	// 150 ms timeout + generous 3 s headroom — must not block near 60 s.
	assert.Less(t, elapsed, 3*time.Second,
		"runCmd must not block past the deadline; elapsed=%v", elapsed)
}

// ── SEC-001: SSH host key verification must not be bypassed ───────────────────
//
// Before the fix, buildSSHClientConfig used gossh.InsecureIgnoreHostKey() which
// accepts any server key — allowing a full MITM of remote deploys.
// After the fix, loadHostKeyCallback uses gossh.FixedHostKey so an attacker's
// rogue server is rejected.

func generateTestSSHKey(t *testing.T) (gossh.PublicKey, gossh.Signer) {
	t.Helper()
	pubRaw, privRaw, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := gossh.NewPublicKey(pubRaw)
	require.NoError(t, err)
	sshSigner, err := gossh.NewSignerFromKey(privRaw)
	require.NoError(t, err)
	return sshPub, sshSigner
}

// writeSSHWireKeyFile writes the binary wire-format of pub to a temp file
// (the format expected by gossh.ParsePublicKey / loadHostKeyCallback).
func writeSSHWireKeyFile(t *testing.T, dir string, pub gossh.PublicKey) string {
	t.Helper()
	path := filepath.Join(dir, "host.pub")
	require.NoError(t, os.WriteFile(path, pub.Marshal(), 0o600))
	return path
}

func TestSEC001_LoadHostKeyCallback_NoEnvVar_Errors(t *testing.T) {
	t.Setenv("SYSFIG_SSH_HOST_KEY", "")
	_, _, err := loadHostKeyCallback()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SYSFIG_SSH_HOST_KEY",
		"error must mention the env var so operators know how to fix it")
}

func TestSEC001_LoadHostKeyCallback_MissingFile_Errors(t *testing.T) {
	t.Setenv("SYSFIG_SSH_HOST_KEY", filepath.Join(t.TempDir(), "nonexistent.pub"))
	_, _, err := loadHostKeyCallback()
	require.Error(t, err)
}

func TestSEC001_HostKeyCallback_AcceptsConfiguredKey(t *testing.T) {
	legitPub, _ := generateTestSSHKey(t)
	keyFile := writeSSHWireKeyFile(t, t.TempDir(), legitPub)
	t.Setenv("SYSFIG_SSH_HOST_KEY", keyFile)

	cb, _, err := loadHostKeyCallback()
	require.NoError(t, err)

	err = cb("target:22", &net.TCPAddr{}, legitPub)
	assert.NoError(t, err, "callback must accept the configured host key")
}

func TestSEC001_HostKeyCallback_RejectsAttackerKey(t *testing.T) {
	// Regression: InsecureIgnoreHostKey accepted any key; FixedHostKey must not.
	legitPub, _ := generateTestSSHKey(t)
	attackerPub, _ := generateTestSSHKey(t)

	keyFile := writeSSHWireKeyFile(t, t.TempDir(), legitPub)
	t.Setenv("SYSFIG_SSH_HOST_KEY", keyFile)

	cb, _, err := loadHostKeyCallback()
	require.NoError(t, err)

	err = cb("target:22", &net.TCPAddr{}, attackerPub)
	assert.Error(t, err,
		"SEC-001 regression: callback must reject a key that differs from the configured host key")
}

// ── SEC-009: SSH key file must have restrictive permissions ──────────────────
//
// Before the fix, buildSSHClientConfig read any key file regardless of its
// permissions. A world-readable identity file (0644) is a credential leak
// waiting to happen. After the fix the function rejects any key file whose
// group or other bits are set.

func writeSSHPrivKeyFile(t *testing.T, dir string, perm os.FileMode) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pemBlock, err := gossh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	path := filepath.Join(dir, "id_ed25519")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(pemBlock), perm))
	return path
}

func TestSEC009_SSHKey_WorldReadable_Rejected(t *testing.T) {
	keyPath := writeSSHPrivKeyFile(t, t.TempDir(), 0o644)
	t.Setenv("SYSFIG_SSH_HOST_KEY", "") // host-key check is not the point

	_, err := buildSSHClientConfig(keyPath)
	require.Error(t, err, "SEC-009: world-readable SSH key must be rejected")
	assert.Contains(t, err.Error(), "unsafe permissions",
		"error must tell the user the key permissions are the problem")
	assert.Contains(t, err.Error(), "chmod 600",
		"error must give the operator the remediation command")
}

func TestSEC009_SSHKey_RestrictedPermissions_Accepted(t *testing.T) {
	keyPath := writeSSHPrivKeyFile(t, t.TempDir(), 0o600)
	// No host key configured — failure at host-key step confirms permissions were accepted.
	t.Setenv("SYSFIG_SSH_HOST_KEY", "")

	_, err := buildSSHClientConfig(keyPath)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "unsafe permissions",
		"SEC-009: a 0600 key must pass the permissions check")
}

// ── SEC-010: Passphrase-protected SSH key must give a helpful error ───────────
//
// Before the fix, gossh.ParsePrivateKey returned an opaque error for passphrase-
// protected keys. After the fix the error message names the problem and directs
// the user to ssh-agent.

func TestSEC010_SSHKey_Passphrase_HelpfulError(t *testing.T) {
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pemBlock, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	require.NoError(t, err)
	keyPath := filepath.Join(dir, "id_passphrase")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600))

	t.Setenv("SYSFIG_SSH_HOST_KEY", "")

	_, err = buildSSHClientConfig(keyPath)
	require.Error(t, err, "SEC-010: passphrase-protected key must error")
	assert.Contains(t, err.Error(), "passphrase",
		"error must mention that the key is passphrase-protected")
	assert.Contains(t, err.Error(), "ssh-agent",
		"error must direct the user to ssh-agent as the solution")
}
