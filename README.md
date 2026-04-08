# kb — KB-Developpement Frappe App Installer

An interactive CLI that runs inside a Frappe bench container and lets you pick and install KB-Developpement custom apps in one command.

## Requirements

- A running Frappe bench managed by [ffm](https://github.com/nasroykh/foxmayn_frappe_manager)
- Access to the bench container via `ffm shell`

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

`kb` will:

1. Verify it is running inside a Frappe bench container
2. Auto-detect the active site name
3. Detect which KB apps are already installed and exclude them
4. Show an interactive multi-select list of available apps
5. Download and install each selected app sequentially

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
kb                   Launch the interactive app installer (default)
kb update            Check GitHub and update the binary in place
kb update --check    Only check, do not install
kb update --yes      Update without confirmation prompt
kb --version         Print version, commit, and build date
kb --help            Show help
```

## Building from source

```bash
make build    # → ./bin/kb (linux/amd64)
make install  # install to $GOPATH/bin
make vet      # go vet
make fmt      # gofmt
make tidy     # go mod tidy
make clean    # remove binary
```

## Releasing

Push a version tag to trigger the GitHub Actions release workflow:

```bash
git tag v0.1.0
git push origin v0.1.0
```

GoReleaser builds linux and darwin binaries (amd64 + arm64), creates a GitHub release, and publishes the checksums file used by `install.sh`.
