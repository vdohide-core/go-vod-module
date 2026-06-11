package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"go-vod-module/internal/config"
	"go-vod-module/internal/server"
)

func main() {
	configPath := "config.json"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Fallback to parent directory if run from subdirectories
		configPath = "../config.json"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Printf("Warning: Failed to load config from %s (%v). Using default fallback config.", configPath, err)
		cfg = &config.Config{
			Port:                   8889,
			Mode:                   "mapped",
			UpstreamJSONURL:        "http://127.0.0.1:8888",
			DefaultSegmentDuration: 4000,
		}
	}

	mux := http.NewServeMux()
	server.RegisterHandlers(mux, cfg)

	log.Printf("Starting Golang VOD Server on port %d...", cfg.Port)
	log.Printf("VOD Mode: %s", cfg.Mode)
	if cfg.Mode == "local" {
		log.Printf("Media Root: %s", cfg.MediaRoot)
	} else {
		log.Printf("Upstream JSON Server: %s", cfg.UpstreamJSONURL)
	}
	log.Printf("Default segment duration: %d ms", cfg.DefaultSegmentDuration)

	addr := fmt.Sprintf(":%d", cfg.Port)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
