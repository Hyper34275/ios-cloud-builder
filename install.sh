#!/bin/bash
set -e

REPO="MobAI-App/ios-builder"
BINARY="builder"
INSTALL_DIR="/usr/local/bin"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    darwin) OS="darwin" ;;
    linux) OS="linux" ;;
    mingw*|msys*|cygwin*) OS="windows" ;;
    *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Windows not supported via this script
if [ "$OS" = "windows" ]; then
    echo "Windows installation not supported via this script."
    echo "Download manually from: https://github.com/$REPO/releases"
    exit 1
fi

# Get latest version
echo "Fetching latest version..."
VERSION=$(curl -sI "https://github.com/$REPO/releases/latest" | grep -i "^location:" | sed 's/.*tag\///' | tr -d '\r\n')
if [ -z "$VERSION" ]; then
    echo "Failed to fetch latest version"
    exit 1
fi
echo "Latest version: $VERSION"

# Download URL
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/$BINARY-$OS-$ARCH"
echo "Downloading $BINARY-$OS-$ARCH..."

# Create temp file
TMP_FILE=$(mktemp)
trap "rm -f $TMP_FILE" EXIT

# Download
if ! curl -sL "$DOWNLOAD_URL" -o "$TMP_FILE"; then
    echo "Download failed"
    exit 1
fi

# Make executable
chmod +x "$TMP_FILE"

# Install
if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP_FILE" "$INSTALL_DIR/$BINARY"
else
    echo "Installing to $INSTALL_DIR (requires sudo)..."
    sudo mv "$TMP_FILE" "$INSTALL_DIR/$BINARY"
fi

echo ""
echo "Successfully installed $BINARY $VERSION to $INSTALL_DIR/$BINARY"
echo "Run 'builder --help' to get started"
