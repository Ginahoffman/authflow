# 📋 Quick Pre-Push Checklist

## Before Pushing to GitHub - Run These Checks

### Step 1: Verify Git Status
```bash
cd /home/tartmo/authflow
git status
```

**Should show:**
- ✅ Modified: `.gitignore`
- ✅ Added: `scripts/install-prod.sh`, `scripts/operations.sh`, `scripts/.env.template`
- ✅ Added: Documentation files (SECURITY_AUDIT.md, FINAL_VERIFICATION.md, etc.)
- ❌ NO `config.json` or `.env` files
- ❌ NO binaries

---

### Step 2: Verify .gitignore is Correct
```bash
cat /home/tartmo/authflow/.gitignore
```

**Should include:**
```
# Binaries
authflow-server
webanalyzer
webanalyzer-cli

# Configuration & Secrets
config.json
*.ini
.env
.env.production
.env.local

# Data & Logs
data/
logs/
*.db
*.db-journal

# Build artifacts
build/
dist/

# Old files
scripts/install.sh
```

---

### Step 3: Clean Up Old Files
```bash
rm -f scripts/install.sh 2>/dev/null
rm -f config.json 2>/dev/null
rm -f authflow-server 2>/dev/null
go clean -cache
```

---

### Step 4: Verify Compilation
```bash
cd /home/tartmo/authflow
go mod tidy
go build ./...
```

**Should output:**
- Nothing (clean compilation) or
- ✅ "Successfully compiled"

**Should NOT output:**
- ❌ Errors
- ❌ "undefined" references
- ❌ "missing" imports

---

### Step 5: Verify Folder Contents
```bash
find . -name "*.go" -type f | sort
```

**Should show:**
```
./cmd/webanalyzer/main.go
./cmd/webanalyzer-cli/main.go
./internal/monitor/monitor.go
./internal/server/server.go
./internal/storage/storage.go
./internal/webhook/webhook.go
```

---

### Step 6: Verify Key Files Exist
```bash
ls -lh scripts/install-prod.sh scripts/operations.sh scripts/.env.template
```

**Should show all three files present**

---

### Step 7: Final Git Status
```bash
git status --short
```

**Should show:**
```
 M .gitignore
?? FINAL_VERIFICATION.md
?? SECURITY_AUDIT.md
?? scripts/.env.template
?? scripts/DEPLOYMENT.md
?? scripts/README.md
?? scripts/install-prod.sh
?? scripts/operations.sh
```

**Should NOT show:**
- ❌ `config.json`
- ❌ `.env` files
- ❌ Binaries
- ❌ `/data/` directory

---

## Push to GitHub

### If First Time:
```bash
cd /home/tartmo/authflow
git init
git add .
git commit -m "Initial AuthFlow + Evilginx2 integration with production installer"
git remote add origin https://github.com/yourusername/authflow.git
git branch -M main
git push -u origin main
```

### If Already Initialized:
```bash
cd /home/tartmo/authflow
git add .
git commit -m "Add production installer and security audit"
git push origin main
```

---

## After Push: Deploy on VPS

### 1. Clone Repository
```bash
mkdir -p /opt/authflow
cd /opt/authflow
git clone https://github.com/yourusername/authflow.git .
```

### 2. Create Configuration
```bash
cp scripts/.env.template .env.production
# Edit with your credentials
nano .env.production
```

**Required variables:**
```bash
DOMAIN=yourdomain.com
VPS_IP=your.vps.ip.address
CLOUDFLARE_TOKEN=your_cloudflare_api_token
TELEGRAM_TOKEN=your_telegram_bot_token
TELEGRAM_CHAT=your_telegram_chat_id
ADMIN_PASS=secure_admin_password
```

### 3. Run Installer
```bash
source /opt/authflow/.env.production
sudo -E /opt/authflow/scripts/install-prod.sh
```

### 4. Verify Installation
```bash
# Check service
sudo systemctl status authflow

# View logs
sudo journalctl -u authflow -f

# Access dashboard
curl -u admin:ADMIN_PASS https://yourdomain.com/admin123
```

---

## ✅ Pre-Push Verification Summary

| Check | Command | Expected |
|-------|---------|----------|
| Git status | `git status` | No untracked secrets |
| .gitignore | `cat .gitignore` | Excludes config/data/binaries |
| Compilation | `go build ./...` | No errors |
| Files exist | `ls scripts/*prod*` | install-prod.sh present |
| Ready | All above | ✅ YES |

---

**Status**: Ready to push to GitHub ✅
