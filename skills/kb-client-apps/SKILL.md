---
name: kb-client-apps
description: >
  Day-to-day KB app management on a client bench with the `kb` CLI: install, add,
  site-install, upgrade, uninstall and remove KB-Developpement Frappe apps (kb_pro,
  kb_compta, kb_cheque, kb_facilite, kb_print, kb_stock, HR2025, kb_distri,
  kb_commercial, AchatsExtern), upgrade the KB Frappe fork, pin an app version, recover
  from a failed upgrade (`.kb-old` rollback, `bench migrate` failures), handle the dev
  server and production restarts, and self-update `kb`. Use this skill whenever someone
  wants to install, update, upgrade, roll back, remove or pin a KB app on a bench, asks
  what `kb install`/`kb upgrade`/`kb add`/`kb site-install`/`kb manage`/`kb update` do, or
  reports a failed KB app install or upgrade — even if they just say "update the apps on
  the client" or "kb_compta is outdated".
---

# KB app operations with `kb`

Assumes the bench is already set up and licensed (`kb-client-setup`). For license
errors see `kb-client-license`; for symptoms without an obvious cause see
`kb-client-troubleshooting`.

## Ground rules

- Run `kb` inside the bench environment as the bench user (`ffm shell <bench>`; or
  `KB_BENCH_ROOT=…` on non-ffm benches).
- Every download goes through the license server with the cached token
  (`GET /download/<app>`); the server re-checks the license on every download. There is
  no git clone and no client-side GitHub token. App source lands without `.git`.
- The app list is fixed in `kb` (registry): `kb_pro, kb_compta, kb_cheque, kb_facilite,
  kb_print, kb_stock` (standard tier) and `HR2025, kb_distri, kb_commercial,
  AchatsExtern` (full tier). What a customer may install is the **`allowed_apps` claim
  in their token**, shown by `kb license`. Other apps (e.g. kb_ai, kb_kbdev) are not
  managed by `kb`.
- `kb_frappe` never appears in these pickers; the fork is managed only with
  `kb init-kb-frappe` (see "Upgrading the fork").
- `install`, `add`, `site-install` and `upgrade` refuse to run while `apps/frappe` is
  still stock Frappe ("apps/frappe is still stock Frappe — run: kb init-kb-frappe first").
  `--skip-frappe-check` overrides — only for a bench you know is fine.
- Bench mutations (`add`, `install`, `site-install`, `upgrade`, `manage`, `init-kb-frappe`,
  `adopt`) take a bench-wide lock (`.kb/lock`): a second one fails at once and names
  the holder's PID. They also refuse when run as a user that does not own the bench root
  (e.g. root) — run as the bench user.
- Any app directory that holds a `.git` (clone or linked-worktree file) is **never**
  replaced or deleted by `kb`; `upgrade`, `add`, `install` and `manage` removal refuse
  the whole selection before touching anything. Git clones are managed with git.
  (`init-kb-frappe` makes one exception: a clean stock `frappe/frappe` checkout.)
- Every install/add/upgrade writes a receipt `.kb/apps/<dir>.json` (repo, tag, commit,
  archive SHA-256) and an unfinished operation is tracked in `.kb/journal.json`.
  `kb status` (read-only, no network) shows both, plus retained copies.
- `--no-input` requires `--apps`. Global flags: `--quiet/-q`, `--verbose` (prints bench
  output on success), `--no-color`. A command where any app failed exits non-zero.
- `install`, `add`, `upgrade` and `update` first do a blocking license refresh (5 s
  timeout). A server rejection stops the command and clears the local token; a network
  failure is ignored and the cached token is used.

## Choosing the command

| Goal | Command |
|---|---|
| Download + build + install on the site | `kb install --apps a,b` (alias `kb i`) |
| Put source in the bench only (multi-site benches, stage now/install later) | `kb add --apps a,b` |
| Install something already in the bench onto the site | `kb site-install --apps a` |
| Newest release of apps already in the bench | `kb upgrade --apps a,b` (alias `kb up`) |
| Uninstall from the site / delete from the bench | `kb manage` (interactive only; `-f` passes `--force` to `bench uninstall-app`) |
| Update the `kb` binary | `kb update` (`--check` to only look) |

`kb` with no arguments opens the same actions as a menu (TTY only).

## Install / add — what actually runs

1. Plan: each requested app must be a known KB app, in the license, and not already
   installed on the site. Otherwise:
   `app "X" is not a KB app` / `app "X" is not in your license` /
   `app "X" is already installed on this site — use: kb upgrade to update it`.
   An app already present in the bench but not installed on the site skips the
   download and goes straight to `bench install-app` (picker shows "already
   downloaded — will install on site").
2. Download (up to 3 in parallel, 10 min each) to a temp file. With exactly one app you
   may pin a ref: `kb install --apps kb_pro --version v1.0.14` (tag, branch or commit;
   refused with several apps). Default: the newest GitHub **release** on the app's
   release line (`major=1`).
3. Then, one app at a time **in command-line order**, each in its own journal
   transaction: extract into `apps/<app>.kb-new` (tar members vetted against path escape
   and links; the gzip trailer must verify, so a truncated download fails) → rename to
   `apps/<app>` → append to `sites/apps.txt` → `bench setup requirements --python
   <app>` and `--node <app>` → `pip install -e apps/<app>` (uv, pip fallback) →
   `bench build --app <app>`. A failure here deletes the app dir and its `apps.txt`
   line. (`bench get-app` is avoided because it requires a git repo.)
4. Finalize: write `.kb/apps/<app>.json` and the `sites/apps.json` entry. If that fails
   the journal stays pending and the **next** mutating command replays it first; if the
   replay fails too, every mutating command refuses and names `.kb/journal.json`. If an
   app is left pending, the remaining apps are not attempted.
5. `install` only: `bench --site <site> install-app <app>` (10 min each).
6. Dev server restarted / started again; on prod a reminder to restart services.

Install dependencies first: `kb_pro` before apps that require it (kb_compta, kb_cheque,
kb_stock, AchatsExtern…). In one `kb install --apps kb_pro,kb_compta`, downloads run in
parallel but site installs run in the listed order — list `kb_pro` first.

## Upgrade — what actually runs, and how it fails

`kb upgrade --apps kb_pro,kb_compta` processes apps one by one, 15 minutes each:
download the newest release on the app's line → extract to `apps/<app>.kb-new` →
**rename the current tree to `apps/<app>.kb-old`** → swap the new tree in →
requirements → `pip install -e` → `bench build --app` → `bench migrate` (whole bench) →
receipt + `apps.json` → retire `.kb-old`: deleted if the app had a valid receipt, else
moved to `.kb/recovery/<app>-pre-receipt-<UTC>` (it may hold hand edits; `kb status`
lists it with its size; deleting it is your decision). A leftover `.kb-old`/`.kb-new`
from an earlier run is moved to `.kb/recovery/legacy/` before the swap, never deleted.

- Failure **before migrate** (requirements, pip, build): the previous tree is put back
  and re-registered; the error ends with "(the previous version was restored)". The
  bench is as it was. Read `--verbose` output or `~/.config/kb/error.log`, fix the
  cause, retry.
- Failure **in `bench migrate`**: the journal stays at step `swapped` and every later
  mutating command refuses, printing the old and new source paths, until you resolve it
  by hand and delete `.kb/journal.json`. Nothing is rolled back (the schema may be half
  migrated). The error says "app files were upgraded and NOT rolled back; the previous
  source is kept at apps/<app>.kb-old". On production, first stop users from writing
  into a half-migrated site: `bench --site <site> set-maintenance-mode on` (and
  `bench --site <site> scheduler pause`); undo both when done. Backups made by
  `bench backup` live in `sites/<site>/private/backups/` (`ls -lt` to find the one
  taken before the upgrade; `bench --site <site> restore <file.sql.gz>
  --with-public-files <files.tar> --with-private-files <private-files.tar>` restores it).
  Recover:
  1. Read the migrate error (`bench --site <site> migrate` again shows it).
  2. Usually fix forward: resolve the data/patch problem and re-run `bench migrate`.
     When it succeeds, `rm -rf apps/<app>.kb-old`.
  3. To go back instead: restore a site backup taken before the upgrade, then
     `mv apps/<app> apps/<app>.failed && mv apps/<app>.kb-old apps/<app> &&
     ./env/bin/python -m pip install -e apps/<app> && bench build --app <app> &&
     bench --site <site> migrate`. Code rollback without a DB restore is only safe if the
     new migration did not run.
  4. "ROLLBACK FAILED — previous version left at …" means the automatic restore could
     not rename; move the directory back by hand as above.
- `kb upgrade --to <app>=<tag>` (repeatable) deploys exactly that **published release**
  (the server must confirm it is not a draft, prerelease, branch or bare commit); with
  `--to` and no `--apps`, only the `--to` apps upgrade. It keeps the same guards and
  retention as any upgrade, and a downgrade does not undo migrations (back up first).
  Do not use `kb manage` → "Remove from bench" to re-pin: it uninstalls the app from
  the site and deletes its data.
- Upgrades need roughly twice the app's disk space while running.
- Take a site backup before upgrading production: `bench --site <site> backup
  --with-files`.

## Upgrading the fork (`kb_frappe`)

There is no `kb upgrade frappe`. Re-run the init with force:

```sh
bench --site <site> backup          # production: back up first
kb init-kb-frappe --force --no-input
grep __version__ apps/frappe/frappe/__init__.py
```

It uses the same `.kb-old` swap, rollback-before-migrate rule and dev-server handling as
upgrade. It takes the newest `kb_frappe` release on **line 1** (1.x, Frappe v15); the
line is pinned, so a published 2.x (v16) release does not become "latest" for it. An
`apps/frappe` that holds a `.git` is refused unless it is a clean stock checkout.

## Manage (uninstall / remove)

`kb manage` → "Uninstall from site" runs `bench --site <site> uninstall-app <app>`
(asks for confirmation; source stays). "Remove from bench" uninstalls from the site if
needed, then `bench remove-app <app>` (an app directory holding a `.git` is refused
before the first uninstall, for the whole selection). Interactive only. Data of an uninstalled app is
deleted by Frappe — back up first. After uninstalling a licensed app the fork stops
requiring it in the license.

## Dev server and production restarts

`kb` checks for `honcho start` before touching `apps/`:
- was running and still running → restarted ("Dev server restarted.");
- was running and died during the swap → started again with `bench start` detached,
  output in `logs/bench-start.log`;
- not running but `gunicorn` is → "Restart the bench services to apply changes." —
  do it (`ffm restart <bench>`, `sudo supervisorctl restart all`, or `bench restart`);
- nothing running → silent.

## Self-update of `kb`

- Background check (most commands) caches the latest release for 24 h in
  `~/.config/kb/.update_check.json` and prints
  `Update available: vX → vY  (run: kb update)`.
- `kb update` requires an active license, downloads the release archive and
  `checksums.txt` from GitHub, verifies SHA-256, extracts only a regular file named `kb`,
  and replaces the running binary. `--yes` / `--no-input` skip the prompt;
  `--check` needs no license.
- Errors: "no write permission to … — try running with sudo" (binary installed by
  root), "checksum mismatch … refusing to install", "release has no checksums.txt asset".

## Verification after any operation

```sh
bench --site <site> list-apps          # installed apps and versions
cat sites/apps.json | head -40          # versions kb recorded
kb status                               # provenance, journal step, retained copies (read-only)
tail -20 ~/.config/kb/error.log
```

A `apps/<app>.kb-old` is the previous source after a migrate failure — keep it until the
site is confirmed healthy. Old `.kb-old`/`.kb-new` leftovers are moved (not deleted) to
`.kb/recovery/legacy/` by the next swap and listed by `kb status`.

`kb adopt` gives an archive bench installed before receipts existed its first receipt
(`kb adopt --check` compares only; `--tag <app>=<tag>` names the release when the
version hint is wrong). It never changes `apps/`, sites or the database; exit codes
0 adopted, 2 mismatch, 3 ambiguous, 4 refused. See `references/failure-messages.md` for exact messages.
