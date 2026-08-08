package main

import (
	"sync"

	"github.com/gorilla/websocket"
)

// Client 表示一个 WebSocket 连接
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	role string // "agent" | "viewer" | "terminal"
	// writeMu 保护对 conn 的并发写（同一 agent 连接可能被多个终端会话同时写入，
	// 例如同一客户端同时开多个 Web SSH）。viewer/terminal 走 send 通道由 writePump 单协程写出，无需此锁。
	writeMu sync.Mutex
}

// Hub 管理所有 viewer（浏览器）连接，负责向它们广播实时数据；
// 同时维护按 uuid 登记的 agent 连接，供 Web SSH 终端网关按 uuid 找到目标客户端并下发指令。
type Hub struct {
	mu      sync.RWMutex
	viewers map[*Client]struct{}
	agents  map[string]*Client // uuid -> agent WS 连接
}

// NewHub 创建 Hub
func NewHub() *Hub {
	return &Hub{
		viewers: make(map[*Client]struct{}),
		agents:  make(map[string]*Client),
	}
}

func (h *Hub) addViewer(c *Client) {
	h.mu.Lock()
	h.viewers[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) removeViewer(c *Client) {
	h.mu.Lock()
	delete(h.viewers, c)
	h.mu.Unlock()
}

// addAgent 把一台客户端的 WS 连接登记到 uuid 下（终端网关据此找到目标 agent）
func (h *Hub) addAgent(uuid string, c *Client) {
	h.mu.Lock()
	h.agents[uuid] = c
	h.mu.Unlock()
}

// removeAgent 注销某 uuid 的 agent 连接
func (h *Hub) removeAgent(uuid string) {
	h.mu.Lock()
	delete(h.agents, uuid)
	h.mu.Unlock()
}

// findAgent 按 uuid 找到在线客户端的 WS 连接；不在或离线返回 nil
func (h *Hub) findAgent(uuid string) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.agents[uuid]
}

// BroadcastToViewers 向所有已连接的 viewer 推送消息。
//
// 推送的是「全量最新状态快照」，语义上只有最后一帧有价值——晚到的旧帧不仅
// 无用，还会把界面打回过期状态。因此当 viewer 的发送队列已满（弱网 / 机器多
// 导致单帧近 1MB，客户端消费不过来）时，采取 drop-oldest：
// 丢掉队列里最旧的一帧，把最新帧挤进去。
//
// 这里原先是 drop-newest（满了就 default 丢弃当前帧），方向恰好反了：
// 队列里会一直压着一批陈旧快照慢慢外推，管理员改完分组后，先收到的是
// 「改动之前」生成的旧帧，界面上机器会跳回原分组，要等整队旧帧排空才生效。
func (h *Hub) BroadcastToViewers(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.viewers {
		select {
		case c.send <- payload:
		default:
			// 队列已满：先弹掉最旧的一帧，再尝试放入最新帧。
			// 两步都用非阻塞写法，避免 writePump 正好消费时相互卡住。
			select {
			case <-c.send:
			default:
			}
			select {
			case c.send <- payload:
			default:
			}
		}
	}
}

// safeWrite 带锁地写一条文本消息，避免多个 goroutine 同时写同一 agent 连接
func (c *Client) safeWrite(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, payload)
}

// writePump 持续把 send 通道的消息写出到连接
func (c *Client) writePump() {
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}
