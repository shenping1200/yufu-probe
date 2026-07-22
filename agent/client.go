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
