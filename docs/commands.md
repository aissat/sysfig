## Command Reference

### `deploy`

Pull latest configs from remote and apply them (ongoing use).

```
sysfig deploy [<remote-url>] [options]
```

Use `deploy` for routine updates on machines already set up with `sysfig bootstrap`. Idempotent: safe to re-run as many times as needed.

The `<remote-url>` can be any URL supported by `sysfig remote set`: a git remote (`git@github.com:…`) **or a bundle remote** (`bundle+local://…`, `bundle+ssh://…`). sysfig detects the transport from the URL scheme automatically.

**Behaviour:**

| Situation | What happens |
| --------- | ------------ |
| First-time, git remote | `git clone --bare` → seed `state.json` → apply all files |
| First-time, bundle remote | `git init --bare` + bundle pull → seed `state.json` → apply all files |
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
| `--sudo`       | off     | Wrap remote writes with `sudo` — required for `/etc/` and other root-owned paths |

> When `--host` is set sysfig reads files from the **local** repo and writes them to the remote using Go's native SSH client (no `ssh` binary required on the local machine). **No sysfig installation is needed on the remote** — only `mkdir`, `cat`, and `chmod`.
>
> Files tracked with `--local` or `--hash-only` are silently skipped — they have no content in the repo.
>
> **Troubleshooting — 90-second hang per file:** If deploy is slow, the remote sshd is likely running `pam_systemd.so` without `systemd-logind` active (common in containers and minimal VMs). Fix: `sudo sed -i 's/^session.*optional.*pam_systemd.so/#&/' /etc/pam.d/common-session` on the target.

**Local deploy examples:**

```bash
# First-time machine (git remote)
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

**Bundle remote deploy examples (air-gapped / no git server):**

```bash
# Pull from an NFS share and apply in one step
sysfig deploy bundle+local:///mnt/corp-nfs/sysfig/ops-machine.bundle

# Pull from a remote SSH file server and apply
sysfig deploy bundle+ssh://backup@fileserver/srv/sysfig/ops-machine.bundle

# Already configured — just pull + apply (remote URL already set)
sysfig deploy

# Preview without writing to disk
sysfig deploy bundle+local:///mnt/share/ops.bundle --dry-run
```

**Remote deploy examples:**

```bash
# Deploy all tracked files to a remote server
sysfig deploy --host user@192.168.1.10

# Deploy /etc/ files (root-owned on remote) — requires sudo on target
sysfig deploy --host user@server --sudo

# Preview what would be pushed (no SSH writes)
sysfig deploy --host user@server --dry-run

# Deploy only specific files
sysfig deploy --host user@server --id nginx_main --id sshd_config

# Use a specific SSH key
sysfig deploy --host deploy@server --ssh-key ~/.ssh/deploy_ed25519

# Non-standard SSH port
sysfig deploy --host user@server --ssh-port 2222 --sudo
```

**Use in CI / server provisioning:**

```bash
#!/bin/bash
# Ensure the machine matches the config repo — run on boot or in cron
sysfig deploy git@github.com:ops/server-configs.git --yes --skip-encrypted

# Air-gapped: first-time bootstrap from an NFS share
sysfig deploy bundle+local:///mnt/corp-nfs/sysfig/ops.bundle --yes

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

### `bootstrap`

First-time setup: clone a remote config repo and **immediately apply all configs** on this machine.

```
sysfig bootstrap [<remote-url>] [options]
```

This is the primary onboarding command. One command, machine is ready:
1. Clones your remote config repo as a bare git repository to `~/.sysfig/repo.git/`
2. Seeds `state.json` from the `sysfig.yaml` manifest
3. Applies all tracked configs to disk
4. Shows a `sudo sysfig apply` hint for any files that failed due to permissions

If the machine is already set up (repo exists + state populated), bootstrap exits cleanly with hints.

**Options:**

| Flag               | Default     | Description                                              |
| ------------------ | ----------- | -------------------------------------------------------- |
| `--base-dir`       | `~/.sysfig` | Directory where sysfig stores its data                   |
| `--no-apply`       | `false`     | Skip applying configs after clone (apply manually later) |
| `--configs-only`   | `false`     | Skip package installation, deploy configs only           |
| `--skip-encrypted` | `false`     | Skip encrypted files when master key is absent           |
| `--yes`            | `false`     | Non-interactive: skip all prompts                        |

**Examples:**

```bash
# Clone + apply immediately (recommended)
sysfig bootstrap git@github.com:you/myconfigs.git

# Clone only — review before applying
sysfig bootstrap git@github.com:you/myconfigs.git --no-apply
sysfig status
sysfig apply

# Air-gapped machine (bundle on USB or NFS)
sysfig bootstrap bundle+local:///mnt/usb/conf.bundle
```

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

Copies the file into the bare git repo's index, records its BLAKE3 hash and `uid`/`gid`/`mode` metadata in `state.json`, and updates `sysfig.yaml`. **A commit is created automatically** on the new file's `track/<path>` branch — no separate `sysfig sync` needed.

**`~/.sysfig` is created automatically** on the first `track` — no explicit `sysfig init` needed.

**`sudo sysfig track /etc/...`** — runs as root to read privileged files, but the repo and state live in the **invoking user's** `~/.sysfig` (resolved via `SUDO_USER`). No second repo, no flags needed. After every sudo write, sysfig re-chowns `~/.sysfig` back to the invoking user, so subsequent non-sudo commands (`sysfig node add`, `sysfig status`, etc.) work without permission errors.

**Options:**

| Flag           | Default     | Description                                              |
| -------------- | ----------- | -------------------------------------------------------- |
| `--id`         | derived     | Explicit tracking ID. Derived from path if omitted.      |
| `--tag`        | —           | Label to attach (repeatable: `--tag web --tag nginx`)    |
| `--encrypt`    | `false`     | Encrypt the file at rest in the repo                     |
| `--template`   | `false`     | Mark as a template with `{{variable}}` expansions        |
| `--local`      | `false`     | Track locally only — content stored in a `local/` branch that is never pushed to remote. Full git history, diffs, and undo work locally. |
| `--hash-only`  | `false`     | Record the hash only — no content stored anywhere. Reports `TAMPERED` on drift. Ideal for files too sensitive to store even locally. |
| `--exclude`    | —           | Path or glob to skip when tracking a directory (repeatable) |
| `--base-dir`   | `~/.sysfig` | Directory where sysfig stores its data                   |

> `--local` and `--hash-only` are mutually exclusive. See [docs/integrity.md](docs/integrity.md) for a full guide.

**ID derivation:**

If `--id` is omitted, the ID is derived from the absolute path: strip the leading `/`, replace `/` and `.` with `_`. Leading dots in a path component do not produce a double underscore.

```
/etc/nginx/nginx.conf  →  etc_nginx_nginx_conf
/home/you/.bashrc      →  home_you_bashrc
```

**Examples:**

```bash
# Single file — auto-committed immediately to its track branch
sysfig track /etc/nginx/nginx.conf

# With explicit ID and tags
sysfig track /etc/nginx/nginx.conf --id nginx_main --tag web --tag nginx

# Encrypt a secret
sysfig track /etc/myapp/secrets.env --encrypt

# Track a sensitive file locally — full history, never pushed to remote
sysfig track --local /etc/wireguard/wg0.conf

# Track a file for integrity monitoring only — no content stored
sysfig track --hash-only /etc/ssh/sshd_config

# Track an entire directory — auto-detected, no --recursive flag needed
# New files added to the directory later are detected by `sysfig status` (shown as NEW)
# and committed automatically by the next `sysfig sync`
sysfig track /etc/nginx/

# Track /etc but skip secrets
sysfig track /etc --exclude /etc/ssl/private --exclude /etc/shadow.d

# Glob pattern — skip all .bak files
sysfig track /etc/nginx --exclude "*.bak"

# Track a template file — placeholders substituted at apply time
sysfig track ~/.gitconfig --template
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

### `audit`

Check integrity of local-only and hash-only tracked files.

```
sysfig audit [options]
```

Checks all files tracked with `--local` or `--hash-only` and reports any that have drifted. Designed for use in scripts, cron jobs, and systemd timers.

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | all checked files are clean |
| `1` | one or more files are `TAMPERED` or `DIRTY` |
| `2` | error (could not read state or hash a file) |

**Options:**

| Flag           | Default | Description |
| -------------- | ------- | ----------- |
| `--hash-only`  | `false` | Audit only hash-only tracked files |
| `--local`      | `false` | Audit only local-only tracked files |
| `--all`        | `false` | Audit all tracked files (not just local/hash-only) |
| `--quiet`      | `false` | Suppress per-file output; exit code still reflects drift |
| `--base-dir`   | `~/.sysfig` | sysfig data directory |

**Examples:**

```bash
# Check all local/hash-only files (default):
sysfig audit

# Check only files tracked with --hash-only:
sysfig audit --hash-only

# Quiet mode for scripts/timers — exit code only:
sysfig audit --quiet && echo "clean" || echo "drift detected"

# systemd timer (see contrib/systemd/):
sysfig audit --quiet  # exits 1 if any file drifted → marks unit as failed
```

**Example output:**
```
  DIRTY    /etc/wireguard/wg0.conf
  TAMPERED /etc/ssh/sshd_config

  Audit: 2/2 file(s) drifted
```

See [docs/integrity.md](docs/integrity.md) for full details including systemd timer setup.

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

**Excluding NEW files in tracked directories:**
If a path is not yet tracked but sits inside a directory that was tracked as a group (e.g. `sysfig track /etc/pacman.d/`), `untrack` adds it to the excludes list so it no longer appears as `NEW` in `sysfig status`:

```bash
# /etc/pacman.d/ is a tracked group — /etc/pacman.d/gnupg/ shows as NEW
sysfig untrack /etc/pacman.d/gnupg   # adds to excludes, hides from status
```

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

# Exclude a NEW subdirectory from a tracked group dir
# (adds to excludes so it no longer appears as NEW in status)
sysfig untrack /etc/pacman.d/gnupg

# After untracking, commit the manifest change
sysfig sync --all -m "stop tracking nginx"
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
1. Reads the content from the file's dedicated `track/<path>` branch in the bare repo (falls back to `SanitizeBranchName` resolution for records that predate the branch-per-track migration)
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

**Default output is grouped by directory.** Directories where every file is SYNCED are collapsed to a single summary line. Directories with any dirty or pending files expand to show each affected file — with its tracking ID — aligned under the same `HASH` column as parent rows. If only one file is tracked in a directory, its full path is shown rather than a folder line.

```
PATH                                      HASH        STATUS
────────────────────────────────────────────────────────────────────────────
/home/you/                                018e4a02    1 synced  ·  1 dirty
  └ .zshrc                               7734be1e    DIRTY
/etc/pacman.d/                            63d01e28    10 synced
/etc/nginx/
  └ nginx.conf                           a3f2b1c0    DIRTY
  └ nginx-new.conf                                   NEW  → sysfig track /etc/nginx
/home/you/.bashrc                         ef891234    SYNCED
────────────────────────────────────────────────────────────────────────────
  5 files  ·  3 synced  ·  2 dirty  ·  1 new
```

The tracking ID shown in the `HASH` column of expanded rows (e.g. `7734be1e`) can be used directly in `sysfig sync`, `sysfig undo`, and other commands — no need to run `--files` first.

Use `--files` (or `-f`) to bypass grouping and see every tracked file individually on its own line.

**Status labels:**

| Label           | Meaning                                                             | Action                       |
| --------------- | ------------------------------------------------------------------- | ---------------------------- |
| `SYNCED`        | System file matches repo                                            | Nothing to do                |
| `DIRTY`         | System file has been modified since last sync                       | Run `sysfig sync`            |
| `PENDING/APPLY` | Repo has a newer version than the system file                       | Run `sysfig apply`           |
| `MISSING`       | File is tracked but does not exist on the system                    | Run `sysfig apply`           |
| `ENCRYPTED`     | Encrypted file — content comparison skipped (no master key present) | Copy key, then re-check      |
| `SOURCE`        | Source-managed file; on-disk content matches committed render       | Nothing to do                |
| `NEW`           | File exists on disk inside a tracked group dir but is not yet tracked | Run `sysfig sync` to auto-track, or `sysfig untrack <path>` to exclude |

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

Show changes between system files and repo versions.

```
sysfig diff [options]
```

Colorized output with word-level inline highlighting — changed tokens are shown with dark red/green background so you can see exactly what changed within a line.

**Exit codes:** `0` = no differences, `1` = differences found, `2` = error.

**Options:**

| Flag              | Default    | Description                                        |
| ----------------- | ---------- | -------------------------------------------------- |
| `--id`            | all        | Diff only this ID (repeatable)                     |
| `--side-by-side`/`-y` | false  | Side-by-side view (default: unified)               |
| `--color`         | auto (TTY) | Force or disable colorized output                  |
| `--base-dir`      | `~/.sysfig`| Directory where sysfig stores data                 |

**Examples:**

```bash
sysfig diff                           # unified diff, all dirty files
sysfig diff -y                        # side-by-side view
sysfig diff --id nginx_main           # diff one file
sysfig diff --no-color | grep "^[+-]" # scriptable

# In a script: exit 1 if anything differs
sysfig diff --id sshd_config || echo "sshd_config has drifted"
```

---

### `sync`

Capture the current state of tracked files and commit locally (offline-safe).

```
sysfig sync [target] [options]
```

Stages any modified tracked files, creates a git commit in the local bare repo, and updates `state.json` hashes and timestamps. No network access required. Use `--push` to also push in one step; use `--pull` to fetch remote changes before committing.

**A commit message is required.** Pass `-m "..."` for a custom message or `--auto` to let sysfig generate one (`sysfig: update <path>`). Running `sysfig sync` with neither flag exits with an error and a helpful hint.

**CWD-aware scoping:**

When you run `sysfig sync` without a target, it automatically scopes to files under your current working directory. To sync everything regardless of CWD, use `--all`.

```bash
cd /etc/nginx
sysfig sync --auto          # only syncs files under /etc/nginx/
sysfig sync --all --auto    # syncs ALL tracked files
```

**Target argument:**

The optional `[target]` narrows which files are staged and committed. It can be:
- A directory path — only files under that path are synced
- A system file path — only that specific file
- A tracking ID (e.g. `7734be1e`) — only that file

> **HASH vs tracking ID:** The `HASH` column in `sysfig status` is a BLAKE3 **content hash** of the committed file — it tells you *what version* is stored. The shorter hex strings shown under dirty/pending files in the grouped view (also in the `HASH` column) are the **tracking IDs** — use those with `sync`, `undo`, and other commands. CWD scoping applies when no target is given: files outside your current directory are skipped. Use an explicit path, ID, or `--all` to reach them.

```bash
sysfig sync /etc/nginx --auto              # sync only nginx files
sysfig sync /home/you/.zshrc -m "update"  # sync one file by path
sysfig sync 7734be1e -m "update zshrc"    # sync one file by tracking ID
sysfig sync --all --auto                  # sync everything regardless of CWD
```

**NEW files in tracked directories:** `sysfig sync` also auto-tracks any files discovered inside a tracked group directory that haven't been tracked yet (shown as `NEW` in `sysfig status`). They are committed in the same run.

**Commit strategy:**

- Each changed file gets its **own commit** with a meaningful message (`sysfig: update etc/nginx/nginx.conf`)
- Files tracked as a **directory** (`sysfig track /etc/pacman.d/`) are grouped — all changed files in that directory land in one commit
- Files tracked **individually** always get separate commits regardless of folder
- Unchanged files are skipped entirely (no empty commits)

**Options:**

| Flag              | Default     | Description                                                    |
| ----------------- | ----------- | -------------------------------------------------------------- |
| `-m`, `--message` | _(required)_ | Custom commit message (mutually exclusive with `--auto`)      |
| `--auto`          | `false`     | Auto-generate message: `sysfig: update <path>`                 |
| `--all`           | `false`     | Bypass CWD scoping — sync all tracked files                    |
| `--push`          | `false`     | Also push all track/* and manifest branches to remote after committing (runs even when nothing to commit) |
| `--pull`          | `false`     | Fetch remote changes before committing (non-fatal)             |
| `--base-dir`      | `~/.sysfig` | Directory where sysfig stores data                             |

**Examples:**

```bash
sysfig sync -m "tuned worker_processes"    # custom message, CWD-scoped
sysfig sync --auto                         # auto-generated message, CWD-scoped
sysfig sync --all --auto                   # sync everything regardless of CWD
sysfig sync --auto --push                  # commit + push in one step
sysfig sync --auto --pull --push           # full round-trip: pull → commit → push
sysfig sync /etc/nginx --auto              # explicit path target
```

**Example output:**

```
  ✓ Committed: etc/nginx/nginx.conf
  ✓ Committed: home/you/.zshrc
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

# First push to a non-empty remote (e.g. GitHub repo with a README) — use --force to overwrite
sysfig sync --push --force

# Push even when nothing changed locally (sync local branches to remote)
sysfig sync --push --auto
```

#### Bundle remotes (air-gapped / no git server)

When no git server is available, sysfig can use a single bundle file as the transport. The URL scheme selects the transport automatically — `sysfig sync --push` and `sync --pull` work exactly the same way.

| URL scheme | Transport |
|------------|-----------|
| `bundle+local:///path/to/file.bundle` | Local filesystem, NFS mount, USB key, SMB share |
| `bundle+ssh://user@host/path/file.bundle` | SSH file server via `scp` |

```bash
# NFS / local disk
sysfig remote set bundle+local:///mnt/corp-nfs/sysfig/workstation.bundle
sysfig sync --push --auto     # exports entire repo as a bundle file

# SSH file server
sysfig remote set bundle+ssh://backup@fileserver/srv/sysfig/web1.bundle
sysfig sync --push --auto

# Another machine pulls the bundle
sysfig remote set bundle+local:///mnt/corp-nfs/sysfig/workstation.bundle
sysfig sync --pull --auto     # imports all track/* branches from the bundle
sysfig apply                  # deploy pulled configs to disk
```

**How it works:** `sync --push` runs `git bundle create --all`, writes the file atomically (`.tmp` → rename), and copies it to the destination. `sync --pull` downloads the bundle, runs `git bundle verify` to reject corrupt files, then imports each branch. The branch-per-track layout and all other sysfig commands are completely unchanged.

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

**Editor compatibility:** sysfig watches both the file inode and its parent directory. This means atomic saves from `sed -i`, vim, nano, and any editor that writes via a temp file + rename are caught correctly — the new inode is re-registered automatically.

**Flags:**

| Flag         | Default     | Description                                         |
| ------------ | ----------- | --------------------------------------------------- |
| `--debounce` | `2s`        | Wait this long after the last change before syncing |
| `--dry-run`  | `false`     | Print detected changes without syncing              |
| `--push`     | `false`     | Push to remote after each successful sync           |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores its data              |

```bash
sysfig watch                    # foreground, 2s debounce
sysfig watch --debounce 500ms   # faster
sysfig watch --dry-run          # preview only
sysfig watch --push             # auto-push to remote after every commit
```

**Output:**

```
Watching tracked files for changes  (Ctrl-C to stop)
  base-dir: /home/you/.sysfig
  debounce: 2s
────────────────────────────────────────────────────────────────────────────
  10:42:15  changed  /etc/nginx/nginx.conf
            committed etc/nginx/nginx.conf
            sysfig: update etc/nginx/nginx.conf
```

Each event line shows:
- `changed` — the file on disk that triggered the sync
- `committed` — each repo path actually committed (one per file; grouped for dir-tracked files)
- The commit message (`sysfig: update <path>`)

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
| `--push`     | `false`     | Persist `--push` flag in the service ExecStart line   |

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

Show the commit history of your config repo. Each commit is expanded to show which file(s) changed, one line per commit with a meaningful path label.

```
sysfig log [system-path] [options]
```

**Options:**

| Flag         | Default     | Description                                                  |
| ------------ | ----------- | ------------------------------------------------------------ |
| `-n`         | unlimited   | Limit to last N commits                                      |
| `--id`       | —           | Show only commits touching the file with this tracking ID    |
| `--path`     | —           | Filter by repo-relative path                                 |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data                           |

**Examples:**

```bash
sysfig log                          # full history
sysfig log -n 10                    # last 10 commits
sysfig log /etc/nginx               # all commits touching /etc/nginx/*
sysfig log /home/you/.zshrc         # history for one file
sysfig log --id 7734be1e            # history by tracking ID
```

**Example output:**

```
* a3f2b1c 2026-03-20 etc/nginx/nginx.conf    sysfig: update etc/nginx/nginx.conf (HEAD -> master)
* 8d4e92a 2026-03-20 home/you/.zshrc         sysfig: update home/you/.zshrc
* 1c7f03b 2026-03-19 etc/pacman.d/mirrorlist +3  sysfig: update etc/pacman.d (4 files)
```

### `undo`

One command for two undo modes — choose the form that matches your situation:

| Form | What it does |
|---|---|
| `sysfig undo <path\|id>` | **Non-destructive.** Restores the on-disk file from the HEAD of its track branch. No commit is created; git history is unchanged. Use this to discard accidental edits. |
| `sysfig undo <commit> <path\|id>` | **Destructive.** Rewinds the file's track branch to `<commit>` via `git update-ref`. The branch pointer moves back; no new commit is added. Other branches are untouched. |

A path, tracking ID, or `--all` is required. `--all` is only valid with a commit hash.

```
sysfig undo <path|id> [options]
sysfig undo <commit> <path|id> [options]
sysfig undo <commit> --all     [options]
```

**Options:**

| Flag         | Default     | Description                                               |
| ------------ | ----------- | --------------------------------------------------------- |
| `--all`      | `false`     | Apply the commit reset to every `track/*` branch          |
| `--force`    | `false`     | Skip the confirmation prompt (destructive form only)      |
| `--dry-run`  | `false`     | Preview what would be changed without making any edits    |
| `--base-dir` | `~/.sysfig` | Directory where sysfig stores data                        |

**Examples:**

```bash
sysfig undo /etc/nginx/nginx.conf                # discard unsaved edits — by path
sysfig undo a3f2b1c0                             # discard unsaved edits — by tracking ID
sysfig undo a3f2b1c /etc/nginx/nginx.conf        # rewind one file to a commit — by path
sysfig undo a3f2b1c a3f2b1c0                     # rewind one file to a commit — by ID
sysfig undo a3f2b1c --all                        # rewind all track branches (prompts)
sysfig undo a3f2b1c --all --force                # same, no prompt
sysfig undo a3f2b1c --all --dry-run              # preview what would change
```

> The tracking ID shown next to a dirty file in `sysfig status` (e.g. `7734be1e`) can be passed directly to `undo` — no need to type the full path.

> `sysfig undo <path|id>` does **not** create a commit. If you want the reverted content recorded in history, run `sysfig sync --auto` after restoring.

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

### `source`

Manage **Config Source** template catalogs — consume shared config templates (proxy settings, DNS, NTP, rsyslog forwarding, or any repeated config pattern) from a remote bundle or git repo, inject per-machine variable values, and commit the rendered output as ordinary tracked files.

```
sysfig source add    <name> <url>
sysfig source list   <source>
sysfig source use    <source/profile> [--var key=value]... | --values <file>
sysfig source render [--values <file>] [--profile <source/profile>] [--force] [--dry-run]
sysfig source pull   <source>
```

**How it works (Render-to-Git):**

1. A *source bundle* is a git repo or bundle file containing profile directories (`profiles/<name>/profile.yaml` + templates).
2. `sysfig source add` registers the source URL in `~/.sysfig/sources.yaml` (local, never committed).
3. `sysfig source use` sets per-machine variable values (interactive prompts, `--var key=value` flags, or `--values file`) and saves them to `sources.yaml`.
4. `sysfig source render` fetches the bundle, renders each template with your variables, and commits the output to `track/*` branches exactly like manually tracked files.
5. `sysfig diff` / `sysfig apply` work unchanged — nothing new to learn.

**Supported source URL types:**

| URL | Transport |
|---|---|
| `bundle+local:///path/to/file.bundle` | NFS, USB, local disk |
| `bundle+ssh://user@host/path/file.bundle` | SSH file server |
| `git@host:org/repo.git` | GitHub, GitLab, Gitea, self-hosted git |
| `https://host/org/repo.git` | Public git over HTTPS |

**Typical workflow:**

```bash
# Register the source (git remote or bundle — same command)
sysfig source add corp git@github.com:your-org/corp-configs.git
# or bundle:
# sysfig source add corp bundle+local:///mnt/nfs/corp-configs.bundle

# See what profiles are available (also fetches the latest)
sysfig source list corp
#   ────────────────────────────────────────────────────────────────────────
#     PROFILE             FILES  DESCRIPTION
#   ────────────────────────────────────────────────────────────────────────
#     dns-resolvers       2      DNS resolver config — systemd-resolved and resolv.conf
#     ntp-pool            1      NTP time synchronisation — systemd-timesyncd
#     syslog-forwarder    1      Remote syslog forwarding — rsyslog TCP/TLS
#     system-proxy        4      HTTP/HTTPS proxy — /etc/environment, apt, Docker
#   ────────────────────────────────────────────────────────────────────────

# Activate a profile — three ways to supply variables:

# 1. Interactive (TTY prompts)
sysfig source use corp/system-proxy
#   bypass_list [localhost,127.0.0.1,::1]: 10.0.0.0/8,localhost
#   proxy_url (required): http://proxy.corp.com:3128
#   ✓ Profile "corp/system-proxy" added to sources.yaml

# 2. Inline flags (any order, no prompts)
sysfig source use corp/system-proxy \
  --var proxy_url=http://proxy.corp.com:3128 \
  --var bypass_list=10.0.0.0/8,localhost

# 3. Values file — all profiles at once, then render in one command
sysfig source render --values corp-values.yaml
#   ✓ Activated: corp/dns-resolvers
#   ✓ Activated: corp/system-proxy
#   ✓ Rendered: /etc/environment
#   ...

# Render — commits rendered output to track/* branches
sysfig source render
#   ✓ Rendered: /etc/environment
#   ✓ Rendered: /etc/apt/apt.conf.d/95proxy
#   ✓ Rendered: /etc/systemd/system/docker.service.d/http-proxy.conf
#   ✓ Rendered: /etc/profile.d/proxy.sh

# Review and apply (standard sysfig flow — nothing new)
sysfig diff
sysfig apply
```

**Updating when the upstream template changes:**

```bash
sysfig source pull corp      # fetch latest bundle or git commits
sysfig source render         # re-render with your existing variables
sysfig diff                  # review what changed
sysfig apply
```

**Rendering a single profile:**

```bash
sysfig source render --profile corp/system-proxy
```

**Status labels for source-managed files:**

| Status | Meaning |
|---|---|
| `SOURCE` | Content matches committed render — nothing to do |
| `DIRTY` | Drifted from committed render (manually edited after apply) |
| `MISSING` | Not yet on disk — run `sysfig apply` |
| `PENDING` | A new render is committed but not yet applied |

`sysfig sync` skips source-managed files — their content is owned by `source render`, not by `sync`.

**Taking manual ownership** of a source-managed file:

```bash
sysfig track --force /etc/environment   # clears source_profile, enables sync
```

**Conflict handling:** If two profiles declare the same output file, `source render` refuses. Use `--force` to transfer ownership.

**Subcommands:**

| Subcommand | Description |
| ---------- | ----------- |
| `source add <name> <url>` | Register a source bundle URL in `~/.sysfig/sources.yaml` |
| `source list <source>` | List available profiles (pulls latest first) |
| `source use <source/profile> [--var key=value]...` | Activate a profile; set variables inline (mutually exclusive with `--values`) |
| `source use <source/profile> --values <file>` | Activate a profile from a flat YAML values file |
| `source render [--values F] [--profile P] [--dry-run] [--force]` | Render profiles; `--values` activates all listed profiles before rendering |
| `source pull <source>` | Fetch the latest bundle / git commits without rendering |

> See [docs/config-sources.md](docs/config-sources.md) for the complete guide — from writing templates to publishing and deploying on a new machine. For the design rationale see [docs/rfcs/config-sources.md](docs/rfcs/config-sources.md).

---

### `tag`

Inspect and manage the tags stored on tracked files.

```
sysfig tag --list
sysfig tag --auto [--overwrite]
sysfig tag --rename <old> --to <new>
sysfig tag <path-or-id> [tag...]
```

Tags are plain strings stored in `FileRecord.Tags` inside `state.json` and `sysfig.yaml`. They drive tag-filtered remote deploy (`deploy --host --tag <tag>`) and are displayed in the `TAGS` column of `sysfig status` and `sysfig audit`.

**Modes:**

| Mode | Command | Description |
| ---- | ------- | ----------- |
| List | `sysfig tag --list` | Print every distinct tag with a file count; shows untagged file count with a hint to run `sysfig tag --auto` |
| Auto | `sysfig tag --auto` | Write OS + distro tags (`DetectPlatformTags()`) to all untagged files in `state.json` |
| Auto overwrite | `sysfig tag --auto --overwrite` | Rewrite tags on ALL tracked files (replaces existing tags) |
| Rename | `sysfig tag --rename <old> --to <new>` | Rename a tag across every file that carries it |
| Set | `sysfig tag <path-or-id> [tag...]` | Set explicit tags on a specific file; passing no tags clears them |

> **Deploy implicit tag fallback:** when `deploy --host --tag <tag>` is used and a file has no stored tags, the deploy falls back to `DetectPlatformTags()` for matching. This means `sysfig deploy --host user@server --tag linux` picks up untagged files on a Linux machine even before `sysfig tag --auto` has been run.

**Options:**

| Flag          | Description |
| ------------- | ----------- |
| `--list`      | Show all tags and per-tag file counts |
| `--auto`      | Tag untagged files with OS + distro family |
| `--overwrite` | Combined with `--auto`: rewrite tags on all tracked files |
| `--rename`    | Old tag name to replace (requires `--to`) |
| `--to`        | New tag name for `--rename` |
| `--base-dir`  | `~/.sysfig` — directory where sysfig stores its data |

**Examples:**

```bash
# See all tags currently in use
sysfig tag --list
#   arch      14 files
#   linux     14 files
#   debian     3 files
#   (untagged: 2 files — run 'sysfig tag --auto' to tag them)

# Tag all currently-untagged files with the local OS + distro
sysfig tag --auto
#   ✓ Tagged  /etc/pacman.conf        linux,arch
#   ✓ Tagged  /home/aye7/.zshrc      linux,arch
#   ✓ Skipped /etc/nginx/nginx.conf  (already tagged)

# Rewrite tags on every tracked file (useful after distro migration)
sysfig tag --auto --overwrite

# Rename the "debian" tag to "ubuntu" across all files
sysfig tag --rename debian --to ubuntu

# Tag a specific file explicitly
sysfig tag /etc/nginx/nginx.conf web nginx linux

# Clear all tags from a file (no tags provided)
sysfig tag /etc/nginx/nginx.conf
```

**Example output (tag --list):**

```
TAG        FILES
────────────────────────────────────
arch          14
linux         14
web            2

  (untagged: 2 files — run 'sysfig tag --auto' to tag them)
```

---

## Configuration File: sysfig.yaml

`sysfig.yaml` lives at the root of your config repo and is committed to git. It is the shared manifest that tells `sysfig bootstrap` what to seed on a new machine.

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
| **Read by** | `sysfig bootstrap` (to bootstrap state on new machines) | `sysfig status`, `sysfig diff`, `sysfig apply` |

When you run `sysfig bootstrap` on a new machine, sysfig reads `sysfig.yaml` from the cloned repo and populates a fresh `state.json` with the file records. From that point on, `state.json` is maintained locally.

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
sysfig bootstrap --skip-encrypted git@github.com:you/myconfigs.git
```

---

## Local Integrity Tracking

sysfig can track sensitive files without ever pushing them to the remote repo.

| Flag | Stored where | Synced locally | Pushed |
|------|-------------|---------------|--------|
| `--local` | `~/.sysfig/repo.git` on `local/<path>` branch | ✅ yes | ❌ never |
| `--hash-only` | hash only in `state.json` | ❌ no | ❌ never |

```bash
# Full local history, zero remote exposure:
sysfig track --local /etc/wireguard/wg0.conf

# After rotating keys — commit the change with a message:
sysfig sync /etc/wireguard/wg0.conf -m "rotated VPN keys"
sysfig log /etc/wireguard/wg0.conf

# Tamper detection only — nothing stored:
sysfig track --hash-only /etc/ssh/sshd_config

# Check for drift:
sysfig audit          # exits 1 if anything changed
sysfig audit --quiet  # silent, for timers/scripts
```

`sysfig status` shows `local` or `hash` tags and the `TAMPERED` status for hash-only drift:

```
/etc/wireguard/wg0.conf     0b5aac93    DIRTY    local
/etc/ssh/sshd_config        a26787d2    TAMPERED hash
```

A systemd timer is available in `contrib/systemd/` for automated hourly checks.

See [docs/integrity.md](docs/integrity.md) for the full guide.

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

### Profile post_apply hooks (`source render`)

Config Source profiles can also declare hooks in `profile.yaml`. These fire automatically after `sysfig source render` commits new content — no `hooks.yaml` needed.

```yaml
# profile.yaml
hooks:
  post_apply:
    - systemd_reload: rsyslog.service
    - exec: "nginx -t"
```

| Field | Effect |
|---|---|
| `systemd_reload: <service>` | `systemctl reload <service>` after render |
| `exec: "cmd arg1 arg2"` | Run the command after render |

Hook errors from `source render` are **non-fatal** — the render succeeds and a warning is printed. This differs from `hooks.yaml` hooks (which are fatal by default) because profile hooks may require privileges not always available on every machine.

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
├── sources/                         ← config source cache (local only)
│   └── corp/
│       └── repo.git/                ← bare cache of the source bundle / git remote
│
├── sources.yaml                     ← source declarations + activated profiles (local only)
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
| `audit`    | All checked files clean   | Any TAMPERED / DIRTY           | Error |
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
| `source render` (cache hit)      | Never             |
| `push`                           | Always            |
| `pull`                           | Always            |
| `bootstrap` (initial setup only) | Yes, one-time     |
| `deploy --host` (remote push)    | SSH to target     |
| `source add/list/pull/use`       | Yes (fetches bundle or git remote) |
| `source render` (cache miss)     | Yes (auto-fetches bundle) |

If `sysfig bootstrap` detects the bare repo already exists locally, it shows hints and exits cleanly — no silent pull, no network call. The user must explicitly run `sysfig pull` to fetch changes. This prevents silent data loss on intermittent networks and keeps the local repo as the always-valid source of truth.

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
| Local-only branch leak  | `local/*` branches excluded from push refspec and bundle creation; they can never reach the remote even by accident |
| Tamper detection        | `--hash-only` records a BLAKE3 hash of sensitive files without storing any content; `sysfig audit` exits 1 on drift |

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
sysfig bootstrap  # re-reads sysfig.yaml and rebuilds state.json
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
