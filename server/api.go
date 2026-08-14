package main

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

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

// viewerUpgrader 专供浏览器 viewer 连接，比 upgrader 多开了 permessage-deflate 压缩。
//
// 为什么必须压缩：全量快照的体积与机器数成正比，2500 台时单帧约 1.6MB，
// 每秒一帧就是 12.8 Mbps 的持续下行。浏览器吃不下，帧就会堆在 viewer 的发送队列里，
// 于是管理员改完分组后，界面要先把队列中「改动之前」的陈旧快照逐帧消费完才会更新——
// 表现就是机器改完分组先消失、几秒后跳回原组、再过十几秒才真正过去。
// deflate 对这种高度重复的 JSON 压缩率极高：拿线上 2583 台的真实快照实测，
// 1.6MB 压到 284KB（压掉 82%，level 1 仅耗时 9ms），是消除回跳的关键。
//
// 为什么只给 viewer 开：agent 连接数量以千计，逐连接的压缩上下文会显著抬高内存，
// 而上报报文只有几百字节、压缩收益微乎其微。所以 agent 与终端继续用不压缩的 upgrader。
var viewerUpgrader = websocket.Upgrader{
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: true,
}

// 源码仓库全名（用于生成安装命令的下载链接），与 install-agent.sh 默认仓库保持一致
const repoOwner = "shenping1200"
const repoName = "yufu-probe"

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
// broadcastSeq 是每次向 viewer 广播全量快照时自增的序列号。
// 前端据此丢弃过期/重复帧：只有 seq 严格大于已应用 seq 的帧才会覆盖本地状态，
// 这样即便后端发送缓冲积压先推来了改动前的旧快照，也不会把前端的乐观更新打回原状。
var broadcastSeq uint64

func broadcastAgents(hub *Hub) {
	list := live.Snapshot()
	groups := live.Groups()
	seq := atomic.AddUint64(&broadcastSeq, 1)
	payload, err := json.Marshal(map[string]interface{}{"type": "agents", "data": list, "groups": groups, "seq": seq})
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
		token, err := createSession(db, roleAdmin)
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

func meHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		role := roleVisitor
		if c, err := r.Cookie(sessionCookie); err == nil && sessionRole(db, c.Value) == roleAdmin {
			role = roleAdmin
		}
		json.NewEncoder(w).Encode(map[string]string{
			"username": cfg.Admin.Username,
			"role":     role,
		})
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
		groupProvided := false
		if v, ok := raw["group"]; ok {
			_ = json.Unmarshal(v, &group)
			groupProvided = true
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
		// 管理员改分组即重新武装自动部署：清空 done/failed 终态，
		// 使本机进入源分组后能被规则重新部署（见 resetDeployState）。
		if groupProvided {
			if err := resetDeployState(db, uuid); err != nil {
				log.Printf("[api] 清空 %s deploy_state 失败: %v", uuid, err)
			}
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

// requireAgentTokenOrAdmin 允许「Agent Token」或「管理员会话」任一种身份调用。
// 用于删除接口：uninstall-agent.sh（带 Agent Token 自卸载）与管理员面板共用同一 DELETE 入口，
// 不需要为面板再开一条独立路由。
func requireAgentTokenOrAdmin(cfg *Config, db *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1) 管理员会话优先
		if c, err := r.Cookie(sessionCookie); err == nil {
			if sessionRole(db, c.Value) == roleAdmin {
				next(w, r)
				return
			}
		}
		// 2) 退而求其次：Agent Token（uninstall-agent.sh 用）
		tok := r.Header.Get("Authorization")
		tok = strings.TrimPrefix(tok, "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if tok != "" && tok == cfg.AgentToken {
			next(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
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

// batchDeleteAgentsHandler 批量删除机器（管理员）。body: {"uuids":[...]}
func batchDeleteAgentsHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UUIDs []string `json:"uuids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.UUIDs) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, uuid := range req.UUIDs {
			_ = DeleteAgent(db, uuid)
			live.Remove(uuid)
		}
		broadcastAgents(hub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": len(req.UUIDs)})
	}
}

// batchUpdateAgentsHandler 批量更新选中机器的分组/备注/到期（管理员）。
// 只改请求中出现的字段，未出现的字段保持原值（避免清空）。
// body: {"uuids":[...], "group":可选, "remark":可选, "expire_at":可选}
func batchUpdateAgentsHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UUIDs    []string `json:"uuids"`
			Group    *string  `json:"group"`
			Remark   *string  `json:"remark"`
			ExpireAt *int64   `json:"expire_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.UUIDs) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, uuid := range req.UUIDs {
			if req.Group != nil {
				if err := SetAgentGroup(db, uuid, *req.Group); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				live.PatchAgentFields(uuid, req.Group, nil, nil)
				if *req.Group != "" {
					live.AddGroup(*req.Group)
				}
				// 管理员改分组即重新武装自动部署：清空 done/failed 终态，
				// 使本机进入源分组后能被规则重新部署（见 resetDeployState）。
				if err := resetDeployState(db, uuid); err != nil {
					log.Printf("[api] 清空 %s deploy_state 失败: %v", uuid, err)
				}
			}
			if req.Remark != nil {
				if err := SetAgentRemark(db, uuid, *req.Remark); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				live.PatchAgentFields(uuid, nil, req.Remark, nil)
			}
			if req.ExpireAt != nil {
				if err := SetAgentExpire(db, uuid, req.ExpireAt); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				live.PatchAgentFields(uuid, nil, nil, req.ExpireAt)
			}
		}
		broadcastAgents(hub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "updated": len(req.UUIDs)})
	}
}

// uninstallCommandHandler 返回在客户端执行即可卸载 Agent 的命令（管理员）。
// 与 installCommandHandler 镜像，只把脚本换成 uninstall-agent.sh。
// 卸载脚本会自动探测本机 UUID 并调用 DELETE /api/agents/{uuid}（带 Agent Token）。
func uninstallCommandHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		command := "bash <(curl -sSL https://raw.githubusercontent.com/" + repoFullName() + "/main/uninstall-agent.sh) " + wsURL + " " + cfg.AgentToken
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"command": command,
			"ws":      wsURL,
			"token":   cfg.AgentToken,
		})
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
		// agent 连接也需要一个 Client 句柄（终端网关靠它 safeWrite 下发 shell 指令）。
		// 这里不跑 writePump：agent 方向走带锁的 safeWrite 直写，不用 send 通道。
		client := &Client{hub: hub, conn: conn, send: make(chan []byte, 8), role: "agent"}
		var agentUUID string
		defer func() {
			if agentUUID != "" {
				hub.removeAgent(agentUUID)
				notifyAgentGone(agentUUID)   // 关闭该 agent 名下所有 Web SSH 会话
				abortExecForAgent(agentUUID) // 中止该 agent 名下所有批量命令会话，别让调用方干等到超时
			}
			conn.Close()
		}()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// 1) 控制消息：客户端主动注销（收到 SIGTERM 时发送）
			// 注意：这里**不**删除数据库记录，只清理内存中的实时状态。
			// 因为 agent 每次重启/升级时，旧进程都会发一次 unregister，如果这里硬删
			// 会把用户手动设的备注/别名一起带走（即使新二进制立刻带着同 UUID 重连也来不及）。
			// 真正需要删机器走 DELETE /api/agents/{uuid}（uninstall-agent.sh 用的接口）。
			var ctrl struct {
				Action string `json:"action"`
				UUID   string `json:"uuid"`
			}
			if err := json.Unmarshal(data, &ctrl); err == nil && ctrl.Action == "unregister" {
				if ctrl.UUID != "" {
					live.Remove(ctrl.UUID)
					hub.removeAgent(ctrl.UUID)
					notifyAgentGone(ctrl.UUID)
					abortExecForAgent(ctrl.UUID)
					broadcastAgents(hub)
					log.Printf("[ws] agent %s 主动断开（记录保留，由 DELETE 接口或离线超时处理）", ctrl.UUID)
				}
				return
			}
			// 2) Web SSH：agent 回传的 shell 输出/结束信号，按会话 id 转发给浏览器
			var term struct {
				Action  string `json:"action"`
				Session string `json:"session"`
				Data    string `json:"data"`
			}
			if err := json.Unmarshal(data, &term); err == nil {
				switch term.Action {
				case "shell_data":
					// 先查批量命令会话（服务端驱动），再回落到浏览器终端会话
					if es := findExec(term.Session); es != nil {
						feedExecData(term.Session, term.Data)
					} else {
						forwardShellData(term.Session, term.Data)
					}
					continue
				case "shell_exit":
					if ts := unregisterTerm(term.Session); ts != nil {
						ts.browser.writeJSON(map[string]string{"action": "ended", "message": "会话已结束"})
					}
					continue
				}
			}
			// 3) 普通状态上报：只更新内存，不做 DB 写入、不广播
			var rep AgentReport
			if err := json.Unmarshal(data, &rep); err != nil || rep.UUID == "" {
				continue
			}
			// 首次收到带 UUID 的上报时，把该 agent 连接登记到 hub，
			// 供 Web SSH 终端网关按 uuid 找到目标客户端并下发 shell 指令。
			if agentUUID == "" {
				agentUUID = rep.UUID
				hub.addAgent(agentUUID, client)
				log.Printf("[ws] agent %s 已登记，Web SSH 可用", agentUUID)
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

// ---------- 压力测试（内置引擎）----------

func stressOptionsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(StressOptions())
	}
}

func stressStartHandler(cfg *Config, db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p StressParams
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := stressEngine.Start(p, db, hub); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "group": p.Group, "count": p.Count})
	}
}

func stressStopHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := stressEngine.Stop(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func stressStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stressEngine.Status())
	}
}

// ---------- 访客链接 ----------

// 访客链接默认有效期 24 小时
const visitorLinkTTL = 24 * time.Hour

// visitorLinkHandler 管理员签发访客链接：生成随机 token、写入 visitor_links（带过期）、返回路径
func visitorLinkHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		token := hex.EncodeToString(b)
		now := time.Now().Unix()
		_, err := db.Exec(`INSERT INTO visitor_links (token, created_at, expires_at) VALUES (?, ?, ?)`,
			token, now, now+int64(visitorLinkTTL.Seconds()))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"path":    "/v/" + token,
			"token":   token,
			"expires": time.Unix(now+int64(visitorLinkTTL.Seconds()), 0).UTC().Format(time.RFC3339),
		})
	}
}

// visitorLandingHandler 访客打开 /v/{token}：校验 token 未过期 → 创建访客会话 → 跳首页
func visitorLandingHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := mux.Vars(r)["token"]
		if token == "" {
			http.NotFound(w, r)
			return
		}
		var expires int64
		err := db.QueryRow(`SELECT expires_at FROM visitor_links WHERE token=?`, token).Scan(&expires)
		if err != nil || time.Now().Unix() > expires {
			http.Error(w, "link expired or invalid", http.StatusGone)
			return
		}
		st, err := createSession(db, roleVisitor)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    st,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/", http.StatusFound)
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
		conn, err := viewerUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// 压缩级别取 1（最快档）：全量快照仍能压掉八成以上，而 CPU 开销只有默认档的三分之一，
		// 面板每秒推一帧、viewer 数量很少，用最快档最划算。
		conn.EnableWriteCompression(true)
		_ = conn.SetCompressionLevel(1)
		// 发送队列只留 2 帧（配合 BroadcastToViewers 的 drop-oldest）：
		// 全量快照只有最后一帧有价值，队列越深，改完分组后要回放的陈旧快照就越多。
		// 原来的 16 帧意味着最坏情况要先播完 16 帧旧状态（2500 台时约 26MB）才轮到新状态，
		// 这正是「改完分组机器跳回原组」的根源。降到 2 后陈旧回放窗口最多 1 帧。
		client := &Client{hub: hub, conn: conn, send: make(chan []byte, 2), role: "viewer"}
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
	r.HandleFunc("/api/me", requireLogin(db, meHandler(cfg, db))).Methods("GET")
	r.HandleFunc("/api/install-command", requireAdmin(db, installCommandHandler(cfg))).Methods("GET")
	r.HandleFunc("/api/uninstall-command", requireAdmin(db, uninstallCommandHandler(cfg))).Methods("GET")
	r.HandleFunc("/api/agents", requireLogin(db, agentsHandler(db))).Methods("GET")
	r.HandleFunc("/api/agents/{uuid}/alias", requireAdmin(db, aliasHandler(db))).Methods("PUT")
	r.HandleFunc("/api/agents/{uuid}", requireAdmin(db, updateAgentHandler(db))).Methods("PATCH")
	// 批量操作（管理员）：DELETE/PATCH 作用于整批 uuid，路径不带 {uuid} 以区分单台
	r.HandleFunc("/api/agents", requireAdmin(db, batchDeleteAgentsHandler(db, hub))).Methods("DELETE")
	r.HandleFunc("/api/agents", requireAdmin(db, batchUpdateAgentsHandler(db, hub))).Methods("PATCH")
	// 批量命令下发：管理员 + Web SSH 密码，向一批机器并发执行脚本
	r.HandleFunc("/api/agents/exec", requireAdmin(db, execHandler(cfg, db, hub))).Methods("POST")
	// 自动部署规则（仅管理员）：列表/新建/更新/删除
	r.HandleFunc("/api/deploy-rules", requireAdmin(db, deployRulesListHandler(db))).Methods("GET")
	r.HandleFunc("/api/deploy-rules", requireAdmin(db, deployRulesCreateHandler(db))).Methods("POST")
	r.HandleFunc("/api/deploy-rules/{id}", requireAdmin(db, deployRulesUpdateHandler(db))).Methods("PATCH")
	r.HandleFunc("/api/deploy-rules/{id}", requireAdmin(db, deployRulesDeleteHandler(db))).Methods("DELETE")
	// 自动部署全局暂停开关：GET 公开（看状态），POST 仅管理员（切状态）
	r.HandleFunc("/api/deploy-paused", deployPausedHandler(db)).Methods("GET")
	r.HandleFunc("/api/deploy-paused", requireAdmin(db, deployPausedHandler(db))).Methods("POST")
	// 分组级管理：列表（只读）访客可看；新建/重命名/删除仅管理员
	r.HandleFunc("/api/groups", requireAdmin(db, createGroupHandler(db, hub))).Methods("POST")
	r.HandleFunc("/api/groups", requireLogin(db, listGroupsHandler(db))).Methods("GET")
	r.HandleFunc("/api/groups/{name}", requireAdmin(db, renameGroupHandler(db, hub))).Methods("PATCH")
	r.HandleFunc("/api/groups/{name}", requireAdmin(db, deleteGroupHandler(db, hub))).Methods("DELETE")
	r.HandleFunc("/api/agents/{uuid}", requireAgentTokenOrAdmin(cfg, db, deleteAgentHandler(db, hub))).Methods("DELETE")
	r.HandleFunc("/api/agents/{uuid}/traffic", requireLogin(db, trafficHandler(db))).Methods("GET")
	r.HandleFunc("/ws/agent", agentWSHandler(cfg, db, hub))
	r.HandleFunc("/ws/viewer", viewerWSHandler(db, hub))
	// Web SSH 终端网关与解锁：仅管理员（访客不可 SSH）
	r.HandleFunc("/ws/terminal/{uuid}", requireAdmin(db, terminalWSHandler(cfg, db, hub)))
	r.HandleFunc("/api/ssh/unlock", requireAdmin(db, unlockHandler(db))).Methods("POST")
	// 压力测试：选项/状态访客可看，启动/停止仅管理员
	r.HandleFunc("/api/stress/options", requireLogin(db, stressOptionsHandler(db))).Methods("GET")
	r.HandleFunc("/api/stress/start", requireAdmin(db, stressStartHandler(cfg, db, hub))).Methods("POST")
	r.HandleFunc("/api/stress/stop", requireAdmin(db, stressStopHandler(db, hub))).Methods("POST")
	r.HandleFunc("/api/stress/status", requireLogin(db, stressStatusHandler())).Methods("GET")
	// 访客链接：签发仅管理员，落地页免登录
	r.HandleFunc("/api/visitor/link", requireAdmin(db, visitorLinkHandler(db))).Methods("POST")
	r.HandleFunc("/v/{token}", visitorLandingHandler(db)).Methods("GET")
	r.PathPrefix("/").Handler(http.FileServer(http.FS(staticSubFS)))
	return r
}
