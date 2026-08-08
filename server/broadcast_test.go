package main

import (
	"encoding/json"
	"testing"
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
