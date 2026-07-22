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
	role string // "agent" | "viewer"
}

// Hub 管理所有 viewer（浏览器）连接，负责向它们广播实时数据
type Hub struct {
	mu      sync.RWMutex
	viewers map[*Client]struct{}
}

// NewHub 创建 Hub
func NewHub() *Hub {
	return &Hub{viewers: make(map[*Client]struct{})}
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

// BroadcastToViewers 向所有已连接的 viewer 推送消息
func (h *Hub) BroadcastToViewers(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.viewers {
		select {
		case c.send <- payload:
		default:
		}
	}
}

// writePump 持续把 send 通道的消息写出到连接
func (c *Client) writePump() {
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}
