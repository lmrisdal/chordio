#!/usr/bin/env bash
# Install Chordio: download the latest prebuilt release binary, install an
# example config, and optionally enable the systemd service.
#
# Safe to re-run: it will not overwrite an existing config.
set -euo pipefail

REPO="${CHORDIO_REPO:-lmrisdal/chordio}"
BIN="/usr/local/bin/chordio"
CONF_DIR="/etc/chordio"
CONF="$CONF_DIR/config.json"
UNIT="chordio.service"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." 2>/dev/null && pwd || echo "")"

ASSUME_YES=0
WANT_SERVICE="ask" # ask | yes | no

usage() {
  cat <<'USAGE'
Install Chordio: binary, config, and optional systemd service.

Options:
  --service      install and enable the systemd service without asking
  --no-service   skip the systemd service
  -y, --yes      accept defaults for all prompts (installs the service)
  -h, --help     show this help
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --service)    WANT_SERVICE="yes" ;;
    --no-service) WANT_SERVICE="no" ;;
    -y|--yes)     ASSUME_YES=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "Unknown option: $1 (try --help)" >&2; exit 1 ;;
  esac
  shift
done

ask_yn() {
  local q="$1" ans
  if [[ "$ASSUME_YES" -eq 1 ]]; then echo "yes"; return; fi
  if ! { exec 3<>/dev/tty; } 2>/dev/null; then echo "yes"; return; fi
  printf '%s [Y/n] ' "$q" >&3
  IFS= read -r ans <&3 || ans=""
  exec 3>&-
  case "$ans" in [nN]*) echo "no" ;; *) echo "yes" ;; esac
}

case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

have_gh() { command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; }

fetch_repo_file() {
  local path="$1" dest="$2"
  if [[ -n "$SCRIPT_DIR" && -f "$SCRIPT_DIR/$path" ]]; then
    cp "$SCRIPT_DIR/$path" "$dest"
  elif have_gh; then
    gh api -H "Accept: application/vnd.github.raw" "repos/$REPO/contents/$path" > "$dest"
  else
    curl -fsSL -o "$dest" "https://raw.githubusercontent.com/$REPO/main/$path"
  fi
}

echo "==> Downloading chordio ($ARCH)"
asset="chordio-linux-$ARCH"
if [[ -n "$SCRIPT_DIR" && -f "$SCRIPT_DIR/chordio" ]]; then
  cp "$SCRIPT_DIR/chordio" "$TMP/chordio"
elif [[ -n "$SCRIPT_DIR" && -f "$SCRIPT_DIR/go.mod" ]] && command -v go >/dev/null 2>&1; then
  echo "    No local binary found; building from local source"
  (cd "$SCRIPT_DIR" && go build -o "$TMP/chordio" .)
elif have_gh; then
  gh release download --repo "$REPO" --pattern "$asset" --output "$TMP/chordio" --clobber
else
  url="https://github.com/$REPO/releases/latest/download/$asset"
  if ! curl -fSL -o "$TMP/chordio" "$url"; then
    if command -v go >/dev/null 2>&1; then
      echo "    Release asset not found; building from source tarball"
      curl -fsSL -o "$TMP/source.tar.gz" "https://github.com/$REPO/archive/refs/heads/main.tar.gz"
      tar -xzf "$TMP/source.tar.gz" -C "$TMP"
      src="$(find "$TMP" -maxdepth 2 -type f -name go.mod -exec dirname {} \; | head -n 1)"
      if [[ -z "$src" ]]; then
        echo "Could not find Chordio source in tarball" >&2
        exit 1
      fi
      (cd "$src" && go build -o "$TMP/chordio" .)
    else
      echo "Download failed and Go is not installed for a source build." >&2
      echo "Install Go, or clone the repo and run: go build -o chordio ." >&2
      exit 1
    fi
  fi
fi
chmod +x "$TMP/chordio"

echo "==> Installing binary to $BIN"
sudo install -Dm755 "$TMP/chordio" "$BIN.new"
sudo mv -f "$BIN.new" "$BIN"

echo "==> Installing config to $CONF"
sudo mkdir -p "$CONF_DIR"
if [[ -f "$CONF" ]]; then
  echo "    $CONF already exists, leaving it untouched"
else
  fetch_repo_file "packaging/config.example.json" "$TMP/config.json"
  sudo install -Dm644 "$TMP/config.json" "$CONF"
  echo "    Edit $CONF to configure your chords."
fi

[[ "$WANT_SERVICE" == "ask" ]] && WANT_SERVICE="$(ask_yn "Install and enable the Chordio service?")"

if [[ "$WANT_SERVICE" == "yes" ]]; then
  echo "==> Installing systemd unit"
  fetch_repo_file "packaging/$UNIT" "$TMP/$UNIT"
  sudo install -Dm644 "$TMP/$UNIT" "/etc/systemd/system/$UNIT"
  sudo systemctl daemon-reload
  sudo systemctl enable "$UNIT"
else
  echo "==> No systemd service selected; installed binary + config only"
fi

echo
echo "Done. Next steps:"
echo "  1. chordio --list-devices"
echo "  2. sudo nano /etc/chordio/config.json"
echo "  3. chordio --config /etc/chordio/config.json --debug"
[[ "$WANT_SERVICE" == "yes" ]] && echo "  4. sudo systemctl start chordio.service"
[[ "$WANT_SERVICE" == "yes" ]] && echo "  5. journalctl -u chordio.service -f"
