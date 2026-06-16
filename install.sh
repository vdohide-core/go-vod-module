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
    BINARY="linux"
elif [ "$ARCH" = "aarch64" ]; then
    BINARY="linux-arm64"
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

# ─── Install & configure nginx public proxy ───────────────────
print_status "Setting up nginx public proxy..."

if ! command -v nginx &>/dev/null; then
    print_status "Installing nginx..."
    if command -v apt-get &>/dev/null; then
        apt-get install -y -qq nginx
    elif command -v yum &>/dev/null; then
        yum install -y nginx
    elif command -v dnf &>/dev/null; then
        dnf install -y nginx
    else
        print_warning "Could not install nginx automatically. Please install it manually."
    fi
fi

if command -v nginx &>/dev/null; then
    # Disable default nginx site to avoid port 80 conflict
    if [ -f /etc/nginx/sites-enabled/default ]; then
        print_status "Disabling default nginx site..."
        rm -f /etc/nginx/sites-enabled/default
    fi

    print_status "Writing /etc/nginx/conf.d/local.conf..."
    mkdir -p /etc/nginx/conf.d

    cat >/etc/nginx/conf.d/local.conf <<'NGX'
# Public proxy server (port 80) — generated by go-vod-server installer
server {
  listen 80 default_server;
  server_name _;

  # Proxy HLS streaming
  # Pattern 1: /filename.json/playlist.m3u8 -> /hls/filename.json/master.m3u8
  location ~ ^/([^/]+\.json)/playlist\.m3u8$ {
    if ($request_method = OPTIONS) {
      add_header Access-Control-Allow-Origin * always;
      add_header Access-Control-Allow-Headers '*' always;
      add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
      add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
      add_header Content-Length 0;
      add_header Content-Type text/plain;
      return 204;
    }

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header Accept-Encoding "";
    proxy_pass http://127.0.0.1:8889/hls/$1/master.m3u8;

    proxy_buffering on;
    proxy_buffer_size 4k;
    proxy_buffers 8 4k;

    proxy_hide_header Access-Control-Allow-Origin;
    proxy_hide_header Access-Control-Allow-Headers;
    proxy_hide_header Access-Control-Allow-Methods;
    proxy_hide_header Access-Control-Expose-Headers;

    add_header Access-Control-Allow-Origin * always;
    add_header Access-Control-Allow-Headers '*' always;
    add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
    add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;

    sub_filter_types application/vnd.apple.mpegurl;
    sub_filter 'index-v1-a1.m3u8' 'video.m3u8';
    sub_filter_once off;
  }

  # Pattern 2: /filename.json/index.m3u8 or any .m3u8
  location ~ ^/([^/]+\.json)/(.+\.m3u8)$ {
    if ($request_method = OPTIONS) {
      add_header Access-Control-Allow-Origin * always;
      add_header Access-Control-Allow-Headers '*' always;
      add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
      add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
      add_header Content-Length 0;
      add_header Content-Type text/plain;
      return 204;
    }

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_pass http://127.0.0.1:8889/hls/$1/$2;

    proxy_buffering on;
    proxy_buffer_size 4k;
    proxy_buffers 8 4k;

    proxy_hide_header Access-Control-Allow-Origin;
    proxy_hide_header Access-Control-Allow-Headers;
    proxy_hide_header Access-Control-Allow-Methods;
    proxy_hide_header Access-Control-Expose-Headers;

    add_header Access-Control-Allow-Origin * always;
    add_header Access-Control-Allow-Headers '*' always;
    add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
    add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;

    sub_filter_types application/vnd.apple.mpegurl;
    sub_filter_once off;
  }

  # Pattern 3: /filename.json/segments (ts, m4s, etc.)
  location ~ ^/([^/]+\.json)/(.+)$ {
    if ($request_method = OPTIONS) {
      add_header Access-Control-Allow-Origin * always;
      add_header Access-Control-Allow-Headers '*' always;
      add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
      add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
      add_header Content-Length 0;
      add_header Content-Type text/plain;
      return 204;
    }

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_pass http://127.0.0.1:8889/hls/$1/$2;

    proxy_hide_header Access-Control-Allow-Origin;
    proxy_hide_header Access-Control-Allow-Headers;
    proxy_hide_header Access-Control-Allow-Methods;
    proxy_hide_header Access-Control-Expose-Headers;

    add_header Access-Control-Allow-Origin * always;
    add_header Access-Control-Allow-Headers '*' always;
    add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
    add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
  }

  # Pattern 4: Friendly URLs — /test/master.m3u8 -> /hls/test.json/master.m3u8
  location ~ ^/([^/]+)/master\.m3u8$ {
    if ($request_method = OPTIONS) {
      add_header Access-Control-Allow-Origin * always;
      add_header Access-Control-Allow-Headers '*' always;
      add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
      add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
      add_header Content-Length 0;
      add_header Content-Type text/plain;
      return 204;
    }

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header Accept-Encoding "";
    proxy_pass http://127.0.0.1:8889/hls/$1.json/master.m3u8;

    proxy_buffering on;
    proxy_buffer_size 4k;
    proxy_buffers 8 4k;

    proxy_hide_header Access-Control-Allow-Origin;
    proxy_hide_header Access-Control-Allow-Headers;
    proxy_hide_header Access-Control-Allow-Methods;
    proxy_hide_header Access-Control-Expose-Headers;

    add_header Access-Control-Allow-Origin * always;
    add_header Access-Control-Allow-Headers '*' always;
    add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
    add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;

    sub_filter_types application/vnd.apple.mpegurl;
    sub_filter_types text/plain;
    sub_filter 'index-v1-a1.m3u8' 'video.m3u8';
    sub_filter_once off;
  }

  # Pattern 5: /test/video.m3u8 -> /hls/test.json/index-v1-a1.m3u8
  location ~ ^/([^/]+)/video\.m3u8$ {
    if ($request_method = OPTIONS) {
      add_header Access-Control-Allow-Origin * always;
      add_header Access-Control-Allow-Headers '*' always;
      add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
      add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
      add_header Content-Length 0;
      add_header Content-Type text/plain;
      return 204;
    }

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header Accept-Encoding "";
    proxy_pass http://127.0.0.1:8889/hls/$1.json/index-v1-a1.m3u8;

    proxy_buffering on;
    proxy_buffer_size 4k;
    proxy_buffers 8 4k;

    proxy_hide_header Access-Control-Allow-Origin;
    proxy_hide_header Access-Control-Allow-Headers;
    proxy_hide_header Access-Control-Allow-Methods;
    proxy_hide_header Access-Control-Expose-Headers;

    add_header Access-Control-Allow-Origin * always;
    add_header Access-Control-Allow-Headers '*' always;
    add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
    add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;

    sub_filter_types application/vnd.apple.mpegurl;
    sub_filter_types text/plain;
    sub_filter 'seg-' 'v-';
    sub_filter '-v1-a1.ts' '.jpeg';
    sub_filter_once off;
  }

  # Pattern 6: /xxx/v-2.jpeg -> /hls/xxx.json/seg-2-v1-a1.ts
  location ~ ^/([^/]+)/v-(\d+)\.jpeg$ {
    limit_rate 3m;
    limit_rate_after 100k;

    if ($request_method = OPTIONS) {
      add_header Access-Control-Allow-Origin * always;
      add_header Access-Control-Allow-Headers '*' always;
      add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
      add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
      add_header Content-Length 0;
      add_header Content-Type text/plain;
      return 204;
    }

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_pass http://127.0.0.1:8889/hls/$1.json/seg-$2-v1-a1.ts;

    proxy_hide_header Access-Control-Allow-Origin;
    proxy_hide_header Access-Control-Allow-Headers;
    proxy_hide_header Access-Control-Allow-Methods;
    proxy_hide_header Access-Control-Expose-Headers;
    proxy_hide_header Content-Type;
    proxy_hide_header Cache-Control;
    proxy_hide_header Expires;
    proxy_hide_header last-modified;
    proxy_hide_header Server;
    proxy_hide_header Timing-Allow-Origin;
    proxy_hide_header Vary;
    proxy_hide_header Accept-Ranges;
    proxy_hide_header Connection;
    proxy_hide_header Date;

    add_header Content-Type 'image/jpeg' always;
    add_header Accept-Ranges 'bytes' always;
    add_header Access-Control-Allow-Origin '*' always;
    add_header Access-Control-Allow-Credentials 'false' always;
    add_header Cache-Control 'public, max-age=31536000, immutable' always;
    add_header Timing-Allow-Origin '*' always;
    add_header Vary 'Accept-Encoding' always;
    server_tokens off;
  }

  # Pattern 7: Catch-all — /test/anything -> /hls/test.json/anything
  location ~ ^/([^/]+)/(.*)$ {
    if ($request_method = OPTIONS) {
      add_header Access-Control-Allow-Origin * always;
      add_header Access-Control-Allow-Headers '*' always;
      add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
      add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;
      add_header Content-Length 0;
      add_header Content-Type text/plain;
      return 204;
    }

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header Accept-Encoding "";
    proxy_pass http://127.0.0.1:8889/hls/$1.json/$2;

    proxy_buffering on;
    proxy_buffer_size 4k;
    proxy_buffers 8 4k;

    proxy_hide_header Access-Control-Allow-Origin;
    proxy_hide_header Access-Control-Allow-Headers;
    proxy_hide_header Access-Control-Allow-Methods;
    proxy_hide_header Access-Control-Expose-Headers;

    add_header Access-Control-Allow-Origin * always;
    add_header Access-Control-Allow-Headers '*' always;
    add_header Access-Control-Allow-Methods 'GET, HEAD, OPTIONS' always;
    add_header Access-Control-Expose-Headers 'Server,range,Content-Length,Content-Range' always;

    sub_filter_types application/vnd.apple.mpegurl;
    sub_filter_types text/plain;
    sub_filter 'seg-' 'v-';
    sub_filter '-v1-a1.ts' '.jpeg';
    sub_filter_once off;
  }

  location = /healthz {
    return 200 "ok\n";
  }

  access_log /var/log/nginx/public.log;
  error_log /var/log/nginx/public-error.log warn;
}
NGX

    print_status "Testing nginx configuration..."
    if nginx -t 2>&1; then
        print_status "Restarting nginx..."
        systemctl enable nginx 2>/dev/null || true
        systemctl restart nginx
    else
        print_warning "nginx config test failed — check /etc/nginx/conf.d/local.conf"
    fi
else
    print_warning "nginx not found — skipping public proxy setup"
fi

# ─── Verify ───────────────────────────────────────────────────
sleep 2
RUNNING=0
if systemctl is-active --quiet ${SERVICE_NAME}; then
    RUNNING=1
fi

NGINX_RUNNING=0
if command -v nginx &>/dev/null && systemctl is-active --quiet nginx; then
    NGINX_RUNNING=1
fi

echo ""
echo "============================================"
if [ $RUNNING -eq 1 ]; then
    print_status "✅ go-vod-server is running (port $PORT)"
else
    print_warning "go-vod-server failed to start — check logs below"
    journalctl -u "${SERVICE_NAME}" -n 15 --no-pager
fi

if [ $NGINX_RUNNING -eq 1 ]; then
    print_status "✅ nginx public proxy is running (port 80)"
else
    print_warning "nginx is not running"
fi
echo "============================================"
echo ""
echo "  Directory:  $APP_DIR"
echo "  VOD Status: $(systemctl is-active ${SERVICE_NAME})"
echo "  Nginx:      $(systemctl is-active nginx 2>/dev/null || echo 'not installed')"
echo ""
echo "  Commands:"
echo "    View logs:   journalctl -u ${SERVICE_NAME} -f"
echo "    Restart:     systemctl restart ${SERVICE_NAME}"
echo "    Stop:        systemctl stop ${SERVICE_NAME}"
echo "    Nginx logs:  tail -f /var/log/nginx/public-error.log"
echo "    Uninstall:   curl -fsSL https://raw.githubusercontent.com/$GITHUB_REPO/main/install.sh | sudo bash -s -- --uninstall"
echo ""
echo "  Public URLs:"
echo "    http://YOUR_IP/test/master.m3u8"
echo "    http://YOUR_IP/test/video.m3u8"
echo "    http://YOUR_IP/healthz"
echo "============================================"

