#!/bin/sh
set -e

# nollama installer
# Usage: curl -fsSL https://raw.githubusercontent.com/sovereignty-labs/nollama/main/install.sh | sh

REPO="sovereignty-labs/nollama"
BINARY="nollama"

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  linux) ;;
  darwin) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

ASSET="${BINARY}-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"

# Determine install directory
if [ "$(id -u)" -eq 0 ]; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="${HOME}/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi

echo "Installing nollama..."
echo "  Platform: ${OS}/${ARCH}"
echo "  From:     ${URL}"
echo "  To:       ${INSTALL_DIR}/${BINARY}"

# Download
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o "${INSTALL_DIR}/${BINARY}"
elif command -v wget >/dev/null 2>&1; then
  wget -q "$URL" -O "${INSTALL_DIR}/${BINARY}"
else
  echo "Error: curl or wget required"
  exit 1
fi

chmod +x "${INSTALL_DIR}/${BINARY}"

# Verify
if "${INSTALL_DIR}/${BINARY}" version >/dev/null 2>&1; then
  echo ""
  $INSTALL_DIR/$BINARY version
  echo ""
  echo "Installed successfully."
else
  echo ""
  echo "Binary installed but failed to execute. Check your platform."
  exit 1
fi

# Check PATH
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo ""
    echo "Note: ${INSTALL_DIR} is not in your PATH."
    echo "Add it with: export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac

echo ""
echo "Next steps:"
echo "  nollama runtime build              # build llama-server with CUDA (~3 min)"
echo "  nollama search qwen3 8b            # find a model"
echo "  nollama pull unsloth/Qwen3-8B-GGUF:Q4_K_M"
echo "  nollama run Qwen3-8B-Q4_K_M        # start chatting"
echo ""
echo "  No CUDA? Use: nollama runtime install   (CPU-only pre-built binary)"
