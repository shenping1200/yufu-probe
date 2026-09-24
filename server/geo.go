package main

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	geoMu       sync.Mutex
	geoCache    = map[string]geoInfo{}
	geoNegUntil = map[string]time.Time{} // 负缓存：失败冷却，geoNegTTL 内不再重试同一 IP
	geoInflight = map[string]bool{}     // 在途去重：避免 cache miss 窗口内对同一 IP 并发起请求
	// geoSem 限制并发地理查询数，防止离线/限流时大量 IP 同时发起请求打挂服务端
	geoSem = make(chan struct{}, 100)
)

// geoHTTP 带连接数上限的 HTTP 客户端
var geoHTTP = &http.Client{
	Timeout: 4 * time.Second,
	Transport: &http.Transport{
		MaxConnsPerHost:     50,
		MaxIdleConns:        50,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 4 * time.Second,
	},
}

const geoNegTTL = 10 * time.Minute

type geoInfo struct {
	Display string
	Code    string
}

// isPrivateIP 判断内网/保留地址（无需地理定位，直接标记"内网"）
func isPrivateIP(ip string) bool {
	ipObj := net.ParseIP(ip)
	if ipObj == nil {
		return true
	}
	if ipObj.IsLoopback() || ipObj.IsLinkLocalUnicast() || ipObj.IsUnspecified() {
		return true
	}
	if v4 := ipObj.To4(); v4 != nil {
		switch {
		case v4[0] == 10:
			return true
		case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
			return true
		case v4[0] == 192 && v4[1] == 168:
			return true
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // CGNAT 100.64.0.0/10
			return true
		}
	}
	return false
}

// lookupCountry 返回 IP 对应的国家展示串（如 "🇸🇬 Singapore"）。
// 同步读内存缓存；公网 IP 未命中时异步拉取并回写 DB（同时写入 country_code），不阻塞上报通道。
// uuid 用于精确定位回写行（避免按公网 IP 更新时与表内网 IP 不匹配）。
func lookupCountry(db *sql.DB, ip, uuid string) (display, code string) {
	if ip == "" || isPrivateIP(ip) {
		return "", ""
	}
	geoMu.Lock()
	if info, ok := geoCache[ip]; ok {
		geoMu.Unlock()
		return info.Display, info.Code
	}
	// 负缓存命中：失败冷却期内直接返回，不再重试
	if t, ok := geoNegUntil[ip]; ok && time.Now().Before(t) {
		geoMu.Unlock()
		return "", ""
	}
	// 在途去重：已有查询在进行，复用其结果，不重复发请求（避免 goroutine / HTTP 风暴）
	if geoInflight[ip] {
		geoMu.Unlock()
		return "", ""
	}
	geoInflight[ip] = true
	geoMu.Unlock()

	go func() {
		geoSem <- struct{}{} // 限制并发请求数
		info := fetchCountry(ip)
		<-geoSem
		geoMu.Lock()
		if info != nil {
			geoCache[ip] = *info
			delete(geoNegUntil, ip)
		} else {
			geoNegUntil[ip] = time.Now().Add(geoNegTTL) // 失败也写负缓存，阻断持续风暴
		}
		delete(geoInflight, ip)
		geoMu.Unlock()
		if info != nil {
			db.Exec(`UPDATE agents SET country=?, country_code=? WHERE uuid=?`, info.Display, info.Code, uuid)
			// 查成功后立即回写内存态，避免运行中 country/country_code 停留在 server 启动时的旧值
			live.SetCountry(uuid, info.Display, info.Code)
		}
	}()
	return "", ""
}

// fetchCountry 调用 ipwho.is 获取国家名、国家代码与旗帜 emoji
func fetchCountry(ip string) *geoInfo {
	resp, err := geoHTTP.Get("https://ipwho.is/" + ip)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var g struct {
		Success     bool   `json:"success"`
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
		Flag        struct {
			Emoji string `json:"emoji"`
		} `json:"flag"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return nil
	}
	if !g.Success || g.Country == "" {
		return nil
	}
	display := g.Country
	if g.Flag.Emoji != "" {
		display = g.Flag.Emoji + " " + g.Country
	}
	return &geoInfo{Display: display, Code: g.CountryCode}
}
