package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
)

// deployKeyEnv 自动部署密码的加密密钥环境变量。
// 任意长度字符串会经 SHA-256 派生为 32 字节 AES-256 密钥，
// 因此不必要求用户精确输入 32 字节；但必须设置，否则无法解密已存密码。
const deployKeyEnv = "YUFU_DEPLOY_KEY"

func deployKey() ([]byte, error) {
	raw := os.Getenv(deployKeyEnv)
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New(deployKeyEnv + " 未设置，无法加解密自动部署密码")
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:], nil
}

// encryptPassword AES-GCM 加密，返回 base64(nonce+ciphertext)
func encryptPassword(plain string) (string, error) {
	key, err := deployKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// decryptPassword 解密；空密文返回空密码（规则未设密码时）
func decryptPassword(enc string) (string, error) {
	if strings.TrimSpace(enc) == "" {
		return "", nil
	}
	key, err := deployKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足")
	}
	pt, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("密码解密失败（密钥不匹配或密文损坏）")
	}
	return string(pt), nil
}

// DeployRule 一条自动部署规则
type DeployRule struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	SourceGroups []string `json:"source_groups"`
	Command      string   `json:"command"`
	TargetGroup  string   `json:"target_group"`
	FailGroup    string   `json:"fail_group"`
	Enabled      bool     `json:"enabled"`
	Concurrency  int      `json:"concurrency"`
	Timeout      int      `json:"timeout"`
	CreatedAt    int64    `json:"created_at"`
	HasPassword  bool     `json:"has_password"`
	pwEnc        string   // 内部：加密后的密码，不对外序列化
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListDeployRules 返回全部规则（不含密码密文，仅给 has_password 标记）
func ListDeployRules(db *sql.DB) ([]DeployRule, error) {
	rows, err := db.Query(`SELECT id, name, source_groups, command, target_group, fail_group, enabled, concurrency, timeout, created_at, ssh_password_enc FROM deploy_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeployRule
	for rows.Next() {
		var r DeployRule
		var src, enc sql.NullString
		var enabled, conc int
		if err := rows.Scan(&r.ID, &r.Name, &src, &r.Command, &r.TargetGroup, &r.FailGroup, &enabled, &conc, &r.Timeout, &r.CreatedAt, &enc); err != nil {
			return nil, err
		}
		if src.Valid && src.String != "" {
			_ = json.Unmarshal([]byte(src.String), &r.SourceGroups)
		}
		r.Enabled = enabled != 0
		if conc <= 0 {
			r.Concurrency = 50
		}
		if r.Timeout <= 0 {
			r.Timeout = 60
		}
		r.HasPassword = strings.TrimSpace(enc.String) != ""
		r.pwEnc = enc.String
		out = append(out, r)
	}
	return out, nil
}

// CreateDeployRule 新建规则，password 必填并加密落库
func CreateDeployRule(db *sql.DB, r *DeployRule, password string) error {
	enc, err := encryptPassword(password)
	if err != nil {
		return err
	}
	sg, _ := json.Marshal(r.SourceGroups)
	if r.Concurrency <= 0 {
		r.Concurrency = 50
	}
	if r.Timeout <= 0 {
		r.Timeout = 60
	}
	_, err = db.Exec(`INSERT INTO deploy_rules (name, source_groups, command, target_group, fail_group, enabled, concurrency, timeout, ssh_password_enc, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.Name, string(sg), r.Command, r.TargetGroup, r.FailGroup, boolToInt(r.Enabled), r.Concurrency, r.Timeout, enc, time.Now().Unix())
	return err
}

// UpdateDeployRule 更新规则；password 为 nil 时保留原密码密文
func UpdateDeployRule(db *sql.DB, id int64, r *DeployRule, password *string) error {
	sg, _ := json.Marshal(r.SourceGroups)
	if r.Concurrency <= 0 {
		r.Concurrency = 50
	}
	if r.Timeout <= 0 {
		r.Timeout = 60
	}
	if password != nil {
		enc, err := encryptPassword(*password)
		if err != nil {
			return err
		}
		_, err = db.Exec(`UPDATE deploy_rules SET name=?, source_groups=?, command=?, target_group=?, fail_group=?, enabled=?, concurrency=?, timeout=?, ssh_password_enc=? WHERE id=?`,
			r.Name, string(sg), r.Command, r.TargetGroup, r.FailGroup, boolToInt(r.Enabled), r.Concurrency, r.Timeout, enc, id)
		return err
	}
	_, err := db.Exec(`UPDATE deploy_rules SET name=?, source_groups=?, command=?, target_group=?, fail_group=?, enabled=?, concurrency=?, timeout=? WHERE id=?`,
		r.Name, string(sg), r.Command, r.TargetGroup, r.FailGroup, boolToInt(r.Enabled), r.Concurrency, r.Timeout, id)
	return err
}

// DeleteDeployRule 删除规则
func DeleteDeployRule(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM deploy_rules WHERE id=?`, id)
	return err
}

// setDeployState 把某台机器的部署状态记为终态（done/failed），防止重复部署
func setDeployState(db *sql.DB, uuid, state string) error {
	_, err := db.Exec(`UPDATE agents SET deploy_state=? WHERE uuid=?`, state, uuid)
	return err
}

// pendingDeployUUIDs 返回「处于源分组、尚未部署完成、且在线」的机器 uuid 列表
func pendingDeployUUIDs(db *sql.DB, sources []string) ([]string, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	ph := strings.Repeat("?,", len(sources))
	ph = ph[:len(ph)-1]
	args := make([]interface{}, len(sources))
	for i, s := range sources {
		args[i] = s
	}
	rows, err := db.Query(`SELECT uuid FROM agents WHERE group_name IN (`+ph+`) AND (deploy_state IS NULL OR deploy_state NOT IN ('done','failed')) AND online=1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// effectiveSSHPassword 解析出实际用于 Web SSH 鉴权的密码：显式设置的 ssh_password 优先，
// 否则回退到管理员密码（与 terminal.go / exec.go 保持一致）。
// 自动部署复用同一套 Web SSH 通道，因此规则里保存的密码也必须与这里一致才算鉴权通过。
func effectiveSSHPassword(cfg *Config) string {
	if cfg != nil && cfg.SSHPassword != "" {
		return cfg.SSHPassword
	}
	if cfg != nil && cfg.Admin.Password != "" {
		return cfg.Admin.Password
	}
	return ""
}

// runDeployScheduler 自动部署调度器：每 10s 扫描启用规则，对源分组内「待部署且在线」的机器
// 并发下发部署命令；成功(exit 0)→移到目标分组并标记 done；失败/超时→移到失败分组并标记 failed。
// 用内存 in-flight 集防止同一台被重复捞取；进程重启后 pending 机器会重新部署（命令应幂等）。
// 规则里保存的 Web SSH 密码必须与服务端配置一致（effectiveSSHPassword）才允许执行，
// 否则视为配置错误，跳过该规则并记录日志（与浏览器终端手动输密码鉴权同口径）。
func runDeployScheduler(cfg *Config, db *sql.DB, hub *Hub) {
	inFlight := make(map[string]struct{})
	var mu sync.Mutex
	if os.Getenv(deployKeyEnv) == "" {
		log.Printf("[deploy] 警告：%s 未设置，已存密码的规则将无法解密（这些规则不会执行）", deployKeyEnv)
	}
	eff := effectiveSSHPassword(cfg)
	if eff == "" {
		log.Printf("[deploy] 警告：未配置 ssh_password 且管理员密码为空，自动部署规则将无法鉴权（全部跳过）")
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	log.Printf("[deploy] 调度器已启动（每 10s 扫描一次启用规则）")
	for range ticker.C {
		rules, err := ListDeployRules(db)
		if err != nil {
			log.Printf("[deploy] 读取规则失败: %v", err)
			continue
		}
		if len(rules) > 0 {
			log.Printf("[deploy] tick: 扫描到 %d 条规则", len(rules))
		}
		var changed int32
		for i := range rules {
			rule := rules[i]
			if !rule.Enabled {
				continue
			}
			pw, err := decryptPassword(rule.pwEnc)
			if err != nil {
				log.Printf("[deploy] 规则 %d(%s) 解密密码失败: %v（请检查 %s）", rule.ID, rule.Name, err, deployKeyEnv)
				continue
			}
			if pw != eff {
				log.Printf("[deploy] 规则 %d(%s) 密码与服务器 Web SSH 密码不符，跳过（请在规则中填入正确的 Web SSH 密码）", rule.ID, rule.Name)
				continue
			}
			cands, err := pendingDeployUUIDs(db, rule.SourceGroups)
			if err != nil {
				log.Printf("[deploy] 规则 %d 查询候选失败: %v", rule.ID, err)
				continue
			}
			if len(cands) == 0 {
				continue
			}
			sem := make(chan struct{}, rule.Concurrency)
			var wg sync.WaitGroup
			for _, uuid := range cands {
				mu.Lock()
				if _, ok := inFlight[uuid]; ok {
					mu.Unlock()
					continue
				}
				inFlight[uuid] = struct{}{}
				mu.Unlock()
				wg.Add(1)
				go func(u string) {
					defer wg.Done()
					defer func() {
						mu.Lock()
						delete(inFlight, u)
						mu.Unlock()
					}()
					sem <- struct{}{}
					defer func() { <-sem }()
					agent := hub.findAgent(u)
					if agent == nil {
						return // 离线，下一轮再试
					}
					res := runExec(agent, u, rule.Command, time.Duration(rule.Timeout)*time.Second)
					if res.Status == "ok" && res.ExitCode == 0 {
						if rule.TargetGroup != "" {
							_ = SetAgentGroup(db, u, rule.TargetGroup)
							live.PatchAgentFields(u, &rule.TargetGroup, nil, nil)
							live.AddGroup(rule.TargetGroup)
						}
						_ = setDeployState(db, u, "done")
					} else {
						if rule.FailGroup != "" {
							_ = SetAgentGroup(db, u, rule.FailGroup)
							live.PatchAgentFields(u, &rule.FailGroup, nil, nil)
							live.AddGroup(rule.FailGroup)
						}
						_ = setDeployState(db, u, "failed")
						log.Printf("[deploy] 规则 %d(%s) 机器 %s 部署失败: status=%s exit=%d err=%s", rule.ID, rule.Name, u, res.Status, res.ExitCode, res.Error)
					}
					atomic.StoreInt32(&changed, 1)
				}(uuid)
			}
			wg.Wait()
		}
		if atomic.LoadInt32(&changed) == 1 {
			broadcastAgents(hub)
		}
	}
}

// ---------- 自动部署规则 REST（仅管理员）----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// deployRuleReq 是创建/更新共用的请求体
type deployRuleReq struct {
	Name         string   `json:"name"`
	SourceGroups []string `json:"source_groups"`
	Command      string   `json:"command"`
	TargetGroup  string   `json:"target_group"`
	FailGroup    string   `json:"fail_group"`
	Enabled      bool     `json:"enabled"`
	Concurrency  int      `json:"concurrency"`
	Timeout      int      `json:"timeout"`
	Password     string   `json:"password"`
}

func deployRulesListHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := ListDeployRules(db)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, rules)
	}
}

func deployRulesCreateHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req deployRuleReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			strings.TrimSpace(req.Command) == "" || strings.TrimSpace(req.Password) == "" {
			http.Error(w, "bad request（命令与密码必填）", http.StatusBadRequest)
			return
		}
		rule := &DeployRule{
			Name:         req.Name,
			SourceGroups: req.SourceGroups,
			Command:      req.Command,
			TargetGroup:  req.TargetGroup,
			FailGroup:    req.FailGroup,
			Enabled:      req.Enabled,
			Concurrency:  req.Concurrency,
			Timeout:      req.Timeout,
		}
		if err := CreateDeployRule(db, rule, req.Password); err != nil {
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}

func deployRulesUpdateHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		var req deployRuleReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rule := &DeployRule{
			Name:         req.Name,
			SourceGroups: req.SourceGroups,
			Command:      req.Command,
			TargetGroup:  req.TargetGroup,
			FailGroup:    req.FailGroup,
			Enabled:      req.Enabled,
			Concurrency:  req.Concurrency,
			Timeout:      req.Timeout,
		}
		var pw *string
		if req.Password != "" {
			p := req.Password
			pw = &p
		}
		if err := UpdateDeployRule(db, id, rule, pw); err != nil {
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}

func deployRulesDeleteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := DeleteDeployRule(db, id); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}
