package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/fdcs99/biligo/internal/config"
	"github.com/fdcs99/biligo/internal/httpapi"
	"github.com/fdcs99/biligo/internal/panelauth"
	"github.com/fdcs99/biligo/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg.GeneratedPanelPassword != "" {
		log.Printf("panel auth password generated and written to %s: %s", cfg.Path, cfg.GeneratedPanelPassword)
	}

	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	pausedTasks, err := db.PauseInterruptedTasks(context.Background())
	if err != nil {
		log.Fatalf("pause interrupted tasks: %v", err)
	}
	if len(pausedTasks) > 0 {
		log.Printf("paused %d interrupted task(s) from previous run", len(pausedTasks))
	}

	router := httpapi.NewRouter(db, panelauth.NewManager(cfg.Auth.Password, 24*time.Hour))
	log.Printf("biligo server listening on %s", cfg.Server.Addr)
	if err := router.Run(cfg.Server.Addr); err != nil {
		log.Fatalf("run server: %v", err)
	}
}
