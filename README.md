# sysfig

> **Config management for people who own their machines.**
> Track, encrypt, and audit system configs — without thinking about git.

`sysfig` versions both dotfiles and root-owned `/etc/` configs in a single bare git repo. Built-in [age](https://age-encryption.org/) encryption, atomic deploys, offline-first, no symlinks.

---

## Install

```bash
git clone https://github.com/aissat/sysfig
cd sysfig && go build -o sysfig ./cmd/sysfig
sudo mv sysfig /usr/local/bin/
sysfig doctor   # verify
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
Share encrypted secrets and deploy configs to remote servers via SSH (`sysfig deploy --host`). No agents on remote machines.

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

sysfig status                          # see what changed
sysfig diff                            # show the diff
sysfig sync --push --message "tuned"   # commit + push

# Or just run:
sysfig watch --push                    # auto-commit + push on every save
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

---

## License

MIT — see [LICENSE](LICENSE).
