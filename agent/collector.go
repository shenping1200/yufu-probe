package main

import (
	"encoding/json"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
)

// Snapshot 一次采集的机器状态
type Snapshot struct {
	Hostname  string  `json:"hostname"`
	IP        string  `json:"ip"`
	PublicIP  string  `json:"public_ip"`
	OS        string  `json:"os"`
	Platform  string  `json:"platform"`
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
	RxDelta   float64 `json:"rx_delta"`
	TxDelta   float64 `json:"tx_delta"`
}

type collector struct {
	iface        string
	lastCPUTotal float64
	lastCPUIdle  float64
	lastRx       uint64
	lastTx       uint64
	hasPrev      bool
	pubIP        string
	pubIPAt      int64
}

func newCollector(iface string) *collector {
	return &collector{iface: iface}
}

func diskRoot() string {
	if runtime.GOOS == "windows" {
		return "C:"
	}
	return "/"
}

// getOutboundIP 通过 UDP 探测获得本机出口（内网）IP
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// publicIP 获取本机公网 IP（外部服务探测，5 分钟缓存，失败返回空串）
func (c *collector) publicIP() string {
	now := time.Now().Unix()
	if c.pubIP != "" && now-c.pubIPAt < 300 {
		return c.pubIP
	}
	if ip := fetchPublicIP(); ip != "" {
		c.pubIP = ip
		c.pubIPAt = now
	}
	return c.pubIP
}

// fetchPublicIP 通过公网 IP 查询服务获取出口公网地址（优先 ipwho.is，失败回退 ipify）
func fetchPublicIP() string {
	client := &http.Client{Timeout: 4 * time.Second}
	urls := []string{"https://ipwho.is/", "https://api.ipify.org?format=json"}
	for _, u := range urls {
		resp, err := client.Get(u)
		if err != nil {
			continue
		}
		var raw struct {
			IP      string `json:"ip"`
			Success bool   `json:"success"`
		}
		err = json.NewDecoder(resp.Body).Decode(&raw)
		resp.Body.Close()
		if err != nil {
			continue
		}
		if raw.IP != "" {
			return raw.IP
		}
	}
	return ""
}

func firstUpper(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

func defaultIface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		return i.Name
	}
	return ""
}

// cpuUsage 通过两次 cpu.Times 差值计算总体使用率（%）
func (c *collector) cpuUsage() float64 {
	times, err := cpu.Times(false)
	if err != nil || len(times) == 0 {
		return 0
	}
	t := times[0]
	total := t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	idle := t.Idle + t.Iowait
	if c.lastCPUTotal == 0 {
		c.lastCPUTotal = total
		c.lastCPUIdle = idle
		return 0
	}
	dTotal := total - c.lastCPUTotal
	dIdle := idle - c.lastCPUIdle
	c.lastCPUTotal = total
	c.lastCPUIdle = idle
	if dTotal <= 0 {
		return 0
	}
	return (dTotal - dIdle) / dTotal * 100
}

// netBytes 读取监控网卡的累计收发字节数
func (c *collector) netBytes() (uint64, uint64) {
	stats, err := gnet.IOCounters(true)
	if err != nil || len(stats) == 0 {
		return 0, 0
	}
	iface := c.iface
	if iface == "" {
		iface = defaultIface()
	}
	for _, s := range stats {
		if s.Name == iface {
			return s.BytesRecv, s.BytesSent
		}
	}
	// 回退：累加所有非回环网卡
	var rx, tx uint64
	for _, s := range stats {
		if s.Name == "lo" {
			continue
		}
		rx += s.BytesRecv
		tx += s.BytesSent
	}
	return rx, tx
}

// collect 执行一次采集
func (c *collector) collect(intervalSec float64) (*Snapshot, error) {
	info, _ := host.Info()
	bt, _ := host.BootTime()
	up, _ := host.Uptime()
	vm, _ := mem.VirtualMemory()
	du, _ := disk.Usage(diskRoot())
	cpuCount, _ := cpu.Counts(true)

	rx, tx := c.netBytes()
	var rxRate, txRate, rxDelta, txDelta float64
	if c.hasPrev {
		rxDelta = float64(rx - c.lastRx)
		txDelta = float64(tx - c.lastTx)
		rxRate = rxDelta / intervalSec
		txRate = txDelta / intervalSec
	}
	c.lastRx, c.lastTx = rx, tx
	c.hasPrev = true

	return &Snapshot{
		Hostname:  info.Hostname,
		IP:        getOutboundIP(),
		PublicIP:  c.publicIP(),
		OS:        firstUpper(info.OS),
		Platform:  firstUpper(info.Platform),
		BootTime:  int64(bt),
		Uptime:    int64(up),
		CPU:       c.cpuUsage(),
		CPUCount:  cpuCount,
		MemUsed:   float64(vm.Used) / 1e9,
		MemTotal:  float64(vm.Total) / 1e9,
		DiskUsed:  float64(du.Used) / 1e9,
		DiskTotal: float64(du.Total) / 1e9,
		RxRate:    rxRate,
		TxRate:    txRate,
		RxDelta:   rxDelta,
		TxDelta:   txDelta,
	}, nil
}
