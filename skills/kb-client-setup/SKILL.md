---
name: kb-client-setup
description: >
  Set up a KB-Developpement client bench with the `kb` CLI (kb_pro_cli): install the
  `kb` binary, configure the license server, activate a license key, replace stock
  Frappe with the KB Frappe fork (`kb init-kb-frappe`), and install the first KB apps
  (kb_pro, kb_compta, …) on a site. Use this skill whenever someone is preparing a new
  customer instance, a fresh ffm bench, a production bench or a recreated container for KB
  apps, mentions `install.sh`, `kb init`, `kb activate`, "Init KB Frappe", "first-time
  setup", or asks how to get a KB site from zero to working — even if they only say
  "set up kb on this server" or "onboard a new client machine".
---

# KB client setup — from empty bench to licensed KB site

Audience: KB-Developpement staff working on a customer's machine (or a KB-internal
bench). The goal is a Frappe bench running the **KB Frappe fork** with licensed KB apps
installed on its site.

Companion skills: `kb-client-apps` (installing/upgrading after setup),
`kb-client-license` (license states, refreshes, the runtime gate),
`kb-client-troubleshooting` (symptom → fix). On the server side, `kbls-customer-admin`
in the `kb_pro_license_server` repo creates the client and key you need here.

## How the pieces fit (read once)

Three components, all must agree:

| Component | Where | What it does |
|---|---|---|
| `kbls` license server | `https://license.kbdev.co` (KB's VPS) | Issues Ed25519-signed JWTs, tracks activations, proxies app tarballs from private GitHub repos |
| `kb` CLI | inside the bench container / on the bench host | Activates, downloads and installs apps, refreshes the token |
| KB Frappe fork (`kb_frappe`, installed as `apps/frappe`) | the bench | Reads `~/.config/kb/license.jwt` and **blocks non-Administrator login** without a valid license |

Consequence: a site running the fork with no valid `license.jwt` refuses every user
except Administrator. Activate **before** handing the site over, and activate as the
same OS user that runs the bench processes (the fork reads `Path.home()`).

## Before you start — what you need

1. **A license key** (64 hex chars) for this customer. KB staff create it on the server:
   `kbls create-client …` then `kbls create-key --client <id> --max-activations N`
   (see `kbls-customer-admin`). The client's `allowed_apps` must include `kb_frappe`
   plus every app you plan to install — the fork refuses login if an installed KB app
   is missing from the license.
2. **A bench** with a site. `kb` expects the bench root at `/workspace/frappe-bench`
   (ffm containers). Anywhere else, export `KB_BENCH_ROOT=/abs/path/to/frappe-bench`
   in every shell that runs `kb`. See `references/environments.md` for ffm dev, ffm prod
   and bare-metal benches.
3. **Frappe v15 bench for the current fork line.** The live fork is `kb_frappe` 1.x
   (Frappe v15). A 2.x (Frappe v16) line exists as a tag but is not released; as of
   2026-10-06, `kb init-kb-frappe` always fetches the latest GitHub release, so a v16
   bench would receive the v15 fork. Do not run setup on a v16 bench until line
   selection ships.
4. Outbound HTTPS from the bench to `license.kbdev.co` and `api.github.com` /
   `github.com` (the installer and `kb update` use GitHub releases).

## Step 1 — install `kb`

Inside the bench environment (for ffm: `ffm shell <bench>`), as the bench user:

```sh
curl -fsSL https://raw.githubusercontent.com/KB-Developpement/kb_pro_cli/main/install.sh | sh
kb --version          # e.g. kb 0.8.0 (commit 3db9dbb…, built …)
```

The installer picks the newest GitHub release for the OS/arch, **verifies its SHA-256
against `checksums.txt` and refuses to install without `sha256sum`/`shasum`**, then
installs to `/usr/local/bin` if writable, else `~/.local/bin` (it prints a PATH hint if
that directory is not on PATH). Supported: linux/darwin × amd64/arm64.

If the installer fails: "no sha256 tool found" → install coreutils; "Could not determine
latest release version" → GitHub API unreachable or rate-limited (retry, check proxy).

## Step 2 — configure (usually nothing to do)

The only setting that matters is the license server URL. Precedence:
`KB_LICENSE_SERVER` env → `license_server_url` in `~/.config/kb/config.json` →
built-in `https://license.kbdev.co`.

- Interactive: `kb init` (or `kb config`) — a form with the server URL and an optional,
  **unused** GitHub token field (legacy; downloads go through the license server).
- Non-interactive (CI, `ffm shell --exec`, scripts): bench commands refuse to run until
  "configured", which means `~/.config/kb/config.json` exists **or** `KB_LICENSE_SERVER`
  is set. Either export the variable or write the file:

```sh
mkdir -p ~/.config/kb && chmod 700 ~/.config/kb
printf '{\n  "license_server_url": "https://license.kbdev.co"\n}\n' > ~/.config/kb/config.json
chmod 600 ~/.config/kb/config.json
```

`kb init` / `kb config` need a TTY and reject `--no-input`.

## Step 3 — activate

```sh
kb activate <license-key>     # or run `kb activate` and paste it at the masked prompt
kb license                    # confirm: "License active", client, tier, token date, apps
```

What happens: `kb` computes this installation's fingerprint
(`sha256(<random id in ~/.config/kb/machine-id> | /etc/machine-id | CPU model)`),
POSTs key + fingerprint to `/activate`, and stores the 21-day token in
`~/.config/kb/license.json` and the raw JWT in `~/.config/kb/license.jwt` (the file the
fork reads). The key is saved to `~/.config/kb/license_key`, so later `kb activate`
needs no argument.

Prefer the masked prompt over passing the key on the command line when other people
share the machine: an argument lands in shell history and is visible in `ps`.

Each installation (each `~/.config/kb/machine-id`) consumes **one activation seat** on
the key. Recreating a container or deleting `~/.config/kb` creates a new identity and a
new seat. If activation fails with `activation limit reached`, see `kb-client-license`
("Seat limit").

Check `kb license` shows `kb_frappe` and the apps you intend to install under "Apps".
If not, fix the client on the server first (`kbls edit-client --apps …`), then run
`kb license` twice (the first run fetches the new token, the second displays it).

## Step 4 — replace stock Frappe with the KB fork

```sh
kb init-kb-frappe                      # refuses unless apps/frappe is stock frappe/frappe
kb init-kb-frappe --force --no-input   # scripted; also the way to re-run / upgrade the fork
```

Or from the menu (`kb` with no arguments): on a stock bench the menu shows only
**Init KB Frappe / License / Settings**.

What it does: checks the license allows `kb_frappe`, downloads the fork tarball through
the license server, extracts it to `apps/frappe.kb-new`, renames the old tree to
`apps/frappe.kb-old`, swaps in the new one, then runs `bench setup requirements
--python/--node frappe`, `pip install -e apps/frappe` (uv, pip fallback), `bench build
--app frappe`, `bench migrate`, and writes `sites/apps.json`. Any failure **before
migrate** restores the previous `apps/frappe` automatically. A migrate failure keeps the
new code and leaves `apps/frappe.kb-old` for manual recovery (see `kb-client-apps`).
It has no timeout; a first run takes ~2 minutes on a small bench and can exceed 15 on
a large one. Expect the dev server to restart.

Detection rules worth knowing:
- Stock is detected from `git remote -v` in `apps/frappe` containing `frappe/frappe`.
- After the swap, `apps/frappe` has **no `.git`** (it came from a tarball). Origin
  detection then errors, which is treated as "not stock": the full menu appears and app
  commands work. `kb init-kb-frappe` without `--force` on such a bench stops with
  "could not read frappe git remotes … pass --force".
- Verify: `grep __version__ apps/frappe/frappe/__init__.py` → `1.0.x` for the v15 fork.

## Step 5 — install apps on the site

```sh
kb install --apps kb_pro,kb_compta            # or `kb install` for a picker
kb install --no-input --apps kb_pro           # scripted
```

Install order matters. `kb_pro` is the ERPNext replacement the others build on:

| App | Requires |
|---|---|
| `kb_pro` | the KB Frappe fork |
| `kb_compta`, `kb_cheque`, `kb_stock`, `AchatsExtern` | `kb_pro` (install it first) |
| `kb_print` | nothing beyond the fork |
| `kb_facilite`, `HR2025`, `kb_distri`, `kb_commercial` | check the app's `required_apps` in `apps/<app>/<app>/hooks.py` after `kb add` |

Details, flags and failure handling are in `kb-client-apps`. Business configuration after
install (setup wizard, company, chart of accounts, users and roles) belongs to the KB
apps themselves, not to `kb`.

The site used is: `default_site` in `sites/common_site_config.json`, else legacy
`sites/currentsite.txt`, else the only site directory. With several sites and no
default, set one: `bench use <site>`.

## Step 6 — hand-over checklist

- `kb license` → "License active", expected tier/apps.
- Log in to the site as a **normal user** (not Administrator) — this is the real test of
  the fork's gate. "No license found at …" means the web process runs as a different
  user/home than the one that activated; see `kb-client-license`.
- Scheduler enabled on production sites (`bench --site <site> enable-scheduler`); the
  fork refreshes the token daily through it. Without it, only `kb` commands refresh the
  token and the site locks after ~35 days (21-day token + 14-day grace).
- Record the bench name, site, key's first 8 chars and the fingerprint
  (`kbls list-activations --key <key>` on the server) in KB's customer notes, so a later
  reinstall can release the old seat.

## Files `kb` keeps (`~/.config/kb/`, per OS user)

| File | Meaning |
|---|---|
| `config.json` | settings (0600) |
| `license.json` | cached token + `last_check` + `activated_at` |
| `license.jwt` | raw token, read by the KB Frappe fork |
| `license_key` | saved key |
| `machine-id` | random installation id — deleting it costs a seat |
| `previous-fingerprint` | old identity after an identity change (for the seat-release hint) |
| `error.log` | timestamped errors shown to users (~512 KB cap) |
| `.update_check.json` | self-update check cache (24 h) |

Read `references/environments.md` when the bench is not a standard ffm dev container.
