# sysfig

> **Config management that thinks like a sysadmin, not a git wrapper.**

`sysfig` is a security-first configuration management tool for Linux. It version-controls your config files — both dotfiles and `/etc/` system configs — in a bare git repository, deploys them across machines with a single command, encrypts secrets with [age](https://age-encryption.org/), tracks file ownership and permissions, and stays fully offline-capable.

---

## What it looks like

**One command to deploy everything (new or existing machine):**

```
$ sysfig deploy git@github.com:you/configs.git

  sysfig deploy — syncing your environment
  ─────────────────────────────────────────────

  ✓ Config repo cloned
     location: /home/you/.sysfig/repo.git
  ✓ Manifest seeded: 12 tracked file(s)

  ✓ Applied: nginx_main          → /etc/nginx/nginx.conf
  ✓ Applied: sshd_config         → /etc/ssh/sshd_config
  ✓ Applied: bashrc              → /home/you/.bashrc
  ... (9 more)

────────────────────────────────────────────────────────────────────────────
  ✓ Deploy complete!

  Applied: 12

  What to do next:
   sysfig status  See current sync state
   sysfig doctor  Check environment health
   sysfig log     See commit history
```

**Setting up a new machine (step-by-step alternative):**

```
  sysfig setup — bootstrapping your environment
  ─────────────────────────────────────────────

  [1] Remote config repository
      url: git@github.com:you/myconfigs.git

  [2] Fetching your config repo
  ✓ Config repo ready
      location: /home/you/.sysfig/repo.git

  [3] Reading manifest
  ✓ Found 12 tracked file(s) in your manifest

────────────────────────────────────────────────────────────────────────────
  ✓ Setup complete!

  What to do next:
   sysfig apply    Deploy your config files to this machine
   sysfig status   Check sync status at any time
   sysfig log      See your commit history
   sysfig doctor   Run a health check if anything seems wrong
```

**Daily status check (grouped by directory):**

```
PATH / STATUS
────────────────────────────────────────────────────────────────────────────
/etc/nginx/
└ nginx.conf                             DIRTY/MODIFIED
   ⚠ mode:   0644 → 0600
/etc/ssh/                                1 synced
/etc/ssl/private/server.key              ENCRYPTED
/home/you/.bashrc                        PENDING/APPLY
────────────────────────────────────────────────────────────────────────────
  4 files  ·  1 synced  ·  1 dirty  ·  1 pending  ·  1 encrypted
```

**Commit history as a tree:**

```
* a3f2b1c 2026-03-18 hardened sshd_config (HEAD -> master)
* 8d4e92a 2026-03-17 sysfig: sync 2026-03-17T14:22:01
* 1c7f03b 2026-03-15 added bashrc and vimrc
* 3e9d01f 2026-03-10 initial commit
```

---

## Table of Contents

- [Why sysfig?](#why-sysfig)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Architecture](#architecture)
- [Quick Start](#quick-start)
  - [First machine — track and push](#first-machine--track-and-push)
  - [New machine — setup and apply](#new-machine--setup-and-apply)
  - [Daily workflow](#daily-workflow)
- [Command Reference](#command-reference)
  - [deploy](#deploy)
  - [setup](#setup)
  - [init](#init)
  - [track](#track)
  - [untrack](#untrack)
  - [apply](#apply)
  - [status](#status)
  - [diff](#diff)
  - [sync](#sync)
  - [watch](#watch)
  - [push](#push-deprecated) _(deprecated)_
  - [pull](#pull-deprecated) _(deprecated)_
  - [log](#log)
  - [profile](#profile)
  - [keys](#keys)
  - [doctor](#doctor)
  - [snap](#snap)
  - [node](#node)
- [Configuration File: sysfig.yaml](#configuration-file-sysfigyaml)
- [state.json vs sysfig.yaml](#statejson-vs-sysfigyaml)
- [Encryption](#encryption)
- [File Ownership & Permissions](#file-ownership--permissions)
- [Hooks](#hooks)
- [Directory Layout](#directory-layout)
- [Exit Codes](#exit-codes)
- [Offline Safety Model](#offline-safety-model)
- [Security Design](#security-design)
- [Troubleshooting](#troubleshooting)

---

## Why sysfig?

| Tool       | `/etc/` support | Encryption    | Offline-safe | Metadata tracking | Backup on apply | Health check | Remote deploy (no agent) | Local snapshots | Single binary |
| ---------- | --------------- | ------------- | ------------ | ----------------- | --------------- | ------------ | ------------------------ | --------------- | ------------- |
| GNU Stow   | ✗               | ✗             | ✓            | ✗                 | ✗               | ✗            | ✗                        | ✗               | ✓             |
| YADM       | ✗               | partial       | ✓            | ✗                 | ✗               | ✗            | ✗                        | ✗               | ✓             |
| Chezmoi    | partial         | partial       | ✓            | ✗                 | ✗               | ✗            | ✗                        | ✗               | ✓             |
| Ansible    | ✓               | via vault     | ✗            | ✓                 | ✗               | ✗            | requires agent           | ✗               | ✗             |
| **sysfig** | **✓**           | **✓ (age)**   | **✓**        | **✓**             | **✓**           | **✓ doctor** | **✓ (SSH only)**         | **✓ snap**      | **✓**         |

**Key design decisions:**

- **No symlinks.** Files are physical copies. `ls -la` on your system never reveals your repo structure.
- **Bare git repo.** The shadow repo lives at `~/.sysfig/repo.git/` — no working tree, no accidental edits.
- **Offline-first.** `track`, `apply`, `status`, `sync`, `diff` work 100% without network. Only `sync --push` and `sync --pull` touch the wire.
- **Per-file encryption.** Secrets are encrypted with [age](https://age-encryption.org/) + HKDF-SHA256 per-file keys derived from a single master key.
- **Metadata tracking.** Records `uid`, `gid`, and `mode` for every file. `status` warns when permissions drift.
- **Atomic backups.** Every `apply` creates a timestamped backup before overwriting anything on disk.

---

## Prerequisites

| Dependency | Required for              | Notes                                              |
| ---------- | ------------------------- | -------------------------------------------------- |
| `git`      | Everything                | Must be on `$PATH`. v2.x or later.                 |
| `diff`     | `sysfig diff`             | Usually pre-installed on Linux.                    |
| `ssh`      | `deploy --host` only      | OpenSSH client on the **local** machine only.      |
| Go 1.21+   | Building from source      | Not needed if using a binary.                      |

---

## Installation

**From source:**

```bash
git clone https://github.com/sysfig-dev/sysfig
cd sysfig
go build -o sysfig ./cmd/sysfig
sudo mv sysfig /usr/local/bin/
```

**Verify:**

```bash
sysfig
# → Usage: sysfig <command> [options]
```

---

## Architecture

```
  Your system files          sysfig               Remote git repo
  ─────────────────          ──────               ───────────────

  /etc/nginx/nginx.conf ──track──▶ ~/.sysfig/ ──push──▶ github.com/you/configs
  /etc/ssh/sshd_config            repo.git/  ◀──pull──
  ~/.bashrc             ◀──apply──
                                  state.json   (local cache only)
                                  sysfig.yaml  (committed to git, shared)
                                  keys/        (local only, never pushed)
                                  backups/     (local only)
                                  hooks.yaml   (local only, never pushed)
```

**Two files you need to understand:**

| File           | Where it lives       | What it is                                              |
| -------------- | -------------------- | ------------------------------------------------------- |
| `sysfig.yaml`  | Inside the git repo  | The manifest. Committed and shared across all machines. |
| `state.json`   | `~/.sysfig/`         | Local cache of hashes, metadata, and sync timestamps. Never committed. |

When you run `sysfig setup` on a new machine, sysfig reads `sysfig.yaml` from the cloned repo and builds `state.json` locally. They are separate by design: the manifest is the source of truth for *what* is tracked; state is the per-machine cache for *how in sync* each file is.

---

## Quick Start

### First machine — track and push

```bash
# Track dotfiles (no sudo) — ~/.sysfig is created automatically on first track
sysfig track ~/.bashrc
sysfig track ~/.vimrc

# Track system configs (sudo reads the file, repo stays in your ~/.sysfig)
sudo sysfig track /etc/nginx/nginx.conf
sudo sysfig track /etc/ssh/sshd_config

# Track secrets (encrypted with age)
sysfig keys generate
sysfig track --encrypt ~/.config/tokens.env
sudo sysfig track --encrypt /etc/app/secret.env

# Commit everything to the local repo
sysfig sync --message "initial commit"

# Set remote and push (--force for first push to a non-empty remote)
sysfig remote set git@github.com:you/myconfigs.git
sysfig sync --push --force
```

### New machine — deploy (one command)

```bash
# Everything in one shot: clone + seed state + apply
sysfig deploy git@github.com:you/myconfigs.git

# If you have encrypted files, copy your master key first:
# scp oldhost:~/.sysfig/keys/master.age-identity ~/.sysfig/keys/
# chmod 0600 ~/.sysfig/keys/master.age-identity
# Then re-run — sysfig deploy is idempotent.
```

Or step-by-step if you prefer explicit control:

```bash
sysfig setup git@github.com:you/myconfigs.git
sysfig apply
sysfig status
```

### Daily workflow

You edited `/etc/nginx/nginx.conf` on your server. Here is what to do:

```bash
# See what changed
sysfig status
# → sshd_config    DIRTY/MODIFIED    /etc/nginx/nginx.conf

# Review the exact diff
sysfig diff --id nginx_main

# Commit the change locally (no network needed)
sysfig sync --message "tuned worker_processes"

# Push when you're back online
sysfig push
```

Someone pushed a change to your config repo from another machine:

```bash
# Fetch remote changes
sysfig pull

# See what came in
sysfig status
# → nginx_main    PENDING/APPLY    /etc/nginx/nginx.conf

# Deploy it
sysfig apply --id nginx_main
```

---

## Command Reference

### `deploy`

Pull from remote and apply configs — one command for everything.

```
sysfig deploy [<remote-url>] [options]
```

The recommended entry point for both first-time machines and routine updates. Idempotent: safe to re-run as many times as needed.

**Behaviour:**

| Situation | What happens |
| --------- | ------------ |
| First-time machine (no local repo) | Clone remote → seed `state.json` → apply all files |
| Already set up, network available | Pull latest → apply all files |
| Already set up, `--no-pull` | Skip pull → apply from local repo (fully offline) |
| Pull fails (network error) | Fall back to local repo → apply (non-fatal) |

**Local deploy options:**

| Flag               | Default     | Description                                              |
| ------------------ | ----------- | -------------------------------------------------------- |
| `--base-dir`       | `~/.sysfig` | Directory where sysfig stores its data                   |
| `--id`             | all         | Apply only this ID (repeatable)                          |
| `--dry-run`        | `false`     | Print what would happen without writing anything         |
| `--no-backup`      | `false`     | Skip pre-apply backup                                    |
| `--skip-encrypted` | `false`     | Skip encrypted files when master key is absent           |
| `--no-pull`        | `false`     | Skip pull — apply from local repo only (offline mode)    |
| `--yes`            | `false`     | Non-interactive: skip all prompts                        |

**Remote deploy options (`--host`):**

| Flag           | Default | Description                                                       |
| -------------- | ------- | ----------------------------------------------------------------- |
| `--host`       | —       | SSH target (`user@hostname`) — pushes files to the remote instead |
| `--ssh-key`    | —       | Path to SSH identity file (default: use ssh-agent)                |
| `--ssh-port`   | `22`    | SSH port on the remote host                                       |

> When `--host` is set sysfig reads files from the **local** repo and writes them to the remote over SSH. **No sysfig installation is needed on the remote** — only `mkdir`, `cat`, and `chmod`.

**Local deploy examples:**

```bash
# First-time machine
sysfig deploy git@github.com:you/myconfigs.git

# Already set up — pull + apply
sysfig deploy

# Preview what would change
sysfig deploy --dry-run

# Offline — apply from local repo, no network
sysfig deploy --no-pull

# CI/scripts — non-interactive
sysfig deploy git@github.com:you/myconfigs.git --yes --skip-encrypted

# Apply only specific files
sysfig deploy --id nginx_main --id sshd_config
```

**Remote deploy examples:**

```bash
# Deploy all tracked files to a remote server
sysfig deploy --host user@192.168.1.10

# Preview what would be pushed (no SSH writes)
sysfig deploy --host user@server --dry-run

# Deploy only specific files
sysfig deploy --host user@server --id nginx_main --id sshd_config

# Use a specific SSH key
sysfig deploy --host deploy@server --ssh-key ~/.ssh/deploy_ed25519

# Non-standard SSH port
sysfig deploy --host user@server --ssh-port 2222
```

**Use in CI / server provisioning:**

```bash
#!/bin/bash
# Ensure the machine matches the config repo — run on boot or in cron
sysfig deploy git@github.com:ops/server-configs.git --yes --skip-encrypted

# Or push from a central ops machine to all servers (no sysfig needed on targets)
for host in web1 web2 web3; do
  sysfig deploy --host deploy@$host --id nginx_main
done
```

**Remote deploy behaviour table:**

| Situation                          | What happens                                     |
| ---------------------------------- | ------------------------------------------------ |
| `--host` set, `--dry-run`          | Lists files that would be pushed — no SSH writes |
| `--host` set, file is encrypted    | Decrypted locally with master key, then pushed   |
| `--host` set, no master key        | Fails unless `--skip-encrypted` is set           |
| `--host` set, `--id` filter        | Only matching files are pushed                   |
| Remote path's parent dir missing   | Created automatically (`mkdir -p`)               |

---

### `setup`

Bootstrap sysfig on a new machine from an existing remote config repo.

```
sysfig setup [<remote-url>] [options]
```

This is the primary onboarding command. It:
1. Detects if this machine is already set up (no-op if so — shows hints instead)
2. Clones your remote config repo as a bare git repository to `~/.sysfig/repo.git/`
3. Seeds `state.json` from the `sysfig.yaml` manifest
4. Writes `hooks.yaml` from `hooks.yaml.example` if present in the repo

**Options:**

| Flag               | Default     | Description                                       |
| ------------------ | ----------- | ------------------------------------------------- |
| `--base-dir`       | `~/.sysfig` | Directory where sysfig stores its data            |
| `--configs-only`   | `false`     | Skip package installation, deploy configs only    |
| `--skip-encrypted` | `false`     | Skip encrypted files when master key is absent    |
| `--yes`            | `false`     | Non-interactive: skip all prompts                 |

**Examples:**

```bash
# Interactive (prompts for URL if stdin is a TTY)
sysfig setup

# Non-interactive / scripted
sysfig setup git@github.com:you/myconfigs.git

# No master key available — skip secrets
sysfig setup --skip-encrypted git@github.com:you/myconfigs.git

# Custom data directory
sysfig setup --base-dir /opt/sysfig git@github.com:you/myconfigs.git
```

> `sysfig clone` is a hidden alias for `setup` (backward compatibility).

---

### `init`

Initialise a fresh sysfig environment on a machine with no existing remote.

```
sysfig init [options]
```

Creates `~/.sysfig/` with a bare git repo, empty state, a `sysfig.yaml` template, and optionally generates a master encryption key. Idempotent — safe to run twice.

> **`init` is optional.** `sysfig track` auto-initialises `~/.sysfig` on the first run if it does not already exist. You only need `init` explicitly if you want to generate an encryption key before tracking any files, or to customise the base dir.

**Options:**

| Flag         | Default     | Description                                    |
| ------------ | ----------- | ---------------------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores its data         |
| `--encrypt`  | `false`     | Also generate a master key for encryption      |

**Example output:**

```
Initialising sysfig in /home/you/.sysfig

  ✓ Shadow repo:   /home/you/.sysfig/repo.git
  ✓ Backups dir:   /home/you/.sysfig/backups
  ✓ Keys dir:      /home/you/.sysfig/keys
  ✓ State file:    /home/you/.sysfig/state.json
  ✓ sysfig.yaml:   /home/you/.sysfig/repo.git  (inside repo)
  ✓ Hooks example: /home/you/.sysfig/repo.git/hooks.yaml.example
```

---

### `track`

Start tracking a config file (or an entire directory).

```
sysfig track <path> [options]
```

Copies the file into the bare git repo's index (staged, not yet committed), records its BLAKE3 hash and `uid`/`gid`/`mode` metadata in `state.json`, and updates `sysfig.yaml`.

**`~/.sysfig` is created automatically** on the first `track` — no explicit `sysfig init` needed.

**`sudo sysfig track /etc/...`** — runs as root to read privileged files, but the repo and state live in the **invoking user's** `~/.sysfig` (resolved via `SUDO_USER`). No second repo, no flags needed. After every sudo write, sysfig re-chowns `~/.sysfig` back to the invoking user, so subsequent non-sudo commands (`sysfig node add`, `sysfig status`, etc.) work without permission errors.

**Run `sysfig sync` after tracking to create a commit.**

**Options:**

| Flag          | Default     | Description                                              |
| ------------- | ----------- | -------------------------------------------------------- |
| `--id`        | derived     | Explicit tracking ID. Derived from path if omitted.      |
| `--tag`       | —           | Label to attach (repeatable: `--tag web --tag nginx`)    |
| `--encrypt`   | `false`     | Encrypt the file at rest in the repo                     |
| `--template`  | `false`     | Mark as a template with `{{variable}}` expansions        |
| `--exclude`   | —           | Path or glob to skip when tracking a directory (repeatable) |
| `--base-dir`  | `~/.sysfig` | Directory where sysfig stores its data                   |

**ID derivation:**

If `--id` is omitted, the ID is derived from the absolute path: strip the leading `/`, replace `/` and `.` with `_`. Leading dots in a path component do not produce a double underscore.

```
/etc/nginx/nginx.conf  →  etc_nginx_nginx_conf
/home/you/.bashrc      →  home_you_bashrc
```

**Examples:**

```bash
# Single file
sysfig track /etc/nginx/nginx.conf

# With explicit ID and tags
sysfig track /etc/nginx/nginx.conf --id nginx_main --tag web --tag nginx

# Encrypt a secret
sysfig track /etc/myapp/secrets.env --encrypt

# Track an entire directory — auto-detected, no --recursive flag needed
sysfig track /etc/nginx/

# Track /etc but skip secrets
sysfig track /etc --exclude /etc/ssl/private --exclude /etc/shadow.d

# Glob pattern — skip all .bak files
sysfig track /etc/nginx --exclude "*.bak"

# Track a template file — placeholders substituted at apply time
sysfig track ~/.gitconfig --template

# After tracking, commit
sysfig sync
```

**Template variables:**

When `--template` is set, `sysfig apply` replaces `{{variable}}` placeholders with live values from the current machine. The repo always stores the raw template; each machine gets its own rendered copy.

| Placeholder | Value |
|-------------|-------|
| `{{hostname}}` | `os.Hostname()` |
| `{{user}}` | current username |
| `{{home}}` | current user's home directory |
| `{{os}}` | `linux` / `darwin` / `windows` |
| `{{env.NAME}}` | value of environment variable `NAME` |

```
# ~/.gitconfig stored in repo:
[user]
    name = {{user}}
    email = {{env.GIT_EMAIL}}
[core]
    hostname = {{hostname}}
```

On apply, `{{user}}` → `alice`, `{{hostname}}` → `webserver-01`, etc. Unknown placeholders cause an error before any file is written (typos are caught early).

**Example output (single file):**

```
Tracking /etc/nginx/nginx.conf

  ✓ ID:   nginx_main
  ✓ Repo: etc/nginx/nginx.conf
  ✓ Hash: 3a7f2b...
```

---

### `untrack`

Stop tracking one or more files without touching the system files.

```
sysfig untrack <path-or-id> [options]
```

Removes the matching record(s) from `state.json` and `sysfig.yaml`, and drops the file(s) from the git index. The actual system files are **never deleted** — only the tracking metadata is removed.

Accepts:
- An absolute file path: `sysfig untrack /etc/nginx/nginx.conf`
- A bare tracking ID: `sysfig untrack etc_nginx_nginx_conf`
- A directory path: removes all files tracked under that directory

**Run `sysfig sync` after untracking to commit the manifest change.**

**Options:**

| Flag         | Default     | Description                                  |
| ------------ | ----------- | -------------------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores its data       |

**Examples:**

```bash
# Remove a single file by path
sysfig untrack /etc/nginx/nginx.conf

# Remove a single file by its tracking ID
sysfig untrack etc_nginx_nginx_conf

# Remove all files tracked under /etc/nginx/
sysfig untrack /etc/nginx/

# After untracking, commit the manifest change
sysfig sync --message "stop tracking nginx"
```

**Example output:**

```
  ✓ Untracked: etc_nginx_nginx_conf  (/etc/nginx/nginx.conf)
```

---

### `apply`

Deploy tracked configs from the repo to the system.

```
sysfig apply [options]
```

For each tracked file:
1. Reads the content from `HEAD` in the bare repo
2. Decrypts if the file is encrypted (requires master key)
3. Creates a timestamped backup of the current system file in `~/.sysfig/backups/`
4. Writes the repo version to disk with the recorded permissions and ownership

**Options:**

| Flag          | Default     | Description                                              |
| ------------- | ----------- | -------------------------------------------------------- |
| `--id`        | all         | Apply only this ID (repeatable)                          |
| `--dry-run`   | `false`     | Print what would happen without writing anything         |
| `--no-backup` | `false`     | Skip pre-apply backup (use with care)                    |
| `--force`     | `false`     | Overwrite DIRTY (locally-modified) files without prompting |
| `--base-dir`  | `~/.sysfig` | Directory where sysfig stores its data                   |

**DIRTY file protection:**

By default, `apply` will **refuse** to overwrite a file that has been locally modified since the last `sysfig sync` (status: DIRTY). This prevents accidentally discarding in-progress edits.

```
  ⚠  Skipped nginx_main — file has local changes (DIRTY). Use --force to overwrite.
     Tip: run 'sysfig sync' first to commit local changes, or 'sysfig snap take' to snapshot them.
```

Use `--force` to override when you intentionally want to reset to the repo version.

**Examples:**

```bash
# Preview what will happen
sysfig apply --dry-run

# Apply everything (DIRTY files are skipped with a warning)
sysfig apply

# Apply only specific files
sysfig apply --id nginx_main --id sshd_config

# Force-overwrite even DIRTY files (after taking a snapshot first)
sysfig snap take --label "before reset" && sysfig apply --force
```

**Example output:**

```
  ✓ Applied: nginx_main
      → /etc/nginx/nginx.conf
      backup: /home/you/.sysfig/backups/2026-03-18T10-30-00/etc/nginx/nginx.conf

  ✓ Applied: bashrc
      → /home/you/.bashrc

────────────────────────────────────────────────────────────────────────────
  Applied: 2
```

---

### `status`

Show the sync status of all tracked files.

```
sysfig status [options]
```

Compares every tracked file against the repo using BLAKE3 content hashes. Also checks recorded `uid`/`gid`/`mode` against the current system state and reports drift inline.

**Default output is grouped by directory.** Directories where every file is SYNCED are collapsed to a single summary line. Directories with any dirty or pending files expand to show each affected file on its own line. If only one file is tracked in a directory, its full path is shown rather than a folder line.

```
PATH / STATUS
────────────────────────────────────────────────────────────────────────────
/etc/pacman.d/        10 synced
/etc/nginx/
└ nginx.conf          DIRTY/MODIFIED
/home/you/.zshrc      SYNCED
────────────────────────────────────────────────────────────────────────────
  12 files  ·  11 synced  ·  1 dirty
```

Use `--files` (or `-f`) to bypass grouping and see every tracked file individually on its own line.

**Status labels:**

| Label           | Meaning                                                             | Action                  |
| --------------- | ------------------------------------------------------------------- | ----------------------- |
| `SYNCED`        | System file matches repo                                            | Nothing to do           |
| `DIRTY`         | System file has been modified since last sync                       | Run `sysfig sync`       |
| `PENDING/APPLY` | Repo has a newer version than the system file                       | Run `sysfig apply`      |
| `MISSING`       | File is tracked but does not exist on the system                    | Run `sysfig apply`      |
| `ENCRYPTED`     | Encrypted file — content comparison skipped (no master key present) | Copy key, then re-check |

**Exit codes:** `0` = all SYNCED, `1` = any DIRTY/PENDING/MISSING, `2` = error.

**Options:**

| Flag         | Short | Default     | Description                                                    |
| ------------ | ----- | ----------- | -------------------------------------------------------------- |
| `--id`       | —     | all         | Check only this ID (repeatable)                                |
| `--files`    | `-f`  | `false`     | Flat list — show every tracked file individually (no grouping) |
| `--watch`    | `-w`  | `false`     | Continuously refresh status (Ctrl-C to stop)                   |
| `--interval` | —     | `3s`        | Refresh interval when `--watch` is set                         |
| `--base-dir` | —     | `~/.sysfig` | Directory where sysfig stores data                             |

**Script-friendly:**

```bash
# Use in CI, cron, or monitoring
if ! sysfig status; then
  echo "Config drift detected on $(hostname)" | mail -s "sysfig alert" ops@example.com
fi
```

**Live monitoring:**

```bash
# Refresh every 3 seconds (default)
sysfig status --watch

# Faster refresh
sysfig status -w --interval 1s
```

The watch mode clears the screen on the first frame, then redraws in-place to avoid flickering. Any transition to `DIRTY/MODIFIED` is immediately visible.

---

### `diff`

Show a unified diff between system files and repo versions.

```
sysfig diff [options]
```

Requires `diff` on `$PATH`. Output is colorized automatically when stdout is a TTY (`+` lines green, `-` lines red, `@@` headers cyan).

**Exit codes:** `0` = no differences, `1` = differences found, `2` = error.

**Options:**

| Flag         | Default    | Description                         |
| ------------ | ---------- | ----------------------------------- |
| `--id`       | all        | Diff only this ID (repeatable)      |
| `--color`    | auto (TTY) | Force or disable colorized output   |
| `--base-dir` | `~/.sysfig`| Directory where sysfig stores data  |

**Examples:**

```bash
sysfig diff
sysfig diff --id nginx_main
sysfig diff --no-color | grep "^[+-]"

# In a script: exit 1 if anything differs
sysfig diff --id sshd_config || echo "sshd_config has drifted"
```

---

### `sync`

Capture the current state of tracked files and commit locally (offline-safe).

```
sysfig sync [options]
```

Stages any modified tracked files, creates a git commit in the local bare repo, and updates `state.json` hashes and timestamps. No network access required. Use `--push` to also push in one step; use `--pull` to fetch remote changes before committing.

**Options:**

| Flag         | Default                    | Description                                           |
| ------------ | -------------------------- | ----------------------------------------------------- |
| `--message`  | `sysfig: sync <timestamp>` | Commit message                                        |
| `--push`     | `false`                    | Also push to remote after committing                  |
| `--pull`     | `false`                    | Fetch remote changes before committing (non-fatal)    |
| `--base-dir` | `~/.sysfig`                | Directory where sysfig stores data                    |

**Examples:**

```bash
sysfig sync
sysfig sync --message "hardened sshd_config"
sysfig sync --push             # commit + push in one step
sysfig sync --pull             # pull first, then commit locally
sysfig sync --pull --push      # full round-trip: pull → commit → push
```

**Example output:**

```
  ✓ Committed: hardened sshd_config
  ✓ Repo:      /home/you/.sysfig/repo.git
  ℹ Not pushed. Run sysfig sync --push when online.
```

> **Note:** `--pull` is non-fatal. If the remote is unreachable, sysfig prints a warning and continues with the local commit.

---

### `remote`

Manage the git remote for the sysfig repo without touching raw git commands.

```
sysfig remote <subcommand> [options]
```

| Subcommand | Description |
|---|---|
| `set <url>` | Set (or replace) the origin remote URL |
| `show` | Print the current remote URL |
| `remove` | Remove the origin remote |

```bash
# Set remote (idempotent — works whether origin exists or not)
sysfig remote set git@github.com:you/conf.git

# Show current remote
sysfig remote show

# First push to a non-empty remote (e.g. GitHub repo with a README)
sysfig sync --push --force
```

---

### `watch`

Auto-sync tracked files whenever they change on disk.

```
sysfig watch [subcommand] [options]
```

| Subcommand | Description |
|------------|-------------|
| _(none)_ / `run` | Run the watcher in the foreground (Ctrl-C to stop) |
| `install` | Write a systemd user service file |
| `uninstall` | Stop, disable, and remove the service file |
| `status` | Show `systemctl --user status sysfig-watch` |

#### Foreground mode

Starts a foreground process that monitors every tracked config file using OS-level filesystem events (`inotify` on Linux). When a change is detected, sysfig waits for the debounce window then runs `sysfig sync` automatically.

**Flags:**

| Flag         | Default     | Description                                         |
| ------------ | ----------- | --------------------------------------------------- |
| `--debounce` | `2s`        | Wait this long after the last change before syncing |
| `--dry-run`  | `false`     | Print detected changes without syncing              |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores its data              |

```bash
sysfig watch                    # foreground, 2s debounce
sysfig watch --debounce 500ms   # faster
sysfig watch --dry-run          # preview only
```

**Output:**

```
Watching tracked files for changes  (Ctrl-C to stop)
  base-dir: /home/you/.sysfig
  debounce: 2s

  10:42:15  synced  /etc/nginx/nginx.conf
            sysfig: sync 2026-03-18T10:42:15Z
```

#### Service mode (systemd)

Install as a persistent background service that starts on login and restarts automatically on failure.

```bash
# Write service file and enable immediately
sysfig watch install --enable

# Check it is running
sysfig watch status

# Remove the service
sysfig watch uninstall
```

**`watch install` flags:**

| Flag         | Default     | Description                                           |
| ------------ | ----------- | ----------------------------------------------------- |
| `--base-dir` | `~/.sysfig` | Base dir written into the ExecStart line              |
| `--debounce` | `2s`        | Debounce window written into the ExecStart line       |
| `--enable`   | `false`     | Also run `systemctl --user enable --now sysfig-watch` |

The generated unit file lives at `~/.config/systemd/user/sysfig-watch.service` and uses `Restart=on-failure` so the watcher recovers automatically if it crashes.

---

### `push` _(deprecated)_

> **Deprecated.** Use `sysfig sync --push` instead. This command is hidden from help output but still works for backward compatibility.

---

### `pull` _(deprecated)_

> **Deprecated.** Use `sysfig sync --pull` instead. This command is hidden from help output but still works for backward compatibility.

**Standard update workflow (new):**

```bash
sysfig sync --pull    # pull + commit locally
sysfig status         # see what is PENDING/APPLY
sysfig diff           # review the changes
sysfig apply          # deploy to system
```

---

### `log`

Show the commit history of your config repo as a graph tree.

```
sysfig log [options]
```

**Options:**

| Flag         | Default     | Description                                                  |
| ------------ | ----------- | ------------------------------------------------------------ |
| `-n`         | unlimited   | Limit to last N commits                                      |
| `--file`     | —           | Show only commits touching a specific path (repo-relative)   |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data                           |

**Examples:**

```bash
sysfig log                                      # full history
sysfig log -n 10                                # last 10 commits
sysfig log --file etc/nginx/nginx.conf          # nginx history only
sysfig log --file etc/ssh/sshd_config -n 5     # last 5 sshd changes
```

---

### `keys`

Manage the master encryption key.

```
sysfig keys <subcommand> [options]
```

**Subcommands:**

| Subcommand | Description                                           |
| ---------- | ----------------------------------------------------- |
| `info`     | Show the master key path and its age public key       |
| `generate` | Generate a new master key (fails if one already exists) |

**Options:**

| Flag         | Default     | Description                        |
| ------------ | ----------- | ---------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data |

```bash
sysfig keys info
sysfig keys generate
```

> **Back up your master key immediately.** Loss of the key means permanent loss of all encrypted files. The key lives at `~/.sysfig/keys/master.age-identity` and is **never** committed to git.

---

### `profile`

Manage isolated sets of tracked files. Each profile has its own git repo, state, keys, snapshots, and backups.

```
sysfig profile <subcommand>
sysfig --profile <name> <command>
```

**Why profiles?**

- Keep `work` configs (nginx, sshd) separate from `personal` dotfiles (.gitconfig, .bashrc)
- Manage different server roles from one machine (`--profile webserver`, `--profile database`)
- Share a profile with a team via a git remote without exposing personal configs

**The `--profile` flag** is a persistent flag available on every command. It redirects the base directory to `~/.sysfig/profiles/<name>`. You can also set `SYSFIG_PROFILE=<name>` in your shell environment to avoid typing it every time.

```
~/.sysfig/                     ← default profile (unchanged)
~/.sysfig/profiles/
├── work/                      ← sysfig --profile work ...
└── personal/                  ← sysfig --profile personal ...
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `profile list` / `profile ls` | List all profiles, mark active |
| `profile create <name>` | Init new isolated profile |
| `profile delete <name>` | Delete profile (`--force` required) |

**Options for `profile create`:**

| Flag     | Description |
|----------|-------------|
| `--from <url>` | Clone from remote git URL instead of creating empty repo |

**Examples:**

```bash
# Create profiles
sysfig profile create work
sysfig profile create personal

# Use a profile for every command
sysfig --profile work track /etc/nginx/nginx.conf
sysfig --profile work sync
sysfig --profile work status

# Or set it in the environment
export SYSFIG_PROFILE=work
sysfig track /etc/nginx/nginx.conf
sysfig sync

# List profiles
sysfig profile list

# Delete a profile (requires --force to prevent accidents)
sysfig profile delete personal --force
```

---

### `doctor`

Run a full health check of your sysfig environment.

```
sysfig doctor [options]
```

Audits every layer of the setup — prerequisites, base directory, git repo, state, file health, and encryption — and reports every finding with a colored icon, plain-English detail, and a concrete fix hint. Read-only: never modifies any file.

**Options:**

| Flag         | Default     | Description                                           |
| ------------ | ----------- | ----------------------------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data                    |
| `--network`  | `false`     | Also probe the configured remote via `git ls-remote`  |

**Check categories:**

| Category         | What is checked                                                                              |
| ---------------- | -------------------------------------------------------------------------------------------- |
| `prerequisites`  | `git` on `$PATH` (with version); `diff` on `$PATH`                                          |
| `base directory` | `~/.sysfig/` exists; permissions are `0700`                                                  |
| `git repo`       | `repo.git/` is a valid bare repo; HEAD resolves; no uncommitted staged changes; remote configured; `sysfig.yaml` committed in HEAD |
| `state`          | `state.json` readable; IDs in `state.json` cross-checked against `sysfig.yaml` in HEAD     |
| `file health`    | All tracked system files exist on disk; all tracked repo blobs present in HEAD               |
| `encryption`     | Master key present when encrypted files are tracked; key permissions are `0600`              |

**Exit codes:** `0` = all checks OK or warnings only, `1` = any hard failure.

**Example output:**

```
  sysfig doctor — environment health check
  ─────────────────────────────────────────────

  prerequisites
    ✓  git binary                      /usr/bin/git  (git version 2.53.0)
    ✓  diff binary                     /usr/bin/diff

  base directory
    ✓  exists                          /home/you/.sysfig
    ✓  permissions                     0700

  git repo
    ✓  repo exists                     /home/you/.sysfig/repo.git
    ✓  HEAD resolves                   a3f2b1c
    ⚠  uncommitted changes             staged changes exist that have not been committed
       → Run: sysfig sync
    ✓  remote configured               origin → git@github.com:you/configs.git
    ✓  sysfig.yaml in HEAD

  state
    ✓  state.json readable             5 tracked file(s)
    ✓  state/manifest sync             state.json and sysfig.yaml are in sync

  file health
    ✓  system files present            all 5 file(s) exist on disk
    ✓  repo blobs in HEAD              all 5 file(s) have blobs in HEAD

  encryption
    ✓  master key                      present — covers 2 encrypted file(s)

────────────────────────────────────────────────────────────────────────────
  ✓ 12 passed  ·  ⚠ 1 warnings
```

**Run it whenever something feels wrong** — after `setup`, before a release, or when a command gives an unexpected error. It tells you exactly what is broken and what to run to fix it.

---

### `snap`

Take instant local snapshots of tracked files — test config changes safely and undo them in one command.

```
sysfig snap take     [--label TEXT] [--id ID] [options]
sysfig snap list     [-a] [options]       # alias: snap ls
sysfig snap undo     [-a] [--dry-run] [options]
sysfig snap restore  <snap-id> [--id ID] [--dry-run] [options]
sysfig snap drop     <snap-id> [options]
```

Snapshots capture the **live on-disk content** of your tracked files without creating a git commit. Use them as a fast checkpoint before testing a risky config change:

```
before change:   sysfig snap take --label "before nginx tuning"
                 → Hash: a3f2b1c4   ← use this short hash anywhere
make the change: edit /etc/nginx/nginx.conf
test it:         nginx -t && systemctl reload nginx
if it breaks:    sysfig snap restore a3f2b1c4   ← 8 chars, no full ID needed
                 OR: sysfig snap undo            ← latest snap, no ID at all
```

**`snap list` and `snap undo` are context-aware** — they automatically scope to the current working directory. Working in `/etc/nginx`? Only nginx snaps are shown and undone. Use `-a` / `--all` to see or undo everything.

**Subcommands:**

| Subcommand | Description |
| ---------- | ----------- |
| `snap take` | Capture current on-disk file content into a named snapshot |
| `snap list` / `snap ls` | List snapshots scoped to CWD (`-a` for all) |
| `snap undo` | Restore the most recent snapshot scoped to CWD (`-a` for global) |
| `snap restore <id>` | Restore a specific snapshot by ID |
| `snap drop <id>` | Delete a snapshot permanently |

**`snap take` options:**

| Flag         | Default     | Description                                               |
| ------------ | ----------- | --------------------------------------------------------- |
| `--label`    | —           | Human-readable description (included in the snapshot ID)  |
| `--id`       | all tracked | Limit to these file IDs (repeatable)                      |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores its data                    |

**`snap list` / `snap ls` options:**

| Flag         | Default     | Description                                                    |
| ------------ | ----------- | -------------------------------------------------------------- |
| `--all`/`-a` | `false`     | Show all snapshots regardless of current directory             |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores its data                         |

**`snap undo` options:**

| Flag         | Default     | Description                                                    |
| ------------ | ----------- | -------------------------------------------------------------- |
| `--all`/`-a` | `false`     | Undo the latest snapshot globally (all tracked files)          |
| `--id`       | CWD files   | Further limit restore to specific IDs (repeatable)             |
| `--dry-run`  | `false`     | Show what would be restored without writing                    |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores its data                         |

**`snap restore` options:**

| Flag         | Default       | Description                                       |
| ------------ | ------------- | ------------------------------------------------- |
| `--id`       | all in snap   | Restore only this file ID (repeatable)            |
| `--dry-run`  | `false`       | Show what would be restored without writing       |
| `--base-dir` | `~/.sysfig`   | Directory where sysfig stores its data            |

**Snapshot ID format:** `YYYYMMDD-HHmmss` or `YYYYMMDD-HHmmss-your-label` when `--label` is set.

**Examples:**

```bash
# Save a checkpoint before tuning nginx
cd /etc/nginx
sysfig snap take --label "before nginx tuning"

# Make changes, test, break something...
# Roll back — only nginx files touched:
sysfig snap undo

# List checkpoints for this directory
sysfig snap ls

# List ALL checkpoints across all directories
sysfig snap ls -a

# Also working in /home/user/.config/app at the same time:
cd /home/user/.config/app
sysfig snap take --label "before app update"
# ... make changes, break something ...
sysfig snap undo    # restores only app files — nginx untouched

# Global rollback (all tracked files):
sysfig snap undo -a

# Restore a specific snapshot by ID (without changing directory):
sysfig snap restore 20260318-153042-before-nginx-tuning

# Restore only one file from a snapshot:
sysfig snap restore 20260318-153042-before-nginx-tuning --id nginx_main

# Delete a snapshot once you no longer need it:
sysfig snap drop 20260318-153042-before-nginx-tuning
```

**Example output — `snap ls` scoped to `/etc/nginx`:**

```
  Snapshots (2)  [scope: /etc/nginx]

  96da94e9  20260318-162215-app-before-update    2026-03-18 16:22:15  app before update    [2/4 files]
  6f23a446  20260318-162215-nginx-before-tuning  2026-03-18 16:22:15  nginx before tuning  [2/4 files]
```

The first column is the **short hash** — use it directly in `snap restore`, `snap drop`, and `snap undo`. The `[2/4 files]` counter shows how many files in each snapshot match the current scope.

**Example output — `snap undo` from `/etc/nginx`:**

```
  Undo → restoring snapshot: 20260318-162215-nginx-before-tuning  [scope: /etc/nginx]

  ✓ etc_nginx_limits_conf          → /etc/nginx/limits.conf
  ✓ etc_nginx_nginx_conf           → /etc/nginx/nginx.conf
  ―  home_user__config_app_settings_ini   skipped
  ―  home_user__config_app_locale_ini     skipped
────────────────────────────────────────────────────────────────────────────
  Restored: 2  ·  Skipped: 2
```

> Snapshots are stored locally in `~/.sysfig/snaps/` — never committed to git, never pushed. They are fast (plain file copies) and independent of any git commit. `snap restore`/`snap undo` do **not** run a `sysfig sync` — the system file is updated but the repo is not. Run `sysfig sync` after restoring if you want to capture the restored state as a commit.

---

### `node`

Register remote machines so that encrypted files can be decrypted on each machine using its own key — without sharing the primary master key.

```
sysfig node add    <name> <age-public-key>
sysfig node list
sysfig node remove <name>
```

Each node stores an [age](https://age-encryption.org/) public key. During `sysfig sync`, every encrypted file is re-encrypted to the local master key **plus** all registered node public keys (age multi-recipient). Any single key is sufficient for decryption.

**Typical two-machine workflow:**

```bash
# On the remote machine: get its public key
age-keygen -o ~/.sysfig/keys/master.age-identity
# (note the "Public key: age1..." line printed to stdout)

# On the primary machine: register the remote node
sysfig node add server age1qwerty...serverkey

# Sync — encrypted files now have two recipients
sysfig sync

# On the remote machine: apply normally — decrypts with its own key
sysfig apply
```

**Subcommands:**

| Subcommand | Description |
| ---------- | ----------- |
| `node add <name> <pubkey>` | Register a node's age public key |
| `node list` | List all registered nodes |
| `node remove <name>` | Remove a node (takes effect on next `sync`) |

> After removing a node, run `sysfig sync` to re-encrypt files to the remaining recipients only.

---

## Configuration File: sysfig.yaml

`sysfig.yaml` lives at the root of your config repo and is committed to git. It is the shared manifest that tells `sysfig setup` what to seed on a new machine.

`sysfig track` maintains this file automatically — you rarely need to edit it by hand.

```yaml
tracked_files:
  - id: nginx_main
    description: "Main nginx configuration"
    system_path: /etc/nginx/nginx.conf
    repo_path: etc/nginx/nginx.conf       # git-relative, no leading slash
    encryption:
      enabled: false
    template:
      enabled: false
    tags:
      - web
      - nginx

  - id: sshd_config
    description: "SSH daemon hardening"
    system_path: /etc/ssh/sshd_config
    repo_path: etc/ssh/sshd_config
    encryption:
      enabled: false
    tags:
      - security
      - ssh

  - id: app_secrets
    description: "Application environment secrets"
    system_path: /etc/myapp/secrets.env
    repo_path: etc/myapp/secrets.env
    encryption:
      enabled: true          # stored encrypted in the repo
    tags:
      - secrets
```

---

## state.json vs sysfig.yaml

This is the most important conceptual distinction in sysfig:

| | `sysfig.yaml` | `state.json` |
|---|---|---|
| **What it is** | Manifest — the list of tracked files | Local cache — hashes, metadata, sync times |
| **Where it lives** | Inside the git repo (committed) | `~/.sysfig/state.json` (never committed) |
| **Shared across machines?** | Yes — this is how new machines know what to track | No — every machine has its own |
| **Written by** | `sysfig track`, `sysfig init` | All commands that touch files |
| **Read by** | `sysfig setup` (to bootstrap state on new machines) | `sysfig status`, `sysfig diff`, `sysfig apply` |

When you run `sysfig setup` on a new machine, sysfig reads `sysfig.yaml` from the cloned repo and populates a fresh `state.json` with the file records. From that point on, `state.json` is maintained locally.

---

## Encryption

sysfig uses [age](https://age-encryption.org/) for encryption with HKDF-SHA256 per-file key derivation.

**Enable encryption:**

```bash
# At init time
sysfig init --encrypt

# Or generate a key on an existing setup
sysfig keys generate
```

**Track an encrypted file:**

```bash
sysfig track /etc/myapp/secrets.env --encrypt
sysfig sync
```

**How it works:**

1. A master age identity is generated at `~/.sysfig/keys/master.age-identity` (mode `0600`).
2. For each encrypted file, a unique per-file key is derived from the master using HKDF-SHA256.
3. The encrypted content is stored in the bare repo. The master key is **never** committed.
4. `sysfig apply` decrypts automatically when the master key is present.

**Multi-recipient (multi-node) encryption:**

If you have registered nodes via `sysfig node add`, encrypted files are re-encrypted during `sysfig sync` to **all** registered public keys plus the local master key. Each machine can decrypt using only its own key. See [`node`](#node) for details.

**Moving to a new machine:**

```bash
# Copy the master key securely
scp oldhost:~/.sysfig/keys/master.age-identity ~/.sysfig/keys/master.age-identity
chmod 0600 ~/.sysfig/keys/master.age-identity

# Then apply normally
sysfig apply
```

**No key available on a machine:**

```bash
# Setup will skip encrypted files silently
sysfig setup --skip-encrypted git@github.com:you/myconfigs.git
```

---

## File Ownership & Permissions

sysfig records `uid`, `gid`, and `mode` for every tracked file. During `sysfig apply`, permissions are restored exactly. Ownership is restored where possible (may require running with `sudo` for system files).

`sysfig status` shows drift inline:

```
sshd_config     DIRTY/MODIFIED     /etc/ssh/sshd_config
   ⚠ mode:   0640 → 0644
   ⚠ owner:  root:root → you:you
```

This catches common accidental permission changes (e.g., an editor that recreates files with `0644` instead of the expected `0600`).

---

## Hooks

Hooks run after `sysfig apply` writes a file to disk — validate the config, reload the service, or any other safe action. They live in `~/.sysfig/hooks.yaml`, which is **local-only and never committed to git**.

**Format:**

```yaml
# Binaries permitted in exec hooks — add any tool you need, no code changes required
allowlist: [nginx, sshd, apachectl, haproxy, postfix]

hooks:
  nginx_validate:
    on: [etc_nginx_nginx_conf]   # file ID from 'sysfig status'
    type: exec
    cmd: [nginx, -t]             # runs: nginx -t

  nginx_reload:
    on: [etc_nginx_nginx_conf]
    type: systemd_reload
    service: nginx

  sshd_validate:
    on: [etc_ssh_sshd_config]
    type: exec
    cmd: [sshd, -t]

  haproxy_reload:
    on: [etc_haproxy_haproxy_cfg]
    type: systemd_reload
    service: haproxy
```

**Supported types:**

| Type | Effect |
| --- | --- |
| `exec` | Runs `cmd: [binary, args...]` — binary must appear in `allowlist` |
| `systemd_reload` | `systemctl reload <service>` |
| `systemd_restart` | `systemctl restart <service>` |

**Hook failures propagate as errors:** if a hook fails, `sysfig apply` prints `✗ Applied (hook failed)`, includes `Hook failed: N` in the summary, and exits with code 1.

Adding a new service requires only editing `hooks.yaml` — no code changes.

---

## Directory Layout

```
~/.sysfig/
├── repo.git/                        ← bare git repository
│   ├── HEAD
│   ├── config                       ← git config (remote, branch tracking)
│   ├── objects/
│   ├── refs/
│   ├── sysfig.yaml                  ← manifest (committed, shared)
│   ├── hooks.yaml.example           ← hooks template (committed, shared)
│   ├── etc/
│   │   ├── nginx/nginx.conf         ← tracked files stored at system path
│   │   └── ssh/sshd_config
│   └── home/you/.bashrc
│
├── backups/                         ← pre-apply backups (local only)
│   ├── 2026-03-18T10-30-00/
│   │   └── etc/nginx/nginx.conf
│   └── 2026-03-17T08-12-33/
│       └── etc/ssh/sshd_config
│
├── keys/
│   └── master.age-identity          ← age private key (mode 0600, local only)
│
├── snaps/                           ← local snapshots (local only, never pushed)
│   └── 20260318-153042-before-nginx-tuning/
│       ├── snap.json                ← snapshot manifest (ID, label, timestamp, files)
│       └── files/
│           └── etc/nginx/nginx.conf ← live file content at snapshot time
│
├── state.json                       ← local cache (local only)
└── hooks.yaml                       ← local hooks (local only)
```

---

## Exit Codes

| Command    | `0`                       | `1`                            | `2`   |
| ---------- | ------------------------- | ------------------------------ | ----- |
| `status`   | All files SYNCED          | Any DIRTY / PENDING / MISSING  | Error |
| `diff`     | No differences            | Differences found              | Error |
| `apply`    | Success                   | —                              | Error |
| `sync`     | Success (or nothing to do)| —                              | Error |
| `push`     | Success                   | —                              | Error |
| `pull`     | Success / already up-to-date | —                           | Error |
| All others | Success                   | —                              | Error |

---

## Offline Safety Model

sysfig never touches the network automatically.

| Operation                        | Network required? |
| -------------------------------- | ----------------- |
| `track`, `apply`, `status`       | Never             |
| `sync` (local commit)            | Never             |
| `diff`, `log`                    | Never             |
| `snap take/list/restore/drop`    | Never             |
| `push`                           | Always            |
| `pull`                           | Always            |
| `setup` (initial bootstrap only) | Yes, one-time     |
| `deploy --host` (remote push)    | SSH to target     |

If `sysfig setup` detects the bare repo already exists locally, it shows hints and exits cleanly — no silent pull, no network call. The user must explicitly run `sysfig pull` to fetch changes. This prevents silent data loss on intermittent networks and keeps the local repo as the always-valid source of truth.

---

## Security Design

| Concern                 | Mitigation                                                             |
| ----------------------- | ---------------------------------------------------------------------- |
| Secrets in git          | Per-file age encryption; master key never committed                    |
| Symlink traversal       | No symlinks — physical file copies only                                |
| Privilege escalation    | `sudo`/`polkit` only where needed; no `setuid` binaries                |
| Accidental key loss     | Warning shown on every key-generating operation; key at known path     |
| Hook injection          | Typed action list only (`reload`, `restart`); no arbitrary shell       |
| Repository poisoning    | Bare repo is local; remote is never pulled automatically               |
| Sensitive file tracking | Built-in denylist; blocked paths cannot be tracked regardless of flags |

**Denylist — these paths are always blocked:**

- `/etc/shadow`, `/etc/gshadow`
- `/etc/ssh/ssh_host_*` — SSH host private keys
- `/root/.ssh/id_*` — root SSH private keys
- Any path ending in `.age-identity`

The denylist is not configurable by design.

---

## Troubleshooting

**`git: command not found`**

sysfig shells out to `git` for all repo operations. Install git and ensure it is on `$PATH`.

**`state.json` is out of sync with the repo**

If you manually edited the bare repo or restored from a backup, state.json may be stale. The safest fix:

```bash
# Re-seed state from the manifest
rm ~/.sysfig/state.json
sysfig setup  # re-reads sysfig.yaml and rebuilds state.json
```

**`apply` failed halfway — some files written, some not**

sysfig applies files one at a time. If it stopped mid-run:
1. Check `sysfig status` to see what is still PENDING/APPLY.
2. Re-run `sysfig apply` — it is idempotent and safe to retry.
3. Backups from the failed run are in `~/.sysfig/backups/`.

**Lost master key**

There is no recovery path. Encrypted files in the repo can no longer be decrypted. Going forward:

```bash
# Generate a new key
sysfig keys generate

# Re-track all encrypted files (re-encrypts with the new key)
sysfig track /etc/myapp/secrets.env --encrypt
sysfig sync
```

**`setup` says "This machine is already set up" but the repo is stale**

`setup` is intentionally a no-op when the local repo exists. Use `pull` to update:

```bash
sysfig pull
sysfig status
sysfig apply
```

**Permission denied when applying system files**

Files under `/etc/` require root. Run with sudo:

```bash
sudo sysfig apply --id sshd_config
```

Or use `sysfig apply --dry-run` first to see exactly what would be written.

---

## License

MIT — see [LICENSE](LICENSE).
