package main

import (
	"database/sql"
	"path/filepath"
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

// TestMonthKeyBeijingTimezone 验证月份键固定按北京时间（UTC+8）切分。
// 这是"每月 1 日 0 点重置流量"的时间锚点：容器基础镜像是 alpine 且不带 tzdata，
// 若用 time.Local 会退化成 UTC，重置会推迟到北京时间早上 8 点才发生。
func TestMonthKeyBeijingTimezone(t *testing.T) {
	// 北京时间 2026-08-01 00:30 == UTC 2026-07-31 16:30
	// 按北京时间应已进入 8 月；按 UTC 会错判成 7 月。
	justAfterRollover := time.Date(2026, 7, 31, 16, 30, 0, 0, time.UTC)
	if got := monthOf(justAfterRollover); got != "2026-08" {
		t.Fatalf("北京时间 8/1 00:30 应属 2026-08，实际 %s（说明时区没按东八区切）", got)
	}

	// 北京时间 2026-07-31 23:30 == UTC 2026-07-31 15:30，仍属 7 月
	justBeforeRollover := time.Date(2026, 7, 31, 15, 30, 0, 0, time.UTC)
	if got := monthOf(justBeforeRollover); got != "2026-07" {
		t.Fatalf("北京时间 7/31 23:30 应属 2026-07，实际 %s", got)
	}

	// curMonth 必须与 monthOf(now) 同源
	if curMonth() != monthOf(time.Now()) {
		t.Fatalf("curMonth 与 monthOf 口径不一致")
	}
}

// TestFlushMonthRollover 验证跨月重置：
//  1. 跨月时所有 agent 内存态的本月流量清零（面板立刻从 0 重新计，不必重启容器）；
//  2. 跨月那一轮的残余增量仍记到【上一个月】；
//  3. 上个月的 traffic_monthly 行原样保留（历史可查）；
//  4. 新月份的流量从 0 重新累加，写入新月份的行。
func TestFlushMonthRollover(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	defer db.Close()

	s := NewServerState()
	s.lastMonth = "2026-07"

	// 7 月已累计 5000，本轮又有 100 的残余增量待落库
	s.ApplyReport(AgentReport{UUID: "u1", RxDelta: 100, TxDelta: 100}, "", "")
	s.agents["u1"].RxMonth = 5000
	s.agents["u1"].TxMonth = 5000

	// 跨入 8 月
	s.Flush(db, "2026-08")

	if got := s.agents["u1"].RxMonth; got != 0 {
		t.Fatalf("跨月后内存态 RxMonth 应清零，实际 %v", got)
	}
	if got := s.agents["u1"].TxMonth; got != 0 {
		t.Fatalf("跨月后内存态 TxMonth 应清零，实际 %v", got)
	}
	if s.lastMonth != "2026-08" {
		t.Fatalf("lastMonth 应推进到 2026-08，实际 %s", s.lastMonth)
	}
	if got := trafficOf(t, db, "u1", "2026-07"); got != 100 {
		t.Fatalf("跨月残余增量应记入 2026-07，实际 %v", got)
	}
	if got := trafficOf(t, db, "u1", "2026-08"); got != 0 {
		t.Fatalf("跨月那一轮不应写入 2026-08，实际 %v", got)
	}

	// 8 月正常累加
	s.ApplyReport(AgentReport{UUID: "u1", RxDelta: 300, TxDelta: 300}, "", "")
	s.Flush(db, "2026-08")

	if got := s.agents["u1"].RxMonth; got != 300 {
		t.Fatalf("新月份应从 0 重新累加，实际 %v", got)
	}
	if got := trafficOf(t, db, "u1", "2026-08"); got != 300 {
		t.Fatalf("2026-08 行应为 300，实际 %v", got)
	}
	if got := trafficOf(t, db, "u1", "2026-07"); got != 100 {
		t.Fatalf("上月历史必须保留为 100，实际 %v", got)
	}
}

// trafficOf 读取指定机器指定月份的 rx_total（不存在返回 0）
func trafficOf(t *testing.T, db *sql.DB, uuid, month string) float64 {
	t.Helper()
	var rx float64
	err := db.QueryRow(`SELECT rx_total FROM traffic_monthly WHERE uuid=? AND year_month=?`, uuid, month).Scan(&rx)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("查询 traffic_monthly 失败: %v", err)
	}
	return rx
}
