# Bench environments for `kb`

`kb` was built for and verified in **ffm dev containers**. Other layouts work but need the
adjustments below. Items marked UNVERIFIED have not been exercised on a real bench —
check them on the machine before relying on them, and record what you find.

## 1. ffm dev bench (verified)

- Host: `ffm list` shows benches; `ffm shell <bench>` opens zsh in the `frappe`
  container at `/workspace/frappe-bench` as user `frappe`.
- `kb` defaults match: bench root `/workspace/frappe-bench`, dev server = honcho
  (`bench start`). No `KB_BENCH_ROOT` needed.
- Run non-interactively from the host:
  `ffm shell <bench> --exec "kb license"` / `--exec "kb install --no-input --apps kb_pro"`.
  The bare `kb` menu needs a TTY; through `--exec` use subcommands instead.
  If `ffm` is not found over ssh, use a login shell: `bash -lc "ffm list"`
  (ffm lives in `~/.local/bin`).
- Site: `<bench>.localhost`. Bench files are bind-mounted on the host at
  `~/frappe/<bench>/workspace/frappe-bench/`.
- `~/.config/kb` lives in the container's home, **not** in the bind-mounted workspace.
  `ffm recreate` / `ffm delete` + `create` wipe it: the next `kb activate` creates a new
  identity and consumes a new seat. UNVERIFIED whether `ffm restart --rebuild` keeps it —
  check `ls ~/.config/kb` after a rebuild. Before destroying or rebuilding a licensed
  bench:
  1. note its fingerprint so the seat can be released (server:
     `kbls list-activations --key <key>`);
  2. keep the installation identity: `cp -a ~/.config/kb /workspace/frappe-bench/.kb-config-keep`
     (the workspace survives; the copy holds the license key, so it stays 0700 and is
     deleted afterwards), and after the rebuild
     `mkdir -p ~/.config && cp -a /workspace/frappe-bench/.kb-config-keep ~/.config/kb && rm -rf /workspace/frappe-bench/.kb-config-keep`.
     If `/etc/machine-id` survived too, the fingerprint is unchanged and no seat is
     used; if not, `kb` reports an identity change and its `activation limit reached`
     error names the exact `kbls remove-activation` command. Restoring a bench's own
     identity onto itself is fine; copying it to a different bench is not.
  The `kb` binary (in `/usr/local/bin`) and `config.json` are lost with the container
  too: reinstall with install.sh.
- After `kb` replaces an app directory, the werkzeug reloader inside `bench serve`
  crashes and honcho exits; `kb` samples the dev server before the operation and starts
  it again afterwards (`logs/bench-start.log`). "Dev server was stopped by the update —
  started again." is normal.

## 2. ffm prod bench (UNVERIFIED with kb)

ffm prod runs separate containers: `frappe` (web), `socketio`, `worker-long`,
`worker-short`, `scheduler`, `mariadb`, redis. Implications:

- Run `kb` in the `frappe` container (`ffm shell <bench>` — bash, `/workspace/frappe-bench`).
- `kb` restarts nothing on prod (no honcho). After install/upgrade/init it may print
  "Restart the bench services to apply changes." — or nothing at all if it does not see
  a `gunicorn` process. Either way restart from the host: `ffm restart <bench>`.
- The fork's daily token refresh runs in the **scheduler** container, and the login gate
  runs in the web container. Each container reads `~/.config/kb/license.jwt` from its
  own filesystem unless the home directory is shared. UNVERIFIED for ffm's compose
  layout. Check:
  `docker exec <bench>-scheduler-1 ls -l /home/frappe/.config/kb/` (container names via
  `docker ps`). If the scheduler cannot see the file, the daily refresh silently does
  nothing and only `kb` commands keep the token fresh — schedule `kb license` (e.g.
  weekly cron on the host: `ffm shell <bench> --exec "kb license"`) until this is
  resolved, and report it to the kb_pro_cli maintainers.

## 3. Bare-metal / VM bench (bench CLI + supervisor/systemd, UNVERIFIED with kb)

- Export the bench root for every `kb` invocation:
  `export KB_BENCH_ROOT=/home/frappe/frappe-bench` (add to the bench user's
  `~/.bashrc`). Without it, commands fail with
  `bench directory "/workspace/frappe-bench" is not accessible (set KB_BENCH_ROOT …)` or
  `kb must be run inside a Frappe bench container — use: ffm shell <bench-name>` (the
  check is just "does `$KB_BENCH_ROOT/apps` exist").
- Run `kb` as the bench user (usually `frappe`), never as root: the token is written to
  that user's `~/.config/kb`, which is where gunicorn/workers (same user) read it.
  Running `sudo kb activate` writes to `/root/.config/kb`, and the site then reports
  "No license found at /home/frappe/.config/kb/license.jwt".
- `kb` sees gunicorn and prints "Restart the bench services to apply changes." Do it:
  `sudo supervisorctl restart all` (supervisor setups) or `bench restart`, or the
  systemd units created by `bench setup production`.
- `kb update` replaces its own binary; if it was installed into `/usr/local/bin` by
  root, `kb update` reports "no write permission … try running with sudo".

## 4. Environment variables `kb` honours

| Variable | Effect |
|---|---|
| `KB_LICENSE_SERVER` | License server base URL; also counts as "configured" for `--no-input` runs |
| `KB_BENCH_ROOT` | Absolute bench root (default `/workspace/frappe-bench`) |
| `KB_GITHUB_TOKEN` | Legacy, unused for downloads |
| `NO_COLOR` | Disable colours (same as `--no-color`) |

The fork additionally honours `KB_LICENSE_PATH` (alternative JWT location) and
`KB_LICENSE_SERVER` in the web/scheduler process environment.
