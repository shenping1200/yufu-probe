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
	geoMu    sync.Mutex
	geoCache = map[string]geoInfo{}
	geoHTTP  = &http.Client{Timeout: 4 * time.Second}
)

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
	info, ok := geoCache[ip]
	geoMu.Unlock()
	if ok {
		return info.Display, info.Code
	}
	go func() {
		info := fetchCountry(ip)
		if info == nil {
			return
		}
		geoMu.Lock()
		geoCache[ip] = *info
		geoMu.Unlock()
		db.Exec(`UPDATE agents SET country=?, country_code=? WHERE uuid=?`, info.Display, info.Code, uuid)
		// 查成功后立即回写内存态，避免运行中 country/country_code 停留在 server 启动时的旧值
		live.SetCountry(uuid, info.Display, info.Code)
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
