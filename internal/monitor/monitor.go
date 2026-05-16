package monitor

import (
	"net/http"
	"strings"
)

var BotPatterns = []string{
    "googlebot", "bingbot", "yandexbot", "duckduckbot", "slurp", "baiduspider",
    "facebot", "twitterbot", "linkedinbot", "whatsapp", "discordbot", "slackbot",
    "telegrambot", "applebot", "virustotal", "urlscan", "crawler", "spider", "scanner",
    "curl", "wget", "python-requests", "go-http-client", "node-fetch", "axios",
    "headless", "puppeteer", "playwright", "chatgpt", "claude", "censys", "shodan",
}

func IsBot(userAgent string) (bool, string) {
    if userAgent == "" {
        return true, "Empty UA"
    }
    lower := strings.ToLower(userAgent)
    for _, pattern := range BotPatterns {
        if strings.Contains(lower, pattern) {
            return true, pattern
        }
    }
    if len(userAgent) < 30 && !strings.Contains(lower, "mozilla") {
        return true, "Short UA"
    }
    return false, ""
}

func GetClientIP(header http.Header, remoteAddr string) string {
	if xff := header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}
	if xri := header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	if strings.Contains(remoteAddr, ":") {
		return strings.Split(remoteAddr, ":")[0]
	}
	return remoteAddr
}

func MaskIP(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		return parts[0] + "." + parts[1] + "." + parts[2] + ".***"
	}
	return ip
}
