package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	// 子命令：yufu-server unlock <uuid> 解除单台；yufu-server unlock 解除全部（运维兜底）
	if len(os.Args) > 1 && os.Args[1] == "unlock" {
		cfg, err := LoadConfig("configs/server.yaml")
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		db, err := InitDB(cfg.DBPath)
		if err != nil {
			log.Fatalf("init db: %v", err)
		}
		if len(os.Args) > 2 {
			if err := UnlockSSH(db, os.Args[2]); err != nil {
				log.Fatalf("unlock: %v", err)
			}
			fmt.Printf("已解除 SSH 锁定: %s\n", os.Args[2])
		} else {
			if err := UnlockAllSSH(db); err != nil {
				log.Fatalf("unlock all: %v", err)
			}
			fmt.Println("已一键解除全部 SSH 锁定")
		}
		return
	}

	cfg, err := LoadConfig("configs/server.yaml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	db, err := InitDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	// Web SSH 密码回退提示：未显式设置 ssh_password 时，复用管理员密码
	if cfg.SSHPassword == "" {
		log.Printf("[probe] ssh_password 未设置，Web SSH 将复用管理员密码（建议在 server.yaml 显式设置 ssh_password）")
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
