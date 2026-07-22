package main

import "testing"

// TestApplyReportDualStack 验证：
//  1. 双栈上报时 v4/v6 均正确透传；
//  2. 老 agent 只报单个 public_ip（v4）时，自动归类到 PublicIP4；
//  3. 老 agent 只报单个 public_ip（v6）时，自动归类到 PublicIP6。
// 保证滚动升级（新 server + 老 agent）期间列表 IP 不空白。
func TestApplyReportDualStack(t *testing.T) {
	s := NewServerState()

	// 1) 双栈上报
	s.ApplyReport(AgentReport{UUID: "u1", PublicIP4: "1.2.3.4", PublicIP6: "2603::1"}, "")
	if s.agents["u1"].PublicIP4 != "1.2.3.4" || s.agents["u1"].PublicIP6 != "2603::1" {
		t.Fatalf("双栈透传失败: %+v", s.agents["u1"])
	}

	// 2) 老 agent 只报 public_ip (v4)
	s.ApplyReport(AgentReport{UUID: "u2", PublicIP: "9.9.9.9"}, "")
	if s.agents["u2"].PublicIP4 != "9.9.9.9" || s.agents["u2"].PublicIP6 != "" {
		t.Fatalf("老字段 v4 归类失败: %+v", s.agents["u2"])
	}

	// 3) 老 agent 只报 public_ip (v6)
	s.ApplyReport(AgentReport{UUID: "u3", PublicIP: "2603::2"}, "")
	if s.agents["u3"].PublicIP6 != "2603::2" || s.agents["u3"].PublicIP4 != "" {
		t.Fatalf("老字段 v6 归类失败: %+v", s.agents["u3"])
	}
}
