package monitor

import (
	"strings"
)

var BotPatterns = []string{
	"googlebot", "bingbot", "yandexbot", "duckduckbot", "slurp", "baiduspider",
	"facebot", "twitterbot", "linkedinbot", "whatsapp", "discordbot", "slackbot",
	"telegrambot", "applebot", "virustotal", "urlscan", "netcraft",
	"bitdefender", "crawler", "spider", "scanner", "curl", "wget",
	"python-requests", "go-http-client", "node-fetch", "axios", "okhttp",
	"headless", "phantom", "puppeteer", "playwright", "chatgpt", "claude",
	"censys", "shodan", "zgrab", "nmap", "masscan", "nikto", "nessus", "hydra",
	"burp", "zap", "acunetix", "openvas", "wpscan", "joomscan", "nikto",
	"qualys", "rapid7", "tenable", "nuclei", "gobuster", "dirb", "wfuzz",
}

func IsBot(userAgent string) (bool, string) {
	if userAgent == "" {
		return true, "Empty UA"
	}
	
	lower := strings.ToLower(userAgent)
	
	// Check known bot patterns
	for _, pattern := range BotPatterns {
		if strings.Contains(lower, pattern) {
			return true, pattern
		}
	}
	
	// Check for suspicious patterns
	if strings.Contains(lower, "+http") || strings.Contains(lower, "+https") {
		return true, "URL in UA"
	}
	
	// Very short user agents are likely bots
	if len(userAgent) < 30 && !strings.Contains(lower, "mozilla") && !strings.Contains(lower, "applewebkit") {
		return true, "Short UA"
	}
	
	// Check for headless browsers
	if strings.Contains(lower, "headless") || strings.Contains(lower, "phantom") {
		return true, "Headless"
	}
	
	return false, ""
}

func GetClientIP(headers map[string][]string, remoteAddr string) string {
	// Check Cloudflare
	if cf, ok := headers["Cf-Connecting-Ip"]; ok && len(cf) > 0 && cf[0] != "" {
		return cf[0]
	}
	
	// Check X-Forwarded-For
	if xff, ok := headers["X-Forwarded-For"]; ok && len(xff) > 0 && xff[0] != "" {
		ips := strings.Split(xff[0], ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}
	
	// Check X-Real-IP
	if xri, ok := headers["X-Real-Ip"]; ok && len(xri) > 0 && xri[0] != "" {
		return xri[0]
	}
	
	// Fallback to remote address
	ip := remoteAddr
	if idx := strings.Index(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	
	// Remove IPv6 prefix
	if strings.HasPrefix(ip, "::ffff:") {
		ip = ip[7:]
	}
	
	return ip
}

func GetSourceFromHost(host string, endpoints map[string]string) string {
	for name, endpoint := range endpoints {
		if strings.Contains(host, endpoint) {
			return name
		}
	}
	return "unknown"
}

func MaskIP(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ip
	}
	return parts[0] + "." + parts[1] + "." + "xxx.xxx"
}