package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// termSession 记录一个活动终端会话：桥接「浏览器终端 WS」与「目标 agent WS」
type termSession struct {
	browser *Client // 浏览器终端连接（用 send 通道推送数据）
	uuid    string  // 目标客户端 uuid
	agent   *Client // 目标 agent 连接
}

var (
	termMu       sync.RWMutex
	termSessions = make(map[string]*termSession)
)

func registerTerm(sid string, ts *termSession) {
	termMu.Lock()
	termSessions[sid] = ts
	termMu.Unlock()
}

func unregisterTerm(sid string) *termSession {
	termMu.Lock()
	ts := termSessions[sid]
	delete(termSessions, sid)
	termMu.Unlock()
	return ts
}

func findTerm(sid string) *termSession {
	termMu.RLock()
	defer termMu.RUnlock()
	return termSessions[sid]
}

// writeJSON 把 v 序列化后同步写给该连接（带写锁，保证单写者）。
// 终端浏览器连接走这条路径（而非 send 通道 + writePump），原因：
//  1. 认证阶段发送 error/ready 后 handler 会立即 return 并关闭连接，
//     若走异步 send 通道，writePump 还没把消息刷出去连接就被关掉，浏览器收不到 error；
//  2. forwardShellData / notifyAgentGone 已在另一 goroutine 用 safeWrite 写同一连接，
//     gorilla websocket 要求「单并发写者」，不能再叠加一个 writePump 写者。
func (c *Client) writeJSON(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.safeWrite(b)
}

// forwardShellData 由 agentWSHandler 在收到 agent 的 shell_data 时调用：
// 按会话 id 找到对应浏览器，把 base64 负载原样转发（浏览器端再做 base64 解码写入 xterm）。
func forwardShellData(session, dataB64 string) {
	ts := findTerm(session)
	if ts == nil {
		return
	}
	ts.browser.writeJSON(map[string]string{"action": "data", "data": dataB64})
}

// notifyAgentGone 在 agent 断开时调用：关闭该 agent 名下所有活动终端，通知浏览器会话结束。
func notifyAgentGone(agentUUID string) {
	termMu.RLock()
	var sids []string
	for sid, ts := range termSessions {
		if ts.uuid == agentUUID {
			sids = append(sids, sid)
		}
	}
	termMu.RUnlock()
	for _, sid := range sids {
		ts := unregisterTerm(sid)
		if ts != nil {
			ts.browser.writeJSON(map[string]string{"action": "ended", "message": "客户端已断开，会话结束"})
		}
	}
}

// terminalWSHandler 浏览器侧的 Web SSH 终端网关（需登录）。
// 协议（浏览器↔服务端）：
//
//	进：{"action":"auth","password":"...","cols":80,"rows":24}（首条）
//	    {"action":"input","data":"<base64>"}                    键盘输入
//	    {"action":"resize","cols":,"rows":}                     窗口缩放
//	出：{"action":"ready"} / {"action":"data","data":"<base64>"} / {"action":"error","message":..} / {"action":"ended","message":..}
func terminalWSHandler(cfg *Config, db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetUUID := mux.Vars(r)["uuid"]

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := &Client{hub: hub, conn: conn, send: make(chan []byte, 64), role: "terminal"}
		defer conn.Close()

		// 读取首条鉴权消息
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var auth struct {
			Action   string `json:"action"`
			Password string `json:"password"`
			Cols     int    `json:"cols"`
			Rows     int    `json:"rows"`
		}
		if err := json.Unmarshal(data, &auth); err != nil || auth.Action != "auth" {
			client.writeJSON(map[string]string{"action": "error", "message": "协议错误：首条消息应为 auth"})
			return
		}

		// 目标客户端必须在线（已登记 agent 连接）
		agent := hub.findAgent(targetUUID)
		if agent == nil {
			client.writeJSON(map[string]string{"action": "error", "message": "该客户端当前不在线，无法连接 Web SSH"})
			return
		}

		// 锁定检查（落盘持久化，24h 自动解）
		_, lockedUntil, _ := GetSSHLock(db, targetUUID)
		now := time.Now().Unix()
		if lockedUntil > now {
			remain := lockedUntil - now
			client.writeJSON(map[string]string{"action": "error", "message": fmt.Sprintf("SSH 已被锁定，请 %d 秒后重试（或管理员解锁）", remain)})
			return
		}

		// 密码校验（空 ssh_password 时回退到管理员密码）
		eff := cfg.SSHPassword
		if eff == "" {
			eff = cfg.Admin.Password
		}
		if auth.Password != eff {
			locked, until, _ := RecordSSHFailure(db, targetUUID)
			if locked {
				client.writeJSON(map[string]string{"action": "error", "message": fmt.Sprintf("错误次数过多，SSH 已锁定 24 小时（剩余 %d 秒）", until-now)})
			} else {
				left := 4 // 本次失败前已失败次数固定为 4 即触发锁定；这里给一个保守提示
				fail, _, _ := GetSSHLock(db, targetUUID)
				left = 5 - fail
				if left < 0 {
					left = 0
				}
				client.writeJSON(map[string]string{"action": "error", "message": fmt.Sprintf("SSH 密码错误（还可尝试 %d 次，错误过多将锁定 24 小时）", left)})
			}
			return
		}

		// 成功：清零失败计数
		ResetSSHLock(db, targetUUID)

		// 生成会话 id，登记桥接
		sid := uuid.NewString()
		registerTerm(sid, &termSession{browser: client, uuid: targetUUID, agent: agent})
		defer unregisterTerm(sid)

		// 通知 agent 开 shell（约定 cols/rows，缺省 80x24）
		if auth.Cols <= 0 {
			auth.Cols = 80
		}
		if auth.Rows <= 0 {
			auth.Rows = 24
		}
		openMsg, _ := json.Marshal(map[string]interface{}{
			"action":  "shell_open",
			"session": sid,
			"cols":    auth.Cols,
			"rows":    auth.Rows,
		})
		if err := agent.safeWrite(openMsg); err != nil {
			client.writeJSON(map[string]string{"action": "error", "message": "无法通知客户端开启终端：" + err.Error()})
			return
		}
		client.writeJSON(map[string]string{"action": "ready"})

		// 桥接浏览器输入 -> agent
		for {
			_, bdata, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var m struct {
				Action string `json:"action"`
				Data   string `json:"data"`
				Cols   int    `json:"cols"`
				Rows   int    `json:"rows"`
			}
			if json.Unmarshal(bdata, &m) != nil {
				continue
			}
			switch m.Action {
			case "input":
				fwd, _ := json.Marshal(map[string]string{"action": "shell_input", "session": sid, "data": m.Data})
				if err := agent.safeWrite(fwd); err != nil {
					return
				}
			case "resize":
				fwd, _ := json.Marshal(map[string]interface{}{"action": "shell_resize", "session": sid, "cols": m.Cols, "rows": m.Rows})
				agent.safeWrite(fwd) // 失败不影响主流程
			}
		}

		// 浏览器断开：通知 agent 关闭 shell
		closeMsg, _ := json.Marshal(map[string]string{"action": "shell_close", "session": sid})
		agent.safeWrite(closeMsg)
	}
}

// unlockHandler 手动解除 SSH 锁定（需登录）。
// body: {"uuid":"..."} 解锁单台；不传 uuid 则解除全部（运维兜底）。
func unlockHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UUID string `json:"uuid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UUID != "" {
			if err := UnlockSSH(db, req.UUID); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			log.Printf("[ssh] 已手动解锁 %s", req.UUID)
		} else {
			if err := UnlockAllSSH(db); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			log.Printf("[ssh] 已一键解除全部 SSH 锁定")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}
