#!/usr/bin/env sh
set -e

REPO="KB-Developpement/kb_pro_cli"
BINARY="kb"

# --- optional GitHub token (required for private repo, ignored for public) ---
if [ -z "$GITHUB_TOKEN" ]; then
  echo "Note: GITHUB_TOKEN is not set. This is required if the repository is private." >&2
  echo "  export GITHUB_TOKEN=ghp_..." >&2
  AUTH_HEADER=""
else
  AUTH_HEADER="Authorization: Bearer ${GITHUB_TOKEN}"
fi

# --- detect OS ---
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)
    echo "Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

# --- detect arch ---
ARCH=$(uname -m)
case "$ARCH" in
  x86_64 | amd64) ARCH="amd64" ;;
  arm64 | aarch64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

# --- resolve latest release ---
RELEASE_JSON=$(curl -fsSL \
  ${AUTH_HEADER:+-H "$AUTH_HEADER"} \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/${REPO}/releases/latest")

VERSION=$(printf '%s' "$RELEASE_JSON" | grep '"tag_name"' \
  | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')

if [ -z "$VERSION" ]; then
  echo "Could not determine latest release version." >&2
  echo "Check that your GITHUB_TOKEN has 'repo' scope." >&2
  exit 1
fi

echo "Installing kb ${VERSION} (${OS}/${ARCH})..."

ARCHIVE="kb_${VERSION#v}_${OS}_${ARCH}.tar.gz"

# --- find asset IDs from the release JSON ---
# Extract pairs of "name" / "id" for each asset using portable sed/awk.
ARCHIVE_ID=$(printf '%s' "$RELEASE_JSON" \
  | tr ',' '\n' \
  | awk -v target="\"$ARCHIVE\"" '
      /"name"/ { if ($0 ~ target) found=1 }
      /"id"/ && found { gsub(/[^0-9]/, "", $0); print; found=0; exit }
    ')

CHECKSUM_ID=$(printf '%s' "$RELEASE_JSON" \
  | tr ',' '\n' \
  | awk '
      /"name"/ { if ($0 ~ "\"checksums.txt\"") found=1 }
      /"id"/ && found { gsub(/[^0-9]/, "", $0); print; found=0; exit }
    ')

if [ -z "$ARCHIVE_ID" ]; then
  echo "Asset ${ARCHIVE} not found in release ${VERSION}." >&2
  exit 1
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# --- download archive via asset API ---
curl -fsSL \
  ${AUTH_HEADER:+-H "$AUTH_HEADER"} \
  -H "Accept: application/octet-stream" \
  "https://api.github.com/repos/${REPO}/releases/assets/${ARCHIVE_ID}" \
  -o "$TMP/$ARCHIVE"

# --- download checksums (if found) and verify ---
if [ -n "$CHECKSUM_ID" ]; then
  curl -fsSL \
    ${AUTH_HEADER:+-H "$AUTH_HEADER"} \
    -H "Accept: application/octet-stream" \
    "https://api.github.com/repos/${REPO}/releases/assets/${CHECKSUM_ID}" \
    -o "$TMP/checksums.txt"

  cd "$TMP"
  if command -v sha256sum > /dev/null 2>&1; then
    grep "$ARCHIVE" checksums.txt | sha256sum -c -
  elif command -v shasum > /dev/null 2>&1; then
    grep "$ARCHIVE" checksums.txt | shasum -a 256 -c -
  else
    echo "Warning: no sha256 tool found, skipping checksum verification." >&2
  fi
  cd - > /dev/null
fi

# --- extract ---
tar -xzf "$TMP/$ARCHIVE" -C "$TMP"

# --- install ---
if [ -w "/usr/local/bin" ]; then
  INSTALL_DIR="/usr/local/bin"
elif [ -d "$HOME/.local/bin" ]; then
  INSTALL_DIR="$HOME/.local/bin"
else
  mkdir -p "$HOME/.local/bin"
  INSTALL_DIR="$HOME/.local/bin"
fi

mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
chmod +x "$INSTALL_DIR/$BINARY"

echo "Installed to $INSTALL_DIR/$BINARY"
echo "Run 'kb' inside a Frappe bench container (ffm shell) to install KB apps."

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo ""
    echo "Note: $INSTALL_DIR is not in your PATH."
    echo "Add this to your shell profile:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
    ;;
esac
