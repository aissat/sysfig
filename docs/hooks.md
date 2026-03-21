# Run hooks after apply

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
