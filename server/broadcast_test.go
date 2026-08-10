package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestBroadcastSeqMonotonic 验证每次向 viewer 广播全量快照时，
// 携带的 seq 严格递增，使前端能据此丢弃过期/重复帧（修复改分组后机器跳回原组）。
func TestBroadcastSeqMonotonic(t *testing.T) {
	live = NewServerState()

	hub := NewHub()
	send := make(chan []byte, 16)
	hub.addViewer(&Client{hub: hub, send: send, role: "viewer"})

	broadcastAgents(hub)
	broadcastAgents(hub)

	first := readAgentsSeq(t, send)
	second := readAgentsSeq(t, send)

	if second <= first {
		t.Fatalf("两次广播的 seq 应严格递增，实际 first=%d second=%d", first, second)
	}
	if first < 1 {
		t.Fatalf("seq 应从 1 起自增，实际 %d", first)
	}
}

func readAgentsSeq(t *testing.T, ch <-chan []byte) uint64 {
	t.Helper()
	raw := <-ch
	var msg struct {
		Type string `json:"type"`
		Seq  uint64 `json:"seq"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("解析广播帧失败: %v", err)
	}
	if msg.Type != "agents" {
		t.Fatalf("期望 type=agents，实际 %q", msg.Type)
	}
	return msg.Seq
}

// TestBroadcastDropOldestKeepsLatest 锁住 drop-oldest 语义：
// viewer 的发送队列满了以后，被丢掉的必须是最旧的帧，队列里留下的永远是最新的 N 帧。
// 全量快照只有最后一帧有价值，如果反过来丢最新帧，界面就会长时间停在改动之前的旧状态
// （管理员改完分组后机器跳回原组的老 bug）。队列深度按线上实际配置取 2。
func TestBroadcastDropOldestKeepsLatest(t *testing.T) {
	hub := NewHub()
	const depth = 2
	send := make(chan []byte, depth)
	hub.addViewer(&Client{hub: hub, send: send, role: "viewer"})

	// 连推 5 帧，远超队列容量，且全程没有 writePump 在消费
	frames := []string{"f1", "f2", "f3", "f4", "f5"}
	for _, f := range frames {
		hub.BroadcastToViewers([]byte(f))
	}

	if len(send) != depth {
		t.Fatalf("队列应被填满到 %d 帧，实际 %d", depth, len(send))
	}
	want := frames[len(frames)-depth:] // 期望留下最后两帧 f4 f5
	for i, w := range want {
		got := string(<-send)
		if got != w {
			t.Fatalf("队列第 %d 帧应为最新的 %q，实际 %q（说明丢的是最新帧而非最旧帧）", i, w, got)
		}
	}
}

// TestViewerUpgraderNegotiatesCompression 验证 viewer 连接确实协商上了 permessage-deflate。
// 压缩是本次修复的关键：2500 台时全量快照单帧约 1.6MB，不压缩浏览器就会消费不过来，
// 队列里堆着改动前的陈旧快照逐帧回放，界面表现为「改完分组机器跳回原组」。
// 一旦哪天误把 viewer 换回不压缩的 upgrader，这个用例会立刻失败。
func TestViewerUpgraderNegotiatesCompression(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := viewerUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.EnableWriteCompression(true)
		_ = conn.SetCompressionLevel(1)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"agents","seq":1}`))
	}))
	defer srv.Close()

	dialer := websocket.Dialer{EnableCompression: true}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/viewer", nil)
	if err != nil {
		t.Fatalf("viewer 握手失败: %v", err)
	}
	defer conn.Close()

	ext := resp.Header.Get("Sec-WebSocket-Extensions")
	if !strings.Contains(ext, "permessage-deflate") {
		t.Fatalf("viewer 连接未协商压缩，Sec-WebSocket-Extensions=%q", ext)
	}

	// 压缩通道下报文仍要能正常收发
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取压缩帧失败: %v", err)
	}
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "agents" {
		t.Fatalf("压缩帧内容异常: raw=%q err=%v", string(raw), err)
	}
}
