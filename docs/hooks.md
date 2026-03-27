# Run hooks after apply

Hooks let you validate or reload services automatically after `sysfig apply` writes a config file.

Create `~/.sysfig/hooks.yaml` (never committed to the repo):

```yaml
# binaries allowed in exec hooks — full absolute paths required
allowlist:
  - /usr/sbin/nginx
  - /usr/sbin/sshd
  - /usr/sbin/apachectl
  - /usr/sbin/haproxy

hooks:
  nginx_validate:
    on: [etc_nginx_nginx_conf]     # file ID from 'sysfig status'
    type: exec
    cmd: [/usr/sbin/nginx, -t]     # runs: nginx -t

  nginx_reload:
    on: [etc_nginx_nginx_conf]
    type: systemd_reload
    service: nginx

  sshd_validate:
    on: [etc_ssh_sshd_config]
    type: exec
    cmd: [/usr/sbin/sshd, -t]
```

> **Note:** `allowlist` entries and `cmd` binaries must be **full absolute paths** (e.g. `/usr/sbin/nginx`, not `nginx`). sysfig resolves the binary and compares its absolute path — a binary at `/tmp/nginx` will be rejected even if `nginx` is listed.

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

## Profile hooks (`source render`)

Config Source profiles can declare their own `post_apply` hooks in `profile.yaml`. These fire automatically when `sysfig source render` commits new content — no local `hooks.yaml` needed on each machine.

```yaml
# profile.yaml (inside the source bundle)
hooks:
  post_apply:
    - systemd_reload: rsyslog.service
    - exec: "nginx -t"
```

**Differences from `hooks.yaml`:**

| | `hooks.yaml` | `profile.yaml` post_apply |
|---|---|---|
| Scope | fires after `sysfig apply` | fires after `sysfig source render` |
| Location | `~/.sysfig/hooks.yaml` (local, never pushed) | inside the source bundle (shared) |
| On failure | fatal — apply exits with code 1 | non-fatal — warning printed, render succeeds |
| `exec` format | `cmd: [binary, arg1]` (list) | `exec: "binary arg1"` (string) |
| Allowlist | required | not required |
