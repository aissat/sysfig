# Getting Started with sysfig

> **The Linux config manager that unifies dotfiles and `/etc` system configs.**

Welcome! If you're new to `sysfig`, the most important thing to know is the **mental model**. While `sysfig` has many powerful features (encryption, hooks, remote deployments, safe atomic rollbacks), you only need to learn 3 commands to use it effectively:

1. `sysfig track <file>` — Add a file to your local configuration vault.
2. `sysfig sync` — Commit all your tracked changes and (optionally) push them.
3. `sysfig deploy <url>` — Replicate your exact setup on a brand new machine in 10 seconds.

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

**Air-gapped / no git server?** `deploy` accepts bundle remotes too — works exactly the same way:

```sh
# First-time bootstrap from an NFS share (no internet, no GitHub)
sysfig deploy bundle+local:///mnt/corp-nfs/sysfig/ops.bundle

# First-time bootstrap from a remote SSH file server
sysfig deploy bundle+ssh://backup@fileserver/srv/sysfig/ops.bundle
```

sysfig creates a bare repo locally, pulls all branches from the bundle, seeds `state.json` from the manifest, and applies all tracked files — identical result to a git remote deploy.

---

## What's next?

- **[Run hooks after apply](hooks.md):** Automatically reload systemd services or validate files (e.g. `nginx -t`) after a deploy.
- **[Share secrets with remote machines](secrets.md):** Securely share encrypted configs with multiple nodes without sharing your master key.
- **[Bundle remotes RFC](rfcs/bundle-remotes.md):** Deep dive into the bundle transport design — atomic writes, verification, publication model, and future phases.

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
| `sysfig remote set <url>` | Set the remote URL (git or `bundle+local://` / `bundle+ssh://`) |
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
