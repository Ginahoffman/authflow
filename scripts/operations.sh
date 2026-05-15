#!/bin/bash
# ============================================================================
# AuthFlow Operations Quick Reference
# ============================================================================
# Common commands for managing an installed AuthFlow system
# ============================================================================

# Color codes
readonly GREEN='\033[0;32m'
readonly BLUE='\033[0;34m'
readonly YELLOW='\033[0;33m'
readonly NC='\033[0m'

show_menu() {
    echo ""
    echo -e "${BLUE}AuthFlow Operations Menu${NC}"
    echo "================================"
    echo ""
    echo -e "${GREEN}Service Management:${NC}"
    echo "  1. Status          - Show service status"
    echo "  2. Restart         - Restart AuthFlow"
    echo "  3. Stop            - Stop AuthFlow"
    echo "  4. Start           - Start AuthFlow"
    echo "  5. Logs            - View live logs"
    echo "  6. Log (recent)    - View recent logs"
    echo ""
    echo -e "${GREEN}System Checks:${NC}"
    echo "  7. Health          - Full health check"
    echo "  8. Certs           - Check TLS certificates"
    echo "  9. Nginx           - Check nginx status"
    echo " 10. Disk            - Check disk usage"
    echo " 11. Resources       - Check CPU/Memory"
    echo ""
    echo -e "${GREEN}Configuration:${NC}"
    echo " 12. Config          - View configuration"
    echo " 13. Dashboard URL   - Show dashboard URL"
    echo " 14. Edit Config     - Edit config (nano)"
    echo ""
    echo -e "${GREEN}Updates & Maintenance:${NC}"
    echo " 15. Renew Cert      - Force certificate renewal"
    echo " 16. Reload Nginx    - Reload nginx"
    echo " 17. Update App      - Pull latest code & rebuild"
    echo " 18. Backup          - Create backup"
    echo ""
    echo -e "${GREEN}Troubleshooting:${NC}"
    echo " 19. Firewall        - Check firewall rules"
    echo " 20. Ports           - Check open ports"
    echo " 21. Errors          - View recent errors"
    echo ""
    echo "  0. Exit"
    echo ""
    echo "================================"
}

# ============================================================================
# Service Management Functions
# ============================================================================

service_status() {
    echo -e "\n${BLUE}=== AuthFlow Service Status ===${NC}\n"
    sudo systemctl status authflow --no-pager
    echo ""
    echo -e "${BLUE}Recent logs:${NC}"
    sudo journalctl -u authflow -n 10 --no-pager
}

service_restart() {
    echo -e "\n${YELLOW}Restarting AuthFlow...${NC}\n"
    sudo systemctl restart authflow
    sleep 2
    echo -e "${GREEN}✓ Service restarted${NC}"
    sudo systemctl status authflow --no-pager
}

service_stop() {
    echo -e "\n${YELLOW}Stopping AuthFlow...${NC}\n"
    sudo systemctl stop authflow
    sleep 1
    echo -e "${GREEN}✓ Service stopped${NC}"
    sudo systemctl status authflow --no-pager
}

service_start() {
    echo -e "\n${YELLOW}Starting AuthFlow...${NC}\n"
    sudo systemctl start authflow
    sleep 2
    echo -e "${GREEN}✓ Service started${NC}"
    sudo systemctl status authflow --no-pager
}

service_logs() {
    echo -e "\n${BLUE}=== AuthFlow Live Logs (Press Ctrl+C to exit) ===${NC}\n"
    sudo journalctl -u authflow -f
}

service_logs_recent() {
    echo -e "\n${BLUE}=== AuthFlow Recent Logs (Last 50 entries) ===${NC}\n"
    sudo journalctl -u authflow -n 50 --no-pager
}

# ============================================================================
# System Check Functions
# ============================================================================

health_check() {
    echo -e "\n${BLUE}=== AuthFlow Health Check ===${NC}\n"
    
    echo -e "${YELLOW}1. Service Status:${NC}"
    if sudo systemctl is-active --quiet authflow; then
        echo -e "   ${GREEN}✓ Running${NC}"
    else
        echo -e "   ${YELLOW}✗ Not running${NC}"
    fi
    
    echo -e "\n${YELLOW}2. Process Check:${NC}"
    if pgrep -f "authflow-server" > /dev/null; then
        PID=$(pgrep -f "authflow-server")
        echo -e "   ${GREEN}✓ Process running (PID: $PID)${NC}"
    else
        echo -e "   ${YELLOW}✗ Process not found${NC}"
    fi
    
    echo -e "\n${YELLOW}3. Port Check (8080):${NC}"
    if sudo netstat -tunap 2>/dev/null | grep -q 8080; then
        echo -e "   ${GREEN}✓ Port 8080 listening${NC}"
    else
        echo -e "   ${YELLOW}✗ Port 8080 not listening${NC}"
    fi
    
    echo -e "\n${YELLOW}4. nginx Status:${NC}"
    if sudo systemctl is-active --quiet nginx; then
        echo -e "   ${GREEN}✓ nginx running${NC}"
    else
        echo -e "   ${YELLOW}✗ nginx not running${NC}"
    fi
    
    echo -e "\n${YELLOW}5. TLS Certificates:${NC}"
    DOMAIN=$(sudo jq -r '.domain' /opt/authflow/config.json)
    if sudo certbot certificates 2>/dev/null | grep -q "$DOMAIN"; then
        echo -e "   ${GREEN}✓ Certificate found for $DOMAIN${NC}"
    else
        echo -e "   ${YELLOW}✗ Certificate not found${NC}"
    fi
    
    echo -e "\n${YELLOW}6. Configuration File:${NC}"
    if [[ -f /opt/authflow/config.json ]]; then
        echo -e "   ${GREEN}✓ config.json exists${NC}"
        if jq empty /opt/authflow/config.json 2>/dev/null; then
            echo -e "   ${GREEN}✓ JSON is valid${NC}"
        else
            echo -e "   ${YELLOW}✗ JSON is invalid${NC}"
        fi
    else
        echo -e "   ${YELLOW}✗ config.json not found${NC}"
    fi
    
    echo -e "\n${YELLOW}7. Data Directory:${NC}"
    if [[ -d /opt/authflow/data ]]; then
        USED=$(du -sh /opt/authflow/data 2>/dev/null | cut -f1)
        echo -e "   ${GREEN}✓ Data directory exists ($USED)${NC}"
    else
        echo -e "   ${YELLOW}✗ Data directory not found${NC}"
    fi
}

check_certs() {
    echo -e "\n${BLUE}=== TLS Certificate Status ===${NC}\n"
    sudo certbot certificates --no-pager
    echo ""
    echo -e "${YELLOW}Certificate locations:${NC}"
    sudo find /etc/letsencrypt/live -name "*.pem" | head -10
}

check_nginx() {
    echo -e "\n${BLUE}=== nginx Status ===${NC}\n"
    echo -e "${YELLOW}Service Status:${NC}"
    sudo systemctl status nginx --no-pager
    echo ""
    echo -e "${YELLOW}Configuration Test:${NC}"
    sudo nginx -t
}

check_disk() {
    echo -e "\n${BLUE}=== Disk Usage ===${NC}\n"
    echo -e "${YELLOW}Overall:${NC}"
    df -h /
    echo ""
    echo -e "${YELLOW}AuthFlow Directory:${NC}"
    sudo du -sh /opt/authflow/*
}

check_resources() {
    echo -e "\n${BLUE}=== System Resources ===${NC}\n"
    echo -e "${YELLOW}CPU & Memory:${NC}"
    top -bn1 | grep -E "Cpu|Mem" | head -2
    echo ""
    echo -e "${YELLOW}AuthFlow Process:${NC}"
    sudo ps aux | grep authflow-server | grep -v grep || echo "Process not running"
    echo ""
    echo -e "${YELLOW}Open Connections:${NC}"
    sudo netstat -an | grep ESTABLISHED | wc -l
}

# ============================================================================
# Configuration Functions
# ============================================================================

show_config() {
    echo -e "\n${BLUE}=== AuthFlow Configuration ===${NC}\n"
    sudo jq . /opt/authflow/config.json
}

show_dashboard_url() {
    echo -e "\n${BLUE}=== Dashboard Access ===${NC}\n"
    DOMAIN=$(sudo jq -r '.domain' /opt/authflow/config.json)
    ADMIN_PATH=$(sudo jq -r '.admin_path' /opt/authflow/config.json)
    echo -e "${GREEN}Dashboard URL:${NC}"
    echo "https://$DOMAIN/$ADMIN_PATH"
    echo ""
    echo -e "${GREEN}Username:${NC} admin"
    echo -e "${GREEN}Password:${NC} (see config.json)"
}

edit_config() {
    echo -e "\n${YELLOW}Opening configuration editor...${NC}\n"
    sudo nano /opt/authflow/config.json
    echo ""
    echo -e "${YELLOW}Restarting service to apply changes...${NC}"
    sudo systemctl restart authflow
    echo -e "${GREEN}✓ Service restarted${NC}"
}

# ============================================================================
# Maintenance Functions
# ============================================================================

renew_cert() {
    echo -e "\n${YELLOW}Force renewing certificates...${NC}\n"
    sudo certbot renew --force-renewal
    echo ""
    echo -e "${YELLOW}Reloading nginx...${NC}"
    sudo systemctl reload nginx
    echo -e "${GREEN}✓ Certificates renewed and nginx reloaded${NC}"
}

reload_nginx() {
    echo -e "\n${YELLOW}Testing nginx configuration...${NC}"
    sudo nginx -t || return 1
    echo -e "\n${YELLOW}Reloading nginx...${NC}\n"
    sudo systemctl reload nginx
    echo -e "${GREEN}✓ nginx reloaded${NC}"
}

update_app() {
    echo -e "\n${YELLOW}Pulling latest code...${NC}\n"
    cd /opt/authflow || return 1
    sudo git pull origin main
    
    echo -e "\n${YELLOW}Building application...${NC}\n"
    sudo go build -ldflags="-s -w" -o authflow-server
    
    echo -e "\n${YELLOW}Restarting service...${NC}\n"
    sudo systemctl restart authflow
    echo -e "${GREEN}✓ Application updated and restarted${NC}"
}

backup_system() {
    echo -e "\n${YELLOW}Creating backup...${NC}\n"
    BACKUP_FILE="/root/authflow-backup-$(date +%Y%m%d-%H%M%S).tar.gz"
    sudo tar -czf "$BACKUP_FILE" \
        /opt/authflow/data \
        /opt/authflow/config.json \
        /etc/letsencrypt/live \
        --exclude='/opt/authflow/data/.git'
    
    echo -e "${GREEN}✓ Backup created: $BACKUP_FILE${NC}"
    ls -lh "$BACKUP_FILE"
}

# ============================================================================
# Troubleshooting Functions
# ============================================================================

check_firewall() {
    echo -e "\n${BLUE}=== Firewall Status ===${NC}\n"
    echo -e "${YELLOW}UFW Status:${NC}"
    sudo ufw status numbered || echo "UFW not enabled"
    echo ""
    echo -e "${YELLOW}Open Ports:${NC}"
    sudo netstat -tulpn | grep LISTEN
}

check_ports() {
    echo -e "\n${BLUE}=== Port Status ===${NC}\n"
    echo -e "${YELLOW}Port 80 (HTTP):${NC}"
    sudo lsof -i :80 2>/dev/null || echo "  Not listening"
    echo ""
    echo -e "${YELLOW}Port 443 (HTTPS):${NC}"
    sudo lsof -i :443 2>/dev/null || echo "  Not listening"
    echo ""
    echo -e "${YELLOW}Port 8080 (AuthFlow):${NC}"
    sudo lsof -i :8080 2>/dev/null || echo "  Not listening"
}

view_errors() {
    echo -e "\n${BLUE}=== Recent Errors ===${NC}\n"
    echo -e "${YELLOW}AuthFlow Errors:${NC}"
    sudo journalctl -u authflow -p err -n 20 --no-pager
    echo ""
    echo -e "${YELLOW}nginx Errors:${NC}"
    sudo tail -20 /var/log/nginx/authflow_error.log
}

# ============================================================================
# Main Loop
# ============================================================================

main() {
    while true; do
        show_menu
        read -p "Select option (0-21): " choice
        
        case $choice in
            1) service_status ;;
            2) service_restart ;;
            3) service_stop ;;
            4) service_start ;;
            5) service_logs ;;
            6) service_logs_recent ;;
            7) health_check ;;
            8) check_certs ;;
            9) check_nginx ;;
            10) check_disk ;;
            11) check_resources ;;
            12) show_config ;;
            13) show_dashboard_url ;;
            14) edit_config ;;
            15) renew_cert ;;
            16) reload_nginx ;;
            17) update_app ;;
            18) backup_system ;;
            19) check_firewall ;;
            20) check_ports ;;
            21) view_errors ;;
            0) echo -e "\n${GREEN}Goodbye!${NC}\n"; exit 0 ;;
            *) echo -e "\n${YELLOW}Invalid option${NC}\n" ;;
        esac
        
        echo ""
        read -p "Press Enter to continue..."
    done
}

# Run main function if script is executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    [[ $EUID -ne 0 ]] && { echo "This script must be run as root"; exit 1; }
    main
fi
