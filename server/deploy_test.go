package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	insert("a2", "", 1, "done")      // 未分组+在线+done → 排除
	insert("a3", "", 0, "pending")   // 未分组+离线 → 排除
	insert("a4", "g1", 1, "pending") // g1+在线+pending → 选中
	insert("a5", "g1", 1, "failed")  // g1+失败 → 排除
	insert("a6", "g2", 1, "pending") // g2（非源分组）→ 排除

	got, err := pendingDeployUUIDs(db, []string{"", "g1"})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, u := range got {
		m[u] = true
	}
	if !m["a1"] || !m["a4"] {
		t.Fatalf("应含 a1,a4，实际 %v", got)
	}
	if m["a2"] || m["a3"] || m["a5"] || m["a6"] {
		t.Fatalf("不应含 a2/a3/a5/a6，实际 %v", got)
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
