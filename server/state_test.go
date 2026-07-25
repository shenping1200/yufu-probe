package main

import (
	"testing"
	"time"
)

// TestApplyReportDualStack 验证：
//  1. 双栈上报时 v4/v6 均正确透传；
//  2. 老 agent 只报单个 public_ip（v4）时，自动归类到 PublicIP4；
//  3. 老 agent 只报单个 public_ip（v6）时，自动归类到 PublicIP6。
// 保证滚动升级（新 server + 老 agent）期间列表 IP 不空白。
func TestApplyReportDualStack(t *testing.T) {
	s := NewServerState()

	// 1) 双栈上报
	s.ApplyReport(AgentReport{UUID: "u1", PublicIP4: "1.2.3.4", PublicIP6: "2603::1"}, "", "")
	if s.agents["u1"].PublicIP4 != "1.2.3.4" || s.agents["u1"].PublicIP6 != "2603::1" {
		t.Fatalf("双栈透传失败: %+v", s.agents["u1"])
	}

	// 2) 老 agent 只报 public_ip (v4)
	s.ApplyReport(AgentReport{UUID: "u2", PublicIP: "9.9.9.9"}, "", "")
	if s.agents["u2"].PublicIP4 != "9.9.9.9" || s.agents["u2"].PublicIP6 != "" {
		t.Fatalf("老字段 v4 归类失败: %+v", s.agents["u2"])
	}

	// 3) 老 agent 只报 public_ip (v6)
	s.ApplyReport(AgentReport{UUID: "u3", PublicIP: "2603::2"}, "", "")
	if s.agents["u3"].PublicIP6 != "2603::2" || s.agents["u3"].PublicIP4 != "" {
		t.Fatalf("老字段 v6 归类失败: %+v", s.agents["u3"])
	}
}

// TestApplyReportCountryCode 验证：
//  1. cache hit 路径：country/country_code 同步透传；
//  2. 空值不覆盖既有值；
//  3. 异步地理查询回调 SetCountry 能立即回写内存态（修复运行中 country_code 不更新的缺陷）。
func TestApplyReportCountryCode(t *testing.T) {
	s := NewServerState()

	// 1) 同步透传
	s.ApplyReport(AgentReport{UUID: "u1"}, "🇸🇬 Singapore", "SG")
	if s.agents["u1"].Country != "🇸🇬 Singapore" || s.agents["u1"].CountryCode != "SG" {
		t.Fatalf("country/code 透传失败: %+v", s.agents["u1"])
	}

	// 2) 空值不覆盖既有值
	s.ApplyReport(AgentReport{UUID: "u1"}, "", "")
	if s.agents["u1"].Country != "🇸🇬 Singapore" || s.agents["u1"].CountryCode != "SG" {
		t.Fatalf("空值应保留既有 country/code: %+v", s.agents["u1"])
	}

	// 3) 异步回写（模拟 lookupCountry 的 goroutine 回调）
	s.SetCountry("u1", "🇺🇸 United States", "US")
	if s.agents["u1"].Country != "🇺🇸 United States" || s.agents["u1"].CountryCode != "US" {
		t.Fatalf("SetCountry 回写失败: %+v", s.agents["u1"])
	}
}

// TestCleanupStale 验证幽灵清理只杀"僵尸"，不误伤真实机器：
//  1. 幽灵孤儿（online=0 且 last_seen=0，被写入 DB 但从未连上来上报）→ 必须被清；
//  2. 真实离线机（online=0 但 last_seen>0，曾上报后来掉线）→ 必须保留；
//  3. 在线机（online=1）→ 必须保留。
// 这样监控面板既能挡住离线幽灵，又能正常显示真掉线的机器。
func TestCleanupStale(t *testing.T) {
	s := NewServerState()
	now := time.Now().Unix()

	s.agents["ghost"] = &AgentRow{UUID: "ghost", Online: false, LastSeen: 0}
	s.agents["real-offline"] = &AgentRow{UUID: "real-offline", Online: false, LastSeen: now - 3600}
	s.agents["online"] = &AgentRow{UUID: "online", Online: true, LastSeen: now}

	// 无 DB（nil）仅验证内存态清理逻辑
	s.CleanupStale(nil)

	if _, ok := s.agents["ghost"]; ok {
		t.Fatalf("幽灵孤儿未被清理")
	}
	if _, ok := s.agents["real-offline"]; !ok {
		t.Fatalf("真实离线机被误删（last_seen>0 不应被杀）")
	}
	if _, ok := s.agents["online"]; !ok {
		t.Fatalf("在线机被误删")
	}
}

// TestApplyReportEphemeralTraffic 验证压测机（ephemeral）的本月流量在内存中累加，
// 让面板每行「本月流量」和顶部月度聚合能反映压测流量（更真实）；同时确认
// s.traffic 与 s.dirty 不被污染——ephemeral 仍不入库、不落盘，保持"内存临时态"。
func TestApplyReportEphemeralTraffic(t *testing.T) {
	s := NewServerState()
	rep := AgentReport{UUID: "ephemeral-1", RxDelta: 1024, TxDelta: 2048}
	s.ApplyReportEphemeral(rep, "", "")
	if got := s.agents["ephemeral-1"].RxMonth; got != 1024 {
		t.Fatalf("ephemeral RxMonth 应累加 1024，实际 %v", got)
	}
	if got := s.agents["ephemeral-1"].TxMonth; got != 2048 {
		t.Fatalf("ephemeral TxMonth 应累加 2048，实际 %v", got)
	}
	if _, ok := s.traffic["ephemeral-1"]; ok {
		t.Fatalf("ephemeral 不应写入 s.traffic（避免落库）")
	}
	if _, ok := s.dirty["ephemeral-1"]; ok {
		t.Fatalf("ephemeral 不应被标记 dirty")
	}

	// 连续上报 RxDelta/TxDelta>0 时应继续累加
	s.ApplyReportEphemeral(AgentReport{UUID: "ephemeral-1", RxDelta: 512, TxDelta: 256}, "", "")
	if got := s.agents["ephemeral-1"].RxMonth; got != 1024+512 {
		t.Fatalf("第二次上报 RxMonth 累加错误：%v", got)
	}
	if got := s.agents["ephemeral-1"].TxMonth; got != 2048+256 {
		t.Fatalf("第二次上报 TxMonth 累加错误：%v", got)
	}
}
