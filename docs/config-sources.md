# Config Sources — From Template to Deployment

> **Config Sources** let you define shared config templates once — for proxy settings, DNS, NTP, TLS certs, rsyslog forwarding, or any repeated config pattern — and render them across every machine with a single command.

This guide covers the complete lifecycle:

1. [Creating a source bundle](#1-creating-a-source-bundle) — write `profile.yaml` and templates
2. [Publishing the bundle](#2-publishing-the-bundle) — distribute via NFS, SSH, or git
3. [Using a source bundle](#3-using-a-source-bundle) — add, list, use, render, apply
4. [Day-to-day maintenance](#4-day-to-day-maintenance) — update variables, pull upstream changes
5. [Deploy to a new machine](#5-deploy-to-a-new-machine) — bootstrap from bundle
6. [Reference](#reference)

---

## The mental model

A **source bundle** is either a git bundle file (`git bundle create --all`) or a standard git repository. You create it once, publish it to a shared location, and every machine consumes it read-only.

```
bundle author                     consumer machine
─────────────────                 ─────────────────────────────────────
profiles/                         sysfig source add corp <url>
  system-proxy/                   sysfig source use corp/system-proxy
    profile.yaml         ──────▶  sysfig source render
    templates/                    sysfig diff
      environment.tmpl            sysfig apply
      apt-proxy.tmpl
```

Templates use `{{variable}}` — the same syntax as `sysfig track --template`. The rendered output lands in `track/*` branches exactly like manually tracked files. `diff`, `log`, and `apply` work on rendered files identically to manually tracked ones. `status`, `sync`, and `track` gain source-aware behaviour: `status` shows a `SOURCE` label, `sync` skips source-managed files, and `track` refuses to re-take ownership without `--force`.

**Supported transports:**

| URL prefix | Use case |
|---|---|
| `bundle+local:///path/to/file.bundle` | NFS mount, USB key, local disk |
| `bundle+ssh://user@host/path/to/file.bundle` | SSH file server — no git daemon needed |
| `git@host:org/repo.git` | GitHub, GitLab, Gitea, self-hosted git |
| `https://host/org/repo.git` | Public git hosting over HTTPS |

---

## 1. Creating a source bundle

### 1.1 Directory structure

A source bundle is a git repo with this layout:

```
my-configs/                          ← your git repo
  profiles/
    system-proxy/
      profile.yaml                   ← declares variables + output files
      templates/
        environment.tmpl
        apt-proxy.tmpl
        docker-proxy.tmpl
    dns-resolvers/
      profile.yaml
      templates/
        resolv.conf.tmpl
    ntp-pool/
      profile.yaml
      templates/
        ntp.conf.tmpl
    syslog-forwarder/
      profile.yaml
      templates/
        rsyslog-remote.conf.tmpl
```

Each `profiles/<name>/` directory is one profile. Names must be filesystem-safe (letters, digits, hyphens, underscores).

### 1.2 `profile.yaml` schema

Every profile needs a `profile.yaml` at its root:

```yaml
name: system-proxy
version: "1.0"
description: "HTTP/HTTPS proxy for /etc/environment, apt, Docker, and systemd"

variables:
  proxy_url:
    required: true
    description: "Full proxy URL including port"
    example: "http://proxy.corp.com:3128"
  bypass_list:
    required: false
    default: "localhost,127.0.0.1,::1"
    description: "Comma-separated hosts and networks that bypass the proxy"

files:
  - dest: /etc/environment
    template: templates/environment.tmpl
    mode: "0644"
    owner: "root"
    group: "root"

  - dest: /etc/apt/apt.conf.d/95proxy
    template: templates/apt-proxy.tmpl
    mode: "0644"
    owner: "root"
    group: "root"

  - dest: /etc/systemd/system/docker.service.d/http-proxy.conf
    template: templates/docker-proxy.tmpl
    mode: "0644"
    owner: "root"
    group: "root"

```

**Field reference:**

| Field | Required | Description |
|---|---|---|
| `name` | yes | Profile name (should match the directory name) |
| `version` | no | Semantic version string |
| `description` | no | One-line description shown by `sysfig source list` |
| `variables.<name>.required` | no | If `true`, render fails when the variable is not set and has no default |
| `variables.<name>.default` | no | Value used when the variable is omitted (use `""` for intentionally empty) |
| `variables.<name>.description` | no | Shown during interactive `sysfig source use` prompt |
| `variables.<name>.example` | no | Example value shown when the field is required |
| `files[].dest` | yes | Absolute path on the target machine (e.g. `/etc/environment`) |
| `files[].template` | yes | Path to the template file, relative to the profile directory |
| `files[].mode` | no | Octal permission string (e.g. `"0644"`) |
| `files[].owner` | no | File owner name |
| `files[].group` | no | File group name |
| `files[].encrypt` | no | If `true`, the rendered file is age-encrypted in the repo |

**`hooks.post_apply`** runs automatically after `sysfig source render` commits new content for this profile. Each entry has either an `exec` command or a `systemd_reload` service:

```yaml
hooks:
  post_apply:
    - systemd_reload: rsyslog.service   # systemctl reload rsyslog.service
    - exec: "nginx -t"                  # validate nginx config
```

Hook errors are non-fatal — the render succeeds and a warning is printed. Hooks are skipped when `--dry-run` is set or when no files changed (all skipped due to matching hash).

### 1.3 Writing templates

Templates use `{{variable}}` placeholders — the same engine used by `sysfig track --template`. Any variable declared in `profile.yaml` can be referenced by name. Optional variables with an empty or no default resolve to an empty string in the rendered output.

**`templates/environment.tmpl`:**

```sh
# /etc/environment — system-wide proxy settings
# Managed by sysfig source: corp/system-proxy
http_proxy={{proxy_url}}
https_proxy={{proxy_url}}
HTTP_PROXY={{proxy_url}}
HTTPS_PROXY={{proxy_url}}
no_proxy={{bypass_list}}
NO_PROXY={{bypass_list}}
```

**`templates/apt-proxy.tmpl`:**

```
## /etc/apt/apt.conf.d/95proxy — apt proxy settings
## Managed by sysfig source: corp/system-proxy
Acquire::http::Proxy "{{proxy_url}}";
Acquire::https::Proxy "{{proxy_url}}";
```

**`templates/docker-proxy.tmpl`:**

```ini
# /etc/systemd/system/docker.service.d/http-proxy.conf
# Managed by sysfig source: corp/system-proxy
[Service]
Environment="HTTP_PROXY={{proxy_url}}"
Environment="HTTPS_PROXY={{proxy_url}}"
Environment="NO_PROXY={{bypass_list}}"
```

**Built-in variables** (from the current machine) are also available in every template:

| Placeholder | Value |
|---|---|
| `{{hostname}}` | Machine hostname |
| `{{user}}` | Current username |
| `{{home}}` | Home directory path |
| `{{os}}` | `linux`, `darwin`, or `windows` |
| `{{env.VAR}}` | Value of environment variable `VAR` at render time |

Built-in variables (`{{hostname}}`, `{{env.VAR}}`, etc.) are available **inside template files**. They are resolved at render time by the same engine that renders `{{variable}}` placeholders. Default values in `profile.yaml` are treated as plain strings and are **not** re-rendered — writing `default: "{{hostname}}"` would produce the literal string `{{hostname}}` in output, not the machine's hostname. To use a built-in as a fallback, reference it directly in the template.

**Sensitive values:** Use `{{env.PROXY_PASSWORD}}` in template files — values are resolved from the shell environment at render time and never stored in `sources.yaml` or the repo.

### 1.4 Adding more profiles to the same bundle

Add more directories under `profiles/`. A single bundle can contain any number of independent profiles:

```
profiles/
  system-proxy/        ← HTTP/HTTPS proxy
  dns-resolvers/       ← DNS resolver config
  ntp-pool/            ← NTP server pool
  tls-certs/           ← Corporate CA bundle
  syslog-forwarder/    ← rsyslog remote forwarding
```

Machines that only need DNS resolvers activate only that profile — the rest are ignored.

### 1.5 Commit and bundle

Once your profiles are ready, commit them to a git repo and optionally create a bundle file for file-based distribution:

```bash
cd ~/my-configs

# Initial setup
git init
git add profiles/
git commit -m "add system-proxy and dns-resolvers profiles"

# Create (or update) the bundle file for NFS/SSH distribution
git bundle create /path/to/publish/corp-configs.bundle --all

# Update the bundle after adding or changing profiles
git add profiles/
git commit -m "system-proxy: add docker proxy support"
git bundle create /path/to/publish/corp-configs.bundle --all
```

The bundle is a single self-contained file. Copy it to wherever machines can reach it. If you are using a standard git remote (GitHub, GitLab, etc.), skip the `git bundle` step and just push.

---

## 2. Publishing the bundle

Choose the transport that matches your environment:

### Option A — Local filesystem / NFS mount

Put the bundle on a path every machine can read:

```bash
# Copy to shared NFS mount
cp corp-configs.bundle /mnt/corp-nfs/sysfig/corp-configs.bundle

# Machines register it as:
# bundle+local:///mnt/corp-nfs/sysfig/corp-configs.bundle
```

This works for air-gapped environments, office LANs, and corporate NFS/SMB shares. No git server needed.

### Option B — SSH file server

Upload to any machine reachable by SSH:

```bash
scp corp-configs.bundle backup@fileserver:/srv/sysfig/corp-configs.bundle

# Machines register it as:
# bundle+ssh://backup@fileserver/srv/sysfig/corp-configs.bundle
```

`scp` is the only requirement on the server — no git daemon, no special software.

### Option C — Standard git remote (GitHub, GitLab, Gitea, self-hosted)

Push the repo to any standard git host:

```bash
git remote add origin git@github.com:your-org/corp-configs.git
git push -u origin main

# Machines register it using the standard git remote URL directly:
sysfig source add corp git@github.com:your-org/corp-configs.git
# or HTTPS:
sysfig source add corp https://github.com/your-org/corp-configs.git
```

On first `source list` or `source render`, sysfig clones the repo into a local bare cache at `~/.sysfig/sources/<name>/repo.git`. Subsequent operations run `git fetch` to pull updates.

For private repos, SSH key authentication is the most reliable option. Ensure the machine's SSH key is authorised on the remote.

### Keeping the bundle up to date

For file-based transports (Options A and B), every time you change a profile, re-create the bundle and copy it to the publish location:

```bash
git add profiles/
git commit -m "dns-resolvers: switch to Cloudflare primary"
git bundle create /tmp/corp-configs.bundle --all
scp /tmp/corp-configs.bundle backup@fileserver:/srv/sysfig/corp-configs.bundle
```

For git remotes (Option C), just push:

```bash
git add profiles/
git commit -m "dns-resolvers: switch to Cloudflare primary"
git push
```

Machines pull the update with `sysfig source pull corp` and re-render with `sysfig source render`.

---

## 3. Using a source bundle

### 3.1 Register the source

```bash
# Local filesystem / NFS
sysfig source add corp bundle+local:///mnt/corp-nfs/sysfig/corp-configs.bundle

# SSH file server
sysfig source add corp bundle+ssh://backup@fileserver/srv/sysfig/corp-configs.bundle

# Standard git remote
sysfig source add corp git@github.com:your-org/corp-configs.git
sysfig source add corp https://github.com/your-org/corp-configs.git

# Public community bundle
sysfig source add community git@github.com:aissat/config-template.git
```

This writes to `~/.sysfig/sources.yaml` and does not yet fetch any content. You can register multiple sources with different names.

### 3.2 See what profiles are available

```bash
sysfig source list corp
```

```
────────────────────────────────────────────────────────────────────────────
  PROFILE             FILES  DESCRIPTION
────────────────────────────────────────────────────────────────────────────
  dns-resolvers       2      DNS resolver configuration — systemd-resolved and resolv.conf
  ntp-pool            1      NTP time synchronisation — systemd-timesyncd
  syslog-forwarder    1      Remote syslog forwarding — rsyslog TCP/TLS to a central log server
  system-proxy        4      HTTP/HTTPS proxy — /etc/environment, apt, Docker, and systemd
────────────────────────────────────────────────────────────────────────────

  To activate a profile: sysfig source use corp/<profile>
```

`source list` automatically pulls the latest bundle or fetches from the git remote so you always see current profiles.

### 3.3 Activate a profile

```bash
sysfig source use corp/system-proxy
```

When connected to a TTY, sysfig prompts for each variable in alphabetical order. Optional variables show their default in brackets; required variables are marked `(required)`:

```
  bypass_list [localhost,127.0.0.1,::1]: 10.0.0.0/8,localhost,corp.internal
  proxy_url (required): http://proxy.corp.com:3128

  ✓ Profile "corp/system-proxy" added to sources.yaml
  ℹ Run 'sysfig source render' to commit the rendered files.
```

Press Enter to accept a default. For required variables with no default, you must provide a value.

**Non-interactive use with `--values` (recommended for scripting):**

Create a YAML file with all variable values and pass it to `source render` directly — activates and renders all profiles in one command:

```bash
# lab-values.yaml
cat > lab-values.yaml <<'EOF'
corp/system-proxy:
  proxy_url: http://proxy.corp.com:3128
  bypass_list: 10.0.0.0/8,localhost

corp/dns-resolvers:
  primary_dns: 10.0.0.53
  secondary_dns: 10.0.0.54
  search_domain: corp.internal
  dnssec: allow-downgrade
  dns_over_tls: "no"
EOF

sysfig source render --values lab-values.yaml
sysfig diff
sysfig apply
```

You can also pass `--values` to `source use` for a single profile (flat key/value YAML):

```bash
sysfig source use corp/dns-resolvers --values dns-values.yaml
```

**Non-interactive use with `--var`:**

Pass variables directly with `--var key=value` (repeatable) — no need to match alphabetical order:

```bash
sysfig source use corp/system-proxy \
  --var proxy_url=http://proxy.corp.com:3128 \
  --var bypass_list=10.0.0.0/8,localhost
```

```bash
sysfig source use corp/dns-resolvers \
  --var primary_dns=10.0.0.53 \
  --var secondary_dns=10.0.0.54 \
  --var search_domain=corp.internal \
  --var dnssec=allow-downgrade \
  --var dns_over_tls=no
```

Variables not supplied via `--var` are prompted interactively when stdin is a TTY, or read line-by-line in alphabetical order when piped.

The variable values are written to `~/.sysfig/sources.yaml`. This file is local to the machine and is not committed to the machine's config repo.

If the profile is already activated, running `source use` again overwrites the variable values.

### 3.4 Render

```bash
sysfig source render
```

```
  ✓ Rendered: /etc/environment
  ✓ Rendered: /etc/apt/apt.conf.d/95proxy
  ✓ Rendered: /etc/systemd/system/docker.service.d/http-proxy.conf
  · Unchanged: /etc/profile.d/proxy.sh

  ℹ Run 'sysfig diff' to review, then 'sysfig apply' to write files to disk.
```

What `source render` does:

1. Reads `~/.sysfig/sources.yaml` for active profiles and their variables
2. Fetches the latest bundle / git ref into `~/.sysfig/sources/<name>/repo.git` (cached bare repo)
3. Reads `profile.yaml` and validates required variables; applies defaults for omitted optional variables
4. For each output file: renders the template, compares the hash with what is already committed
5. If the content is new or changed: commits it to the `track/<path>` branch
6. Updates `state.json` to mark each file as owned by `corp/system-proxy`
7. Files with no change are reported as `Unchanged` and skipped

### 3.5 Preview before rendering

```bash
sysfig source render --dry-run
```

```
  ℹ [dry-run] Would render: /etc/environment
  ℹ [dry-run] Would render: /etc/apt/apt.conf.d/95proxy
  ℹ [dry-run] Would render: /etc/systemd/system/docker.service.d/http-proxy.conf
```

No commits are written. Use this to verify the list of affected files before committing.

### 3.6 Render a single profile

```bash
sysfig source render --profile corp/system-proxy
```

Useful when you have multiple profiles activated and only want to refresh one.

### 3.7 Review and apply

After rendering, use the standard sysfig workflow — nothing is different here:

```bash
# See the full diff between the repo (rendered) and current disk state
sysfig diff

# Write all pending changes to disk
sysfig apply
```

### 3.8 Check status

```bash
sysfig status
```

```
PATH                                          HASH        STATUS
──────────────────────────────────────────────────────────────────────────
/etc/environment                              a3f2b1c0    SOURCE
/etc/apt/apt.conf.d/95proxy                   b1e4d2a1    SOURCE
/etc/systemd/system/docker.service.d/...      c2f3e4b5    SOURCE
/etc/profile.d/proxy.sh                       cc2f1b3e    SOURCE
/etc/nginx/nginx.conf                         d4e5f6a7    SYNCED
/home/you/.bashrc                             ef891234    DIRTY
──────────────────────────────────────────────────────────────────────────
  6 files  ·  1 synced  ·  1 dirty
```

**Status labels for source-managed files:**

| Status | Meaning |
|---|---|
| `SOURCE` | File is source-managed; on-disk content matches the committed render |
| `DIRTY` | File is source-managed but has drifted from the committed render (manually edited after apply) |
| `MISSING` | File is source-managed but does not exist on disk yet (needs `sysfig apply`) |
| `PENDING` | A new render has been committed but not yet applied |

---

## 4. Day-to-day maintenance

### Changing a variable

Edit `~/.sysfig/sources.yaml` directly:

```yaml
profiles:
  - source: corp/system-proxy
    variables:
      proxy_url: "http://new-proxy.corp.com:3128"   # ← change this
      bypass_list: "localhost,127.0.0.1,10.0.0.0/8"
```

Or re-run `sysfig source use corp/system-proxy` to be prompted again (existing values are pre-filled as defaults).

Then re-render and apply:

```bash
sysfig source render
sysfig diff        # verify the change is what you expect
sysfig apply
```

### Pulling an updated template from upstream

When the bundle author releases updated profiles:

```bash
# Fetch the latest bundle or git commits
sysfig source pull corp

# Re-render with your existing variables — only changed files get new commits
sysfig source render

# Review what the upstream change did to your rendered files
sysfig diff

# Apply
sysfig apply
```

`sysfig log /etc/environment` shows the full history — every re-render produces a new commit with message `sysfig: generate corp/system-proxy`.

### Drift — what happens when you edit a source-managed file

If you manually edit a source-managed file after `sysfig apply`, `sysfig status` shows `DIRTY`:

```
/etc/environment    DIRTY   ← local edit after apply
```

`sysfig sync` will **not** commit the drift back to the repo — source-managed files are excluded from sync. To fix drift, re-apply the repo version:

```bash
sysfig apply      # discards the drift, restores repo version
```

Or, if the edit represents a real change you want to keep, update the variable in `sources.yaml` and re-render:

```bash
# Edit ~/.sysfig/sources.yaml to update the variable value
sysfig source render
sysfig diff
sysfig apply
```

### Conflict: taking manual ownership from a source profile

If you want to stop using a profile and manage a file manually:

```bash
sysfig track --force /etc/environment
```

`--force` transfers ownership from the source profile to a manual track. The `source_profile` field is cleared in `state.json`. From this point, `sysfig sync` picks up the file normally and `sysfig source render` no longer touches it.

### Conflict: two profiles claiming the same file

If two different source profiles declare the same output file, the second `sysfig source render` will refuse:

```
Error: source render: 1 conflict(s) — re-run with --force to transfer ownership
```

To force one profile to take ownership:

```bash
sysfig source render --force
```

---

## 5. Deploy to a new machine

### Full one-command deploy (from existing machine bundle)

If the new machine is inheriting the same setup as an existing machine:

```bash
sysfig deploy bundle+local:///mnt/corp-nfs/sysfig/machine-A.bundle
```

This clones the machine repo, seeds `state.json`, and applies all tracked files — including source-rendered files that are already committed in the repo. No separate `source render` step is needed.

### Step-by-step bootstrap on a new machine

For a fresh machine that needs different profile variables (e.g. a different proxy bypass list):

```bash
# 1. Initialise sysfig
sysfig init

# 2. Register the source bundle
sysfig source add corp git@github.com:your-org/corp-configs.git

# 3. See what's available
sysfig source list corp

# 4. Set profile variables for this machine
sysfig source use corp/system-proxy
#   bypass_list [localhost,127.0.0.1,::1]: 10.10.0.0/16,localhost
#   proxy_url (required): http://proxy.corp.com:3128

# 5. Render — commits rendered files to track/* branches
sysfig source render

# 6. Review
sysfig diff

# 7. Apply
sysfig apply

# 8. (Optional) push the machine's repo to a remote for backup
sysfig remote set git@github.com:you/machine-A.git
sysfig sync --push
```

### Scripted / unattended bootstrap

For CI or auto-provisioning where no TTY is available:

```bash
sysfig init
sysfig source add corp git@github.com:your-org/corp-configs.git

# All profiles + variables in one file
sysfig source render --values /etc/provision/corp-values.yaml
sysfig apply
```

Or reference environment variables in `sources.yaml` so no prompts are needed at all:

```yaml
profiles:
  - source: corp/system-proxy
    variables:
      proxy_url: "{{env.CORP_PROXY_URL}}"
      bypass_list: "{{env.CORP_PROXY_BYPASS}}"
```

Then set the environment variables in the provisioning script:

```bash
export CORP_PROXY_URL="http://proxy.corp.com:3128"
export CORP_PROXY_BYPASS="10.0.0.0/8,localhost"
sysfig source render
sysfig apply
```

---

## Reference

### Quick reference

```bash
# ── Bundle author ─────────────────────────────────────────────────────────
git add profiles/
git commit -m "update proxy profile"
git bundle create /mnt/nfs/corp-configs.bundle --all
# or just: git push   (for git remote transport)

# ── First-time setup — values file (all profiles at once) ────────────────
sysfig source add  corp git@github.com:your-org/corp-configs.git
sysfig source render --values corp-values.yaml
sysfig diff && sysfig apply

# ── First-time setup — inline flags (one profile at a time) ──────────────
sysfig source add  corp git@github.com:your-org/corp-configs.git
sysfig source list corp
sysfig source use  corp/system-proxy \
  --var proxy_url=http://proxy.corp.com:3128 \
  --var bypass_list=10.0.0.0/8,localhost
sysfig source use  corp/dns-resolvers \
  --var primary_dns=10.0.0.53 \
  --var search_domain=corp.internal
sysfig source render
sysfig diff && sysfig apply

# ── Routine update ────────────────────────────────────────────────────────
sysfig source pull   corp
sysfig source render
sysfig diff && sysfig apply

# ── One specific profile ──────────────────────────────────────────────────
sysfig source render --profile corp/system-proxy

# ── Preview without writing ───────────────────────────────────────────────
sysfig source render --dry-run

# ── Override conflicting file ownership ──────────────────────────────────
sysfig source render --force

# ── Take manual ownership of a source-managed file ───────────────────────
sysfig track --force /etc/environment
```

### Subcommand reference

| Command | Description |
|---|---|
| `sysfig source add <name> <url>` | Register a new source bundle |
| `sysfig source list <name>` | List all profiles in a source (pulls latest first) |
| `sysfig source use <name>/<profile> [--var key=value]...` | Activate a profile; pass variables inline (mutually exclusive with `--values`) |
| `sysfig source use <name>/<profile> --values <file>` | Activate a profile from a flat YAML values file (mutually exclusive with `--var`) |
| `sysfig source render [--values F] [--profile P] [--dry-run] [--force]` | Render profiles; `--values` activates all listed profiles before rendering |
| `sysfig source render [--var key=value]... [--profile P]` | Render with inline variable overrides (`--var` and `--values` are mutually exclusive) |
| `sysfig source pull <name>` | Fetch the latest bundle without rendering |

### `sources.yaml` reference

`~/.sysfig/sources.yaml` is the local config file for source declarations and profile activations. It is **not** committed to the machine repo.

```yaml
# Registered source bundles
sources:
  - name: corp
    url: git@github.com:your-org/corp-configs.git
  - name: community
    url: bundle+ssh://backup@fileserver/srv/sysfig/community.bundle
  - name: local
    url: bundle+local:///mnt/usb/corp-configs.bundle

# Activated profiles with per-machine variable values
profiles:
  - source: corp/system-proxy
    variables:
      proxy_url: "http://proxy.corp.com:3128"
      bypass_list: "localhost,127.0.0.1,10.0.0.0/8"

  - source: corp/dns-resolvers
    variables:
      primary_dns: "10.0.0.53"
      secondary_dns: "10.0.0.54"
      search_domain: "corp.internal"
      dnssec: "allow-downgrade"
      dns_over_tls: "no"

  - source: corp/syslog-forwarder
    variables:
      log_server: "logs.corp.internal"
      log_port: "514"
      log_protocol: "tcp"
      log_facility: "*.warn;auth,authpriv.*"
      # hostname_override omitted — uses the plain-string default from profile.yaml
```

**Notes:**

- Variable values are plain strings. Use `{{env.VAR}}` to reference shell environment variables at render time.
- Variables not listed in a profile's `profile.yaml` are ignored.
- Optional variables not listed here will use the default declared in `profile.yaml`.
- The file is written/updated by `sysfig source use`. You can also edit it directly.

### `profile.yaml` full example

```yaml
name: system-proxy
version: "1.0"
description: "HTTP/HTTPS proxy for /etc/environment, apt, Docker, and systemd"

variables:
  proxy_url:
    required: true
    description: "Full proxy URL including port"
    example: "http://proxy.corp.com:3128"
  bypass_list:
    required: false
    default: "localhost,127.0.0.1,::1"
    description: "Comma-separated hosts that bypass the proxy"

files:
  - dest: /etc/environment
    template: templates/environment.tmpl
    mode: "0644"
    owner: "root"
    group: "root"

  - dest: /etc/apt/apt.conf.d/95proxy
    template: templates/apt-proxy.tmpl
    mode: "0644"
    owner: "root"
    group: "root"

  - dest: /etc/systemd/system/docker.service.d/http-proxy.conf
    template: templates/docker-proxy.tmpl
    mode: "0644"
    owner: "root"
    group: "root"

  - dest: /etc/profile.d/proxy.sh
    template: templates/profile-proxy.tmpl
    mode: "0644"
    owner: "root"
    group: "root"
```
