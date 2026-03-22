# Local Integrity Tracking

sysfig can track sensitive files **locally** — recording their hash without ever
pushing the content to the remote git repository.

Two modes are available:

| Flag | Content stored | Git branch | Sync locally | Push to remote |
|------|---------------|------------|--------------|----------------|
| `--local` | full copy in `~/.sysfig/repo.git` | `local/<path>` | ✅ yes | ❌ never |
| `--hash-only` | hash only — no content | none | ❌ skipped | ❌ never |

**Use `--local`** when you want full local git history (diffs, logs, undo) but the
file must never leave this machine — e.g. WireGuard private keys, VPN configs,
machine-specific credentials.

**Use `--hash-only`** when even local storage is too sensitive — you only need to
know whether the file has changed, not what it contains.

---

## Tracking a file

```sh
# Local-only — full content in local repo, never pushed:
sysfig track --local /etc/wireguard/wg0.conf

# Hash-only — only the hash recorded, no content stored anywhere:
sysfig track --hash-only /etc/ssh/sshd_config

# --local and --hash-only are mutually exclusive.
```

Output for `--local`:
```
Tracking /etc/wireguard/wg0.conf

  ✓ ID:   0b5aac93
  ✓ Repo: etc/wireguard/wg0.conf
  ✓ Hash: ed36fd92...
  ✓ Mode:  local-only (never pushed to remote)
```

Output for `--hash-only`:
```
Tracking /etc/ssh/sshd_config

  ✓ ID:   a26787d2
  ✓ Repo: etc/ssh/sshd_config
  ✓ Hash: f063ab5c...
  ✓ Mode:  hash-only (integrity monitoring, no content stored)
```

---

## Checking status

`sysfig status` shows dim mode tags next to local/hash-only entries:

```
PATH                        HASH        STATUS
────────────────────────────────────────────────────────────────────────────
/etc/wireguard/wg0.conf     0b5aac93    SYNCED   local
/etc/ssh/sshd_config        a26787d2    SYNCED   hash
```

After tampering:

```
/etc/wireguard/wg0.conf     0b5aac93    DIRTY    local
/etc/ssh/sshd_config        a26787d2    TAMPERED hash
```

- `DIRTY` — content changed on disk (local-only file; can be synced locally)
- `TAMPERED` — hash-only file drifted from its recorded hash; no content was stored, so drift means something external modified the file

---

## Syncing a local-only file

`--local` files behave like normal tracked files for all local operations.
You can sync them with a commit message to build a local audit trail:

```sh
# After rotating VPN keys:
sysfig sync /etc/wireguard/wg0.conf -m "rotated VPN keys 2026-03-22"

# View the local history:
sysfig log /etc/wireguard/wg0.conf
```

Example log output:
```
* cd3ed88  2026-03-22 12:54  tmp/lab-wg/wg0.conf  rotated VPN keys 2026-03-22  (local/etc/wireguard/wg0.conf)
```

The branch name starts with `local/` — this prefix ensures the branch is never
included in `sysfig push` or bundle exports.

`sysfig diff` and `sysfig show` also work on local-only files.

`--hash-only` files are **always skipped** by sync — there is no content to commit.

---

## Auditing

`sysfig audit` checks all local-only and hash-only files and exits with:

| Exit code | Meaning |
|-----------|---------|
| `0` | all checked files are clean |
| `1` | one or more files are TAMPERED or DIRTY |
| `2` | error (could not read state or hash a file) |

```sh
# Check all local/hash-only files (default):
sysfig audit

# Check only hash-only files:
sysfig audit --hash-only

# Check only local-only files:
sysfig audit --local

# Check every tracked file (not just local/hash-only):
sysfig audit --all

# Silent — exit code only, no output (for scripts/timers):
sysfig audit --quiet
```

Example output when drift is found:
```
  DIRTY    /etc/wireguard/wg0.conf
  TAMPERED /etc/ssh/sshd_config

  Audit: 2/2 file(s) drifted
```

---

## Automated integrity checks with systemd

Install the provided unit and timer files from `contrib/systemd/`:

```sh
cp contrib/systemd/sysfig-audit.service ~/.config/systemd/user/
cp contrib/systemd/sysfig-audit.timer   ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now sysfig-audit.timer
```

The timer runs `sysfig audit --quiet` **every hour** and **5 minutes after boot**.
If any file has drifted, the service unit exits with code 1 — systemd marks it
failed, which is visible in:

```sh
systemctl --user status sysfig-audit.service
journalctl --user -u sysfig-audit.service
```

To react to drift (e.g. send a desktop notification or email), uncomment the
`OnFailure=` line in `sysfig-audit.service` and point it at a notify service.

---

## Behaviour summary

| Action | `--local` file | `--hash-only` file |
|--------|---------------|---------------------|
| `sysfig track` | stages content in local `local/` branch | records hash only; nothing staged |
| `sysfig sync` | commits to local `local/<path>` branch | skipped — no content |
| `sysfig push` | **never** included | **never** included |
| bundle export | **never** included | **never** included |
| `sysfig status` | `SYNCED` / `DIRTY` + `local` tag | `SYNCED` / `TAMPERED` + `hash` tag |
| `sysfig audit` | included by default | included by default |
| `sysfig diff` | works (local repo copy) | not applicable |
| `sysfig log` | shows local commit history | not applicable |
| `sysfig apply` | writes file from local repo | not applicable |

---

## Security notes

- `local/*` branches exist **only** in `~/.sysfig/repo.git` on this machine.
  They are excluded from both `git push` (refspec filter) and bundle creation
  (`git bundle create` only bundles `track/*` and `manifest`).
- If `~/.sysfig/repo.git` is on an encrypted partition, local-only content is
  protected at rest without any additional flags.
- `--hash-only` provides tamper detection with zero local content exposure —
  even if `~/.sysfig/` is compromised, no file content is recoverable from it.
