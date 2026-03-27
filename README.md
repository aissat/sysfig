# sysfig

> **Config management for people who own their machines.**
> Track, encrypt, and audit system configs — without thinking about git.

`sysfig` versions both dotfiles and root-owned `/etc/` configs in a single bare git repo. Built-in [age](https://age-encryption.org/) encryption, atomic deploys, offline-first, no symlinks.

---

## Install

**Debian / Ubuntu (.deb):**

```bash
curl -Lo sysfig.deb https://github.com/aissat/sysfig/releases/latest/download/sysfig_latest_amd64.deb
sudo dpkg -i sysfig.deb
```

**RHEL / Fedora / Rocky (.rpm):**

```bash
sudo rpm -i https://github.com/aissat/sysfig/releases/latest/download/sysfig_latest_amd64.rpm
```

**Alpine (.apk):**

```bash
curl -Lo sysfig.apk https://github.com/aissat/sysfig/releases/latest/download/sysfig_latest_amd64.apk
sudo apk add --allow-untrusted sysfig.apk
```

**Binary (Linux / macOS):**

```bash
curl -Lo sysfig https://github.com/aissat/sysfig/releases/latest/download/sysfig-linux-amd64
chmod +x sysfig && sudo mv sysfig /usr/local/bin/
```

Replace `linux-amd64` with `linux-arm64`, `darwin-amd64`, or `darwin-arm64` as needed.

**Arch Linux (AUR):**

```bash
yay -S sysfig   # or: paru -S sysfig
```

**From source:**

```bash
git clone https://github.com/aissat/sysfig
cd sysfig && go build -o sysfig ./cmd/sysfig
sudo mv sysfig /usr/local/bin/
```

```bash
sysfig doctor   # verify install
```

---

## 3 commands to get started

```bash
# 1. Track a file
sysfig track ~/.zshrc
sysfig track /etc/nginx/nginx.conf   # needs sudo for /etc

# 2. Commit changes
sysfig sync

# 3. Restore everything on a new machine
sysfig bootstrap git@github.com:you/conf.git
# → clones repo + applies all configs immediately
```

---

## 4 Ways to Use sysfig

**1. Dotfiles only**
Track `~/.bashrc`, `~/.config` across laptops. No symlinks, no mess.

**2. Solo sysadmin**
Manage `/etc/nginx`, `/etc/ssh`, systemd units across VPS servers. Preserves ownership, permissions, and auto-backs up before replacing.

**3. Small team**
Share encrypted secrets and deploy configs to remote servers via SSH. Tag files by OS/distro so each host only receives what it needs. Run `sysfig tag --auto` to label every tracked file with its OS + distro family, then `sysfig deploy --host user@vm --tag ubuntu --sudo` to push only the matching configs. No agents on remote machines — only a POSIX shell needed on the target.

**4. Security-conscious admin**
Track `/etc/sudoers` with `--hash-only` — hash recorded locally, nothing pushed. `sysfig audit` exits 1 on drift, safe to wire into a systemd timer.

---

## Why sysfig?

| Tool       | `/etc/` | Encryption  | Offline | Metadata | Backup | Audit | No-git-server | Single binary |
| ---------- | ------- | ----------- | ------- | -------- | ------ | ----- | ------------- | ------------- |
| GNU Stow   | ✗       | ✗           | ✓       | ✗        | ✗      | ✗     | ✗             | ✓             |
| YADM       | ✗       | partial     | ✓       | ✗        | ✗      | ✗     | ✗             | ✓             |
| Chezmoi    | partial | partial     | ✓       | ✗        | ✗      | ✗     | ✗             | ✓             |
| Ansible    | ✓       | via vault   | ✗       | ✓        | ✗      | ✗     | ✗             | ✗             |
| **sysfig** | **✓**   | **✓ (age)** | **✓**   | **✓**    | **✓**  | **✓** | **✓**         | **✓**         |

---

## Architecture

```
  /etc/nginx/nginx.conf ─track─▶ ~/.sysfig/repo.git ─sync --push─▶ github / NFS / USB
  /etc/ssh/sshd_config                               ◀─sync --pull─
  ~/.bashrc             ◀─apply─
                                  state.json   (local cache, never committed)
                                  sysfig.yaml  (manifest, committed & shared)
                                  keys/        (encryption keys, never pushed)
                                  backups/     (auto-backups before apply)
```

Each tracked file gets its own git branch (`track/<path>`). No full-tree noise, clean per-file history.

---

## Daily workflow

```bash
vim /etc/nginx/nginx.conf

sysfig status                          # see what changed (TYPE + TAGS columns shown)
sysfig diff                            # show the diff
sysfig sync --push --message "tuned"   # commit + push

# Or just run:
sysfig watch --push                    # auto-commit + push on every save
```

### Auto-tagging and tag-based deploy

```bash
# Track without --tag: sysfig auto-tags with OS + distro family
# On Arch Linux:
sysfig track /etc/pacman.conf          # stored as: linux,arch
# On Ubuntu:
sysfig track /etc/apt/sources.list     # stored as: linux,debian

# status and audit now show TYPE and TAGS columns:
# PATH              HASH        STATUS    TYPE    TAGS
# /home/user/       018e4a02    3 synced  file    linux,arch
# /etc/pacman.d/    63d01e28    11 synced  group   linux,arch

# Deploy only to matching hosts — --tag or --all is required:
sysfig deploy --host user@arch-vm --tag arch --sudo
sysfig deploy --host user@ubuntu-vm --tag ubuntu --sudo
sysfig deploy --host user@server --all --sudo   # deploy everything
```

---

## New machine

```bash
# First time — clone + apply everything immediately
sysfig bootstrap git@github.com:you/conf.git

# Ongoing — pull latest + apply
sysfig deploy

# Air-gapped (USB / NFS / SSH file server)
sysfig bootstrap bundle+local:///mnt/usb/conf.bundle
sysfig deploy    bundle+ssh://backup@server/srv/conf.bundle
```

---

## Key commands

| Command | What it does |
|---------|-------------|
| `sysfig track <path>` | Start tracking a file |
| `sysfig track --encrypt <path>` | Track and encrypt (age) |
| `sysfig track --hash-only <path>` | Record hash only — never pushed |
| `sysfig track --local <path>` | Track locally — never pushed |
| `sysfig status` | Show sync state of all tracked files |
| `sysfig diff` | Show what changed since last commit |
| `sysfig sync` | Commit all changes locally |
| `sysfig sync --push` | Commit and push to remote |
| `sysfig apply` | Write tracked configs to disk |
| `sysfig bootstrap <url>` | New machine: clone + apply |
| `sysfig deploy` | Existing machine: pull + apply |
| `sysfig deploy --host user@server --tag linux --sudo` | Push files matching a tag to remote over SSH (`--tag` or `--all` required) |
| `sysfig tag --list` | Show all tags with file counts |
| `sysfig tag --auto` | Auto-tag untagged files with OS + distro family |
| `sysfig tag --rename old --to new` | Rename a tag across all files |
| `sysfig tag <path> [tag...]` | Set (or clear) tags on a specific file |
| `sysfig watch --push` | Auto-commit + push on every file change |
| `sysfig audit` | Check integrity (exits 1 on drift) |
| `sysfig undo <path>` | Restore file from last commit |
| `sysfig snap take` | Take a point-in-time snapshot |
| `sysfig doctor` | Check environment health |
| `sysfig log` | Show commit history |

---

## Documentation

- **[Getting started](docs/getting-started.md)** — step-by-step walkthrough
- **[Command reference](docs/commands.md)** — full flags and examples for every command
- **[Integrity tracking](docs/integrity.md)** — `--hash-only`, `--local`, `sysfig audit`
- **[Hooks](docs/hooks.md)** — run commands after apply
- **[Config sources](docs/config-sources.md)** — shared templates across machines
- **[Secrets](docs/secrets.md)** — encryption, multi-node key sharing
- **[Security](docs/security.md)** — audit findings, remediations, open issues

---

## License

MIT — see [LICENSE](LICENSE).
