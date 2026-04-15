# kb — KB-Developpement Frappe App Manager

An interactive CLI that runs inside a Frappe bench container and lets you install, add, and manage KB-Developpement custom apps.

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

Open a shell in your bench container and run `kb`:

```bash
ffm shell <bench-name>
kb
```

`kb` shows an interactive main menu. After each action you are returned to the menu — press `Esc` or `Ctrl+C` to exit to the shell.

```
KB — What would you like to do?
  > Install apps          — download and install on this site
    Add apps to bench     — download only, skip site install
    Manage apps           — install downloaded / uninstall / remove
    Update kb             — check for a newer version
```

### Install apps

Downloads (`bench get-app`) all selected apps in parallel (up to 3 concurrent), then installs each one (`bench install-app`) sequentially on the active site. Apps already installed on the site or already present in the bench are excluded from the list.

### Add apps to bench

Downloads (`bench get-app`) selected apps into the bench `apps/` folder in parallel without installing them on any site. Useful when you want to stage apps before installing. Apps already present in the bench are excluded.

### Manage apps

Select one of three actions:

| Action | What it does |
|--------|-------------|
| Install downloaded apps | `bench install-app` — installs a downloaded app on the active site |
| Uninstall from site | `bench uninstall-app` — removes from site, source stays in bench |
| Remove from bench | `bench remove-app` — uninstalls from site if needed, then deletes source folder |

### License

```bash
kb activate          # Activate this machine with a KB Pro license key
kb license           # Show current license status (tier, expiry, allowed apps)
```

### GitHub Token (private repos)

Most KB apps are in private GitHub repositories. `kb` will prompt for a Personal Access Token the first time it is needed and offer to save it to `~/.config/kb/github_token` (mode 0600). The token is never passed via process arguments — it is written to a temporary 0600 credentials file for `git` and deleted immediately after the clone.

To skip the prompt, set the environment variable before running `kb`:

```bash
export KB_GITHUB_TOKEN=ghp_...
kb
```

**Precedence:** `KB_GITHUB_TOKEN` env var > `~/.config/kb/github_token` file.

A warning is printed if the token does not start with `ghp_` or `github_pat_`.

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

All subcommands accept the global flags listed below.

```
kb                         Launch the interactive main menu
kb install  (alias: i)     Download and install apps on this site
kb add                     Download apps into bench without site installation
kb manage   (alias: m)     Open the manage submenu
kb activate (alias: a)     Activate this machine with a KB Pro license key
kb license                 Show current license status
kb update   (alias: u)     Check GitHub and update the binary in place
kb completion <shell>      Print shell completion script (bash, zsh, fish, powershell)
```

### Global flags

| Flag | Short | Description |
|------|-------|-------------|
| `--no-input` | | Disable interactive prompts — requires explicit flags for all inputs |
| `--quiet` | `-q` | Suppress informational output |
| `--verbose` | `-v` | Print raw bench output on success |
| `--no-color` | | Disable colours (also honoured via `NO_COLOR` env var) |
| `--version` | | Print version, commit, and build date |
| `--help` | `-h` | Show help |

### Non-interactive / CI usage

Use `--no-input` with explicit `--apps` to run without any prompts:

```bash
kb install --no-input --apps kb_pro,kb_compta
kb add     --no-input --apps kb_cheque
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

`kb` checks for a newer release in the background on every invocation (results cached for 24 hours). When an update is available it prints a one-line notice to stderr:

```
Update available: v0.1.0 → v0.2.0  (run: kb update)
```

```bash
kb update           # Check and update (asks for confirmation)
kb update --check   # Only check, do not install
kb update --yes     # Update without confirmation prompt
```

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
