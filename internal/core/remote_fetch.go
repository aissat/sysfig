package core

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// RepoRemotePrefix returns a filesystem-safe path prefix derived from the
// full remote spec (user + host + port). Unlike RemoteHostname, it preserves
// the user and port so that alice@server and bob@server, or server:2222 and
// server:2223, never map to the same git tree prefix.
//
// Transformation rules:
//   - "alice@server"       → "alice@server"
//   - "alice@server:2222"  → "alice@server_2222"
//   - "server:2222"        → "server_2222"
//   - "server"             → "server"
//
// The only unsafe character in a Linux path component is "/"; ":" is replaced
// with "_" because it is used as a logical separator elsewhere in sysfig.
func RepoRemotePrefix(remote string) string {
	return strings.ReplaceAll(remote, ":", "_")
}

// RemoteHostname extracts the bare hostname from a remote spec like
// "user@host", "user@host:port", or "host:port". Used for repo-path
// namespacing and display.
//
//	"aye7@10.0.0.1:22"        → "10.0.0.1"
//	"aye7@server.example.com" → "server.example.com"
//	"10.0.0.1"                → "10.0.0.1"
func RemoteHostname(remote string) string {
	host := remote
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	// Strip port if present.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// FetchFromSSH fetches a single file's raw bytes from a remote host via SSH,
// running `cat <remotePath>` over an authenticated session.
//
// host is "user@hostname" or "hostname"; sshKey is the path to an SSH identity
// file (empty = rely on SSH_AUTH_SOCK agent); port 0 defaults to 22.
//
// SYSFIG_SSH_HOST_KEY must be set for host-key pinning (same as deploy).
func FetchFromSSH(host, sshKey string, port int, remotePath string) ([]byte, error) {
	sshCfg, err := buildSSHClientConfig(sshKey)
	if err != nil {
		return nil, fmt.Errorf("remote fetch ssh: %w", err)
	}
	sshUser, sshAddr := parseSSHTarget(host, port)
	sshCfg.User = sshUser

	netConn, err := net.DialTimeout("tcp", sshAddr, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("remote fetch ssh: tcp dial %s: %w", sshAddr, err)
	}
	sshConn, chans, reqs, err := gossh.NewClientConn(netConn, sshAddr, sshCfg)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("remote fetch ssh: handshake %s: %w", sshAddr, err)
	}
	client := gossh.NewClient(sshConn, chans, reqs)
	defer netConn.Close()

	data, err := sshOutput(client, "cat "+shellQuote(remotePath))
	if err != nil {
		return nil, fmt.Errorf("remote fetch ssh %s:%s: %w", host, remotePath, err)
	}
	return data, nil
}

// sshOutput runs cmd on the remote host and returns its stdout bytes.
func sshOutput(client *gossh.Client, cmd string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new ssh session: %w", err)
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	if err := sess.Run(cmd); err != nil {
		if msg := stderr.String(); msg != "" {
			return nil, fmt.Errorf("%w — %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// ListRemoteFiles lists all regular (non-symlink) files under dir on the
// remote host by running `find <dir> -type f ! -type l` via SSH.
// Returns absolute paths as reported by the remote shell, one per line.
func ListRemoteFiles(host, sshKey string, port int, dir string) ([]string, error) {
	sshCfg, err := buildSSHClientConfig(sshKey)
	if err != nil {
		return nil, fmt.Errorf("remote list ssh: %w", err)
	}
	sshUser, sshAddr := parseSSHTarget(host, port)
	sshCfg.User = sshUser

	netConn, err := net.DialTimeout("tcp", sshAddr, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("remote list ssh: tcp dial %s: %w", sshAddr, err)
	}
	sshConn, chans, reqs, err := gossh.NewClientConn(netConn, sshAddr, sshCfg)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("remote list ssh: handshake %s: %w", sshAddr, err)
	}
	client := gossh.NewClient(sshConn, chans, reqs)
	defer netConn.Close()

	// -not -type l ensures symlinks are excluded even when -type f would follow them.
	out, err := sshOutput(client, "find "+shellQuote(dir)+" -type f -not -type l")
	if err != nil {
		return nil, fmt.Errorf("remote list ssh %s:%s: %w", host, dir, err)
	}

	var files []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}
