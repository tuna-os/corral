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

url="https://github.com/${REPO}/releases/download/binaries/corral-${os}-${arch}"
echo "Downloading corral (${os}/${arch})..."
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fSL --progress-bar -o "$tmp" "$url"
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
