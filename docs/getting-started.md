# Getting Started with sysfig

> Track your dotfiles and system configs in one repo — no flags, no boilerplate.

---

## What you get

- `sysfig track ~/.bashrc` — version-control a dotfile
- `sudo sysfig track /etc/nginx/nginx.conf` — version-control a system config (reads as root, stores in **your** repo)
- `sysfig sync --push` — commit everything and push to GitHub/GitLab
- `sysfig deploy git@github.com:you/conf.git` — restore everything on a new machine in one command

Both dotfiles and `/etc/` configs land in a single repo under `~/.sysfig`. No separate "root repo", no env vars, no `--flags`.

---

## Install

Download the latest binary for your architecture:

```sh
curl -Lo /usr/local/bin/sysfig \
  https://github.com/sysfig-dev/sysfig/releases/latest/download/sysfig-linux-amd64
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
ID                                  STATUS          SYSTEM PATH
────────────────────────────────────────────────────────────────
etc_nginx_nginx_conf                SYNCED          /etc/nginx/nginx.conf
etc_ssh_sshd_config                 DIRTY/MODIFIED  /etc/ssh/sshd_config
home_you__bashrc                    SYNCED          /home/you/.bashrc
home_you__config_tokens_env         ENCRYPTED       /home/you/.config/tokens.env
────────────────────────────────────────────────────────────────
  4 files  ·  2 synced  ·  1 dirty  ·  1 encrypted
```

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

```sh
# Start watcher in the foreground (Ctrl-C to stop)
sysfig watch

# Adjust debounce window (default 2s)
sysfig watch --debounce 5s

# Preview changes without committing
sysfig watch --dry-run
```

Install as a systemd user service so it runs on every login:

```sh
sysfig watch install
sysfig watch status
```

---

## Run hooks after apply

Hooks let you validate or reload services automatically after `sysfig apply` writes a config file.

Create `~/.sysfig/hooks.yaml` (never committed to the repo):

```yaml
# binaries allowed in exec hooks — add any tool you need
allowlist: [nginx, sshd, apachectl, haproxy]

hooks:
  nginx_validate:
    on: [etc_nginx_nginx_conf]     # file ID from 'sysfig status'
    type: exec
    cmd: [nginx, -t]               # runs: nginx -t

  nginx_reload:
    on: [etc_nginx_nginx_conf]
    type: systemd_reload
    service: nginx

  sshd_validate:
    on: [etc_ssh_sshd_config]
    type: exec
    cmd: [sshd, -t]
```

When `sysfig apply` writes `/etc/nginx/nginx.conf`:
- `nginx_validate` runs `nginx -t` — if it fails, apply exits with error
- `nginx_reload` runs `systemctl reload nginx`

Supported hook types:

| Type | What it does |
|---|---|
| `exec` | Runs `cmd: [binary, args...]` — binary must be in `allowlist` |
| `systemd_reload` | `systemctl reload <service>` |
| `systemd_restart` | `systemctl restart <service>` |

> Adding a new service requires only editing `hooks.yaml` — no code changes.

---

## Share secrets with remote machines (nodes)

If you need a remote server to decrypt your encrypted configs (e.g. during `sysfig deploy`), register it as a node with its own [age](https://age-encryption.org/) key. sysfig will automatically re-encrypt all secrets for every registered node on the next `sync`.

### 1. Generate a key on the remote server

```sh
# On the remote server
age-keygen -o ~/.sysfig/keys/server.key
# Public key: age1abc123...
```

### 2. Register the node on your local machine

```sh
sysfig node add myserver age1abc123...
```

### 3. Re-encrypt and push

```sh
sysfig sync --push --message "add myserver node"
```

sysfig re-encrypts every secret for both your master key **and** the server's public key. Each machine can only decrypt using its own key.

### 4. Deploy to the server

```sh
sysfig deploy --host user@myserver git@github.com:you/conf.git
```

The server decrypts secrets with its `~/.sysfig/keys/server.key`. Your master key never leaves your machine.

### Manage nodes

```sh
sysfig node list             # show all registered nodes
sysfig node remove myserver  # unregister — re-encrypt single-recipient on next sync
```

> After `node remove`, run `sysfig sync --push` to re-encrypt secrets back to single-recipient. The removed server will get `age: no identity matched any of the recipients` on its next decrypt attempt.

---

## Restore on a new machine

```sh
# One command — clones, applies all configs, done
sysfig deploy git@github.com:you/conf.git
```

Or step by step:

```sh
sysfig setup git@github.com:you/conf.git   # clone repo
sysfig apply                                # write configs to disk
```

---

## sudoers (allow sudo sysfig without a password)

If you run `sudo sysfig` often, add a sudoers entry so it doesn't prompt:

```sh
# /etc/sudoers.d/sysfig  (use visudo or sudoedit)
you ALL=(root) NOPASSWD: /usr/local/bin/sysfig
```

Replace `you` with your username and the path with wherever sysfig is installed (`which sysfig`).

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
| `sysfig remote set <url>` | Set the git remote URL |
| `sysfig remote show` | Show the current remote |
| `sysfig keys generate` | Generate a master encryption key |
| `sysfig log` | Show commit history |
| `sysfig watch` | Auto-commit when tracked files change |
| `sysfig watch install` | Install watch as a systemd user service |
| `sysfig deploy <url>` | Clone and apply configs on a new machine |
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
