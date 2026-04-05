# Getting Started with sysfig

> **Config management for people who own their machines.**
> Track, encrypt, and audit system configs — without thinking about git.

Welcome! If you're new to `sysfig`, the most important thing to know is the **mental model**. While `sysfig` has many powerful features (encryption, hooks, remote deployments, integrity audit), you only need to learn 3 commands to use it effectively:

1. `sysfig track <file>` — Add a file to your local configuration vault.
2. `sysfig sync` — Commit all your tracked changes and (optionally) push them.
3. `sysfig deploy <url>` — Replicate your exact setup on a brand new machine in one command.

Both dotfiles and `/etc/` configs land in a single repo under `~/.sysfig`. No separate "root repo", no complex symlink trees, no `--flags`.

---

## Install

Download the latest binary for your architecture:

```sh
curl -Lo /usr/local/bin/sysfig \
  https://github.com/aissat/sysfig/releases/latest/download/sysfig-linux-amd64
chmod +x /usr/local/bin/sysfig
```

Verify:

```sh
sysfig --help
```

Dependencies: `git` and (optionally) `age` for encryption.

```sh
# Debian/Ubuntu
apt install git age

# Fedora/RHEL
dnf install git age

# Alpine
apk add git age
```

---

## Quick start

### 1. Track your dotfiles (no sudo)

```sh
sysfig track ~/.bashrc
sysfig track ~/.vimrc
sysfig track ~/.ssh/config
```

On the first `track`, sysfig initialises `~/.sysfig` automatically — no `init` step needed.

### 2. Track system configs (with sudo)

```sh
sudo sysfig track /etc/nginx/nginx.conf
sudo sysfig track /etc/ssh/sshd_config
sudo sysfig track /etc/hosts
```

`sudo` gives sysfig read access to root-owned files. The data still lands in **your** `~/.sysfig` — not root's. After every sudo write, sysfig re-chowns `~/.sysfig` back to you, so non-sudo commands like `sysfig status` or `sysfig node add` work without any permission errors.

### 3. Track secrets (encrypted)

Generate a master key once:

```sh
sysfig keys generate
```

Then track any secret file with `--encrypt`:

```sh
sysfig track --encrypt ~/.config/tokens.env
sudo sysfig track --encrypt /etc/app/secret.env
```

Secrets are encrypted with [age](https://age-encryption.org/) before being written to the repo. The plaintext never touches disk inside the repo.

> **Back up `~/.sysfig/keys/master.key`** — without it you cannot decrypt your secrets.

### 4. Check status

```sh
sysfig status
```

```
PATH                        HASH        STATUS
────────────────────────────────────────────────────────────────
/etc/nginx/nginx.conf       ca0ed99c    SYNCED
/etc/ssh/sshd_config        3b71fa2e    DIRTY
/home/you/.bashrc           018e4a02    SYNCED
/home/you/.config/tokens    7734be1e    SYNCED  enc
────────────────────────────────────────────────────────────────
  4 files  ·  3 synced  ·  1 dirty
```

Status labels: `SYNCED` — matches the repo. `DIRTY` — modified since last sync (run `sysfig sync`). `PENDING` — the repo is ahead of disk (run `sysfig apply`). `STALE` — remote-tracked file not checked live yet (run `sysfig status --fetch`). `MISSING` — file deleted from disk.

### 5. Commit changes

```sh
sysfig sync --message "tuned sshd"
```

This stages all modified files and creates a local git commit — works fully offline.

### 6. Set a remote and push

```sh
sysfig remote set git@github.com:you/conf.git

# First push to a new/non-empty remote:
sysfig sync --push --force

# Every subsequent push:
sysfig sync --push
```

### 7. Air-gapped or no git server? Use a bundle remote

If you can't reach a git server (corporate network, air-gapped machine, NFS share), sysfig can use a single bundle file as the transport. Everything else stays the same.

```sh
# Machine A — push to a shared NFS path
sysfig remote set bundle+local:///mnt/corp-nfs/sysfig/machineA.bundle
sysfig sync --push --auto

# Machine B — pull from the same path
sysfig remote set bundle+local:///mnt/corp-nfs/sysfig/machineA.bundle
sysfig sync --pull --auto
sysfig apply                  # deploy pulled configs to disk
```

Works the same over SSH (no git daemon needed — only `scp`):

```sh
sysfig remote set bundle+ssh://backup@fileserver/srv/sysfig/machineA.bundle
sysfig sync --push --auto
```

---

## Day-to-day workflow

```sh
# Edit a config
vim /etc/nginx/nginx.conf

# See what changed
sysfig status
sysfig diff

# Commit and push
sysfig sync --push --message "nginx: increase worker connections"
```

---

## Auto-sync with watch

`sysfig watch` monitors your tracked files and commits automatically whenever they change — no manual `sysfig sync` needed.

Works correctly with all editors: `sed -i`, vim, nano, and any tool that saves via a temp file + rename (atomic saves) are all detected by watching the parent directory for `CREATE` events.

```sh
# Start watcher in the foreground (Ctrl-C to stop)
sysfig watch

# Also push to remote after every commit
sysfig watch --push

# Adjust debounce window (default 2s)
sysfig watch --debounce 5s

# Preview changes without committing
sysfig watch --dry-run
```

When available, watch also shows who triggered the change:

```text
15:04:05  changed  /etc/nginx/nginx.conf
          actor    vim · pid 1234 · user you · exact
          committed etc/nginx/nginx.conf
```

On Linux, process attribution is best with `setcap cap_sys_admin+ep $(command -v sysfig)` so `sysfig watch` keeps running as your user while fanotify remains available. Without that capability, sysfig falls back to best-effort process detection. Other platforms still work, but without actor attribution.

Install as a systemd user service so it runs on every login:

```sh
# Commit-only service
sysfig watch install --enable

# Commit + push service (keeps remote bundle in sync automatically)
sysfig watch install --enable --push

sysfig watch status
```

---

## Restore on a new machine

### First time (bootstrap)

`bootstrap` clones the repo and **applies all configs immediately** — one command, machine is ready:

```sh
sysfig bootstrap git@github.com:you/conf.git
```

If some files need root (e.g. `/etc/`), sysfig applies what it can and tells you exactly what to re-run:

```
  ✓ Applied: ~/.zshrc
  ✓ Applied: ~/.bashrc
  error: /etc/nginx/nginx.conf — permission denied
  warn:  Some files require elevated privileges.
         Re-run: sudo sysfig apply
```

Want to review before applying? Use `--no-apply`:

```sh
sysfig bootstrap git@github.com:you/conf.git --no-apply
sysfig status   # review
sysfig apply    # apply manually
```

### Ongoing updates (deploy)

After the machine is set up, use `deploy` to pull + apply the latest changes:

```sh
sysfig deploy
```

### Air-gapped / no git server?

Both `bootstrap` and `deploy` accept bundle remotes:

```sh
# First-time on an air-gapped machine (USB or NFS share)
sysfig bootstrap bundle+local:///mnt/usb/ops.bundle

# Ongoing updates from the same bundle
sysfig deploy bundle+local:///mnt/usb/ops.bundle
```

---

## What's next?

- **[Run hooks after apply](hooks.md):** Automatically reload systemd services or validate files (e.g. `nginx -t`) after a deploy.
- **[Share secrets with remote machines](secrets.md):** Securely share encrypted configs with multiple nodes without sharing your master key.
- **[Local integrity tracking](integrity.md):** Track sensitive files with `--hash-only` or `--local` so drift is detected immediately without content leaving the machine.
- **[Bundle remotes RFC](rfcs/bundle-remotes.md):** Deep dive into the bundle transport design — atomic writes, verification, publication model, and future phases.
- **[Config Sources](config-sources.md):** Render shared config templates (proxy, DNS, NTP) across many machines from a single bundle — from creating a profile to deploying on a new machine.

---

## Tracking /etc files

Files under `/etc/` are owned by root. You need `sudo` to apply them:

```sh
sudo sysfig track /etc/nginx/nginx.conf
sudo sysfig apply
```

`sysfig track` and `sysfig sync` can run as your regular user — they only write to `~/.sysfig/`. Only `apply` (writing files to disk) needs `sudo` for root-owned paths.

If you run `sudo sysfig apply` often, add a sudoers entry so it doesn't prompt:

```sh
# /etc/sudoers.d/sysfig  (use visudo or sudoedit)
you ALL=(root) NOPASSWD: /usr/local/bin/sysfig
```

Replace `you` with your username and the path with wherever sysfig is installed (`which sysfig`).

> **Integrity audit of /etc files:** `sysfig audit` reads file content to compute hashes — run it with `sudo` if your tracked files include paths your user can't read.

---

## Common commands

| Command | What it does |
|---|---|
| `sysfig track <path>` | Start tracking a file |
| `sysfig track --encrypt <path>` | Track and encrypt a secret |
| `sysfig status` | Show sync state of all tracked files |
| `sysfig diff` | Show what changed since last commit |
| `sysfig sync` | Commit all changes locally |
| `sysfig sync --push` | Commit and push to remote |
| `sysfig sync --pull --push` | Pull, commit, push (full round-trip) |
| `sysfig remote set <url>` | Set the remote URL (git or `bundle+local://` / `bundle+ssh://`) |
| `sysfig remote show` | Show the current remote |
| `sysfig keys generate` | Generate a master encryption key |
| `sysfig undo <path>` | Discard unsaved edits, restore from last sync |
| `sysfig undo <commit> <path>` | Rewind a file's history to a specific commit |
| `sysfig audit` | Check integrity of local-only and hash-only files |
| `sysfig log` | Show commit history |
| `sysfig watch` | Auto-commit when tracked files change |
| `sysfig watch --push` | Auto-commit and push to remote on every change |
| `sysfig watch install --enable` | Install watch as a systemd user service |
| `sysfig watch install --enable --push` | Install watch service with auto-push |
| `sysfig bootstrap <url>` | First-time setup: clone + apply all configs immediately |
| `sysfig bootstrap <url> --no-apply` | Clone only — apply manually afterwards |
| `sysfig deploy` | Pull latest configs from remote and apply (ongoing) |
| `sysfig doctor` | Check environment health |
| `sysfig node add <name> <pubkey>` | Register a remote machine for multi-recipient encryption |
| `sysfig node list` | Show registered nodes |
| `sysfig node remove <name>` | Unregister a node |

---

## File layout

```
~/.sysfig/
  repo.git/          # bare git repo (all tracked file content lives here)
  keys/
    master.key       # age private key — back this up!
  state.json         # index of tracked files and their hashes
  backups/           # automatic backups of overwritten files
```

---

## Tips

**Mix dotfiles and system configs freely** — they all go into the same repo. Use `sysfig status` to see everything at a glance.

**Offline-safe** — `sysfig sync` (without `--push`) works with no network. Push whenever you're online.

**Idempotent deploy** — `sysfig deploy` is safe to re-run. It skips files that are already up to date.

**See what's encrypted** — `sysfig status` shows `ENCRYPTED` for files that have never been decrypted locally, and `SYNCED`/`DIRTY` for plaintext files.
