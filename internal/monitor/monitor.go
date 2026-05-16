package monitor

import "strings"

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
