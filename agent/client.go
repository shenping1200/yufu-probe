package main

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

// Reporter 负责通过 WebSocket 长连接向服务端上报数据，断线自动重连
type Reporter struct {
	url  string
	uuid string
}

func NewReporter(server, token, uuid string) *Reporter {
	return &Reporter{
		url:  server + "/ws/agent?token=" + token,
		uuid: uuid,
	}
}

func buildMsg(uuid string, s *Snapshot) map[string]interface{} {
	return map[string]interface{}{
		"uuid":      uuid,
		"hostname":  s.Hostname,
		"ip":        s.IP,
		"public_ip": s.PublicIP,
		"public_ip4": s.PublicIP4,
		"public_ip6": s.PublicIP6,
		"os":        s.OS,
		"platform":  s.Platform,
		"boot_time":  s.BootTime,
		"uptime":     s.Uptime,
		"cpu":        s.CPU,
		"cpu_count":  s.CPUCount,
		"mem_used":   s.MemUsed,
		"mem_total": s.MemTotal,
		"disk_used": s.DiskUsed,
		"disk_total": s.DiskTotal,
		"rx_rate":   s.RxRate,
		"tx_rate":   s.TxRate,
		"rx_delta":  s.RxDelta,
		"tx_delta":  s.TxDelta,
	}
}

// Run 维持长连接并消费上报通道，断线指数退避重连
func (r *Reporter) Run(send <-chan *Snapshot) {
	var conn *websocket.Conn
	backoff := time.Second
	for {
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
			conn = c
			backoff = time.Second
			log.Printf("[agent] connected to %s", r.url)
		}
		snap := <-send
		data, _ := json.Marshal(buildMsg(r.uuid, snap))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("[agent] send failed: %v", err)
			conn.Close()
			conn = nil
			continue
		}
	}
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
