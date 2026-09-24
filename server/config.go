package main

import (
	"log"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 服务端配置
type Config struct {
	Listen           string      `yaml:"listen"`
	Port             int         `yaml:"port"`
	OfflineThreshold int         `yaml:"offline_threshold"` // 判定离线的秒数；建议 ≥3×最大上报间隔，避免高 interval 部署抖动
	TLS              TLSConfig   `yaml:"tls"`
	AgentToken       string      `yaml:"agent_token"`
	SSHPassword      string      `yaml:"ssh_password"` // Web SSH 连接密码；为空时回退到管理员密码
	Admin            AdminConfig `yaml:"admin"`
	DBPath           string      `yaml:"db_path"`
	// trusted_proxy：逗号分隔的可信反代 IP/CIDR 列表。仅当「直连来源 IP」落在该列表内时，
	// 才信任 X-Forwarded-For 取最左侧真实客户端；否则一律用 RemoteAddr。
	// 为空（默认）则永远不信任 XFF——防止服务直连暴露时攻击者伪造 XFF 无限换 IP 绕过登录限流（#顺手）。
	TrustedProxy string        `yaml:"trusted_proxy"`
	trustedNets  []*net.IPNet `yaml:"-"` // 由 TrustedProxy 解析得到，不参与序列化
}

type TLSConfig struct {
	Enabled bool   `yaml:"enabled"`
	Cert    string `yaml:"cert"`
	Key     string `yaml:"key"`
}

type AdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// LoadConfig 读取并补全默认配置
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// 配置文件不存在时使用默认值，保证可启动
		log.Printf("[config] 未找到配置文件 %s，将使用内置默认值（含默认口令，存在安全风险，请尽快创建配置文件）", path)
		return defaultConfig(), nil
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	applyDefaults(&c)
	return &c, nil
}

func defaultConfig() *Config {
	c := Config{}
	applyDefaults(&c)
	return &c
}

// parseTrustedProxies 把逗号分隔的 IP/CIDR 字符串解析为网段列表（启动时调用一次）。
// 空串或非法项会被忽略；单 IP 自动按 /32 处理。
func parseTrustedProxies(s string) []*net.IPNet {
	var nets []*net.IPNet
	if s == "" {
		return nets
	}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			p += "/32"
		}
		if _, ipnet, err := net.ParseCIDR(p); err == nil {
			nets = append(nets, ipnet)
		} else {
			log.Printf("[config] trusted_proxy 含非法项，已忽略: %q", p)
		}
	}
	return nets
}

func applyDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = "0.0.0.0"
	}
	if c.Port == 0 {
		c.Port = 8080
	}
	if c.OfflineThreshold == 0 {
		c.OfflineThreshold = 15
	}
	if c.DBPath == "" {
		c.DBPath = "data/probe.db"
	}
	if c.AgentToken == "" {
		c.AgentToken = "change-me-agent-token"
	}
	if c.Admin.Username == "" {
		c.Admin.Username = "admin"
	}
	if c.Admin.Password == "" {
		c.Admin.Password = "admin"
	}
	// 解析可信反代列表（供 clientIP 判断是否信任 X-Forwarded-For）
	c.trustedNets = parseTrustedProxies(c.TrustedProxy)
}
