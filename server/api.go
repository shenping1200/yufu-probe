package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

//go:embed static
var staticFS embed.FS

var staticSubFS fs.FS

func init() {
	sub, err := fs.Sub(staticFS, "static")
	if err == nil {
		staticSubFS = sub
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// 源码仓库全名（用于生成安装命令的下载链接），与 install-agent.sh 默认仓库保持一致
const repoOwner = "shenping1200"
const repoName  = "yufu-probe"

func repoFullName() string { return repoOwner + "/" + repoName }

// AgentReport 客户端上报的数据结构
type AgentReport struct {
	UUID      string  `json:"uuid"`
	Hostname  string  `json:"hostname"`
	IP        string  `json:"ip"`
	PublicIP  string  `json:"public_ip"`
	PublicIP4 string  `json:"public_ip4"`
	PublicIP6 string  `json:"public_ip6"`
	OS        string  `json:"os"`
	Platform  string  `json:"platform"`
	BootTime  int64   `json:"boot_time"`
	Uptime    int64   `json:"uptime"`
	CPU       float64 `json:"cpu"`
	CPUCount  int     `json:"cpu_count"`
	MemUsed   float64 `json:"mem_used"`
	MemTotal  float64 `json:"mem_total"`
	DiskUsed  float64 `json:"disk_used"`
	DiskTotal float64 `json:"disk_total"`
	RxRate    float64 `json:"rx_rate"`
	TxRate    float64 `json:"tx_rate"`
	RxDelta   float64 `json:"rx_delta"`
	TxDelta   float64 `json:"tx_delta"`
}

// broadcastAgents 把当前内存中的全量状态推送给所有 viewer。
// 由 main.go 的定时 ticker 周期调用（不再在每条上报里调用），
// 因此广播频率固定为 1 次/秒，与客户端数量解耦。
// 同时携带当前「分组名注册表」，使前端能正确渲染「+ 新建分组」等空分组。
func broadcastAgents(hub *Hub) {
	list := live.Snapshot()
	groups := live.Groups()
	payload, err := json.Marshal(map[string]interface{}{"type": "agents", "data": list, "groups": groups})
	if err != nil {
		return
	}
	hub.BroadcastToViewers(payload)
}

func loginHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Username != cfg.Admin.Username || req.Password != cfg.Admin.Password {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		token, err := createSession(db)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func logoutHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			deleteSession(db, c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func meHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"username": cfg.Admin.Username})
	}
}

// installCommandHandler 返回给新 VPS 用的客户端一键安装命令。
// 服务端地址取自请求 Host（与浏览器访问面板的地址一致，兼容公网域名/反代场景）；
// Token 为服务端配置的 agent_token。
func installCommandHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 推导 ws/wss 协议
		scheme := "ws"
		if r.TLS != nil {
			scheme = "wss"
		}
		if fp := r.Header.Get("X-Forwarded-Proto"); fp == "https" {
			scheme = "wss"
		} else if fp == "http" {
			scheme = "ws"
		}
		host := r.Host
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			host = fh
		}
		wsURL := scheme + "://" + host
		command := "bash <(curl -sSL https://raw.githubusercontent.com/" + repoFullName() + "/main/install-agent.sh) " + wsURL + " " + cfg.AgentToken
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"command": command,
			"ws":      wsURL,
			"token":   cfg.AgentToken,
		})
	}
}

func agentsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list := live.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

func aliasHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uuid := mux.Vars(r)["uuid"]
		var req struct {
			Alias string `json:"alias"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := SetAlias(db, uuid, req.Alias); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func trafficHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uuid := mux.Vars(r)["uuid"]
		list, err := GetTrafficHistory(db, uuid)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

// updateAgentHandler 更新机器的显示名称、备注与到期时间（管理员鉴权）
func updateAgentHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uuid := mux.Vars(r)["uuid"]
		// 兼容两种请求体：{name, remark, expire_at} 或 {alias, remark, expire_at}
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		alias := ""
		if v, ok := raw["name"]; ok {
			_ = json.Unmarshal(v, &alias)
		} else if v, ok := raw["alias"]; ok {
			_ = json.Unmarshal(v, &alias)
		}
		remark := ""
		if v, ok := raw["remark"]; ok {
			_ = json.Unmarshal(v, &remark)
		}
		group := ""
		if v, ok := raw["group"]; ok {
			_ = json.Unmarshal(v, &group)
		}
		var expireAt *int64
		if v, ok := raw["expire_at"]; ok {
			// 支持 null（清空）或数字（Unix 秒）
			if string(v) != "null" && len(v) > 0 {
				var n int64
				if err := json.Unmarshal(v, &n); err == nil {
					expireAt = &n
				}
			}
		}
	if err := UpdateAgent(db, uuid, alias, remark, group, expireAt); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	live.UpdateAdmin(uuid, alias, remark, group, expireAt)
	// 保持内存态「分组名注册表」与 DB 一致：通过编辑弹窗手输的新名字也要注册进来
	if group != "" {
		live.AddGroup(group)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

// 离线分组是前端虚拟分组，不对应任何真实 group_name，禁止当作真实分组增删改
const reservedOfflineGroup = "⚠ 离线"

// renameGroupHandler 重命名分组：把所有 group_name=oldName 的客户端改成新名字
func renameGroupHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oldName := mux.Vars(r)["name"]
		if oldName == "" || oldName == reservedOfflineGroup {
			http.Error(w, "invalid group", http.StatusBadRequest)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		newName := strings.TrimSpace(req.Name)
		if newName == "" || newName == reservedOfflineGroup || strings.ContainsAny(newName, "/\\") {
			http.Error(w, "invalid new name", http.StatusBadRequest)
			return
		}
		n, err := RenameGroup(db, oldName, newName)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		live.RenameGroup(oldName, newName)
		broadcastAgents(hub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "affected": n, "old": oldName, "new": newName})
	}
}

// deleteGroupHandler 删除分组：把该分组下所有客户端移回「未分组」（group_name 置空）
func deleteGroupHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.Vars(r)["name"]
		if name == "" || name == reservedOfflineGroup {
			http.Error(w, "invalid group", http.StatusBadRequest)
			return
		}
		n, err := DeleteGroup(db, name)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		live.DeleteGroup(name)
		broadcastAgents(hub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "affected": n, "name": name})
	}
}

// 校验分组名合法性：禁止空、保留名、含 / \
func isValidGroupName(name string) bool {
	if name == "" || name == reservedOfflineGroup || name == "未分组" {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	return true
}

// createGroupHandler 新建分组（不要求任何客户端属于此分组，可建一个空组供后续使用）
func createGroupHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		if !isValidGroupName(name) {
			http.Error(w, "invalid group name", http.StatusBadRequest)
			return
		}
		if err := AddGroup(db, name); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		live.AddGroup(name)
		broadcastAgents(hub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "name": name})
	}
}

// listGroupsHandler 返回当前所有已注册分组名（用于编辑机器的下拉框）
func listGroupsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 优先使用内存态（包含本进程内新建的组，与 WS 广播一致）
		gs := live.Groups()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gs)
	}
}

// requireAgentToken 校验 Agent Token（兼容 Authorization: Bearer <token> 或 ?token= 查询参数）
func requireAgentToken(cfg *Config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Authorization")
		tok = strings.TrimPrefix(tok, "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if tok != cfg.AgentToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// deleteAgentHandler 删除机器（agent 主动注销或管理员移除，需 Agent Token 鉴权）
func deleteAgentHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uuid := mux.Vars(r)["uuid"]
		if err := DeleteAgent(db, uuid); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		live.Remove(uuid)
		broadcastAgents(hub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

// agentWSHandler 接收客户端上报（需 Token）
func agentWSHandler(cfg *Config, db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != cfg.AgentToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// 1) 控制消息：客户端主动注销（优雅停止时发送）
			var ctrl struct {
				Action string `json:"action"`
				UUID   string `json:"uuid"`
			}
			if err := json.Unmarshal(data, &ctrl); err == nil && ctrl.Action == "unregister" {
				if ctrl.UUID != "" {
					DeleteAgent(db, ctrl.UUID)
					live.Remove(ctrl.UUID)
					broadcastAgents(hub)
					log.Printf("[ws] agent %s 已注销", ctrl.UUID)
				}
				return
			}
			// 2) 普通状态上报：只更新内存，不做 DB 写入、不广播
			var rep AgentReport
			if err := json.Unmarshal(data, &rep); err != nil || rep.UUID == "" {
				continue
			}
		// 地理定位优先用公网 IPv4，其次 v6，再其次老字段 public_ip，
		// 缺失时回退到上报的内网 IP
		geoIP := rep.PublicIP4
		if geoIP == "" {
			geoIP = rep.PublicIP6
		}
		if geoIP == "" {
			geoIP = rep.PublicIP
		}
		if geoIP == "" {
			geoIP = rep.IP
		}
			country, code := "", ""
			if !isPrivateIP(geoIP) {
				country, code = lookupCountry(db, geoIP, rep.UUID)
			}
			live.ApplyReport(rep, country, code)
		}
	}
}

// viewerWSHandler 浏览器实时订阅（需登录 session）
func viewerWSHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !validSession(db, c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := &Client{hub: hub, conn: conn, send: make(chan []byte, 16), role: "viewer"}
		hub.addViewer(client)
		go client.writePump()
		defer func() {
			hub.removeViewer(client)
			conn.Close()
		}()
		broadcastAgents(hub)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}
}

func setupRoutes(cfg *Config, db *sql.DB, hub *Hub) http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/api/login", loginHandler(cfg, db)).Methods("POST")
	r.HandleFunc("/api/logout", logoutHandler(db)).Methods("POST")
	r.HandleFunc("/api/me", requireLogin(db, meHandler(cfg))).Methods("GET")
	r.HandleFunc("/api/install-command", requireLogin(db, installCommandHandler(cfg))).Methods("GET")
	r.HandleFunc("/api/agents", requireLogin(db, agentsHandler(db))).Methods("GET")
	r.HandleFunc("/api/agents/{uuid}/alias", requireLogin(db, aliasHandler(db))).Methods("PUT")
	r.HandleFunc("/api/agents/{uuid}", requireLogin(db, updateAgentHandler(db))).Methods("PATCH")
	// 分组级管理：新建 / 列表 / 重命名 / 删除（删除会把成员移回「未分组」）
	r.HandleFunc("/api/groups", requireLogin(db, createGroupHandler(db, hub))).Methods("POST")
	r.HandleFunc("/api/groups", requireLogin(db, listGroupsHandler(db))).Methods("GET")
	r.HandleFunc("/api/groups/{name}", requireLogin(db, renameGroupHandler(db, hub))).Methods("PATCH")
	r.HandleFunc("/api/groups/{name}", requireLogin(db, deleteGroupHandler(db, hub))).Methods("DELETE")
	r.HandleFunc("/api/agents/{uuid}", requireAgentToken(cfg, deleteAgentHandler(db, hub))).Methods("DELETE")
	r.HandleFunc("/api/agents/{uuid}/traffic", requireLogin(db, trafficHandler(db))).Methods("GET")
	r.HandleFunc("/ws/agent", agentWSHandler(cfg, db, hub))
	r.HandleFunc("/ws/viewer", viewerWSHandler(db, hub))
	r.PathPrefix("/").Handler(http.FileServer(http.FS(staticSubFS)))
	return r
}
