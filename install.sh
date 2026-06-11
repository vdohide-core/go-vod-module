#!/bin/bash

# Go VOD Server Installation Script
# Usage: curl -fsSL https://raw.githubusercontent.com/vdohide-core/go-vod-module/main/install.sh | sudo -E bash -s -- [OPTIONS]

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Defaults
PORT=8889
MODE="mapped"
MEDIA_ROOT="/home/files"
UPSTREAM_JSON_URL="http://127.0.0.1:8888"
DEFAULT_SEGMENT_DURATION=4000
MAX_CACHE_ENTRIES=1000
ALIGN_SEGMENTS_TO_KEY_FRAMES="false"
UNINSTALL=false

APP_NAME="go-vod-server"
APP_DIR="/opt/$APP_NAME"
SERVICE_NAME="go-vod-server"
GITHUB_REPO="vdohide-core/go-vod-module"
RELEASES_URL="https://github.com/$GITHUB_REPO/releases/latest/download"

print_status()  { echo -e "${GREEN}[INFO]${NC} $1"; }
print_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
print_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

# Parse args
while [[ $# -gt 0 ]]; do
    case $1 in
        --uninstall)                     UNINSTALL=true; shift ;;
        --port)                          PORT="$2"; shift 2 ;;
        --mode)                          MODE="$2"; shift 2 ;;
        --media_root)                    MEDIA_ROOT="$2"; shift 2 ;;
        --default_segment_duration)      DEFAULT_SEGMENT_DURATION="$2"; shift 2 ;;
        --max_cache_entries)             MAX_CACHE_ENTRIES="$2"; shift 2 ;;
        --align_segments_to_key_frames)  ALIGN_SEGMENTS_TO_KEY_FRAMES="$2"; shift 2 ;;
        -h|--help)
            echo "Go VOD Server Installer"
            echo ""
            echo "Usage: curl -fsSL https://raw.githubusercontent.com/$GITHUB_REPO/main/install.sh | sudo -E bash -s -- [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --uninstall                    Uninstall completely"
            echo "  --port PORT                    Server port (default: 8889)"
            echo "  --mode MODE                    Operation mode: local or mapped (default: mapped)"
            echo "  --media_root PATH_OR_URL       Media root path (local mode) or upstream API URL (mapped mode)"
            echo "  --default_segment_duration MS  Default HLS segment duration in ms (default: 4000)"
            echo "  --max_cache_entries NUM        Max cache entries (default: 1000)"
            echo "  --align_segments_to_key_frames BOOLEAN (true/false) (default: false)"
            echo "  -h, --help                     Show this help"
            exit 0 ;;
        *)
            print_error "Unknown option: $1"; exit 1 ;;
    esac
done

# ─── Uninstall ────────────────────────────────────────────────
if [ "$UNINSTALL" = true ]; then
    print_warning "⚠️  Starting Uninstallation..."
    systemctl stop "${SERVICE_NAME}"    2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
    [ -f "/etc/systemd/system/${SERVICE_NAME}.service" ] && rm "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    [ -d "$APP_DIR" ] && rm -rf "$APP_DIR"
    print_status "✅ Uninstalled successfully!"
    exit 0
fi

# Check root
if [ "$(id -u)" -ne 0 ]; then
    print_error "This script must be run as root (use sudo)"
    exit 1
fi

print_status "🚀 Starting Installation..."

# ─── System Dependencies ──────────────────────────────────────
print_status "Installing system dependencies (curl)..."
if command -v apt-get &>/dev/null; then
    apt-get update -qq
    apt-get install -y -qq curl
elif command -v yum &>/dev/null; then
    yum install -y curl
elif command -v dnf &>/dev/null; then
    dnf install -y curl
fi

if ! command -v curl &>/dev/null; then
    print_error "curl not found. Please install it manually."
    exit 1
fi

# ─── Stop existing services ───────────────────────────────────
print_status "Stopping existing services..."
systemctl stop ${SERVICE_NAME} 2>/dev/null || true

# ─── Create app directory ─────────────────────────────────────
print_status "Creating app directory: $APP_DIR"
mkdir -p "$APP_DIR"
cd "$APP_DIR"

# ─── Download binary ──────────────────────────────────────────
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    BINARY="go-vod-server-linux-amd64"
elif [ "$ARCH" = "aarch64" ]; then
    BINARY="go-vod-server-linux-arm64"
else
    print_error "Unsupported architecture: $ARCH"
    exit 1
fi

print_status "Downloading binary ($BINARY) from latest release..."
curl -fsSL "$RELEASES_URL/$BINARY" -o "$APP_DIR/$APP_NAME"
chmod +x "$APP_DIR/$APP_NAME"
print_status "Binary downloaded."

# ─── Map media_root to upstream_json_url in mapped mode ───────
if [ "$MODE" = "mapped" ]; then
    UPSTREAM_JSON_URL="$MEDIA_ROOT"
    MEDIA_ROOT=""
else
    UPSTREAM_JSON_URL=""
fi

# ─── Create config.json ─────────────────────────────────────────
print_status "Creating config.json..."
cat > "$APP_DIR/config.json" <<EOF
{
  "port": $PORT,
  "mode": "$MODE",
  "media_root": "$MEDIA_ROOT",
  "upstream_json_url": "$UPSTREAM_JSON_URL",
  "default_segment_duration": $DEFAULT_SEGMENT_DURATION,
  "max_cache_entries": $MAX_CACHE_ENTRIES,
  "align_segments_to_key_frames": $ALIGN_SEGMENTS_TO_KEY_FRAMES
}
EOF
print_status "config.json created successfully."

# ─── Systemd service ──────────────────────────────────────────
print_status "Creating systemd service..."

cat > /etc/systemd/system/${SERVICE_NAME}.service <<EOF
[Unit]
Description=Go VOD Server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/$APP_NAME
Restart=always
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

# ─── Enable & start service ───────────────────────────────────
systemctl daemon-reload
print_status "Starting service..."
systemctl enable ${SERVICE_NAME}
systemctl start  ${SERVICE_NAME}

# ─── Verify ───────────────────────────────────────────────────
sleep 2
RUNNING=0
if systemctl is-active --quiet ${SERVICE_NAME}; then
    RUNNING=1
fi

echo ""
echo "============================================"
if [ $RUNNING -eq 1 ]; then
    print_status "✅ Installation completed successfully!"
else
    print_warning "Service failed to start — check logs below"
    journalctl -u "${SERVICE_NAME}" -n 15 --no-pager
fi
echo "============================================"
echo ""
echo "  Directory:  $APP_DIR"
echo "  Status:     $(systemctl is-active ${SERVICE_NAME})"
echo ""
echo "  Commands:"
echo "    View logs:   journalctl -u ${SERVICE_NAME} -f"
echo "    Restart:     systemctl restart ${SERVICE_NAME}"
echo "    Stop:        systemctl stop ${SERVICE_NAME}"
echo "    Uninstall:   curl -fsSL https://raw.githubusercontent.com/$GITHUB_REPO/main/install.sh | sudo bash -s -- --uninstall"
echo "============================================"
