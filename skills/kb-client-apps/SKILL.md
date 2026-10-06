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
   ignored with several apps). Default: the repo's latest GitHub **release**.
3. Per app: extract into `apps/<app>.kb-new` (tar members vetted against path escape and
   links) → rename to `apps/<app>` → append to `sites/apps.txt` → `bench setup
   requirements --python <app>` and `--node <app>` → `pip install -e apps/<app>` (uv,
   pip fallback). A failure here deletes the app dir and its `apps.txt` line.
   (`bench get-app` is avoided because it requires a git repo.)
4. Sequentially: `bench build --app <app>` → write `sites/apps.json` (version from
   `<app>/__init__.py`).
5. `install` only: `bench --site <site> install-app <app>` (10 min each).
6. Dev server restarted / started again; on prod a reminder to restart services.

Install dependencies first: `kb_pro` before apps that require it (kb_compta, kb_cheque,
kb_stock, AchatsExtern…). In one `kb install --apps kb_pro,kb_compta`, downloads run in
parallel but site installs run in the listed order — list `kb_pro` first.

## Upgrade — what actually runs, and how it fails

`kb upgrade --apps kb_pro,kb_compta` processes apps one by one, 15 minutes each:
download latest release → extract to `apps/<app>.kb-new` → **rename the current tree to
`apps/<app>.kb-old`** → swap the new tree in → requirements → `pip install -e` →
`bench build --app` → `bench migrate` (whole bench) → `apps.json` update → delete
`.kb-old`.

- Failure **before migrate** (requirements, pip, build): the previous tree is put back
  and re-registered; the error ends with "(the previous version was restored)". The
  bench is as it was. Read `--verbose` output or `~/.config/kb/error.log`, fix the
  cause, retry.
- Failure **in `bench migrate`**: nothing is rolled back (the schema may be half
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
- Upgrade always fetches the latest release; there is no `--version` on upgrade. To pin
  or downgrade one app that is installed on the site (back up the site first —
  a downgrade does not undo migrations):
  `mv apps/<app> apps/<app>.before-pin` → `kb add --no-input --apps <app> --version <tag>`
  (add only checks the bench directory, so it accepts an app the site already has) →
  `bench --site <site> migrate` → restart services → delete `apps/<app>.before-pin`
  once the site works. Do not use `kb manage` → "Remove from bench" for this: it
  uninstalls the app from the site and deletes its data.
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
upgrade. It always takes the **latest release** of `kb_frappe`. As of 2026-10-06 that
is the 1.x (Frappe v15) line; a published 2.x (v16) release would become "latest" and
must not be pulled onto a v15 bench — check
`https://github.com/KB-Developpement/kb_frappe/releases` (or ask KB staff) before
upgrading the fork if a 2.x release exists.

## Manage (uninstall / remove)

`kb manage` → "Uninstall from site" runs `bench --site <site> uninstall-app <app>`
(asks for confirmation; source stays). "Remove from bench" uninstalls from the site if
needed, then `bench remove-app <app>`. Interactive only. Data of an uninstalled app is
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
ls apps | grep -E '\.kb-(old|new)$'     # leftovers mean an interrupted/failed operation
tail -20 ~/.config/kb/error.log
```

A leftover `apps/<app>.kb-new` is a stale staging dir (safe to delete). A leftover
`apps/<app>.kb-old` is the previous source after a migrate failure — keep it until the
site is confirmed healthy. See `references/failure-messages.md` for exact messages.
