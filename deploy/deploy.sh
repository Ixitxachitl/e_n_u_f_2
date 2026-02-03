#!/bin/bash
# Deploy script for e_n_u_f 2.0 on Raspberry Pi
# Run this on your Pi after copying the binary

set -e

BINARY_SRC="twitchbot-linux-arm64"
BINARY_DST="twitchbot"
SERVICE="twitchbot.service"

# Auto-detect the actual user (who ran sudo)
if [ -n "$SUDO_USER" ]; then
    USER="$SUDO_USER"
else
    USER="$(whoami)"
fi
INSTALL_DIR="/home/$USER"

# Verify the home directory exists
if [ ! -d "$INSTALL_DIR" ]; then
    echo "Error: Home directory $INSTALL_DIR does not exist"
    echo "Please run this script with sudo from your home directory"
    exit 1
fi

echo "=== e_n_u_f 2.0 Deployment ==="
echo "Installing for user: $USER"
echo "Install directory: $INSTALL_DIR"
echo ""

# Check if running as root for service installation
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root (sudo ./deploy.sh)"
    exit 1
fi

# Stop existing service if running
if systemctl is-active --quiet twitchbot; then
    echo "Stopping existing twitchbot service..."
    systemctl stop twitchbot
fi

# Copy binary
if [ -f "$BINARY_SRC" ]; then
    echo "Installing binary to $INSTALL_DIR/$BINARY_DST..."
    # Remove existing file/directory conflict
    if [ -d "$INSTALL_DIR/$BINARY_DST" ]; then
        echo "Error: $INSTALL_DIR/$BINARY_DST is a directory!"
        echo "Please remove it first: sudo rm -rf $INSTALL_DIR/$BINARY_DST"
        exit 1
    fi
    cp -f "$BINARY_SRC" "$INSTALL_DIR/$BINARY_DST"
    chmod +x "$INSTALL_DIR/$BINARY_DST"
    chown $USER:$USER "$INSTALL_DIR/$BINARY_DST"
else
    echo "Error: $BINARY_SRC not found in current directory"
    echo ""
    echo "Expected files in current directory:"
    echo "  - twitchbot-linux-arm64  (the compiled binary)"
    echo "  - twitchbot.service      (systemd service file)"
    echo "  - deploy.sh              (this script)"
    exit 1
fi

# Update and install systemd service with correct paths
if [ -f "$SERVICE" ]; then
    echo "Installing systemd service..."
    # Update the service file with actual user and paths
    sed -e "s|/home/pi|$INSTALL_DIR|g" -e "s|User=pi|User=$USER|g" "$SERVICE" > /etc/systemd/system/twitchbot.service
    systemctl daemon-reload
    systemctl enable twitchbot
else
    echo "Warning: $SERVICE not found, skipping service installation"
fi

# Install systemd service
if [ -f "$SERVICE" ]; then
    echo "Installing systemd service..."
    cp "$SERVICE" /etc/systemd/system/
    systemctl daemon-reload
    systemctl enable twitchbot
else
    echo "Warning: $SERVICE not found, skipping service installation"
fi

echo ""
echo "=== Deployment Complete ==="
echo ""
echo "Commands:"
echo "  Start:   sudo systemctl start twitchbot"
echo "  Stop:    sudo systemctl stop twitchbot"
echo "  Status:  sudo systemctl status twitchbot"
echo "  Logs:    journalctl -u twitchbot -f"
echo ""
echo "Web UI will be available at: http://<pi-ip>:24601"
echo ""
