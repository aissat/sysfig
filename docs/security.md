# sysfig — Security Findings & Remediations

> **Audit date:** 2026-03-26
> **Methodology:** SAST + DAST + working exploits + QA validation (13/13)
> **Last updated:** 2026-05-18

---

## Summary

| ID | Title | Severity | CVSS | Status |
|---|---|---|---|---|
| [SEC-001](#sec-001--ssh-host-key-verification-disabled) | SSH `InsecureIgnoreHostKey()` — MITM of remote deploy | 🔴 CRITICAL | 8.8 | ✅ Fixed |
| [SEC-002](#sec-002--hook-timeout-silently-ignored) | Hook timeout parameter silently ignored — DoS | 🟠 HIGH | 7.1 | ✅ Fixed |
| [SEC-003](#sec-003--hook-allowlist-bypass-via-filepathbase) | Hook allowlist bypass via `filepath.Base()` | 🟠 HIGH | 7.5 | ✅ Fixed |
| [SEC-004](#sec-004--sensitive-data-in-tmp-via-oscreatemp) | Sensitive data in `/tmp` via `os.CreateTemp` | 🟡 MEDIUM | 5.3 | ✅ Fixed |
| [SEC-005](#sec-005--backup-directory-created-0755) | Backup directory created `0755` not `0700` | 🟡 MEDIUM | 5.3 | ✅ Fixed |
| [SEC-006](#sec-006--incomplete-system-denylist) | Incomplete denylist — `sudoers.d`/`pam.d`/`polkit` unprotected | 🟡 MEDIUM | 5.4 | ✅ Fixed |
| [SEC-007](#sec-007--github-actions-mutable-tag-pinning) | GitHub Actions pinned to mutable tags — supply chain | 🟡 MEDIUM | 5.9 | 🔲 Open |
| [SEC-008](#sec-008--arbitrary-env-var-exfiltration-via-templates) | `{{env.NAME}}` reads arbitrary env vars into git history | 🟡 MEDIUM | 5.0 | ✅ Fixed |
| [FUN-002](#fun-002--state-file-read-without-lock) | `state.json` read without lock — concurrent corruption | 🟢 LOW | 3.1 | ✅ Fixed |
| [FUN-003](#fun-003--needssudo-heuristic-wrong-for-non-home-paths) | `needsSudo` heuristic wrong for non-`/home/` users | 🟢 LOW | 2.5 | ✅ Fixed |
| [DES-001](#des-001--denylist-blocks-hash-only-integrity-monitoring) | Denylist blocked `--hash-only` (design gap, not a vuln) | — | — | ✅ Fixed |
| [SEC-009](#sec-009--ssh-key-file-world-readable) | SSH key file world-readable — credential leak | 🟢 LOW | 3.3 | ✅ Fixed |
| [SEC-010](#sec-010--passphrase-protected-ssh-key-opaque-error) | Passphrase-protected SSH key gives opaque error | 🟢 LOW | 2.0 | ✅ Fixed |
| [SEC-011](#sec-011--mkdirall-creates-0755-dirs-for-secret-files) | `MkdirAll` creates `0755` dirs for `0600` secret files | 🟡 MEDIUM | 5.3 | ✅ Fixed |
| [SEC-012](#sec-012--bech32-decoder-truncates-multibyte-runes) | bech32 decoder truncates multi-byte runes — bypass possible | 🟢 LOW | 3.7 | ✅ Fixed |

---

## Fixed Findings

---

### SEC-001 — SSH Host Key Verification Disabled

| | |
|---|---|
| **Location** | `internal/core/remote_deploy.go` — `buildSSHClientConfig` |
| **CVSS** | 8.8 (AV:N/AC:M/Au:N/C:H/I:H/A:N) |
| **Impact** | Full MITM of remote deployment; decrypted secrets delivered to attacker server |

**Vulnerability:** `buildSSHClientConfig` used `gossh.InsecureIgnoreHostKey()` as the `HostKeyCallback`. This accepts any server key unconditionally — including an attacker's rogue server. sysfig decrypts age-encrypted files *before* opening the SSH connection, so plaintext secrets were delivered to whoever answered on the target port.

**Fix:** Replaced with `loadHostKeyCallback()`, which reads the expected host public key from `$SYSFIG_SSH_HOST_KEY` and returns `gossh.FixedHostKey(pub)`. The connection is rejected if the server presents a different key.

```bash
# Required: point to the remote host's public key before deploying
export SYSFIG_SSH_HOST_KEY=/etc/ssh/ssh_host_ed25519_key.pub
sysfig deploy --host user@target --all
```

**Regression tests:** `TestSEC001_HostKeyCallback_AcceptsConfiguredKey`, `TestSEC001_HostKeyCallback_RejectsAttackerKey` ([sec_regression_test.go](../internal/core/sec_regression_test.go))

---

### SEC-002 — Hook Timeout Silently Ignored

| | |
|---|---|
| **Location** | `internal/core/hooks.go` — `runCmd` |
| **CVSS** | 7.1 (AV:L/AC:L/Au:N/C:N/I:N/A:H) |
| **Impact** | `sysfig apply` / `sysfig deploy` hangs permanently if any hook blocks |

**Vulnerability:** `runCmd(timeout, name, args...)` accepted a `timeout` parameter but used `exec.Command` (no context), so the timeout was never applied. A hook that calls a blocking binary or hangs on a slow `systemctl` could stall the entire pipeline indefinitely.

**Fix:** Replaced `exec.Command` with `exec.CommandContext` using `context.WithTimeout`:

```go
ctx, cancel := context.WithTimeout(context.Background(), timeout)
defer cancel()
cmd := exec.CommandContext(ctx, name, args...)
```

**Regression test:** `TestSEC002_RunCmdTimeoutEnforced` ([sec_regression_test.go](../internal/core/sec_regression_test.go))

---

### SEC-003 — Hook Allowlist Bypass via `filepath.Base()`

| | |
|---|---|
| **Location** | `internal/core/hooks.go` — `LoadHooks`, `runExec` |
| **CVSS** | 7.5 (AV:L/AC:L/Au:L/C:H/I:H/A:H) |
| **Impact** | Any binary on disk named after an allowlisted tool can be executed |

**Vulnerability:** The allowlist was built from basenames (`filepath.Base(entry)`) and checked against `filepath.Base(cmd[0])`. A `hooks.yaml` entry `allowlist: [nginx]` intended to allow `/usr/sbin/nginx` also permitted `/tmp/nginx`, `/home/user/nginx`, or any binary named `nginx` anywhere on the filesystem.

**Combined with SEC-005**, this formed a complete chain exploit: an attacker with write access to a shared hooks repo injects `/tmp/nginx` as the hook binary. It executes without warning, reads `~/.sysfig/keys/master.key` from the world-traversable backup dir, and POSTs it to a remote server.

**Fix:** The allowlist now stores full paths as-is, and `runExec` resolves `cmd[0]` via `exec.LookPath` + `filepath.Abs` before comparing:

```go
resolved, err := exec.LookPath(cmd[0])
absPath, _   := filepath.Abs(resolved)
if !allowlist[absPath] {
    return "", fmt.Errorf("hooks: exec: %q is not in the allowlist", absPath)
}
```

**Breaking change:** `hooks.yaml` allowlist entries must now be full absolute paths:

```yaml
# Before (no longer accepted)
allowlist: [nginx, systemctl]

# After (required)
allowlist:
  - /usr/sbin/nginx
  - /usr/bin/systemctl
```

**Regression tests:** `TestSEC003_AllowlistPathTraversalIsBlocked`, `TestSEC003_AllowlistFullPathIsAccepted`, `TestSEC003_LoadHooks_AllowlistStoresFullPath` ([hooks_test.go](../internal/core/hooks_test.go))

---

### SEC-005 — Backup Directory Created `0755`

| | |
|---|---|
| **Location** | `internal/backup/backup.go` — `Backup` |
| **CVSS** | 5.3 (AV:L/AC:L/Au:N/C:M/I:N/A:N) |
| **Impact** | Any local user can traverse `~/.sysfig/backups/` and read backed-up secrets |

**Vulnerability:** `os.MkdirAll(dir, 0o755)` made the per-file backup directory world-traversable. On a multi-user system, any local user could `ls ~/.sysfig/backups/` and read the contents of backed-up config files, including `/etc/wireguard/wg0.conf` or any other encrypted file that sysfig has a backup of.

**Fix:** Changed to `0o700`.

**Note:** The `~/.sysfig/` root and its `keys/` subdirectory were already created with `0700` in `init.go` and `clone.go`. This was only the per-file backup subdirectory inside `backups/`.

**Regression test:** `TestSEC005_BackupDirCreatedWith0700` ([backup_test.go](../internal/backup/backup_test.go))

---

### SEC-006 — Incomplete System Denylist

| | |
|---|---|
| **Location** | `internal/core/track.go` — `systemDenylist`, `IsDenied` |
| **CVSS** | 5.4 (AV:N/AC:H/Au:L/C:H/I:N/A:N) |
| **Impact** | Privilege-escalation paths (`sudoers.d`, `pam.d`, `polkit`) could be tracked and pushed to a remote git repo |

**Vulnerability:** The original denylist covered `/etc/sudoers` but not the drop-in directory `/etc/sudoers.d/*`. Similarly, `/etc/pam.d/*`, `/etc/polkit-1/**`, `/etc/cron.d/*`, `/run/secrets/*`, and nested SSL keys under `/etc/ssl/private/sub/` were all reachable. The `filepath.Match` glob engine only matches one directory level, so `private/*` did not cover `private/sub/key.pem`.

**Fix:** Extended `systemDenylist` and added `/**` recursive matching to `IsDenied`:

```go
"/etc/sudoers.d/*",
"/etc/pam.d/*",
"/etc/security/*",
"/etc/polkit-1/**",   // recursive — any depth
"/etc/cron.d/*",
"/etc/cron.daily/*",
"/run/secrets/*",
"/etc/ssl/private/**", // upgraded from /* to cover nested certs
```

`IsDenied` now handles `/**` patterns via `strings.HasPrefix` before falling through to `filepath.Match`.

**Regression tests:** `TestSEC006_SudoersDotD_IsDenied`, `TestSEC006_PamD_IsDenied`, `TestSEC006_Polkit_IsDenied`, `TestSEC006_CronD_IsDenied`, `TestSEC006_RunSecrets_IsDenied`, `TestSEC006_SslPrivateNested_IsDenied` ([track_test.go](../internal/core/track_test.go))

---

### SEC-008 — Arbitrary Env Var Exfiltration via Templates

| | |
|---|---|
| **Location** | `internal/core/template.go` — `resolveVar` |
| **CVSS** | 5.0 (AV:N/AC:H/Au:L/C:H/I:N/A:N) |
| **Impact** | Cloud credentials / tokens leaked into git commit history |

**Vulnerability:** `{{env.NAME}}` in a template first checked `TemplateVars.Extra`, then fell back to `os.Getenv(name)`. An attacker with write access to a shared profile repo could add `{{env.AWS_ACCESS_KEY_ID}}` to any template. When a victim runs `sysfig apply` or `sysfig source render`, the rendered file (containing the live credential value) is committed into the local git repo. If `--push` is set, it reaches the remote permanently.

**Fix:** Removed the `os.Getenv` fallback. `{{env.NAME}}` now returns an error unless the key is explicitly present in `TemplateVars.Extra`. Callers opt in by declaring variables in the profile's `variables:` block or populating `Extra` directly.

```go
// Before — fell back to OS environment
return os.Getenv(envKey), nil

// After — only declared vars are allowed
return "", fmt.Errorf("core: template: env var %q not in allowed vars — declare it in profile variables or TemplateVars.Extra", envKey)
```

**Breaking change:** Profile templates using `{{env.VAR}}` must declare the variable in the profile's `variables:` block. Variables populated via `sysfig source use --var KEY=VALUE` or profile defaults are automatically placed in `Extra` and continue to work.

**Regression tests:** `TestRenderTemplate_EnvVarNotInExtra_ReturnsError`, `TestSEC008_EnvVarFromOSEnvironmentIsRejected` ([template_test.go](../internal/core/template_test.go))

---

### DES-001 — Denylist Blocked Hash-Only Integrity Monitoring

| | |
|---|---|
| **Location** | `internal/core/track.go` — `Track`, `TrackDir` |
| **Type** | Design gap (not a vulnerability) |

**Issue:** `IsDenied` was checked unconditionally, preventing `sysfig track --hash-only /etc/shadow`. This made it impossible to use sysfig for integrity monitoring of sensitive files — a legitimate security use case — even though hash-only mode stores no content in git.

**Fix:** The denylist is now bypassed when `opts.HashOnly` is true, in both `Track` and `TrackDir`. The `--local` mode still enforces the denylist (content is committed into a local git branch).

```bash
# Now works — monitors /etc/shadow for unexpected changes, stores no content
sysfig track --hash-only /etc/shadow
sysfig track --hash-only /etc/sudoers /etc/pam.d/sshd
```

**Regression tests:** `TestTrack_HashOnly_DeniedPathAllowed`, `TestTrack_LocalOnly_DeniedPathBlocked` ([track_test.go](../internal/core/track_test.go))

---

### SEC-004 — Sensitive Data in `/tmp` via `os.CreateTemp`

| | |
|---|---|
| **Location** | `internal/core/track.go`, `sync.go`, `diff.go`, `bundle.go`, `git.go` |
| **CVSS** | 5.3 (AV:L/AC:L/Au:N/C:M/I:N/A:N) |

`os.CreateTemp("", "sysfig-*")` creates files in the system-wide `/tmp` directory. On multi-user systems, `/tmp` is world-readable (sticky-bit prevents deletion, not reading). Temporary files containing decrypted config content or git index blobs may be visible to other local users until they are removed.

**Fix:** Added `internal/fs.SecureTempDir()` which creates and returns `$HOME/.sysfig/tmp` (mode `0700`), falling back to `os.TempDir()` only when `$HOME` is unavailable. All `os.CreateTemp("", ...)` calls in `track.go`, `sync.go`, `diff.go`, `bundle.go`, and `git.go` now use `sysfigfs.SecureTempDir()` as the directory argument.

**Regression test:** `TestSecureTempDir_IsPrivate` ([tempdir_test.go](../internal/fs/tempdir_test.go))

---

### FUN-002 — State File Read Without Lock

| | |
|---|---|
| **Location** | `internal/state/state.go` — `Manager.Load` |
| **CVSS** | 3.1 (AV:L/AC:H/Au:N/C:N/I:M/A:N) |

`Manager.Load()` reads `state.json` without acquiring any lock. `WithLock()` correctly uses `LOCK_EX` for mutations, but a concurrent write (e.g. `sysfig watch` committing while `sysfig status` reads) can observe a partial write on filesystems where `rename(2)` atomicity is not guaranteed (overlayfs, some FUSE mounts).

**Fix:** Split `Load()` into a private `load()` (no lock, used internally by `WithLock`) and a public `Load()` that acquires `LOCK_SH` before reading and releases it after. `WithLock` continues to hold `LOCK_EX` and calls `load()` directly to avoid double-locking on the same fd.

---

### FUN-003 — `needsSudo` Heuristic Wrong for Non-`/home/` Paths

| | |
|---|---|
| **Location** | `internal/core/remote_deploy.go` — `needsSudo` |
| **CVSS** | 2.5 (AV:L/AC:H/Au:N/C:N/I:L/A:N) |

`needsSudo` checked `strings.HasPrefix(dstPath, "/home/<sshUser>/")`. This fails for macOS (`/Users/<user>`), system accounts (`/var/`, `/srv/`), or custom `$HOME` from `/etc/passwd`. On macOS targets, all home-directory files incorrectly triggered `sudo`, causing unnecessary privilege escalation.

**Fix:** Added `queryRemoteHome(client, sshUser)` which runs `echo $HOME` via a one-shot SSH session before the deploy loop. Both `RemoteDeploy` and `RemoteDeployRendered` now call this once and pass `remoteHome` to `needsSudo` instead of the hardcoded prefix. Falls back to `/home/<sshUser>` if the remote command fails.

---

### SEC-009 — SSH Key File World-Readable

| | |
|---|---|
| **Location** | `internal/core/remote_deploy.go` — `buildSSHClientConfig` |
| **CVSS** | 3.3 (AV:L/AC:L/Au:N/C:L/I:N/A:N) |

**Vulnerability:** `buildSSHClientConfig` read the identity file without checking its permissions. A `0644` key file is readable by all local users — the private key could be trivially copied by any process running as a different user on the same host.

**Fix:** Before reading the file, `os.Stat` checks that no group or other bits are set. Any key file with `perm & 0o077 != 0` is rejected with a message naming the problem and the remediation:

```
ssh key /home/user/.ssh/id_ed25519 has unsafe permissions 0644 — run: chmod 600 /home/user/.ssh/id_ed25519
```

**Regression tests:** `TestSEC009_SSHKey_WorldReadable_Rejected`, `TestSEC009_SSHKey_RestrictedPermissions_Accepted` ([sec_regression_test.go](../internal/core/sec_regression_test.go))

---

### SEC-010 — Passphrase-Protected SSH Key Gives Opaque Error

| | |
|---|---|
| **Location** | `internal/core/remote_deploy.go` — `buildSSHClientConfig` |
| **CVSS** | 2.0 (AV:L/AC:H/Au:N/C:N/I:N/A:L) |

**Vulnerability:** `gossh.ParsePrivateKey` returns `*gossh.PassphraseMissingError` for encrypted keys, but the error was propagated as-is. Operators saw a raw Go type name instead of actionable guidance, leading to confusion and potentially insecure workarounds.

**Fix:** `errors.As` detects `*gossh.PassphraseMissingError` and wraps it with a clear message directing the user to ssh-agent:

```
ssh key /home/user/.ssh/id_ed25519 is passphrase-protected — use ssh-agent (SSH_AUTH_SOCK) instead
```

**Regression test:** `TestSEC010_SSHKey_Passphrase_HelpfulError` ([sec_regression_test.go](../internal/core/sec_regression_test.go))

---

### SEC-011 — `MkdirAll` Creates `0755` Dirs for `0600` Secret Files

| | |
|---|---|
| **Location** | `internal/fs/atomic.go` — `WriteFileAtomic` |
| **CVSS** | 5.3 (AV:L/AC:L/Au:N/C:M/I:N/A:N) |

**Vulnerability:** `WriteFileAtomic` called `os.MkdirAll(dir, 0o755)` unconditionally. When creating a new directory to hold a `0600` secret file (e.g. `~/.sysfig/keys/`), the directory itself was created world-traversable, defeating the file's restricted permissions — any local user could `ls` the directory and stat its contents.

**Fix:** The directory mode is now derived from the file mode: if the file's world-read bit is unset (`perm & 0o004 == 0`), `MkdirAll` uses `0o700`; otherwise `0o755`.

```go
dirMode := os.FileMode(0o755)
if perm&0o004 == 0 {
    dirMode = 0o700
}
os.MkdirAll(dir, dirMode)
```

**Regression tests:** `TestSEC011_WriteFileAtomic_SecretFile_GetsRestrictedDir`, `TestSEC011_WriteFileAtomic_PublicFile_Gets755Dir` ([atomic_test.go](../internal/fs/atomic_test.go))

---

### SEC-012 — bech32 Decoder Truncates Multi-Byte Runes

| | |
|---|---|
| **Location** | `internal/crypto/keyder.go` — `decodeBech32Payload` |
| **CVSS** | 3.7 (AV:N/AC:H/Au:N/C:L/I:L/A:N) |

**Vulnerability:** `decodeBech32Payload` iterated with `for i, c := range data` (rune iteration) and cast each character to `byte(c)`. For a multi-byte Unicode rune such as `ű` (U+0171), `byte(0x0171) = 0x71 = 'q'` — a valid bech32 character. A carefully crafted non-ASCII string could therefore pass character validation and produce a key whose payload silently differed from what was shown to the user.

**Fix:** Changed to `for i, c := range []byte(data)` so the loop processes raw bytes. Non-ASCII bytes (`> 0x7F`) now fail the bech32 alphabet check immediately.

**Regression test:** `TestSEC012_Bech32_NonASCII_Rejected` ([keyder_test.go](../internal/crypto/keyder_test.go))

---

## Open Findings

---

### SEC-007 — GitHub Actions Mutable Tag Pinning

| | |
|---|---|
| **Location** | `.github/workflows/ci.yml`, `.github/workflows/release.yml` |
| **CVSS** | 5.9 (AV:N/AC:H/Au:N/C:H/I:H/A:N) |
| **Status** | 🔲 Open |

All Actions are pinned to mutable semver tags (`@v4`, `@v5`, `@v2`). A compromised upstream action can push a new commit under the same tag, injecting malicious code into the build or release pipeline. The release workflow has `contents: write`, so a compromise there can backdoor release binaries distributed to all users.

Current vulnerable references:
- `actions/checkout@v4`
- `actions/setup-go@v5`
- `softprops/action-gh-release@v2`
- `kenji-miyake/setup-git-cliff@v2`

---

## Positive Security Controls

These controls were confirmed correct during the audit and should be preserved:

| Control | Detail |
|---|---|
| `filippo.io/age` with HKDF-SHA256 per-file keys | Per-file key derivation limits blast radius of a single key compromise |
| `memguard.LockedBuffer` for key material | `mlock`'d + guard pages + explicit zeroing on `Destroy()` |
| Atomic file writes (`fdatasync` + `rename`) | Same-filesystem temp file, synced before rename — no partial writes |
| `flock`-based state mutation serialisation | `LOCK_EX` on dedicated `.lock` file for all writes |
| Systemd unit name regex in hooks | `[a-zA-Z0-9\-_.@:]+` prevents service-name injection |
| `--local` / `--hash-only` track modes | Privacy-preserving options for sensitive files |
| `~/.sysfig/` hierarchy created `0700` | Root, `keys/`, `backups/` all restricted to owner |
