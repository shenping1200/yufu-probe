package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// AgentConfig 客户端配置
type AgentConfig struct {
	Server   string `yaml:"server"`
	Token    string `yaml:"token"`
	Interval int    `yaml:"interval"`
	UUIDFile string `yaml:"uuid_file"`
	Iface    string `yaml:"iface"`
}

func main() {
	configPath := flag.String("config", "configs/agent.yaml", "config file")
	serverFlag := flag.String("server", "", "server ws/wss url, e.g. ws://1.2.3.4:8080")
	tokenFlag := flag.String("token", "", "agent token")
	intervalFlag := flag.Int("interval", 0, "report interval seconds")
	ifaceFlag := flag.String("iface", "", "network interface to monitor")
	flag.Parse()

	cfg := loadConfig(*configPath)
	if *serverFlag != "" {
		cfg.Server = *serverFlag
	}
	if *tokenFlag != "" {
		cfg.Token = *tokenFlag
	}
	if *intervalFlag > 0 {
		cfg.Interval = *intervalFlag
	}
	if *ifaceFlag != "" {
		cfg.Iface = *ifaceFlag
	}

	// 环境变量兜底（便于 Docker 部署：-e SERVER= -e TOKEN= -e INTERVAL= -e IFACE= -e UUID_FILE=）
	if cfg.Server == "" {
		cfg.Server = os.Getenv("SERVER")
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv("TOKEN")
	}
	if cfg.Interval == 0 {
		if v := os.Getenv("INTERVAL"); v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				cfg.Interval = n
			}
		}
	}
	if cfg.Iface == "" {
		cfg.Iface = os.Getenv("IFACE")
	}
	if cfg.UUIDFile == "" {
		cfg.UUIDFile = os.Getenv("UUID_FILE")
	}

	if cfg.Server == "" {
		log.Fatal("server address required: set 'server' in config or pass -server")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5
	}
	if cfg.UUIDFile == "" {
		cfg.UUIDFile = defaultUUIDFile()
	}

	id := loadUUID(cfg.UUIDFile)

	col := newCollector(cfg.Iface)
	rep := NewReporter(cfg.Server, cfg.Token, id)
	send := make(chan *Snapshot, 1)
	go rep.Run(send)

	// 优雅停止：收到 SIGTERM/SIGINT 时退出进程。
	// 注意：这里**不**主动向服务端发 unregister（注销并删除记录）。
	// 否则正常的重启 / 升级（install-agent.sh 会 restart 本服务）会触发注销，
	// 服务端 DeleteAgent 把这台机器的备注 / 别名等数据一并删掉，重连后只得到空记录。
	// 卸载由 uninstall-agent.sh 通过 DELETE /api/agents/{uuid} 显式完成；
	// 这里只做进程退出，服务端会在超时后把机器标记为离线并保留记录。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[agent] 收到退出信号，正在退出（不注销服务端记录）...")
		os.Exit(0)
	}()

	log.Printf("[agent] uuid=%s server=%s interval=%ds", id, cfg.Server, cfg.Interval)
	locale, _ := time.LoadLocation("Local")
	_ = locale
	// 连接后立即上报一次，让服务端尽快把本机登记到 hub（Web SSH 立即可用，
	// 不必等到第一个上报周期，默认 interval 秒后才登记）。
	if snap0, err := col.collect(float64(cfg.Interval)); err == nil {
		select {
		case send <- snap0:
		default:
		}
	}
	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	for range ticker.C {
		snap, err := col.collect(float64(cfg.Interval))
		if err != nil {
			log.Printf("[agent] collect error: %v", err)
			continue
		}
		select {
		case send <- snap:
		default:
		}
	}
}

func loadConfig(path string) *AgentConfig {
	cfg := &AgentConfig{Interval: 5}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	yaml.Unmarshal(data, cfg)
	if cfg.Interval <= 0 {
		cfg.Interval = 5
	}
	return cfg
}

func defaultUUIDFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	p := filepath.Join(dir, "probe-agent")
	os.MkdirAll(p, 0700)
	return filepath.Join(p, "uuid")
}

func loadUUID(path string) string {
	if data, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(data))
		if s != "" {
			return s
		}
	}
	u := uuid.NewString()
	os.WriteFile(path, []byte(u), 0600)
	return u
}
