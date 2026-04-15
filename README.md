# kb — KB-Developpement Frappe App Manager

An interactive CLI for installing, managing, upgrading, and licensing KB-Developpement Frappe apps. Setup commands (**`kb init`**, **`kb config`**) work from any machine and store settings in `~/.config/kb`; bench commands (**`kb`**, **`kb install`**, **`kb add`**, **`kb manage`**, **`kb upgrade`**) require a Frappe bench container (`ffm shell`).

## Requirements

- A running Frappe bench managed by [ffm](https://github.com/nasroykh/foxmayn_frappe_manager)
- Access to the bench container via `ffm shell`
- A GitHub Personal Access Token with read access to KB-Developpement repos (for private repos)

## Installation

Run this from inside the bench container (`ffm shell <bench-name>`):

```sh
curl -fsSL https://raw.githubusercontent.com/KB-Developpement/kb_pro_cli/main/install.sh | sh
```

Detects OS and architecture, downloads the latest release binary, verifies the SHA256 checksum, and installs to `/usr/local/bin` (or `~/.local/bin` if the former is not writable).

### Build from source

```bash
git clone https://github.com/KB-Developpement/kb_pro_cli
cd kb_pro_cli
make build   # → bin/kb (linux/amd64)
```

## Usage

### First-time setup

Persistent settings live in **`~/.config/kb/config.json`** (mode `0600`). The first time you run **`kb`** with no arguments, or the first time you run **`kb install`**, **`kb add`**, **`kb manage`**, or **`kb upgrade`** in an interactive terminal, an **init wizard** asks for:

- **License server URL** — base URL for activation and heartbeat (defaults to the production server).
- **GitHub Personal Access Token** — optional; leave empty if you only use public flows. Required for private KB repos when cloning.

You can run the same wizard anytime with **`kb init`**, or edit values with **`kb config`** (also under **Settings** in the main menu). **`kb init`** and **`kb config`** do **not** require a bench container — only the interactive **`kb`** menu and the **`install` / `add` / `manage` / `upgrade`** commands need `ffm shell` and a detected site.

If you cancel the wizard before saving, the bare **`kb`** menu does not open until setup is complete.

**`kb activate`**, **`kb license`**, and **`kb update --check`** do **not** require `config.json`. Activation uses the stored license server URL (or the built-in default) unless you set **`KB_LICENSE_SERVER`**.

### Main menu (inside the bench)

Open a shell in your bench container and run **`kb`**:

```bash
ffm shell <bench-name>
kb
```

The main menu lists every top-level action. After each action you return here — press **`Esc`** or **`Ctrl+C`** to exit to the shell. Nested menus (**Manage**, **License**) use the same keys to go back.

```
KB — What would you like to do?
  > Install apps          — download and install on this site
    Add apps to bench     — download only, skip site install
    Manage apps           — install downloaded / uninstall / remove
    Upgrade apps          — pull latest changes and migrate
    License               — status, activate, deactivate locally
    Settings              — license server URL, GitHub token
```

### Install apps

Downloads (`bench get-app`) all selected apps in parallel (up to 3 concurrent), then installs each one (`bench install-app`) sequentially on the active site. Apps already installed on the site or already present in the bench are excluded from the list.

After you pick apps (interactive flow), **`kb`** asks once for an optional **Git branch** — passed as `bench get-app … --branch <name>` for every app in that run. Leave it empty to use each repository's default branch. From the shell, use **`kb install --branch <name>`** (with or without **`--apps`**) instead of the prompt.

### Add apps to bench

Downloads (`bench get-app`) selected apps into the bench `apps/` folder in parallel without installing them on any site. Useful when you want to stage apps before installing. Apps already present in the bench are excluded.

The same optional **Git branch** step (or **`kb add --branch <name>`**) applies as for **Install apps**.

### Manage apps

Select one of three actions:

| Action | What it does |
|--------|-------------|
| Install downloaded apps | `bench install-app` — installs a downloaded app on the active site |
| Uninstall from site | `bench uninstall-app` — removes from site, source stays in bench |
| Remove from bench | `bench remove-app` — uninstalls from site if needed, then deletes source folder |

### Upgrade apps

Pulls the latest changes for selected KB apps already present in the bench and runs the full update cycle — git reset, requirements, migration, asset build, and translation compile.

Runs `bench update --apps <app> --reset` **sequentially** for each selected app (migrations and asset builds are bench-wide and cannot run concurrently). All apps are attempted even if one fails; a summary is printed at the end.

```bash
kb upgrade                          # Interactive — pick from apps currently in bench
kb upgrade --apps kb_pro,kb_compta  # Non-interactive upgrade
kb upgrade --no-input --apps kb_pro # Scripted / CI usage
```

Per-app timeout is **15 minutes**. Use **`--verbose`** to see the full bench output (git log, pip, yarn, migration, asset build).

### License

From the main menu, **License** opens a submenu where you can:

- **View status** — same output as **`kb license`** (tier, expiry, allowed apps; hits the license server to reflect any revocations or bans before displaying).
- **Activate / reactivate** — same flow as **`kb activate`** (saved key, interactive prompt, or paste a new key).
- **Deactivate locally** — after confirmation, deletes **`~/.config/kb/license.json`**, **`license.jwt`**, and **`license_key`**. The license server is not contacted; an activation may still count on the server until removed there. There is no separate **`kb deactivate`** subcommand — use this menu action or delete those files manually.

Equivalent shell commands:

```bash
kb activate [license-key]   # Activate; optional key argument skips the prompt
kb license                  # Print current license status (hits server to verify)
```

#### License enforcement

The CLI validates your license on every command via an offline JWT signature check. For high-value operations (**`kb install`**, **`kb add`**, **`kb upgrade`**, **`kb update`**, **`kb license`**) it also performs a live server check (5-second timeout) before proceeding:

- **Server reachable** — revocations and machine bans take effect immediately.
- **Server unreachable** — falls back to the cached JWT (grace period up to the token's 21-day expiry).

The background heartbeat that refreshes the JWT runs every **24 hours**.

### Configuration & credentials

`~/.config/kb/config.json` stores:

| Field | Meaning |
|-------|---------|
| `license_server_url` | Base URL for license activation and heartbeat (no trailing path). |
| `github_token` | GitHub PAT for `bench get-app` against private KB repos (optional). |

**License server URL — precedence (highest wins):**

1. `KB_LICENSE_SERVER` environment variable  
2. `license_server_url` in `config.json`  
3. Built-in default: `https://license.kbdev.co`

**GitHub token — precedence (highest wins):**

1. `KB_GITHUB_TOKEN` environment variable  
2. `github_token` in `config.json`

Most KB apps are in private GitHub repositories. During **`kb install`** / **`kb add`**, if no token is available from env or `config.json`, `kb` prompts once and can save it into `config.json` alongside any existing settings. The token is never passed via process arguments to `git` — it is written to a temporary `0600` credentials file and removed after the clone.

A warning is printed if a supplied token does not start with `ghp_` or `github_pat_`.

## Available apps

| App | Repository |
|-----|-----------|
| `kb_pro` | KB-Developpement/kb_pro |
| `kb_compta` | KB-Developpement/kb_compta |
| `kb_cheque` | KB-Developpement/kb_cheque |
| `HR2025` | KB-Developpement/HR2025 |
| `kb_facilite` | KB-Developpement/kb_facilite |
| `kb_distri` | KB-Developpement/kb_distri |
| `kb_print` | KB-Developpement/kb_print |
| `kb_commercial` | KB-Developpement/kb_commercial |
| `kb_stock` | KB-Developpement/kb_stock |
| `AchatsExtern` | KB-Developpement/AchatsExtern |

## Commands

All subcommands accept the global flags listed below unless noted.

```
kb                         Interactive main menu (init wizard if ~/.config/kb/config.json is missing)
kb init                    First-time setup wizard — same fields as Settings (TTY; no --no-input)
kb config                  Edit ~/.config/kb/config.json interactively (TTY; no --no-input)
kb install  (alias: i)     Download and install apps on this site (--apps, --branch)
kb add                     Download apps into bench without site installation (--apps, --branch)
kb manage   (alias: m)     Interactive manage submenu (install on site / uninstall / remove)
kb upgrade  (alias: up)    Update KB apps already in bench via bench update --reset (--apps)
kb activate (alias: a)     Activate this machine with a KB Pro license key
kb license                 Show current license status (live server check)
kb update   (alias: u)     Check GitHub and optionally replace the kb binary (see Self-update)
kb completion <shell>      Print shell completion script (bash, zsh, fish, powershell)
```

License **deactivate** (remove local JWT + key files) is available from the main menu under **License**, not as a separate `kb` subcommand.

### Global flags

| Flag | Short | Description |
|------|-------|-------------|
| `--no-input` | | Disable interactive prompts — requires explicit flags for all inputs |
| `--quiet` | `-q` | Suppress informational output |
| `--verbose` | | Print raw bench output on success |
| `--no-color` | | Disable colours (also honoured via `NO_COLOR` env var) |
| `--version` | `-v` | Print version, commit, and build date |
| `--help` | `-h` | Show help |

### Non-interactive / CI usage

**`kb init`** and **`kb config`** require a TTY and cannot be used with **`--no-input`**.

**`kb install`**, **`kb add`**, **`kb manage`**, and **`kb upgrade`** require **`~/.config/kb/config.json`** to exist when you use **`--no-input`** (the file marks "setup complete"). Create it once on the image or runner (for example by copying from a template machine). An empty object `{}` is valid if you supply everything at runtime via **`KB_GITHUB_TOKEN`** and **`KB_LICENSE_SERVER`**.

Example `config.json` with stored credentials (typical for CI):

```json
{
  "license_server_url": "https://license.kbdev.co",
  "github_token": "ghp_your_token_here"
}
```

Use **`--no-input`** with explicit **`--apps`** where applicable:

```bash
kb install --no-input --apps kb_pro,kb_compta
kb install --no-input --apps kb_pro --branch develop
kb add     --no-input --apps kb_cheque
kb add     --no-input --apps kb_pro --branch version-15
kb upgrade --no-input --apps kb_pro,kb_compta
kb activate <license-key>          # key as argument, no prompt
kb update  --no-input --yes        # or just --no-input (implies --yes)
```

### Shell completion

```bash
kb completion bash   >> ~/.bashrc
kb completion zsh    >> ~/.zshrc
kb completion fish   >  ~/.config/fish/completions/kb.fish
```

### Self-update

When the startup hooks run (skipped for example on **`kb activate`**, **`kb init`**, **`kb config`**, **`kb completion`**, and **`kb license`**), `kb` may fetch the latest release in the background; results are cached for 24 hours. When an update is available it prints a one-line notice to stderr:

```
Update available: v0.1.0 → v0.2.0  (run: kb update)
```

```bash
kb update           # Download and replace the binary (asks for confirmation; needs active license)
kb update --check   # Only check — no license required, does not install
kb update --yes     # Update without confirmation (--no-input implies --yes)
```

Installing a new binary with **`kb update`** (without **`--check`**) requires an **active, non-expired license** (`kb activate`). Checking only (`--check`) does not.

## Building from source

```bash
make build    # → ./bin/kb (linux/amd64)
make install  # install to $GOPATH/bin
make test     # go test -race ./...
make vet      # go vet
make fmt      # gofmt
make tidy     # go mod tidy
make clean    # remove binary
make help     # list all targets
```

## Releasing

Push a version tag to trigger the GitHub Actions release workflow:

```bash
git tag v0.1.0
git push origin v0.1.0
```

GoReleaser builds linux and darwin binaries (amd64 + arm64), creates a GitHub release with a `checksums.txt` file, and makes the binaries available to `install.sh` and `kb update`.
