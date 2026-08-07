#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY="markwylde/gitea-runner-lume"
readonly INSTALL_DIR="${GITEA_RUNNER_LUME_INSTALL_DIR:-$HOME/.local/bin}"

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "gitea-runner-lume requires Apple Silicon macOS." >&2
  exit 1
fi

for command in curl shasum tar install awk; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "Required command not found: $command" >&2
    exit 1
  fi
done

version="${GITEA_RUNNER_LUME_VERSION:-}"
if [[ -z "$version" ]]; then
  latest_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' \
    "https://github.com/$REPOSITORY/releases/latest")"
  version="${latest_url##*/v}"
fi
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Invalid release version: $version" >&2
  exit 1
fi

archive="gitea-runner-lume_${version}_macOS_arm64.tar.gz"
release_url="https://github.com/$REPOSITORY/releases/download/v${version}"
temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT

echo "Downloading gitea-runner-lume v$version..."
curl -fsSL "$release_url/$archive" -o "$temp_dir/$archive"
curl -fsSL "$release_url/checksums.txt" -o "$temp_dir/checksums.txt"

checksum_line="$(awk -v archive="$archive" '$2 == archive { print }' \
  "$temp_dir/checksums.txt")"
if [[ "$(printf '%s\n' "$checksum_line" | awk 'NF { count++ } END { print count + 0 }')" != "1" ]]; then
  echo "Release checksum entry missing or ambiguous: $archive" >&2
  exit 1
fi
printf '%s\n' "$checksum_line" | (
  cd "$temp_dir"
  shasum -a 256 -c -
)

tar -xzf "$temp_dir/$archive" -C "$temp_dir"
binary="$temp_dir/gitea-runner-lume"
if [[ ! -f "$binary" || -L "$binary" ]]; then
  echo "Release archive did not contain a regular runner binary." >&2
  exit 1
fi

install -d -m 755 "$INSTALL_DIR"
install -m 755 "$binary" "$INSTALL_DIR/gitea-runner-lume"
"$INSTALL_DIR/gitea-runner-lume" version

echo "Installed to $INSTALL_DIR/gitea-runner-lume"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Add $INSTALL_DIR to PATH before running gitea-runner-lume." ;;
esac
