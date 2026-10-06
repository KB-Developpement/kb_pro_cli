---
name: kb-client-troubleshooting
description: >
  Diagnose and fix problems on a KB-Developpement client instance (ffm bench or production
  bench running the KB Frappe fork and the `kb` CLI): `kb` errors, failed installs or
  upgrades, site down after an upgrade, users locked out, license warnings, dev server not
  coming back, `kb` not found, self-update failures, leftover `.kb-old`/`.kb-new`
  directories, broken `apps.json`, slow or hanging operations, and connectivity to the
  license server. Use this skill whenever something is wrong on a KB client machine and
  the cause is not yet known — start here with the symptom, then follow the pointers —
  even if the user only pastes an error or says "kb is broken" / "the site is down after
  the update".
---

# Troubleshooting a KB client instance

Work from evidence: reproduce, read the exact message, then match it below. Companion
skills hold the detail: `kb-client-setup`, `kb-client-apps` (+ its
`references/failure-messages.md`), `kb-client-license`.

## 0. Collect first (2 minutes, saves guessing)

```sh
# where am I, who am I
whoami; echo "$HOME"; echo "${KB_BENCH_ROOT:-/workspace/frappe-bench}"; ls "${KB_BENCH_ROOT:-/workspace/frappe-bench}/apps"
# kb state
command -v kb && kb --version
kb license
tail -40 ~/.config/kb/error.log
ls -la ~/.config/kb/
# bench state
cd "${KB_BENCH_ROOT:-/workspace/frappe-bench}"
bench --site <site> list-apps
grep __version__ apps/frappe/frappe/__init__.py
ls apps | grep -E '\.kb-(old|new)$'
pgrep -af "honcho start|gunicorn" | head
# server reachability
curl -s -m 10 https://license.kbdev.co/health    # expect {"status":"ok"}
```

On ffm hosts wrap commands: `ffm shell <bench> --exec "<cmd>"` (use `bash -lc` over ssh
if `ffm` is not on PATH). Never paste license keys or full tokens anywhere.

## 1. Symptom → cause → fix

### `kb` itself

| Symptom | Likely cause | Fix |
|---|---|---|
| `kb: command not found` | Installed to `~/.local/bin` not on PATH, or not installed in this container | `export PATH="$PATH:$HOME/.local/bin"`; reinstall with install.sh |
| `kb must be run inside a Frappe bench container…` | Not in the container / non-default bench path | `ffm shell <bench>`; or `export KB_BENCH_ROOT=…` |
| `configuration required — run kb init…` in scripts | No config file, no `KB_LICENSE_SERVER` | See `kb-client-setup` step 2 |
| Bare `kb` exits silently or errors under `--exec` | Menu needs a TTY | Use subcommands (`kb install --no-input --apps …`) |
| Output full of spinner fragments | Old `kb` (<0.7.1) in non-TTY | `kb update` |
| `Update available: …` keeps showing | Newer release exists | `kb update` (needs active license) |
| `kb update`: `no write permission … try running with sudo` | Binary owned by root | `sudo kb update` or reinstall into `~/.local/bin` |

### License / login

Go to `kb-client-license` — it maps every `kb` warning and every fork login message to
a fix. Quick triage:

- Only Administrator can log in → the fork's gate refuses: read the message on the login
  page; `kb license` on the bench.
- `kb license` says active but users are still refused → the web process runs as a
  different user/home (check `ps -o user= -p $(pgrep -f gunicorn | head -1)` and that
  user's `~/.config/kb/license.jwt`), or the refusal is a seat limit / uncovered app,
  not the token.
- Works for `kb` but site shows the grace banner → the fork's copy expired: the
  scheduler is off or cannot reach the server (`bench --site <site> scheduler status`;
  `tail logs/kb_license.log`). Run `kb license` to refresh now.

### Install / upgrade

| Symptom | Likely cause | Fix |
|---|---|---|
| `app "X" is not in your license` right after KB added it on the server | `kb` showed the old token's app list | Run the command again (the first run fetched the new token) |
| `site-install … exit status 1` | Dependency app missing (`kb_pro` first) or app needs the fork | Install `kb_pro`, check the fork version; run `bench --site <site> install-app X` to read the traceback |
| `build … exit status 1` | Node/yarn failure, low memory | `bench build --app X` directly; free memory; then `kb site-install --apps X` |
| `license server error for X: upstream_error (HTTP 502)` for every app | Server's GitHub token expired — server-side | Tell the server operator (`kbls-operations`, "GitHub token expired") |
| `version_not_found (HTTP 404)` | Bad `--version`, or the app repo has no GitHub release | Use an existing tag; ask maintainers to publish a release |
| Upgrade error ends "(the previous version was restored)" | Pre-migrate step failed; bench untouched | Fix cause, retry |
| Upgrade error "NOT rolled back; the previous source is kept at …kb-old" | `bench migrate` failed | `kb-client-apps` → "Upgrade — how it fails" |
| Operation hung then `context deadline exceeded` | 10 min (install) / 15 min (upgrade) budget hit — large migrate | Run `bench --site <site> migrate` manually and let it finish |
| Leftover `apps/X.kb-new` | Interrupted extraction | `rm -rf apps/X.kb-new` |
| Leftover `apps/X.kb-old` | Migrate failure or interrupted upgrade | Keep until the site is healthy, then delete |
| Warning `apps.json is not valid JSON — refusing to overwrite` | Corrupt `sites/apps.json` | Repair JSON by hand (valid object, one entry per app) |

### Site down after an operation

1. Dev bench: `pgrep -af "honcho start"`; if absent, `cd $BENCH && bench start > logs/bench-start.log 2>&1 &`
   and read that log. `kb` starts it automatically but prints a warning when that fails.
2. Prod bench: services were not restarted — `ffm restart <bench>` /
   `sudo supervisorctl restart all` / `bench restart`.
3. Python import errors in the web log after a fork upgrade: an app is incompatible with
   the new fork version, or the fork line does not match the bench's Frappe major
   version (v16 fork on a v15 bench). Check
   `grep __version__ apps/frappe/frappe/__init__.py` (1.x = v15 line).
4. `bench migrate` half-applied: see `kb-client-apps`.

### Fork-specific

| Symptom | Fix |
|---|---|
| Menu still shows only "Init KB Frappe" after init | Init failed — rerun `kb init-kb-frappe --force` and read the error |
| `kb init-kb-frappe` says `could not read frappe git remotes … pass --force` | Normal on an already-forked bench (no `.git`); use `--force` to reinstall/upgrade |
| `kb init-kb-frappe` refused `your license does not allow kb_frappe` | Add `kb_frappe` to the client's apps on the server, then `kb license` twice |
| Bench has `apps/frappe` from a git clone of `KB-Developpement/kb_frappe` | Detected as fork; `kb init-kb-frappe` says nothing to do (use `--force` to replace with the release tarball) |

## 2. When to escalate to the server operator

Escalate (with the diagnostic bundle, key's first 8 chars, `client_id` from
`kb license`, and the fingerprint if known) when: every download returns
`upstream_error`; `/health` is not `{"status":"ok"}`; a customer needs a seat released,
apps added, a contract extended, a key unrevoked or a machine unbanned; or `kb` warns
that the server issued a token it cannot verify (key rotation in progress).

## 3. Known gaps (as of kb 0.8.0 / fork 1.0.4, 2026-10-06)

- `kb init-kb-frappe` always takes the latest `kb_frappe` release; it does not
  choose between the v15 (1.x) and v16 (2.x) lines.
- The fork's daily refresh needs the site scheduler; on ffm prod the scheduler runs in
  its own container and may not see `~/.config/kb` (unverified).
- `kb activate <key>` puts the key in shell history; prefer the prompt.
- `http://` license server URLs are accepted without warning — always use https.
