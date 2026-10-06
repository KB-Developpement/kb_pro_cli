---
name: kb-client-license
description: >
  Everything about KB-Developpement licensing on a client bench: what `kb license` /
  `kb activate` show and do, token lifetime and refresh (21-day JWT, 24 h background
  refresh, daily refresh by the KB Frappe fork), the fork's login gate (users locked out,
  "No license found", "Your license does not cover", grace banner, past-grace lockout,
  user and session seat limits), revocation, bans, contract expiry, activation seat
  limits, installation identity changes, re-activation after a container rebuild, and
  local deactivation. Use this skill whenever a KB site refuses logins, shows a license
  banner or error, `kb` prints a license warning, a customer needs re-activating or
  moving to a new machine, or someone asks how KB licensing works on the client —
  even if they only say "users can't log in" or "the license expired".
---

# KB licensing on the client

Audience: KB staff. You may need server-side actions; those are in the
`kbls-customer-admin` skill (kb_pro_license_server repo). This skill explains the client
half and tells you when to go to the server.

## The model in one screen

- The **license server** signs a JWT (Ed25519) valid **21 days**. Claims: `client_id`,
  `allowed_apps`, `fingerprint`, `tier`, `max_users`, `max_active_users`, `exp`.
- `kb activate` exchanges the key + this installation's fingerprint for a token and
  writes `~/.config/kb/license.json` (token + `last_check`) and `license.jwt` (raw
  token).
- **Refreshing** = `POST /heartbeat` with the current token, even an expired one. The
  server re-checks contract, ban, activation row and key revocation, then issues a fresh
  21-day token. Refresh happens:
  1. in the background on most `kb` commands when `last_check` is older than 24 h;
  2. blocking (5 s) before `kb install/add/upgrade/update`, and inside `kb license`;
  3. **daily via the fork's scheduler job** (`frappe.kb_license.heartbeat`, per site),
     which rewrites only `license.jwt`.
- The **fork** (KB Frappe, `apps/frappe/frappe/kb_license/`) verifies `license.jwt`
  offline with its embedded public key on every login and every `/app`, `/desk`,
  `/api/` request (verdict cached 60 s per process, invalidated when the file changes).
- Server is unreachable → nothing is deleted; the token keeps working until `exp`, then
  the fork grants **14 days of grace** with a banner, then blocks. So a site survives
  ~35 days without contact. `kb` itself has no grace: after `exp`, app downloads stop
  until a refresh succeeds.
- Server answers with a terminal error (`license_revoked`, `machine_banned`,
  `contract_expired`, `activation_not_found`, `fingerprint_mismatch`) → `kb` deletes
  `license.json` + `license.jwt`; the fork deletes `license.jwt`. **The site then blocks
  non-Administrator logins immediately** (next request).

Administrator is never blocked and never counts as a seat, so a locked site is always
recoverable by Administrator. `site_config.json` key `kb_license_exempt: 1` disables the
gate on that site entirely — for KB-internal sites only; never set it on a customer site.

## Reading `kb license`

| Output | Meaning | Action |
|---|---|---|
| `License active` + client/tier/date/apps | Token verifies, not expired. "Token valid until" is the token's `exp`, **not** the contract end | None |
| `License token expired.` + date | Cached token past `exp` and the refresh in this run did not replace it in memory | Run `kb license` again: the refresh in the first run saves the new token, the second run displays it. Still expired → server unreachable or rejecting; see below |
| `No license found.` | No/invalid cache, or a refresh just got a terminal error (a `warning:` line above says which) | Follow the warning; usually `kb activate` |
| `warning: the license server issued a token this version cannot verify — run: kb update` | Server signing key rotated ahead of this `kb` build | `kb update`; the old token is kept until then |

`kb license` contacts the server; it is the canonical "refresh now" command.

## Warnings and their fixes

| `kb` warning / fork message | Cause | Fix |
|---|---|---|
| `license has been revoked — contact KB-Developpement` | Key revoked on server | Server: `kbls unrevoke-key --key …` if wrong, or issue a new key; client: `kb activate <new key>` |
| `this machine has been banned — contact KB-Developpement` | Fingerprint on the ban list | Server: `kbls unban-machine --fingerprint …` if wrong; then `kb activate` |
| `support contract has expired — contact KB-Developpement to renew` | `contract_end` passed (midnight UTC of the end date) | Server: `kbls edit-client --id … --end YYYY-MM-DD`; client: `kb activate` (key is saved, no argument needed) |
| `activation not found on server — run: kb activate` | Activation row removed, key deleted, or client deleted | `kb activate` (uses a seat); if the key/client was deleted, a new key is needed |
| `machine fingerprint changed — run: kb activate` | Token bound to another identity (copied `~/.config/kb`, `/etc/machine-id` or CPU changed, `machine-id` file lost) | `kb activate`; release the old seat on the server if the key is full |
| `this machine's identity changed (kb now identifies each installation separately) — run: kb activate` | First run after upgrading from kb ≤0.7.x | `kb activate`; on `activation limit reached` follow the printed `kbls remove-activation` command |
| `activation limit reached — contact KB-Developpement to add more machines` (+ hint with `kbls remove-activation --key … --fingerprint …`) | Key's `max_activations` used by other identities | Server: release the stale one (`kbls list-activations --key …`, `kbls remove-activation …`) or `kbls edit-key --max-activations N` |
| `invalid license key` | Key unknown (typo, deleted) | Check the key; server `kbls list-keys --client …` |
| `activation denied: <code>` / `license server returned HTTP <n>` | Unexpected server answer (e.g. 429 `rate_limited`, 5xx, proxy page) | Retry after a minute; `curl -s https://license.kbdev.co/health` |

Fork messages at login (title "KB License"):

| Message | Cause | Fix |
|---|---|---|
| `No license found at <path>. Run \`kb activate\` on this server…` | No `license.jwt` for the OS user running the web process | Activate as that user; check `<path>` is the home of the bench user; check a terminal heartbeat did not delete it (see `kb license`) |
| `Could not read the license at <path>.` | Permissions | `license.jwt` must be readable by the bench user (0600, owned by it) |
| `License signature verification failed — …` | Token signed by a key the fork does not embed (key rotation without a fork release) or edited file | `kb init-kb-frappe --force` to get a fork with the current key; then `kb license` |
| `The license file is not a valid token (…)` / `… is incomplete` | Corrupt file | `kb activate` |
| `The license signing key is unusable. This installation of KB is damaged.` | `apps/frappe/frappe/kb_license/license_signing.pub` missing/corrupt | `kb init-kb-frappe --force` |
| `Your license does not cover: <apps>. Contact KB-Developpement to update it.` | An installed KB app (or `kb_frappe` itself) is missing from `allowed_apps` | Server: add the apps to the client; client: `kb license` (refresh). Or uninstall the app |
| Banner `Your KB license expired N day(s) ago. M day(s) of grace remain…` | Token expired, no refresh succeeded | Find why refresh fails: `kb license` output, scheduler enabled?, server reachable? |
| `Your license expired more than N days ago. Renew it to continue using KB.` | Past 14-day grace | Same, then `kb activate` if the cache was deleted |
| `User limit reached. Your license allows N System User(s) but M are enabled…` | `max_users` < enabled System Users (Administrator excluded) | Disable users (always allowed) or raise `--max-users` on the server, then refresh |
| `Too many active sessions (A/N)…` | Distinct users with live sessions ≥ `max_active_users` | Wait/log users out, or raise `--max-active-users` |

Seat rules (fork): a named seat = enabled user with `user_type = System User`, excluding
Administrator; Website Users are free. Creating, enabling or promoting a user beyond
`max_users` is refused at save; editing or disabling existing users is always allowed.
Concurrent seats count distinct users with unexpired sessions. 0 = unlimited.

## Server-side commands you will ask the operator for

Run on the license server as user `frappe` (details: `kbls-customer-admin` skill in the
kb_pro_license_server repo):

| Need | Command |
|---|---|
| See the client's apps, contract, limits | `kbls list-clients` |
| Full keys of a client | `kbls list-keys --client <client_id>` |
| Seats of a key | `kbls list-activations --key <key>` → columns CLIENT, KEY (SHORT), FINGERPRINT, LAST SEEN, ACTIVATED |
| Free a seat | `kbls remove-activation --key <key> --fingerprint <fp>` |
| More seats | `kbls edit-key --key <key> --max-activations N` |
| Add apps / extend contract / seat limits | `kbls edit-client --id <id> --apps … / --end YYYY-MM-DD / --max-users N --max-active-users N` |
| Undo a revoke / ban | `kbls unrevoke-key --key …` / `kbls unban-machine --fingerprint …` |

Spotting the stale seat: a live bench refreshes at least daily (fork scheduler) and on
every `kb` command after 24 h, so its LAST SEEN is recent. A dead installation's LAST
SEEN stops at the day it was destroyed. To read a bench's own fingerprint, decode its
token payload (not a secret by itself, but do not paste the whole token):
`cut -d. -f2 ~/.config/kb/license.jwt | tr '_-' '/+' | base64 -d 2>/dev/null | grep -o '"fingerprint":"[0-9a-f]*"'`.
Any row of that key with a different fingerprint and an old LAST SEEN is a candidate.

## Common procedures

**After a server-side change (apps added, contract extended, limits raised)**: the
client sees it only after a refresh. `kb license` twice, or wait for the daily
scheduler job. If the cache was already deleted by a terminal error (expired contract,
revoked key), refresh cannot work — run `kb activate`.

**Moving to a new machine / rebuilding a container**: the new installation is a new
identity and needs a free seat. Before destroying the old bench get its fingerprint
(server: `kbls list-activations --key <key>`), then after `kb activate` on the new one
release the old seat: `kbls remove-activation --key <key> --fingerprint <old>`.

**`license.jwt` missing but `license.json` still there** (file deleted by hand, home
restored partially): `kb license` refreshes and rewrites both files — `kb license` can
report "License active" from `license.json` while the site is locked, so always run it
rather than only reading it. If the refresh gets a terminal error, both files go and
the warning names the cause.

**What gets logged where**: a terminal answer during a *background* refresh only prints
a `warning:` line on that command's stderr; it is not written to `~/.config/kb/error.log`.
Errors returned by commands (including a blocking refresh that refuses `kb install`)
are logged there. The fork logs its own deletions in `logs/kb_license.log`
("License heartbeat rejected by server (<code>). Removing license file."). The server's
answer for a given seat is reproducible: ask the operator to check the client, key,
activation and ban list.

**Deactivate locally**: menu `kb` → License → "Deactivate locally" deletes
`license.json`, `license.jwt`, `license_key` and `previous-fingerprint` (keeps
`machine-id`, so re-activating reuses the same seat). It does not tell the server — the
seat stays used until removed there. There is no `kb deactivate` command. The site
starts refusing logins as soon as `license.jwt` is gone.

**Activate a scripted / CI bench**: `kb activate <key>` (argument), with
`KB_LICENSE_SERVER` or `config.json` present.

## What is enforced where (know the limits)

- The fingerprint binding is checked by `kb` only (it refuses a cached token whose
  fingerprint differs from this installation's). The fork does not compute the
  fingerprint; it trusts any validly signed `license.jwt`, and its daily refresh echoes
  the token's own fingerprint. Do not tell customers that copying license files "won't
  work"; tell them it is not permitted. Detection is the server's activation list.
- Offline: `kb` keeps working for app commands until `exp`; the site works until
  `exp` + 14 days.
- Revocation/ban reach a client at its next refresh (≤24 h for an active `kb` user,
  ≤1 day via the fork's scheduler), and immediately for any download.

## Diagnostic bundle (collect before escalating)

```sh
kb --version; kb license
ls -la ~/.config/kb/                 # owner, modes, file dates
tail -30 ~/.config/kb/error.log
grep __version__ apps/frappe/frappe/__init__.py
bench --site <site> list-apps
bench --site <site> show-config | grep -i kb_license   # exemption flag?
tail -20 logs/kb_license.log          # fork refresh log (bench root)
curl -s -m 10 https://license.kbdev.co/health
```

Fork log lines (`logs/kb_license.log` in the bench root): `License heartbeat
network error`, `License heartbeat rejected by server (<code>). Removing license file.`,
`License heartbeat: refreshed token is not verifiable here …`, `License heartbeat
unexpected response: HTTP <n>`. Never paste the license key or full JWT into tickets;
the first 8 characters identify a key.
