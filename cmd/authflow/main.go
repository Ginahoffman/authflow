package main

import (
    "encoding/json"
    "flag"
    "log"
    "os"

    "authflow/internal/server"
)

func main() {
    configPath := flag.String("config", "/opt/authflow/config/config.json", "config file path")
    port := flag.Int("port", 8080, "server port")
    flag.Parse()

    data, err := os.ReadFile(*configPath)
    if err != nil {
        log.Fatalf("Failed to read config: %v", err)
    }

    var cfg server.Config
    if err := json.Unmarshal(data, &cfg); err != nil {
        log.Fatalf("Failed to parse config: %v", err)
    }

    srv, err := server.New(cfg)
    if err != nil {
        log.Fatalf("Failed to create server: %v", err)
    }

    if err := srv.Start(*port); err != nil {
        log.Fatalf("Server error: %v", err)
    }
}
