package main

import (
    "encoding/json"
    "flag"
    "log"
    "os"

    "authflow/internal/server"
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
}

func main() {
    configPath := flag.String("config", "/opt/authflow/config.json", "config file path")
    port := flag.Int("port", 8080, "server port")
    flag.Parse()

    data, err := os.ReadFile(*configPath)
    if err != nil {
        log.Fatalf("Failed to read config: %v", err)
    }

    var cfg Config
    if err := json.Unmarshal(data, &cfg); err != nil {
        log.Fatalf("Failed to parse config: %v", err)
    }

    srv, err := server.New(server.Config{
        Domain:        cfg.Domain,
        VpsIp:         cfg.VpsIp,
        TelegramToken: cfg.TelegramToken,
        TelegramChat:  cfg.TelegramChat,
        AdminPass:     cfg.AdminPass,
        DataDir:       cfg.DataDir,
        Endpoints:     cfg.Endpoints,
        AdminPath:     cfg.AdminPath,
        RetentionDays: cfg.RetentionDays,
        WebhookSecret: cfg.WebhookSecret,
    })
    if err != nil {
        log.Fatalf("Failed to create server: %v", err)
    }

    if err := srv.Start(*port); err != nil {
        log.Fatalf("Server error: %v", err)
    }
}
