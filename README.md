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

`kb` shows an interactive main menu:

```
KB — What would you like to do?
  > Install apps          — download and install on this site
    Add apps to bench     — download only, skip site install
    Manage installed apps — uninstall or remove
    Update kb             — check for a newer version
```

### Install apps

Downloads (`bench get-app`) and installs (`bench install-app`) selected apps on the active site. Apps already installed on the site are excluded from the list.

### Add apps to bench

Downloads (`bench get-app`) selected apps into the bench `apps/` folder without installing them on any site. Useful when you want to stage apps before installing. Apps already present in the bench are excluded from the list.

### Manage installed apps

Select one or more installed KB apps and choose an action:

| Action | What it does |
|--------|-------------|
| Uninstall from site | `bench uninstall-app` — removes from site, source stays in bench |
| Remove from bench | `bench remove-app` — deletes source folder, keeps site data |
| Uninstall + Remove | Both of the above in sequence |

### GitHub Token (private repos)

Most KB apps are in private GitHub repositories. `kb` will prompt for a Personal Access Token the first time it is needed and offer to save it to `~/.config/kb/github_token` (mode 0600).

To skip the prompt, set the environment variable before running `kb`:

```bash
export KB_GITHUB_TOKEN=ghp_...
kb
```

**Precedence:** `KB_GITHUB_TOKEN` env var > `~/.config/kb/github_token` file.

## Available apps

| App | Repository |
|-----|-----------|
| `kb_pro` | KB-Developpement/kb_pro |
| `kb_compta_v2` | KB-Developpement/kb_compta_v2 |
| `kb_cheque` | KB-Developpement/kb_cheque |
| `HR2025` | KB-Developpement/HR2025 |
| `kb_facilite` | KB-Developpement/kb_facilite |
| `kb_distri` | KB-Developpement/kb_distri |
| `kb_print` | KB-Developpement/kb_print |
| `kb_commercial` | KB-Developpement/kb_commercial |
| `kb_stock` | KB-Developpement/kb_stock |
| `AchatsExtern` | KB-Developpement/AchatsExtern |

## Commands

```
kb                   Launch the interactive main menu
kb manage            Directly open the manage menu (uninstall / remove)
kb update            Check GitHub and update the binary in place
kb update --check    Only check, do not install
kb update --yes      Update without confirmation prompt
kb --version         Print version, commit, and build date
kb --help            Show help
```

### Self-update

`kb` checks for a newer release in the background on every invocation (results cached for 24 hours). When an update is available it prints a one-line notice to stderr:

```
Update available: v0.1.0 → v0.2.0  (run: kb update)
```

## Building from source

```bash
make build    # → ./bin/kb (linux/amd64)
make install  # install to $GOPATH/bin
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
