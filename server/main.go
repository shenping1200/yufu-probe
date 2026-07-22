package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	cfg, err := LoadConfig("configs/server.yaml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	db, err := InitDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	hub := NewHub()
	go offlineScanner(db)

	router := setupRoutes(cfg, db, hub)
	addr := fmt.Sprintf("%s:%d", cfg.Listen, cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.TLS.Enabled {
		log.Printf("[probe] listening on https://%s (TLS on)", addr)
		log.Fatal(srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key))
	}
	log.Printf("[probe] listening on http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}
