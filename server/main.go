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
	// 启动时把 DB 全量载入内存，作为实时状态基盘
	live.LoadFromDB(db, time.Now().Format("2006-01"))

	// 三个独立周期任务，与上报速率解耦：
	//  - 每秒向所有 viewer 广播一次内存快照（固定 1 次/秒，不再每条上报都广播）
	//  - 每 2 秒把脏数据批量落库（避免每条上报都写 DB）
	//  - 每 10 秒扫描一次离线（超时未上报标记为离线）
	go func() {
		t := time.NewTicker(1 * time.Second)
		for range t.C {
			broadcastAgents(hub)
		}
	}()
	go func() {
		t := time.NewTicker(2 * time.Second)
		for range t.C {
			live.Flush(db, time.Now().Format("2006-01"))
		}
	}()
	go func() {
		t := time.NewTicker(10 * time.Second)
		for range t.C {
			live.SetOffline(15)
		}
	}()

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
