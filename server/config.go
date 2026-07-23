package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config 服务端配置
type Config struct {
	Listen      string      `yaml:"listen"`
	Port        int         `yaml:"port"`
	TLS         TLSConfig   `yaml:"tls"`
	AgentToken  string      `yaml:"agent_token"`
	SSHPassword string      `yaml:"ssh_password"` // Web SSH 连接密码；为空时回退到管理员密码
	Admin       AdminConfig `yaml:"admin"`
	DBPath      string      `yaml:"db_path"`
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

func applyDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = "0.0.0.0"
	}
	if c.Port == 0 {
		c.Port = 8080
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
}
