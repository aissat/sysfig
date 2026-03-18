# sysfig

> **Config management that thinks like a sysadmin, not a git wrapper.**

`sysfig` is a security-first configuration management tool for Linux. It version-controls your config files — both dotfiles and `/etc/` system configs — in a bare git repository, deploys them across machines with a single command, encrypts secrets with [age](https://age-encryption.org/), tracks file ownership and permissions, and stays fully offline-capable.

---

## What it looks like

**Setting up a new machine:**

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

**Daily status check:**

```
ID                                       STATUS               SYSTEM PATH
────────────────────────────────────────────────────────────────────────────
nginx_main                               SYNCED               /etc/nginx/nginx.conf
sshd_config                              DIRTY/MODIFIED       /etc/ssh/sshd_config
   ⚠ mode:   0644 → 0600
bashrc                                   PENDING/APPLY        /home/you/.bashrc
server_key                               ENCRYPTED            /etc/ssl/private/server.key
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
  - [First machine — init and track](#first-machine--init-and-track)
  - [New machine — setup and apply](#new-machine--setup-and-apply)
  - [Daily workflow](#daily-workflow)
- [Command Reference](#command-reference)
  - [setup](#setup)
  - [init](#init)
  - [track](#track)
  - [apply](#apply)
  - [status](#status)
  - [diff](#diff)
  - [sync](#sync)
  - [push](#push)
  - [pull](#pull)
  - [log](#log)
  - [keys](#keys)
  - [doctor](#doctor)
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

| Tool       | `/etc/` support | Encryption    | Offline-safe | Metadata tracking | Backup on apply | Health check | Single binary |
| ---------- | --------------- | ------------- | ------------ | ----------------- | --------------- | ------------ | ------------- |
| GNU Stow   | ✗               | ✗             | ✓            | ✗                 | ✗               | ✗            | ✓             |
| YADM       | ✗               | partial       | ✓            | ✗                 | ✗               | ✗            | ✓             |
| Chezmoi    | partial         | partial       | ✓            | ✗                 | ✗               | ✗            | ✓             |
| Ansible    | ✓               | via vault     | ✗            | ✓                 | ✗               | ✗            | ✗             |
| **sysfig** | **✓**           | **✓ (age)**   | **✓**        | **✓**             | **✓**           | **✓ doctor** | **✓**         |

**Key design decisions:**

- **No symlinks.** Files are physical copies. `ls -la` on your system never reveals your repo structure.
- **Bare git repo.** The shadow repo lives at `~/.sysfig/repo.git/` — no working tree, no accidental edits.
- **Offline-first.** `track`, `apply`, `status`, `sync`, `diff` work 100% without network. Only `push` and `pull` touch the wire.
- **Per-file encryption.** Secrets are encrypted with [age](https://age-encryption.org/) + HKDF-SHA256 per-file keys derived from a single master key.
- **Metadata tracking.** Records `uid`, `gid`, and `mode` for every file. `status` warns when permissions drift.
- **Atomic backups.** Every `apply` creates a timestamped backup before overwriting anything on disk.

---

## Prerequisites

| Dependency | Required for          | Notes                              |
| ---------- | --------------------- | ---------------------------------- |
| `git`      | Everything            | Must be on `$PATH`. v2.x or later. |
| `diff`     | `sysfig diff`         | Usually pre-installed on Linux.    |
| Go 1.21+   | Building from source  | Not needed if using a binary.      |

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

### First machine — init and track

```bash
# 1. Initialise sysfig (creates ~/.sysfig/ with a bare git repo)
sysfig init

# 2. Start tracking config files
sysfig track /etc/nginx/nginx.conf
sysfig track ~/.bashrc
sysfig track /etc/ssh/sshd_config

# 3. Commit everything to the local repo
sysfig sync --message "initial commit"

# 4. Add a remote and push
git --git-dir ~/.sysfig/repo.git remote add origin git@github.com:you/myconfigs.git
sysfig push
```

### New machine — setup and apply

```bash
# 1. Bootstrap from your remote config repo (one-time, needs network)
sysfig setup git@github.com:you/myconfigs.git

# 2. If you have encrypted files, copy your master key first:
#    scp oldhost:~/.sysfig/keys/master.age-identity ~/.sysfig/keys/
#    chmod 0600 ~/.sysfig/keys/master.age-identity

# 3. Deploy all tracked configs to this machine
sysfig apply

# 4. Verify
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
sysfig track --recursive <dir> [options]
```

Copies the file into the bare git repo's index (staged, not yet committed), records its BLAKE3 hash and `uid`/`gid`/`mode` metadata in `state.json`, and updates `sysfig.yaml`.

**Run `sysfig sync` after tracking to create a commit.**

**Options:**

| Flag          | Default     | Description                                              |
| ------------- | ----------- | -------------------------------------------------------- |
| `--id`        | derived     | Explicit tracking ID. Derived from path if omitted.      |
| `--tag`       | —           | Label to attach (repeatable: `--tag web --tag nginx`)    |
| `--encrypt`   | `false`     | Encrypt the file at rest in the repo                     |
| `--template`  | `false`     | Mark as a template with `{{variable}}` expansions        |
| `--recursive` | `false`     | Track all files under a directory recursively            |
| `--base-dir`  | `~/.sysfig` | Directory where sysfig stores its data                   |

**ID derivation:**

If `--id` is omitted, the ID is derived from the absolute path: strip the leading `/`, replace `/` and `.` with `_`.

```
/etc/nginx/nginx.conf  →  etc_nginx_nginx_conf
/home/you/.bashrc      →  home_you__bashrc
```

**Examples:**

```bash
# Single file
sysfig track /etc/nginx/nginx.conf

# With explicit ID and tags
sysfig track /etc/nginx/nginx.conf --id nginx_main --tag web --tag nginx

# Encrypt a secret
sysfig track /etc/myapp/secrets.env --encrypt

# Recursively track a directory
sysfig track --recursive /etc/nginx/

# After tracking, commit
sysfig sync
```

**Example output (single file):**

```
Tracking /etc/nginx/nginx.conf

  ✓ ID:   nginx_main
  ✓ Repo: etc/nginx/nginx.conf
  ✓ Hash: 3a7f2b...
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
| `--base-dir`  | `~/.sysfig` | Directory where sysfig stores its data                   |

**Examples:**

```bash
# Preview what will happen
sysfig apply --dry-run

# Apply everything
sysfig apply

# Apply only specific files
sysfig apply --id nginx_main --id sshd_config
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

| Flag         | Default     | Description                        |
| ------------ | ----------- | ---------------------------------- |
| `--id`       | all         | Check only this ID (repeatable)    |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data |

**Script-friendly:**

```bash
# Use in CI, cron, or monitoring
if ! sysfig status; then
  echo "Config drift detected on $(hostname)" | mail -s "sysfig alert" ops@example.com
fi
```

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

Stages any modified tracked files, creates a git commit in the local bare repo, and updates `state.json` hashes and timestamps. No network access required. Use `--push` to also push in one step.

**Options:**

| Flag         | Default                    | Description                                 |
| ------------ | -------------------------- | ------------------------------------------- |
| `--message`  | `sysfig: sync <timestamp>` | Commit message                              |
| `--push`     | `false`                    | Also push to remote after committing        |
| `--base-dir` | `~/.sysfig`                | Directory where sysfig stores data          |

**Examples:**

```bash
sysfig sync
sysfig sync --message "hardened sshd_config"
sysfig sync --push          # commit + push in one step
```

**Example output:**

```
  ✓ Committed: hardened sshd_config
  ✓ Repo:      /home/you/.sysfig/repo.git
  ℹ Not pushed. Run sysfig push when online.
```

---

### `push`

Push local commits to the remote git repository.

```
sysfig push [options]
```

Requires network access and a configured remote. Set up a remote once with:

```bash
git --git-dir ~/.sysfig/repo.git remote add origin git@github.com:you/myconfigs.git
```

**Options:**

| Flag         | Default     | Description                        |
| ------------ | ----------- | ---------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data |

---

### `pull`

Pull remote changes into the local bare git repo.

```
sysfig pull [options]
```

Fetches and fast-forwards the local repo. **Does not automatically apply changes to the system** — run `sysfig apply` after pulling to deploy.

**Options:**

| Flag         | Default     | Description                        |
| ------------ | ----------- | ---------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data |

**Standard update workflow:**

```bash
sysfig pull           # fetch remote changes
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

Hooks run typed actions after `apply` completes — for example, reloading nginx after its config changes. They are **machine-local and never committed to git**.

`hooks.yaml` is written from `hooks.yaml.example` in your config repo when you run `sysfig setup`. Edit it on each machine to match the local environment.

**Format:**

```yaml
hooks:
  - on: apply
    id: nginx_main
    action: reload
    service: nginx

  - on: apply
    id: sshd_config
    action: restart
    service: sshd
```

**Supported `action` values:**

| Action    | Effect                             |
| --------- | ---------------------------------- |
| `reload`  | `systemctl reload <service>`       |
| `restart` | `systemctl restart <service>`      |

No arbitrary shell commands are permitted. The hook system is intentionally limited to prevent `hooks.yaml` from becoming an attack surface.

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
| `push`                           | Always            |
| `pull`                           | Always            |
| `setup` (initial bootstrap only) | Yes, one-time     |

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
