package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// AgentRow 单台机器的聚合状态（含本月累计流量）
type AgentRow struct {
	UUID      string  `json:"uuid"`
	Alias     string  `json:"alias"`
	Hostname  string  `json:"hostname"`
	IP        string  `json:"ip"`
	BootTime  int64   `json:"boot_time"`
	Uptime    int64   `json:"uptime"`
	CPU       float64 `json:"cpu"`
	CPUCount  int     `json:"cpu_count"`
	MemUsed   float64 `json:"mem_used"`
	MemTotal  float64 `json:"mem_total"`
	DiskUsed  float64 `json:"disk_used"`
	DiskTotal float64 `json:"disk_total"`
	RxRate    float64 `json:"rx_rate"`
	TxRate    float64 `json:"tx_rate"`
	Online    bool    `json:"online"`
	LastSeen  int64   `json:"last_seen"`
	CreatedAt   int64   `json:"created_at"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	OS          string  `json:"os"`
	Platform    string  `json:"platform"`
	Remark      string  `json:"remark"`
	Group       string  `json:"group"`
	RxMonth     float64 `json:"rx_month"`
	TxMonth     float64 `json:"tx_month"`
	// ExpireAt VPS 到期时间（Unix 秒）。为 nil 表示未设置。
	ExpireAt    *int64  `json:"expire_at"`
}

// MonthlyTraffic 自然月流量历史
type MonthlyTraffic struct {
	YearMonth string  `json:"year_month"`
	RxTotal   float64 `json:"rx_total"`
	TxTotal   float64 `json:"tx_total"`
	UpdatedAt int64   `json:"updated_at"`
}

// InitDB 初始化 SQLite 并建表（modernc 纯 Go 驱动，无需 CGO）
func InitDB(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写，避免并发写锁
	if _, err = db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, err
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			uuid TEXT PRIMARY KEY,
			alias TEXT DEFAULT '',
			hostname TEXT DEFAULT '',
			ip TEXT DEFAULT '',
			boot_time INTEGER DEFAULT 0,
			uptime INTEGER DEFAULT 0,
		cpu REAL DEFAULT 0,
		cpu_count INTEGER DEFAULT 0,
		mem_used REAL DEFAULT 0,
			mem_total REAL DEFAULT 0,
			disk_used REAL DEFAULT 0,
			disk_total REAL DEFAULT 0,
			rx_rate REAL DEFAULT 0,
			tx_rate REAL DEFAULT 0,
		online INTEGER DEFAULT 1,
		last_seen INTEGER DEFAULT 0,
		created_at INTEGER DEFAULT 0,
		country TEXT DEFAULT '',
		country_code TEXT DEFAULT '',
		os TEXT DEFAULT '',
		platform TEXT DEFAULT '',
		remark TEXT DEFAULT ''
	)`,
		`CREATE TABLE IF NOT EXISTS traffic_monthly (
			uuid TEXT,
			year_month TEXT,
			rx_total REAL DEFAULT 0,
			tx_total REAL DEFAULT 0,
			updated_at INTEGER DEFAULT 0,
			PRIMARY KEY (uuid, year_month)
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			created_at INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS groups (
			name TEXT PRIMARY KEY,
			created_at INTEGER DEFAULT 0
		)`,
	}
	for _, s := range stmts {
		if _, err = db.Exec(s); err != nil {
			return nil, err
		}
	}
	// 兼容旧库：补加后续新增列（已存在则忽略错误）
	db.Exec(`ALTER TABLE agents ADD COLUMN cpu_count INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE agents ADD COLUMN country TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE agents ADD COLUMN country_code TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE agents ADD COLUMN os TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE agents ADD COLUMN platform TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE agents ADD COLUMN remark TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE agents ADD COLUMN expire_at INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE agents ADD COLUMN group_name TEXT DEFAULT ''`)
	return db, nil
}

// UpsertAgent 写入/更新机器的实时状态（落库用，在线状态以 a.Online 为准）
func UpsertAgent(db *sql.DB, a AgentRow) error {
	now := time.Now().Unix()
	online := 0
	if a.Online {
		online = 1
	}
	_, err := db.Exec(`INSERT INTO agents
		(uuid, alias, hostname, ip, boot_time, uptime, cpu, cpu_count, mem_used, mem_total, disk_used, disk_total, rx_rate, tx_rate, online, last_seen, created_at, country, country_code, os, platform)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uuid) DO UPDATE SET
			hostname=excluded.hostname, ip=excluded.ip, boot_time=excluded.boot_time, uptime=excluded.uptime,
			cpu=excluded.cpu, cpu_count=excluded.cpu_count, mem_used=excluded.mem_used, mem_total=excluded.mem_total,
			disk_used=excluded.disk_used, disk_total=excluded.disk_total,
			rx_rate=excluded.rx_rate, tx_rate=excluded.tx_rate, online=excluded.online, last_seen=excluded.last_seen,
			country = CASE WHEN excluded.country='' THEN agents.country ELSE excluded.country END,
			country_code = CASE WHEN excluded.country_code='' THEN agents.country_code ELSE excluded.country_code END,
			os = CASE WHEN excluded.os='' THEN agents.os ELSE excluded.os END,
			platform = CASE WHEN excluded.platform='' THEN agents.platform ELSE excluded.platform END`,
		a.UUID, a.Alias, a.Hostname, a.IP, a.BootTime, a.Uptime, a.CPU, a.CPUCount, a.MemUsed, a.MemTotal,
		a.DiskUsed, a.DiskTotal, a.RxRate, a.TxRate, online, now, a.CreatedAt, a.Country, a.CountryCode, a.OS, a.Platform)
	return err
}

// AddTraffic 将本次上报的流量增量累加到当前自然月
func AddTraffic(db *sql.DB, uuid, yearMonth string, rxDelta, txDelta float64) error {
	now := time.Now().Unix()
	_, err := db.Exec(`INSERT INTO traffic_monthly (uuid, year_month, rx_total, tx_total, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(uuid, year_month) DO UPDATE SET
			rx_total = rx_total + excluded.rx_total,
			tx_total = tx_total + excluded.tx_total,
			updated_at = excluded.updated_at`,
		uuid, yearMonth, rxDelta, txDelta, now)
	return err
}

// SetAlias 设置机器别名
func SetAlias(db *sql.DB, uuid, alias string) error {
	_, err := db.Exec(`UPDATE agents SET alias=? WHERE uuid=?`, alias, uuid)
	return err
}

// ListAgents 返回所有机器，带当前自然月累计流量
// 排序：按添加时间 created_at 升序（旧的在最前），同秒用 uuid 兜底，保证稳定不抖动
func ListAgents(db *sql.DB, yearMonth string) ([]AgentRow, error) {
	rows, err := db.Query(`SELECT a.uuid, a.alias, a.hostname, a.ip, a.boot_time, a.uptime,
		a.cpu, a.cpu_count, a.mem_used, a.mem_total, a.disk_used, a.disk_total, a.rx_rate, a.tx_rate, a.online, a.last_seen, a.created_at, a.country, a.country_code, a.os, a.platform, a.remark, a.group_name, a.expire_at,
		COALESCE(t.rx_total,0), COALESCE(t.tx_total,0)
		FROM agents a
		LEFT JOIN traffic_monthly t ON a.uuid=t.uuid AND t.year_month=?
		ORDER BY a.created_at ASC, a.uuid ASC`, yearMonth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRow = make([]AgentRow, 0)
	for rows.Next() {
		var a AgentRow
		var online int
		var expireAt sql.NullInt64
		if err := rows.Scan(&a.UUID, &a.Alias, &a.Hostname, &a.IP, &a.BootTime, &a.Uptime,
			&a.CPU, &a.CPUCount, &a.MemUsed, &a.MemTotal, &a.DiskUsed, &a.DiskTotal, &a.RxRate, &a.TxRate,
			&online, &a.LastSeen, &a.CreatedAt, &a.Country, &a.CountryCode, &a.OS, &a.Platform, &a.Remark, &a.Group, &expireAt,
			&a.RxMonth, &a.TxMonth); err != nil {
			return nil, err
		}
		a.Online = online == 1
		if expireAt.Valid && expireAt.Int64 > 0 {
			v := expireAt.Int64
			a.ExpireAt = &v
		}
		out = append(out, a)
	}
	return out, nil
}

// UpdateAgent 更新机器的显示名称(alias)、备注(remark)、分组(group_name)与到期时间(expireAt)。
// expireAt 为 nil 表示清空到期时间（设为 NULL）。
// 如果 group 不为空，会自动注册到 groups 表（INSERT OR IGNORE），保持「分组名注册表」与实际使用同步。
func UpdateAgent(db *sql.DB, uuid, alias, remark, group string, expireAt *int64) error {
	var v sql.NullInt64
	if expireAt != nil {
		v = sql.NullInt64{Int64: *expireAt, Valid: true}
	}
	if group != "" {
		_, _ = db.Exec(`INSERT OR IGNORE INTO groups (name, created_at) VALUES (?, ?)`, group, time.Now().Unix())
	}
	_, err := db.Exec(`UPDATE agents SET alias=?, remark=?, group_name=?, expire_at=? WHERE uuid=?`, alias, remark, group, v, uuid)
	return err
}

// DeleteAgent 删除一台机器及其全部流量历史（主动注销 / 管理员移除）。
// 返回受影响的行数，便于调用方判断 UUID 是否存在。
func DeleteAgent(db *sql.DB, uuid string) error {
	if _, err := db.Exec(`DELETE FROM traffic_monthly WHERE uuid=?`, uuid); err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM agents WHERE uuid=?`, uuid); err != nil {
		return err
	}
	return nil
}

// RenameGroup 重命名分组：同步更新 groups 表（注册表）与 agents.group_name，返回受影响行数（agent 数）。
func RenameGroup(db *sql.DB, oldName, newName string) (int64, error) {
	if _, err := db.Exec(`UPDATE groups SET name=? WHERE name=?`, newName, oldName); err != nil {
		return 0, err
	}
	res, err := db.Exec(`UPDATE agents SET group_name=? WHERE group_name=?`, newName, oldName)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteGroup 删除分组：先把 groups 表中的注册项删掉，再把 agents.group_name 置空，返回受影响行数（agent 数）。
func DeleteGroup(db *sql.DB, name string) (int64, error) {
	if _, err := db.Exec(`DELETE FROM groups WHERE name=?`, name); err != nil {
		return 0, err
	}
	res, err := db.Exec(`UPDATE agents SET group_name='' WHERE group_name=?`, name)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// AddGroup 注册一个新分组（用于「+ 新建分组」按钮）。已存在则忽略。
func AddGroup(db *sql.DB, name string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO groups (name, created_at) VALUES (?, ?)`, name, time.Now().Unix())
	return err
}

// ListGroups 返回所有已注册分组名（按名称排序）。
func ListGroups(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// GetTrafficHistory 返回某机器各自然月流量历史
func GetTrafficHistory(db *sql.DB, uuid string) ([]MonthlyTraffic, error) {
	rows, err := db.Query(`SELECT year_month, rx_total, tx_total, updated_at FROM traffic_monthly WHERE uuid=? ORDER BY year_month`, uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonthlyTraffic
	for rows.Next() {
		var m MonthlyTraffic
		if err := rows.Scan(&m.YearMonth, &m.RxTotal, &m.TxTotal, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
