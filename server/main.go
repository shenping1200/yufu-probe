package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
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

	// 子命令：yufu-server hash-password 生成 bcrypt 哈希，
	// 供运维替换 configs/server.yaml 的 admin.password（迁移到哈希校验，避免明文落盘）。
	// V5-3：优先从 stdin 读取明文口令（避免明文出现在进程列表 / shell 历史）；
	//       也兼容命令行参数 <明文>（但会提示不推荐）。
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		var pw string
		if len(os.Args) >= 3 {
			pw = os.Args[2]
			log.Printf("[WARN] 通过命令行参数传入明文口令会出现在进程列表与 shell 历史中，" +
				"建议改用 stdin：echo -n '口令' | yufu-server hash-password")
		} else {
			data, rerr := io.ReadAll(os.Stdin)
			if rerr != nil {
				log.Fatalf("读取 stdin 失败: %v", rerr)
			}
			pw = strings.TrimRight(string(data), "\r\n")
			if pw == "" {
				fmt.Fprintln(os.Stderr, "用法: echo -n '明文口令' | yufu-server hash-password  或  yufu-server hash-password <明文口令>")
				os.Exit(2)
			}
		}
		// V5-4：bcrypt 仅取前 72 字节，超长口令生成会直接报错且无中文提示；提前拦截给出人话。
		if len(pw) > 72 {
			log.Fatalf("口令过长：bcrypt 上限为 72 字节，请缩短后再生成（当前 %d 字节）", len(pw))
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("生成 bcrypt 哈希失败: %v", err)
		}
		fmt.Println(string(hash))
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
	// 安全自检：管理员口令为明文（非 bcrypt 哈希）时告警，提示迁移到哈希存储
	if !isBcryptHash(cfg.Admin.Password) {
		log.Printf("[SECURITY] 管理员口令为明文存储，建议使用 bcrypt 哈希（运行 `yufu-server hash-password <口令>` 生成后替换 configs/server.yaml 的 admin.password）")
	}
	// V5-5：admin.password 形似 bcrypt 哈希时，启动期探一次合法性（用错误口令比对），
	// 若哈希本身解析失败（复制截断/格式错），登录将永远失败且无诊断——提前告警。
	if isBcryptHash(cfg.Admin.Password) {
		if err := bcrypt.CompareHashAndPassword([]byte(cfg.Admin.Password), []byte("__startup_probe__")); err != nil && err != bcrypt.ErrMismatchedHashAndPassword {
			log.Printf("[SECURITY] admin.password 形似 bcrypt 哈希但解析失败（可能复制截断或格式错误）：%v；登录将永远失败，请重新生成哈希", err)
		}
	}
	// V5-1：管理员口令已是 bcrypt 哈希、却未显式配置 ssh_password 时，Web SSH / 批量执行 /
	// 自动部署会因「哈希当明文比对」全部失败，且 Web SSH/批量会触发 24h 锁定——提前告警。
	if isBcryptHash(cfg.Admin.Password) && cfg.SSHPassword == "" {
		log.Printf("[SECURITY] 管理员口令已用 bcrypt 哈希存储，但未显式配置 ssh_password：" +
			"Web SSH / 批量执行 / 自动部署会把哈希当明文口令比对，将全部失败（且 Web SSH/批量会触发 24h 锁定）。" +
			"请在 configs/server.yaml 中显式设置 ssh_password（明文），再迁移 admin.password")
	}
	db, err := InitDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	// Web SSH 密码回退提示：未显式设置 ssh_password 时，明文部署会复用管理员密码；
	// 若 admin.password 已为 bcrypt 哈希则不会复用（V5-1，配合下方 [SECURITY] 告警）。
	if cfg.SSHPassword == "" {
		if isBcryptHash(cfg.Admin.Password) {
			log.Printf("[probe] ssh_password 未设置，且 admin.password 为 bcrypt 哈希：Web SSH / 批量 / 自动部署将不可用，请显式设置 ssh_password（见上方 [SECURITY] 告警）")
		} else {
			log.Printf("[probe] ssh_password 未设置，Web SSH 将复用管理员密码（建议在 server.yaml 显式设置 ssh_password）")
		}
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

	// 登录失败记录回收：周期性清理过期条目，避免一次性扫描 IP 永久残留导致 map 缓慢增长（v3 🟡）
	startLoginFailsReaper()

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
	// 注意：此处有意不设置 WriteTimeout。
	// 批量执行/部署接口（/api/agents/exec）为同步阻塞，最长允许 600 秒（exec.go:469），
	// 若设 WriteTimeout 会在 60s 处掐断长任务，导致客户端拿到连接被重置而非执行结果（回归 N1）。
	// 慢速攻击防护已由 ReadHeaderTimeout / ReadTimeout（防慢速读）与 IdleTimeout（防空闲 keepalive）覆盖。
	// 正解是把 exec 改为异步（提交返回 job id、前端轮询），可顺带解调度 ticker stall，留作后续优化。
	srv := &http.Server{
		Addr:              addr,
		Handler:           limited,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if cfg.TLS.Enabled {
		log.Printf("[probe] listening on https://%s (TLS on)", addr)
		log.Fatal(srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key))
	}
	log.Printf("[probe] listening on http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}
