package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
)

func testDB(t *testing.T) *sql.DB {
	dir := t.TempDir()
	db, err := InitDB(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	os.Setenv(deployKeyEnv, "test-key-123")
	defer os.Unsetenv(deployKeyEnv)
	c, err := encryptPassword("my-ssh-pass")
	if err != nil {
		t.Fatal(err)
	}
	// 相同明文两次密文应不同（nonce 随机化）
	c2, _ := encryptPassword("my-ssh-pass")
	if c == c2 {
		t.Fatal("密文应随机化（nonce 不同）")
	}
	p, err := decryptPassword(c)
	if err != nil {
		t.Fatal(err)
	}
	if p != "my-ssh-pass" {
		t.Fatalf("加密往返失败：得到 %q", p)
	}
	// 错误密钥解密必须失败
	os.Setenv(deployKeyEnv, "wrong-key")
	if _, err := decryptPassword(c); err == nil {
		t.Fatal("错误密钥应解密失败")
	}
}

func TestDeployRuleCRUD(t *testing.T) {
	os.Setenv(deployKeyEnv, "test-key-123")
	defer os.Unsetenv(deployKeyEnv)
	db := testDB(t)
	r := &DeployRule{
		Name: "r1", SourceGroups: []string{"未分组", "g1"}, Command: "id",
		TargetGroup: "ok", FailGroup: "fail", Enabled: true, Concurrency: 10, Timeout: 30,
	}
	if err := CreateDeployRule(db, r, "pw"); err != nil {
		t.Fatal(err)
	}
	rules, err := ListDeployRules(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("应有 1 条规则，实际 %d", len(rules))
	}
	got := rules[0]
	if got.Name != "r1" || len(got.SourceGroups) != 2 {
		t.Fatalf("字段解析错误：%+v", got)
	}
	if got.Concurrency != 10 {
		t.Fatalf("ListDeployRules 应正确返回 concurrency，实际 %d", got.Concurrency)
	}
	if !got.HasPassword {
		t.Fatal("HasPassword 应为 true")
	}
	// 密码密文绝不能出现在对外 JSON 中
	b, _ := json.Marshal(got)
	if bytes.Contains(b, []byte("ssh_password_enc")) {
		t.Fatal("密码密文不应出现在序列化输出中")
	}
	if got.pwEnc == "" {
		t.Fatal("内部密文应已填充")
	}
	// 更新（不传密码，应保留原密文）
	if err := UpdateDeployRule(db, got.ID, &DeployRule{Name: "r2", Command: "id", TargetGroup: "ok", FailGroup: "fail", Enabled: false}, nil); err != nil {
		t.Fatal(err)
	}
	rules, _ = ListDeployRules(db)
	if rules[0].Name != "r2" || rules[0].Enabled {
		t.Fatal("更新未生效")
	}
	if !rules[0].HasPassword {
		t.Fatal("不传密码时原密码应保留")
	}
	// 删除
	if err := DeleteDeployRule(db, got.ID); err != nil {
		t.Fatal(err)
	}
	rules, _ = ListDeployRules(db)
	if len(rules) != 0 {
		t.Fatal("删除未生效")
	}
}

func TestPendingDeployUUIDs(t *testing.T) {
	os.Setenv(deployKeyEnv, "test-key")
	defer os.Unsetenv(deployKeyEnv)
	db := testDB(t)
	insert := func(uuid, grp string, online int, ds string) {
		if _, err := db.Exec(`INSERT INTO agents (uuid, group_name, online, deploy_state, last_seen) VALUES (?,?,?,?,1)`, uuid, grp, online, ds); err != nil {
			t.Fatal(err)
		}
	}
	insert("a1", "", 1, "pending")   // 未分组+在线+pending → 选中
	insert("a2", "", 1, "done")      // 未分组+在线+done → 排除（已成功，不重跑）
	insert("a3", "", 0, "pending")   // 未分组+离线 → 排除
	insert("a4", "g1", 1, "pending") // g1+在线+pending → 选中
	insert("a5", "g1", 1, "failed")  // g1+失败 → 选中（支持重试失败机器）
	insert("a6", "g2", 1, "pending") // g2（非源分组）→ 排除

	got, err := pendingDeployUUIDs(db, []string{"", "g1"})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, u := range got {
		m[u] = true
	}
	if !m["a1"] || !m["a4"] || !m["a5"] {
		t.Fatalf("应含 a1,a4,a5，实际 %v", got)
	}
	if m["a2"] || m["a3"] || m["a6"] {
		t.Fatalf("不应含 a2/a3/a6，实际 %v", got)
	}
}

func TestDeployStateIdempotent(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO agents (uuid, group_name, online, deploy_state, last_seen) VALUES ('x','',1,'pending',1)`); err != nil {
		t.Fatal(err)
	}
	got, _ := pendingDeployUUIDs(db, []string{""})
	if len(got) != 1 {
		t.Fatal("初始应为待部署")
	}
	if err := setDeployState(db, "x", "done"); err != nil {
		t.Fatal(err)
	}
	got, _ = pendingDeployUUIDs(db, []string{""})
	if len(got) != 0 {
		t.Fatal("标记为 done 后不应再被选中（幂等，防重复部署）")
	}
}

func TestResetDeployState(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO agents (uuid, group_name, online, deploy_state, last_seen) VALUES ('x','',1,'done',1)`); err != nil {
		t.Fatal(err)
	}
	// 标记为 done 后不应被选中
	if got, _ := pendingDeployUUIDs(db, []string{""}); len(got) != 0 {
		t.Fatal("done 状态不应被选中")
	}
	// 管理员把机器改分组时（resetDeployState）应清空终态，使其重新成为待部署
	if err := resetDeployState(db, "x"); err != nil {
		t.Fatal(err)
	}
	if got, _ := pendingDeployUUIDs(db, []string{""}); len(got) != 1 {
		t.Fatal("resetDeployState 后机器应重新成为待部署（可被自动部署规则接管）")
	}
}

func TestEffectiveSSHPassword(t *testing.T) {
	// 显式设置 ssh_password 时优先
	if got := effectiveSSHPassword(&Config{SSHPassword: "ssh-pass", Admin: AdminConfig{Password: "admin-pass"}}); got != "ssh-pass" {
		t.Fatalf("应优先返回 ssh_password，实际 %q", got)
	}
	// 未设置 ssh_password 时回退到管理员密码
	if got := effectiveSSHPassword(&Config{Admin: AdminConfig{Password: "admin-pass"}}); got != "admin-pass" {
		t.Fatalf("应回退到管理员密码，实际 %q", got)
	}
	// 两者皆空时返回空（调度器会跳过所有规则）
	if got := effectiveSSHPassword(&Config{}); got != "" {
		t.Fatalf("应返回空，实际 %q", got)
	}
}

func TestDeployPaused(t *testing.T) {
	db := testDB(t)
	h := deployPausedHandler(db)

	// GET 默认未暂停
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/deploy-paused", nil))
	var g struct{ Paused bool }
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if g.Paused {
		t.Fatal("默认应为未暂停")
	}

	// POST 暂停
	body, _ := json.Marshal(map[string]bool{"paused": true})
	rec2 := httptest.NewRecorder()
	h(rec2, httptest.NewRequest("POST", "/api/deploy-paused", bytes.NewReader(body)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST 应 200，实际 %d", rec2.Code)
	}

	// GET 应已暂停，且 kv 持久化
	rec3 := httptest.NewRecorder()
	h(rec3, httptest.NewRequest("GET", "/api/deploy-paused", nil))
	var g2 struct{ Paused bool }
	if err := json.Unmarshal(rec3.Body.Bytes(), &g2); err != nil {
		t.Fatal(err)
	}
	if !g2.Paused {
		t.Fatal("POST 暂停后 GET 应返回 paused=true")
	}
	if GetKV(db, "deploy_paused", "0") != "1" {
		t.Fatal("deploy_paused 应持久化为 1")
	}

	// POST 恢复
	body2, _ := json.Marshal(map[string]bool{"paused": false})
	rec4 := httptest.NewRecorder()
	h(rec4, httptest.NewRequest("POST", "/api/deploy-paused", bytes.NewReader(body2)))
	if rec4.Code != http.StatusOK {
		t.Fatalf("恢复 POST 应 200，实际 %d", rec4.Code)
	}
	if GetKV(db, "deploy_paused", "0") != "0" {
		t.Fatal("恢复后 deploy_paused 应回到 0")
	}
}

func TestDeployRulePartialUpdate(t *testing.T) {
	os.Setenv(deployKeyEnv, "test-key-123")
	defer os.Unsetenv(deployKeyEnv)
	db := testDB(t)

	// 创建一条完整规则
	orig := &DeployRule{
		Name: "完整规则", SourceGroups: []string{"src"}, Command: "apt update",
		TargetGroup: "ok", FailGroup: "err", Enabled: true, Concurrency: 10, Timeout: 30,
	}
	if err := CreateDeployRule(db, orig, "pw"); err != nil {
		t.Fatal(err)
	}
	rules, _ := ListDeployRules(db)
	id := rules[0].ID

	// 用真实 mux 路由（确保 mux.Vars 能正确提取 {id}）
	router := newmux()
	router.Handle("/api/deploy-rules/{id}", deployRulesUpdateHandler(db))

	// 模拟前端 toggle：只发 {enabled:false}，其他字段全零值
	body, _ := json.Marshal(map[string]any{"enabled": false})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("PATCH", fmt.Sprintf("/api/deploy-rules/%d", id), bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 验证：enabled 变了，但其他字段保持原值
	after, err := GetDeployRuleByID(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled {
		t.Fatal("应已停用")
	}
	if after.Name != "完整规则" {
		t.Fatalf("name 应保留，实际 %q", after.Name)
	}
	if after.Command != "apt update" {
		t.Fatalf("command 应保留，实际 %q", after.Command)
	}
	if len(after.SourceGroups) != 1 || after.SourceGroups[0] != "src" {
		t.Fatalf("source_groups 应保留，实际 %v", after.SourceGroups)
	}
	if after.TargetGroup != "ok" {
		t.Fatalf("target_group 应保留，实际 %q", after.TargetGroup)
	}
	if after.FailGroup != "err" {
		t.Fatalf("fail_group 应保留，实际 %q", after.FailGroup)
	}
	if after.Concurrency != 10 {
		t.Fatalf("concurrency 应保留，实际 %d", after.Concurrency)
	}

	// 再 toggle 回启用
	body2, _ := json.Marshal(map[string]any{"enabled": true})
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, httptest.NewRequest("PATCH", fmt.Sprintf("/api/deploy-rules/%d", id), bytes.NewReader(body2)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("恢复 PATCH 应 200，实际 %d: %s", rec2.Code, rec2.Body.String())
	}
	final, _ := GetDeployRuleByID(db, id)
	if !final.Enabled {
		t.Fatal("应已启用")
	}
	if final.Name != "完整规则" {
		t.Fatalf("toggle 往返后 name 仍应保留，实际 %q", final.Name)
	}
}

// newmux 创建一个干净的 gorilla/mux router（供单测使用）
func newmux() *mux.Router { return mux.NewRouter() }
