// Security regression tests — white-box (package core) so private functions
// runCmd and loadHostKeyCallback can be reached directly.
package core

import (
	"crypto/ed25519"
	"crypto/rand"
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
	_, err := loadHostKeyCallback()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SYSFIG_SSH_HOST_KEY",
		"error must mention the env var so operators know how to fix it")
}

func TestSEC001_LoadHostKeyCallback_MissingFile_Errors(t *testing.T) {
	t.Setenv("SYSFIG_SSH_HOST_KEY", filepath.Join(t.TempDir(), "nonexistent.pub"))
	_, err := loadHostKeyCallback()
	require.Error(t, err)
}

func TestSEC001_HostKeyCallback_AcceptsConfiguredKey(t *testing.T) {
	legitPub, _ := generateTestSSHKey(t)
	keyFile := writeSSHWireKeyFile(t, t.TempDir(), legitPub)
	t.Setenv("SYSFIG_SSH_HOST_KEY", keyFile)

	cb, err := loadHostKeyCallback()
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

	cb, err := loadHostKeyCallback()
	require.NoError(t, err)

	err = cb("target:22", &net.TCPAddr{}, attackerPub)
	assert.Error(t, err,
		"SEC-001 regression: callback must reject a key that differs from the configured host key")
}
