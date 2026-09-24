package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// resolveConfigPath 解析配置文件路径：优先命令行 -config <path> 或 -config=<path>，
// 其次 YUFU_CONFIG 环境变量，最后回退默认相对路径 configs/server.yaml。
// 避免「在错误工作目录执行运维命令连到空库」的事故（#18）。
func resolveConfigPath() string {
	for i, a := range os.Args {
		if a == "-config" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if strings.HasPrefix(a, "-config=") {
			return strings.TrimPrefix(a, "-config=")
		}
	}
	if p := os.Getenv("YUFU_CONFIG"); p != "" {
		return p
	}
	return "configs/server.yaml"
}

func main() {
	// 子命令：yufu-server unlock <uuid> 解除单台；yufu-server unlock 解除全部（运维兜底）
	if len(os.Args) > 1 && os.Args[1] == "unlock" {
		cfgPath := resolveConfigPath()
		cfg, err := LoadConfig(cfgPath)
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		log.Printf("[probe] 加载配置: %s (db_path=%s)", cfgPath, cfg.DBPath)
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

	cfgPath := resolveConfigPath()
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	log.Printf("[probe] 加载配置: %s (db_path=%s)", cfgPath, cfg.DBPath)
	// 安全自检：默认凭据告警（仅告警，不致命，避免误 brick 已有部署）
	if cfg.Admin.Username == "admin" && cfg.Admin.Password == "admin" {
		log.Printf("[SECURITY] 检测到默认管理员凭据 admin/admin，请立即在 configs/server.yaml 修改 admin.password！")
	}
	if cfg.AgentToken == "change-me-agent-token" {
		log.Printf("[SECURITY] 检测到默认 agent_token change-me-agent-token，请立即修改，否则任何人可用它注册机器！")
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
	live.LoadFromDB(db, curMonth())
	// 启动后立即清理一次"幽灵孤儿"（online=0 且 last_seen=0 的僵尸记录），
	// 防止历史上误入库的离线孤儿在面板冒出来。
	live.CleanupStale(db)

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
			live.Flush(db, curMonth())
		}
	}()
	go func() {
		t := time.NewTicker(10 * time.Second)
		for range t.C {
			live.SetOffline(int64(cfg.OfflineThreshold))
			// 周期清理"幽灵孤儿"：online=0 且 last_seen=0 的僵尸记录，
			// 防止运行期误入库的离线孤儿污染面板（真实离线机 last_seen>0 不受影响）。
			live.CleanupStale(db)
		}
	}()

	// 定期清理：过期访客链接 + 30 天以上会话，避免两张表只增不减（#11）
	go func() {
		t := time.NewTicker(24 * time.Hour)
		for range t.C {
			now := time.Now().Unix()
			if _, err := db.Exec(`DELETE FROM visitor_links WHERE expires_at < ?`, now); err != nil {
				log.Printf("[probe] 清理过期访客链接失败: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM sessions WHERE created_at < ?`, now-30*86400); err != nil {
				log.Printf("[probe] 清理过老会话失败: %v", err)
			}
		}
	}()

	// 自动部署调度器：每 10s 扫描规则，对源分组内待部署且在线的机器自动下发命令，
	// 成功(exit 0)→移到目标分组，失败/超时→移到失败分组。
	go runDeployScheduler(cfg, db, hub)

	router := setupRoutes(cfg, db, hub)
	// 全局请求体大小限制（4MB），防止超大 body 撑爆内存（#10）
	limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		router.ServeHTTP(w, r)
	})
	addr := fmt.Sprintf("%s:%d", cfg.Listen, cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           limited,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if cfg.TLS.Enabled {
		log.Printf("[probe] listening on https://%s (TLS on)", addr)
		log.Fatal(srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key))
	}
	log.Printf("[probe] listening on http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}
