# sysfig

> **Config management that thinks like a sysadmin, not a git wrapper.**

`sysfig` is a security-first configuration management tool for Linux systems. It tracks your config files — both dotfiles and `/etc/` system configs — version-controls them in a bare git repository, and deploys them across machines with a single command. It encrypts secrets, records file ownership and permissions, and keeps everything fully offline-capable.

---

## Table of Contents

- [Why sysfig?](#why-sysfig)
- [Architecture](#architecture)
- [Installation](#installation)
- [Quick Start](#quick-start)
  - [First machine (init + track)](#first-machine-init--track)
  - [New machine (setup + apply)](#new-machine-setup--apply)
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
- [Configuration File: sysfig.yaml](#configuration-file-sysfigyaml)
- [Encryption](#encryption)
- [File Ownership & Permissions](#file-ownership--permissions)
- [Hooks](#hooks)
- [Directory Layout](#directory-layout)
- [Exit Codes](#exit-codes)
- [Offline Safety Model](#offline-safety-model)
- [Security Design](#security-design)
- [Comparison](#comparison)

---

## Why sysfig?

| Tool          | `/etc/` support | Encryption | Privilege model       | Offline-safe | Package snapshots |
| ------------- | --------------- | ---------- | --------------------- | ------------ | ----------------- |
| GNU Stow      | ✗               | ✗          | none (symlinks)       | ✓            | ✗                 |
| YADM          | ✗               | partial    | user only             | ✓            | ✗                 |
| Chezmoi       | partial         | partial    | user only             | ✓            | ✗                 |
| Ansible       | ✓               | via vault  | full but heavyweight  | ✗            | ✗                 |
| **sysfig**    | **✓**           | **✓ age**  | **sudo/polkit aware** | **✓**        | **roadmap**       |

**Key design decisions:**

- **No symlinks.** Files are physical copies. `ls -la` on your system never reveals your repo structure.
- **Bare git repo.** The shadow repo lives at `~/.sysfig/repo.git/` — no working tree, no accidental edits.
- **Offline-first.** `track`, `apply`, `status`, `sync`, `diff` all work 100% without network. Only `push` and `pull` require connectivity.
- **Per-file encryption.** Secrets are encrypted with [age](https://age-encryption.org/) + HKDF-SHA256 per-file keys derived from a single master key.
- **Metadata tracking.** Records `uid`, `gid`, and `mode` for every file. `status` warns when permissions drift.

---

## Architecture

```
~/.sysfig/
├── repo.git/          ← bare git repository (the version-controlled store)
│   ├── HEAD
│   ├── objects/
│   └── refs/
├── backups/           ← pre-apply backups, timestamped
│   └── 2026-03-18T10-30-00/
│       └── etc/nginx/nginx.conf
├── keys/              ← master key (mode 0600, never committed)
│   └── master.age-identity
├── state.json         ← local cache: IDs, hashes, metadata, sync times
├── sysfig.yaml        ← manifest: declares all tracked files
└── hooks.yaml         ← local hooks (never committed to git)
```

The manifest `sysfig.yaml` is committed to the repo and shared across machines. The `state.json` is local cache only. The `keys/` directory is local only — never pushed.

---

## Installation

**From source (requires Go 1.21+):**

```bash
git clone https://github.com/sysfig-dev/sysfig
cd sysfig
go build -o sysfig ./cmd/sysfig
sudo mv sysfig /usr/local/bin/
```

**Verify:**

```bash
sysfig --help
```

---

## Quick Start

### First machine (init + track)

```bash
# 1. Initialise sysfig (creates ~/.sysfig/)
sysfig init

# 2. Start tracking config files
sysfig track /etc/nginx/nginx.conf
sysfig track ~/.bashrc
sysfig track /etc/ssh/sshd_config

# 3. Commit changes to the local repo
sysfig sync

# 4. Push to your remote (GitHub, Gitea, self-hosted...)
#    First, add a remote to your bare repo:
git --git-dir ~/.sysfig/repo.git remote add origin git@github.com:you/myconfigs.git
sysfig push
```

### New machine (setup + apply)

```bash
# 1. Bootstrap from your remote config repo
sysfig setup git@github.com:you/myconfigs.git

# 2. Deploy all tracked configs to the system
sysfig apply

# 3. Check everything is in sync
sysfig status
```

---

## Command Reference

### `setup`

Bootstrap sysfig on a new machine from an existing remote config repo.

```
sysfig setup [<remote-url>] [options]
```

This is the primary onboarding command. It:
1. Detects if this machine is already set up (no-op if so)
2. Clones your remote config repo as a bare git repository
3. Seeds `state.json` from the `sysfig.yaml` manifest
4. Creates `hooks.yaml` from template if present in the repo

**Options:**

| Flag               | Default        | Description                                           |
| ------------------ | -------------- | ----------------------------------------------------- |
| `--base-dir`       | `~/.sysfig`    | Directory where sysfig stores its data                |
| `--configs-only`   | `false`        | Skip package installation, deploy configs only        |
| `--skip-encrypted` | `false`        | Skip encrypted files when master key is absent        |
| `--yes`            | `false`        | Non-interactive: skip all prompts                     |

**Examples:**

```bash
# Interactive (prompts for URL if omitted)
sysfig setup

# Non-interactive
sysfig setup git@github.com:you/myconfigs.git

# Custom data directory
sysfig setup --base-dir /opt/sysfig git@github.com:you/myconfigs.git

# Skip encrypted files (no master key on this machine)
sysfig setup --skip-encrypted git@github.com:you/myconfigs.git
```

> `sysfig clone` is a hidden alias for `setup` (backward compatibility).

---

### `init`

Initialise a fresh sysfig environment on a machine with no existing remote.

```
sysfig init [options]
```

Creates `~/.sysfig/` with a bare git repo, empty state, manifest template, and optionally generates a master key. Idempotent — safe to run twice.

**Options:**

| Flag          | Default     | Description                                   |
| ------------- | ----------- | --------------------------------------------- |
| `--base-dir`  | `~/.sysfig` | Directory where sysfig stores its data        |
| `--encrypt`   | `false`     | Generate a master key for encryption-at-rest  |

**Example:**

```bash
sysfig init
sysfig init --encrypt    # also generates a master key
```

---

### `track`

Start tracking a config file (or an entire directory).

```
sysfig track <path> [options]
sysfig track --recursive <dir> [options]
```

Copies the file content into the bare git repo's index (staged but not yet committed), records its hash and metadata in `state.json`, and updates `sysfig.yaml`.

Run `sysfig sync` after tracking to create a commit.

**Options:**

| Flag            | Default | Description                                               |
| --------------- | ------- | --------------------------------------------------------- |
| `--id`          | derived | Explicit tracking ID. Derived from path if omitted.       |
| `--tag`         | —       | Label to attach (repeatable: `--tag server --tag nginx`)  |
| `--encrypt`     | `false` | Encrypt the file at rest in the repo                      |
| `--template`    | `false` | Mark as a template with `{{variable}}` expansions         |
| `--recursive`   | `false` | Track all files under a directory recursively             |
| `--base-dir`    | `~/.sysfig` | Directory where sysfig stores its data              |
| `--sys-root`    | —       | Strip this prefix from paths (sandbox/testing use)        |

**ID derivation:**

If `--id` is not given, the ID is derived from the absolute path by stripping the leading `/` and replacing `/` and `.` with `_`. For example:

- `/etc/nginx/nginx.conf` → `etc_nginx_nginx_conf`
- `~/.bashrc` → `home_you_.bashrc`

**Examples:**

```bash
# Track a single file
sysfig track /etc/nginx/nginx.conf

# Track with explicit ID and tags
sysfig track /etc/nginx/nginx.conf --id nginx_main --tag web --tag nginx

# Track an encrypted secret
sysfig track /etc/ssl/private/server.key --encrypt

# Recursively track everything under /etc/nginx/
sysfig track --recursive /etc/nginx/
```

---

### `apply`

Deploy tracked configs from the repo to the system.

```
sysfig apply [options]
```

Reads each file from the bare git repo (at `HEAD`), decrypts if necessary, creates a timestamped backup of the existing system file, then writes the repo version to disk. Preserves recorded ownership and permissions.

**Options:**

| Flag          | Default     | Description                                             |
| ------------- | ----------- | ------------------------------------------------------- |
| `--id`        | all         | Apply only this ID (repeatable)                         |
| `--dry-run`   | `false`     | Print what would happen without writing anything        |
| `--no-backup` | `false`     | Skip pre-apply backup (dangerous — use with care)       |
| `--base-dir`  | `~/.sysfig` | Directory where sysfig stores its data                  |
| `--sys-root`  | —           | Prepend this path to all system paths (sandbox testing) |

**Examples:**

```bash
# Apply all tracked files
sysfig apply

# Dry run first
sysfig apply --dry-run

# Apply only specific files
sysfig apply --id nginx_main --id sshd_config

# Apply without backing up
sysfig apply --no-backup
```

---

### `status`

Show the sync status of all tracked files.

```
sysfig status [options]
```

Compares the current system files against the repo versions using BLAKE3 content hashes. Also checks recorded metadata (uid/gid/mode) against current state.

**Status labels:**

| Label          | Meaning                                                          |
| -------------- | ---------------------------------------------------------------- |
| `SYNCED`       | System file matches repo — all good                              |
| `DIRTY`        | System file has been modified since last sync — run `sysfig sync` |
| `PENDING/APPLY`| Repo has a newer version — run `sysfig apply`                    |
| `MISSING`      | File exists in repo but is absent from the system               |
| `ENCRYPTED`    | Encrypted file — content comparison skipped (no master key)     |

**Exit codes:** `0` = all SYNCED, `1` = any DIRTY/PENDING/MISSING, `2` = error.

**Options:**

| Flag         | Default     | Description                          |
| ------------ | ----------- | ------------------------------------ |
| `--id`       | all         | Check only this ID (repeatable)      |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data   |
| `--sys-root` | —           | Prepend this path (sandbox testing)  |

**Example output:**

```
ID                                       STATUS               SYSTEM PATH
────────────────────────────────────────────────────────────────────────────
nginx_main                               SYNCED               /etc/nginx/nginx.conf
sshd_config                              DIRTY/MODIFIED       /etc/ssh/sshd_config
   ⚠ mode:   0644 → 0600
bashrc                                   PENDING/APPLY        /home/you/.bashrc
────────────────────────────────────────────────────────────────────────────
  3 files  ·  1 synced  ·  1 dirty  ·  1 pending
```

**Script-friendly:**

```bash
if ! sysfig status; then
  echo "Config drift detected"
fi
```

---

### `diff`

Show a unified diff between system files and repo versions.

```
sysfig diff [options]
```

Requires `diff` to be installed on the system.

**Exit codes:** `0` = no differences, `1` = differences found, `2` = error.

**Options:**

| Flag         | Default         | Description                              |
| ------------ | --------------- | ---------------------------------------- |
| `--id`       | all             | Diff only this ID (repeatable)           |
| `--color`    | auto (TTY)      | Colorize diff output                     |
| `--base-dir` | `~/.sysfig`     | Directory where sysfig stores data       |
| `--sys-root` | —               | Prepend this path (sandbox testing)      |

**Example:**

```bash
sysfig diff
sysfig diff --id nginx_main
sysfig diff --no-color | grep "^[+-]"
```

---

### `sync`

Capture local changes and commit to the bare git repo (offline-safe).

```
sysfig sync [options]
```

Stages modified files, creates a commit in the local bare repo, and updates `state.json`. No network access required. Optionally pushes after committing with `--push`.

**Options:**

| Flag          | Default                         | Description                                   |
| ------------- | ------------------------------- | --------------------------------------------- |
| `--message`   | `sysfig: sync <timestamp>`      | Commit message                                |
| `--push`      | `false`                         | Also push to remote after committing          |
| `--base-dir`  | `~/.sysfig`                     | Directory where sysfig stores data            |
| `--sys-root`  | —                               | Prefix all system paths (sandbox testing)     |

**Examples:**

```bash
sysfig sync
sysfig sync --message "hardened sshd_config"
sysfig sync --push    # commit + push in one step
```

---

### `push`

Push local commits to the remote git repository.

```
sysfig push [options]
```

Requires network access and a configured remote. Add a remote with:

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

Fetches and fast-forwards the local repo. After pulling, run `sysfig apply` to deploy updated configs to the system.

**Options:**

| Flag         | Default     | Description                        |
| ------------ | ----------- | ---------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data |

**Example workflow:**

```bash
sysfig pull
sysfig status         # see what changed
sysfig apply          # deploy updates
```

---

### `log`

Show the commit history of your config repo as a graph tree.

```
sysfig log [options]
```

Shells out to `git log --graph` on the bare repo, showing commit hashes, dates, and messages with color.

**Options:**

| Flag         | Default     | Description                                                        |
| ------------ | ----------- | ------------------------------------------------------------------ |
| `-n`         | unlimited   | Limit to last N commits                                            |
| `--file`     | —           | Show only commits that touched a specific repo-relative path       |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data                                 |

**Examples:**

```bash
sysfig log
sysfig log -n 20
sysfig log --file etc/nginx/nginx.conf
```

**Example output:**

```
* a3f2b1c 2026-03-18 hardened sshd_config (HEAD -> master)
* 8d4e92a 2026-03-17 sysfig: sync 2026-03-17T14:22:01
* 1c7f03b 2026-03-15 initial commit
```

---

### `keys`

Manage the master encryption key.

```
sysfig keys <subcommand> [options]
```

**Subcommands:**

| Subcommand | Description                                         |
| ---------- | --------------------------------------------------- |
| `info`     | Show the master key path and its age public key     |
| `generate` | Generate a new master key (fails if one exists)     |

**Options (both subcommands):**

| Flag         | Default     | Description                        |
| ------------ | ----------- | ---------------------------------- |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data |

**Examples:**

```bash
sysfig keys info
sysfig keys generate
```

> **Important:** Back up your master key immediately after generating it. Loss of the key means loss of all encrypted files. The key lives at `~/.sysfig/keys/master.age-identity` and is **never** committed to git.

---

## Configuration File: sysfig.yaml

`sysfig.yaml` lives in the root of your config repo (committed to git). It declares all tracked files and is the manifest that lets `sysfig setup` populate state on new machines.

**Schema:**

```yaml
tracked_files:
  - id: nginx_main
    description: "Main nginx configuration"
    system_path: /etc/nginx/nginx.conf
    repo_path: etc/nginx/nginx.conf     # git-relative path, no leading slash
    encryption:
      enabled: false
    template:
      enabled: false
    tags:
      - web
      - nginx

  - id: sshd_config
    description: "SSH daemon configuration"
    system_path: /etc/ssh/sshd_config
    repo_path: etc/ssh/sshd_config
    encryption:
      enabled: false
    tags:
      - security
      - ssh

  - id: env_secrets
    description: "Application environment secrets"
    system_path: /etc/myapp/secrets.env
    repo_path: etc/myapp/secrets.env
    encryption:
      enabled: true
    tags:
      - secrets
```

`sysfig track` automatically updates this file when you add new files.

---

## Encryption

sysfig uses [age](https://age-encryption.org/) for file encryption with HKDF-SHA256 per-file key derivation.

**Setup:**

```bash
# Option 1: init with encryption enabled
sysfig init --encrypt

# Option 2: generate a key separately
sysfig keys generate
```

**Track an encrypted file:**

```bash
sysfig track /etc/myapp/secrets.env --encrypt
```

**How it works:**

1. A master age identity is generated and stored at `~/.sysfig/keys/master.age-identity` (mode `0600`, never committed).
2. When tracking an encrypted file, a per-file key is derived from the master key using HKDF-SHA256.
3. The file content is encrypted and stored in the bare repo. Only machines with the master key can decrypt it.
4. `sysfig apply` decrypts automatically if the master key is present. Without it, use `--skip-encrypted` in `setup`.

**Transferring the key to a new machine:**

```bash
# Securely copy your master key
scp ~/.sysfig/keys/master.age-identity newhost:~/.sysfig/keys/
chmod 0600 ~/.sysfig/keys/master.age-identity
```

---

## File Ownership & Permissions

sysfig records the `uid`, `gid`, and `mode` of each tracked file at the time of tracking. During `sysfig apply`:

- Permissions (`mode`) are restored exactly.
- Ownership (`uid`/`gid`) is restored where possible (may require `sudo`).

`sysfig status` detects ownership and permission drift and shows it inline:

```
sshd_config     DIRTY/MODIFIED     /etc/ssh/sshd_config
   ⚠ mode:   0644 → 0600
   ⚠ owner:  root:root → you:you
```

---

## Hooks

Hooks run predefined actions after `apply` completes (e.g., reloading a service). They are **never** committed to git — hooks are machine-local for security.

`hooks.yaml` is created from `hooks.yaml.example` in your config repo when you run `sysfig setup`.

**Example `hooks.yaml`:**

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

Only predefined action types are permitted (no arbitrary shell commands).

---

## Directory Layout

```
~/.sysfig/
├── repo.git/                   ← bare git repository
│   ├── HEAD
│   ├── config                  ← git config (remote, branch tracking)
│   ├── objects/
│   └── refs/
│
├── backups/                    ← pre-apply backups
│   └── 2026-03-18T10-30-00/
│       └── etc/
│           └── nginx/
│               └── nginx.conf  ← original before apply
│
├── keys/
│   └── master.age-identity     ← age private key (mode 0600, local only)
│
├── state.json                  ← local cache (IDs, hashes, metadata)
├── sysfig.yaml                 ← manifest (committed to git, shared)
└── hooks.yaml                  ← local hooks (never committed)
```

**Inside `repo.git/` (the bare repo), tracked files are stored at their system paths without leading slash:**

```
repo.git/
└── (git object store)
    ├── etc/nginx/nginx.conf
    ├── etc/ssh/sshd_config
    └── home/you/.bashrc
```

---

## Exit Codes

| Command    | 0                      | 1                              | 2         |
| ---------- | ---------------------- | ------------------------------ | --------- |
| `status`   | All files SYNCED       | Any DIRTY / PENDING / MISSING  | Error     |
| `diff`     | No differences         | Differences found              | Error     |
| `apply`    | Success                | —                              | Error     |
| `sync`     | Success (or no-op)     | —                              | Error     |
| `push`     | Success                | —                              | Error     |
| `pull`     | Success (or up-to-date)| —                              | Error     |
| All others | Success                | —                              | Error     |

---

## Offline Safety Model

sysfig never touches the network automatically. The design principle:

| Operation                         | Network required? |
| --------------------------------- | ----------------- |
| `track`, `apply`, `status`        | Never             |
| `sync` (local commit)             | Never             |
| `diff`, `log`                     | Never             |
| `push`                            | Always            |
| `pull`                            | Always            |
| `setup` (initial bootstrap only)  | Yes (one-time)    |

If `sysfig setup` detects the bare repo already exists locally, it is a no-op. The user must explicitly run `sysfig pull` to fetch remote changes. This prevents silent data loss on intermittent networks.

---

## Security Design

| Concern                   | Mitigation                                                         |
| ------------------------- | ------------------------------------------------------------------ |
| Secrets in git            | Per-file age encryption; master key never committed                |
| Symlink traversal         | No symlinks used anywhere — physical file copies only              |
| Privilege escalation      | `sudo`/`polkit` only where needed; no setuid binaries              |
| Accidental key loss       | Warning on every key-generating operation; key stored at known path |
| Hook injection            | Typed action list only; no arbitrary shell execution in hooks      |
| Repository poisoning      | Bare repo is local; remote is never pulled automatically           |
| Sensitive file tracking   | Built-in denylist prevents tracking `/etc/shadow`, SSH host keys, etc. |

**Denylist (always blocked):**

- `/etc/shadow`, `/etc/gshadow`
- `/etc/ssh/ssh_host_*` (host private keys)
- `/root/.ssh/id_*` (root private keys)
- Any path ending in `.age-identity`

---

## Comparison

```
┌──────────────────────────────────────────────────────────────────┐
│                     Config Management Tools                      │
├────────────────┬──────────┬──────────┬──────────┬───────────────┤
│                │ GNU Stow │  Chezmoi │ Ansible  │   sysfig      │
├────────────────┼──────────┼──────────┼──────────┼───────────────┤
│ /etc/ support  │    ✗     │ partial  │    ✓     │      ✓        │
│ Encryption     │    ✗     │ partial  │  vault   │   age ✓       │
│ No symlinks    │    ✗     │    ✓     │    ✓     │      ✓        │
│ Offline-first  │    ✓     │    ✓     │    ✗     │      ✓        │
│ Metadata track │    ✗     │    ✗     │    ✓     │      ✓        │
│ Drift detect   │    ✗     │ manual   │ partial  │  continuous ✓ │
│ Backup on apply│    ✗     │    ✗     │    ✗     │      ✓        │
│ Single binary  │    ✓     │    ✓     │    ✗     │      ✓        │
└────────────────┴──────────┴──────────┴──────────┴───────────────┘
```

---

## License

MIT — see [LICENSE](LICENSE).
