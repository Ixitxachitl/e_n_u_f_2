#!/bin/bash
# Deploy script for e_n_u_f 2.0 on Raspberry Pi
# Run this on your Pi after copying the binary

set -e

BINARY="twitchbot"
SERVICE="twitchbot.service"
INSTALL_DIR="/home/pi"
USER="pi"

echo "=== e_n_u_f 2.0 Deployment ==="

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
if [ -f "$BINARY" ]; then
    echo "Installing binary to $INSTALL_DIR..."
    cp "$BINARY" "$INSTALL_DIR/"
    chmod +x "$INSTALL_DIR/$BINARY"
    chown $USER:$USER "$INSTALL_DIR/$BINARY"
else
    echo "Error: $BINARY not found in current directory"
    exit 1
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
