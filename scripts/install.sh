#!/bin/sh
# Corral installer: fetches the rolling-release binary and wires up shell
# completions. Safe to re-run (upgrades in place).
#
#   curl -fsSL https://raw.githubusercontent.com/tuna-os/corral/main/scripts/install.sh | sh
#
# Options (env vars):
#   CORRAL_INSTALL_DIR   install target (default: ~/.local/bin, /usr/local/bin if root)
#   CORRAL_VERSION       reserved for future tagged releases (currently rolling only)
set -eu

REPO="tuna-os/corral"

case "$(uname -s)" in
  Linux)  os="linux" ;;
  Darwin) os="darwin" ;;
  *) echo "unsupported OS: $(uname -s) (Linux/macOS only)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${CORRAL_INSTALL_DIR:-}" ]; then
  dir="$CORRAL_INSTALL_DIR"
elif [ "$(id -u)" = "0" ]; then
  dir="/usr/local/bin"
else
  dir="${HOME}/.local/bin"
fi
mkdir -p "$dir"

base="https://github.com/${REPO}/releases/download/binaries"
asset="corral-${os}-${arch}"
echo "Downloading corral (${os}/${arch})..."
tmp="$(mktemp)"
sums="$(mktemp)"
trap 'rm -f "$tmp" "$sums"' EXIT
curl -fSL --progress-bar -o "$tmp" "${base}/${asset}"

# Verify against the SHA256SUMS published beside the binary. This is a
# `curl | sh` pipeline putting an executable on PATH, so "the download
# succeeded" is not the same question as "this is the file CI built".
verify_checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    set -- sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    set -- shasum -a 256          # macOS ships shasum, not sha256sum
  else
    echo "note: no sha256sum or shasum — skipping checksum verification" >&2
    return 0
  fi
  curl -fsSL -o "$sums" "${base}/SHA256SUMS" || {
    echo "could not download SHA256SUMS from ${base}" >&2
    return 1
  }
  # The name in SHA256SUMS may carry a leading '*' (binary mode).
  expected="$(awk -v want="$asset" '{ name = $2; sub(/^\*/, "", name); if (name == want) { print $1; exit } }' "$sums")"
  if [ -z "$expected" ]; then
    echo "SHA256SUMS has no entry for ${asset}" >&2
    return 1
  fi
  actual="$("$@" "$tmp" | awk '{print $1}')"
  if [ "$actual" != "$expected" ]; then
    echo "checksum mismatch for ${asset}" >&2
    echo "  expected ${expected}" >&2
    echo "  actual   ${actual}" >&2
    return 1
  fi
  echo "Checksum verified (${expected})"
}

if ! verify_checksum; then
  echo "refusing to install an unverified binary" >&2
  exit 1
fi

chmod +x "$tmp"
install "$tmp" "${dir}/corral"
ver="$("${dir}/corral" version 2>/dev/null | head -1 || true)"
echo "Installed ${dir}/corral${ver:+ ($ver)}"

case ":${PATH}:" in
  *":${dir}:"*) ;;
  *) echo "note: ${dir} is not on your PATH — add it to your shell profile" ;;
esac

# Shell completions — best-effort, never fails the install.
install_completions() {
  shell_name="$(basename "${SHELL:-}")"
  case "$shell_name" in
    bash)
      target="${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions"
      mkdir -p "$target"
      "${dir}/corral" completion bash > "${target}/corral" && echo "Installed bash completions (${target}/corral)"
      ;;
    zsh)
      target="${HOME}/.zsh/completions"
      mkdir -p "$target"
      "${dir}/corral" completion zsh > "${target}/_corral" &&
        echo "Installed zsh completions (${target}/_corral) — ensure it's in your fpath:" &&
        echo '  fpath=(~/.zsh/completions $fpath); autoload -Uz compinit && compinit'
      ;;
    fish)
      target="${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions"
      mkdir -p "$target"
      "${dir}/corral" completion fish > "${target}/corral.fish" && echo "Installed fish completions (${target}/corral.fish)"
      ;;
    *)
      echo "note: unknown shell '${shell_name}' — generate completions with: corral completion --help"
      ;;
  esac
}
install_completions || true

echo
echo "Try it without a cluster:  corral --demo    (TUI)"
echo "                           corral web --demo    (dashboard)"
