# Exact `kb` app-operation messages and what they mean

Source: kb_pro_cli v0.8.0. Messages are quoted as printed; `<…>` marks variable parts.

## Preconditions

| Message | Cause | Fix |
|---|---|---|
| `kb must be run inside a Frappe bench container — use: ffm shell <bench-name>` | `$KB_BENCH_ROOT/apps` (default `/workspace/frappe-bench/apps`) does not exist | Run inside the container, or `export KB_BENCH_ROOT=/abs/bench` |
| `configuration required — run kb init (or kb for the interactive menu) …` | No `~/.config/kb/config.json` and no `KB_LICENSE_SERVER`, non-interactive run | Write config.json or export `KB_LICENSE_SERVER` |
| `could not detect site name: … set the active site with: bench use <site>` | Several sites, no `default_site` | `bench use <site>` |
| `apps/frappe is still stock Frappe — run: kb init-kb-frappe first …` | Stock-Frappe guard | `kb init-kb-frappe`; or `--skip-frappe-check` if you are sure |
| `license required to download apps — run: kb activate` / `license required to upgrade apps — run: kb activate` | No cached token, or cached token **expired** | `kb license` (refreshes an expired-but-renewable token), run again; else `kb activate` |
| `specify apps with --apps when using --no-input` | Missing `--apps` | Add `--apps` |
| `app "<x>" is not available for upgrade (not licensed, not in bench, or unknown)` | Upgrade target not in bench or license | `kb license`; `ls apps/` |
| `app "<x>" already exists at <dir> — remove it or use upgrade` | `kb add` raced with an existing directory | Use `kb upgrade`, or move the directory aside |

## Download (`license server error for <app>: <code> (HTTP <n>)`)

| Code | HTTP | Meaning | Fix |
|---|---|---|---|
| `missing_token` / `invalid_token` | 401 | No/garbled token, or signed by a key the server no longer uses | `kb activate` |
| `token_expired` | 402 | Cached token past `exp` | `kb license` then retry; `kb activate` if that fails |
| `app_not_licensed` | 403 | App not in the token's `allowed_apps` | Server: add app to client (`kbls edit-client --apps …`), then `kb license` twice |
| `contract_expired` | 402 | Client contract ended | Server: `kbls edit-client --id <c> --end <date>`, then `kb activate` |
| `machine_banned` / `activation_not_found` / `license_revoked` | 403 | Machine standing changed on the server | See `kb-client-license` |
| `client_not_found` | 401 | Client deleted on the server | Recreate client + key, `kb activate <new key>` |
| `version_not_found` | 404 | Tag/branch in `--version` does not exist, or the repo has no GitHub release | Check releases; omit `--version` |
| `upstream_error` | 502 | GitHub refused the server's PAT (expired) or failed | Server side: replace `github_pat` (see `kbls-operations`) |
| `invalid_version` / `invalid_app_name` | 400 | Malformed ref/app name | Fix the argument |
| `rate_limited` | 429 | Too many activate/heartbeat calls from this IP | Wait a minute (only `/activate` and `/heartbeat` are limited) |
| `license server returned HTTP <n> for <app>` | any | Non-JSON answer (proxy page, outage) | Check `curl -s https://license.kbdev.co/health` |
| `download <app>: … dial tcp … / context deadline exceeded` | — | Network/DNS/proxy, or >10 min download | Check egress, retry |

## Bench steps

| Message fragment | Meaning |
|---|---|
| `setup requirements for <app>: exit status 1` | pip/uv or yarn failed; rerun with `--verbose` or run `bench setup requirements --python <app>` by hand to see why. The app dir was removed (install) or restored (upgrade). |
| `pip install -e for <app>: …` | Python packaging failure in the app |
| `build <app>: exit status 1` / `build assets: exit status 1 (the previous version was restored)` | `bench build --app <app>` failed (node/yarn, memory). On install the app stays in the bench but not on the site; fix and run `kb site-install` after `bench build --app <app>` succeeds. |
| `site-install <app> on <site>: exit status 1` | `bench install-app` failed — commonly a missing dependency app (install `kb_pro` first) or the fork missing |
| `bench migrate: … (app files were upgraded and NOT rolled back; the previous source is kept at …kb-old)` | See SKILL.md "Upgrade — how it fails" |
| `ROLLBACK FAILED — previous version left at <dir>` | Restore the directory manually |
| `apps.json is not valid JSON — refusing to overwrite` (warning) | `sites/apps.json` is corrupt; fix the JSON by hand. The operation itself succeeded. |
| `could not update apps.json for <app>` (warning) | Metadata only; app works. |
| `could not detect installed apps — all apps will be shown` (warning) | `bench --site <site> list-apps --format json` failed; check bench health |

## Summary line

`kb` prints a per-app ✓/✗ summary and exits non-zero when any app failed. Every error is
also appended with a timestamp to `~/.config/kb/error.log`.
