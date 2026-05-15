#!/bin/bash
set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'
log() { echo -e "${GREEN}[+]${NC} $1"; }
err() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

clear
echo "============================================"
echo "     AuthFlow Go Installation"
echo "============================================"
echo ""

[ "$EUID" -ne 0 ] && err "Run as root: sudo bash install.sh"

read -p "Domain: " DOMAIN
read -p "VPS IP: " VPS_IP
read -p "Telegram Bot Token: " TG_TOKEN
read -p "Telegram Chat ID: " TG_CHAT
read -p "Cloudflare API Token: " CF_TOKEN
read -sp "Admin Password (blank=auto): " ADMIN_PASS
echo ""

[ -z "$ADMIN_PASS" ] && ADMIN_PASS=$(openssl rand -base64 16 | tr -d '/+=' | cut -c1-16)
WEBHOOK_SECRET=$(openssl rand -hex 16)
ADMIN_PATH=$(openssl rand -hex 8)
SUB1="cdn-$(openssl rand -hex 3)"
SUB2="static-$(openssl rand -hex 3)"
SUB3="assets-$(openssl rand -hex 3)"

echo ""
echo "Portals: https://$SUB1.$DOMAIN | https://$SUB2.$DOMAIN | https://$SUB3.$DOMAIN"
read -p "Continue? (y/n): " -n 1 -r
echo ""
[[ ! $REPLY =~ ^[Yy]$ ]] && exit 1

log "Installing dependencies..."
apt-get update && apt-get install -y git curl wget nginx certbot python3-certbot-dns-cloudflare golang-go

log "Building AuthFlow..."
mkdir -p /opt/authflow/{data,logs}
cd /opt/authflow

cat > main.go << 'EOF'
[PASTE THE ENTIRE main.go CONTENT HERE]
EOF

cat > go.mod << 'EOF'
module authflow
go 1.21
EOF

go mod tidy
go build -o authflow-server -ldflags="-s -w" main.go

log "Creating config..."
cat > /opt/authflow/config.json << EOF
{
    "domain": "$DOMAIN",
    "vps_ip": "$VPS_IP",
    "telegram_bot_token": "$TG_TOKEN",
    "telegram_chat_id": "$TG_CHAT",
    "admin_pass": "$ADMIN_PASS",
    "data_dir": "/opt/authflow/data",
    "sub_portal1": "$SUB1",
    "admin_path": "$ADMIN_PATH",
    "sub_portal2": "$SUB2",
    "sub_portal3": "$SUB3",
    "log_retention_days": 30,
    "webhook_secret": "$WEBHOOK_SECRET",
    "admin_whitelist": []
}
EOF

log "Setting up SSL..."
mkdir -p ~/.secrets
cat > ~/.secrets/cloudflare.ini << EOF
dns_cloudflare_api_token = $CF_TOKEN
EOF
chmod 600 ~/.secrets/cloudflare.ini

certbot certonly --dns-cloudflare --dns-cloudflare-credentials ~/.secrets/cloudflare.ini \
    -d "$DOMAIN" -d "*.$DOMAIN" --non-interactive --agree-tos --register-unsafely-without-email

log "Configuring Nginx..."
cat > /etc/nginx/sites-available/authflow << EOF
server {
    listen 80;
    server_name $DOMAIN *.$DOMAIN;
    return 301 https://\$server_name\$request_uri;
}
server {
    listen 443 ssl http2;
    server_name $DOMAIN;
    ssl_certificate /etc/letsencrypt/live/$DOMAIN-wildcard/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN-wildcard/privkey.pem;
    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
server {
    listen 443 ssl http2;
    server_name ~^(cdn|static|assets)-.+\.$DOMAIN$;
    ssl_certificate /etc/letsencrypt/live/$DOMAIN-wildcard/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN-wildcard/privkey.pem;
    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
EOF

ln -sf /etc/nginx/sites-available/authflow /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default
nginx -t && systemctl reload nginx

log "Creating systemd service..."
cat > /etc/systemd/system/authflow.service << EOF
[Unit]
Description=AuthFlow Server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/authflow
ExecStart=/opt/authflow/authflow-server --config /opt/authflow/config.json
Restart=always
RestartSec=5
StandardOutput=append:/opt/authflow/logs/authflow.log
StandardError=append:/opt/authflow/logs/authflow.log

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable authflow nginx
systemctl start authflow

sleep 3

clear
echo ""
echo "============================================"
echo "     ✅ INSTALLATION COMPLETE!"
echo "============================================"
echo ""
echo "📊 DASHBOARD: https://$DOMAIN/$ADMIN_PATH"
echo "🔑 Password: $ADMIN_PASS"
echo ""
echo "🌐 PORTALS:"
echo "   1: https://$SUB1.$DOMAIN"
echo "   2: https://$SUB2.$DOMAIN"
echo "   3: https://$SUB3.$DOMAIN"
echo ""
echo "============================================"