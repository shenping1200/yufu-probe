package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// shellSession 表示一个活动的 Web SSH 会话（一台客户端上可同时存在多个）
type shellSession struct {
	pty *os.File
	cmd *exec.Cmd
}

// buildMsg 把一次快照序列化为上报消息
func buildMsg(uuid string, s *Snapshot) map[string]interface{} {
	return map[string]interface{}{
		"uuid":       uuid,
		"hostname":   s.Hostname,
		"ip":         s.IP,
		"public_ip":  s.PublicIP,
		"public_ip4": s.PublicIP4,
		"public_ip6": s.PublicIP6,
		"os":         s.OS,
		"platform":   s.Platform,
		"boot_time":  s.BootTime,
		"uptime":     s.Uptime,
		"cpu":        s.CPU,
		"cpu_count":  s.CPUCount,
		"mem_used":   s.MemUsed,
		"mem_total":  s.MemTotal,
		"disk_used":  s.DiskUsed,
		"disk_total": s.DiskTotal,
		"rx_rate":    s.RxRate,
		"tx_rate":    s.TxRate,
		"rx_delta":   s.RxDelta,
		"tx_delta":   s.TxDelta,
	}
}

// Reporter 负责通过 WebSocket 长连接向服务端上报数据，并复用同一连接接收
// 服务端下发的 Web SSH 控制指令（shell_open / shell_input / shell_resize / shell_close）。
// 单连接同时承载「周期上报」与「终端 I/O」：上报走写锁，控制指令走独立读循环。
type Reporter struct {
	url      string
	uuid     string
	mu       sync.Mutex
	conn     *websocket.Conn
	sessions map[string]*shellSession
}

func NewReporter(server, token, uuid string) *Reporter {
	return &Reporter{
		url:      server + "/ws/agent?token=" + token,
		uuid:     uuid,
		sessions: make(map[string]*shellSession),
	}
}

// writeMsg 在写锁保护下发送一条消息，保证多个 goroutine（上报循环 / shell 输出）不会并发写同一连接
func (r *Reporter) writeMsg(msgType int, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return errors.New("disconnected")
	}
	return r.conn.WriteMessage(msgType, data)
}

// dropConn 关闭当前连接并清理所有活动 shell 会话
func (r *Reporter) dropConn() {
	r.mu.Lock()
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.mu.Unlock()
	r.killAllSessions()
}

// Run 维持长连接并消费上报通道，断线指数退避重连；同时跑一个读循环处理服务端下发的控制消息
func (r *Reporter) Run(send <-chan *Snapshot) {
	backoff := time.Second
	for {
		r.mu.Lock()
		conn := r.conn
		r.mu.Unlock()
		if conn == nil {
			c, _, err := websocket.DefaultDialer.Dial(r.url, nil)
			if err != nil {
				log.Printf("[agent] connect failed: %v, retry in %v", err, backoff)
				time.Sleep(backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			r.mu.Lock()
			r.conn = c
			r.mu.Unlock()
			backoff = time.Second
			log.Printf("[agent] connected to %s", r.url)
			go r.readLoop(c)
		}
		snap := <-send
		data, _ := json.Marshal(buildMsg(r.uuid, snap))
		if err := r.writeMsg(websocket.TextMessage, data); err != nil {
			log.Printf("[agent] send failed: %v", err)
			r.dropConn()
		}
	}
}

// readLoop 只读循环：处理服务端下发的 Web SSH 控制消息（shell_open/input/resize/close）
func (r *Reporter) readLoop(c *websocket.Conn) {
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			r.dropConn()
			return
		}
		var msg struct {
			Action  string `json:"action"`
			Session string `json:"session"`
			Cols    int    `json:"cols"`
			Rows    int    `json:"rows"`
			Data    string `json:"data"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Action {
		case "shell_open":
			r.handleShellOpen(msg.Session, msg.Cols, msg.Rows)
		case "shell_input":
			r.handleShellInput(msg.Session, msg.Data)
		case "shell_resize":
			r.handleShellResize(msg.Session, msg.Cols, msg.Rows)
		case "shell_close":
			r.handleShellClose(msg.Session)
		}
	}
}

// handleShellOpen 在服务端下发指令时，于本机起一个 PTY shell，并把输出回传
func (r *Reporter) handleShellOpen(session string, cols, rows int) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	cmd := exec.Command("/bin/bash", "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		r.sendShellData(session, []byte("\r\n[!] 无法启动 shell: "+err.Error()+"\r\n"))
		return
	}
	r.mu.Lock()
	r.sessions[session] = &shellSession{pty: f, cmd: cmd}
	r.mu.Unlock()

	// 读 shell 输出并回传
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				r.sendShellData(session, buf[:n])
			}
			if err != nil {
				break
			}
		}
		r.sendShellData(session, []byte("\r\n[会话已结束]\r\n"))
		r.sendControl("shell_exit", session, "")
		r.handleShellClose(session)
	}()
	// 等待 shell 进程结束
	go func() {
		cmd.Wait()
		f.Close()
	}()
}

// handleShellInput 把浏览器侧输入的 base64 数据写入 shell 标准输入
func (r *Reporter) handleShellInput(session, b64 string) {
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return
	}
	s.pty.Write(raw)
}

// handleShellResize 调整 PTY 窗口尺寸（浏览器终端缩放时下发）
func (r *Reporter) handleShellResize(session string, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s == nil || s.pty == nil {
		return
	}
	pty.Setsize(s.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// handleShellClose 关闭指定会话（杀掉 shell 进程、关 PTY）
func (r *Reporter) handleShellClose(session string) {
	r.mu.Lock()
	s := r.sessions[session]
	delete(r.sessions, session)
	r.mu.Unlock()
	if s != nil {
		if s.pty != nil {
			s.pty.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
	}
}

// killAllSessions 关闭全部活动会话（连接断开/重连时调用）
func (r *Reporter) killAllSessions() {
	r.mu.Lock()
	sessions := r.sessions
	r.sessions = make(map[string]*shellSession)
	r.mu.Unlock()
	for _, s := range sessions {
		if s.pty != nil {
			s.pty.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
	}
}

// sendShellData 把 shell 输出做 base64 后回传给服务端（服务端再转发给浏览器）
func (r *Reporter) sendShellData(session string, b []byte) {
	r.sendControl("shell_data", session, base64.StdEncoding.EncodeToString(b))
}

// sendControl 发送一条控制消息（shell_data / shell_exit 等）
func (r *Reporter) sendControl(action, session, data string) {
	msg, _ := json.Marshal(map[string]string{"action": action, "session": session, "data": data})
	r.writeMsg(websocket.TextMessage, msg)
}

// Unregister 主动通知服务端注销本 agent（优雅停止时调用）。
// 通过一条独立的 WS 连接发送 {"action":"unregister","uuid":...}，
// 让服务端立即删除该机器记录，无需再到面板手动清理。
func (r *Reporter) Unregister() {
	msg, _ := json.Marshal(map[string]interface{}{"action": "unregister", "uuid": r.uuid})
	c, _, err := websocket.DefaultDialer.Dial(r.url, nil)
	if err != nil {
		log.Printf("[agent] unregister 连接失败: %v", err)
		return
	}
	defer c.Close()
	if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Printf("[agent] unregister 发送失败: %v", err)
		return
	}
	log.Printf("[agent] 已发送注销请求，服务端将移除本机")
	time.Sleep(500 * time.Millisecond)
}
