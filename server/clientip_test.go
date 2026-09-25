package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mustCIDR 解析 CIDR，失败即崩（仅测试用）。
func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// reqWith 构造一个带 RemoteAddr 与 X-Forwarded-For 的请求。
func reqWith(remote, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		remote  string // RemoteAddr（含端口）
		xff     string
		trusted []*net.IPNet
		want    string
	}{
		// 公网直连（675 现状）：不信任 XFF，回落 RemoteAddr
		{"public-direct", "198.51.100.9:54321", "1.2.3.4", nil, "198.51.100.9"},
		// 回环反代单层
		{"loopback-single", "127.0.0.1:54321", "203.0.113.5", nil, "203.0.113.5"},
		// V7-1 修复点：两层反代 + 未配置 trusted_proxy，跳过私网反代自身 IP
		{"v7-1-two-layer-unconfigured", "10.0.0.1:54321", "203.0.113.5, 10.0.0.2", nil, "203.0.113.5"},
		// 两层反代 + 配置 trusted_proxy：显式跳过可信段
		{"two-layer-configured", "10.0.0.1:54321", "203.0.113.5, 10.0.0.2", []*net.IPNet{mustCIDR("10.0.0.0/8")}, "203.0.113.5"},
		// 未配置 + XFF 全私网（反代自身）：全部跳过 → 回落 RemoteAddr
		{"all-private-fallback", "10.0.0.1:54321", "10.0.0.2, 10.0.0.3", nil, "10.0.0.1"},
		// IPv4-mapped 规范化
		{"ipv4-mapped", "127.0.0.1:54321", "::ffff:1.2.3.4", nil, "1.2.3.4"},
		// 公网源命中 trusted 列表
		{"public-trusted", "198.51.100.9:54321", "203.0.113.5", []*net.IPNet{mustCIDR("198.51.100.0/24")}, "203.0.113.5"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clientIP(reqWith(c.remote, c.xff), c.trusted)
			if got != c.want {
				t.Errorf("clientIP() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRightmostValidXFF(t *testing.T) {
	cases := []struct {
		name    string
		xff     string
		trusted []*net.IPNet
		want    string
	}{
		{"single-public", "203.0.113.5", nil, "203.0.113.5"},
		{"v7-1-two-layer-unconfigured", "203.0.113.5, 10.0.0.2", nil, "203.0.113.5"},
		{"two-layer-configured", "203.0.113.5, 10.0.0.2", []*net.IPNet{mustCIDR("10.0.0.0/8")}, "203.0.113.5"},
		{"garbage-right", "xxx, 203.0.113.5", nil, "203.0.113.5"},
		{"ipv4-mapped", "::ffff:1.2.3.4", nil, "1.2.3.4"},
		{"all-private", "10.0.0.2, 10.0.0.3", nil, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rightmostValidXFF(c.xff, c.trusted)
			if got != c.want {
				t.Errorf("rightmostValidXFF() = %q, want %q", got, c.want)
			}
		})
	}
}
