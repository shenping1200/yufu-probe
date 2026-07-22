package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
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
	PublicIP4 string  `json:"public_ip4"`
	PublicIP6 string  `json:"public_ip6"`
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
	pubIP4       string
	pubIP6       string
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

// firstNonEmpty 返回第一个非空字符串
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// publicIPv4 / publicIPv6 获取本机公网 IPv4 / IPv6。
// 通过强制走对应 IP 栈探测外部服务（等价 curl -4 / curl -6）分别取得，
// 无该栈的机器对应值为空串。v4/v6 共用一个 5 分钟缓存，保证两端取数一致。
func (c *collector) publicIPv4() string {
	c.ensurePublicIP()
	return c.pubIP4
}

func (c *collector) publicIPv6() string {
	c.ensurePublicIP()
	return c.pubIP6
}

// ensurePublicIP 刷新并缓存公网 v4/v6（5 分钟 TTL）
func (c *collector) ensurePublicIP() {
	now := time.Now().Unix()
	if c.pubIPAt != 0 && now-c.pubIPAt < 300 {
		return
	}
	c.pubIP4 = fetchPublicIPOver("tcp4")
	c.pubIP6 = fetchPublicIPOver("tcp6")
	c.pubIPAt = now
}

// fetchPublicIPOver 通过公网 IP 查询服务获取出口公网地址，
// network 指定强制使用的源 IP 栈（tcp4 / tcp6），从而分别拿到 v4 / v6 公网 IP。
// 优先 ipwho.is，失败回退 ipify。失败返回空串。
func fetchPublicIPOver(network string) string {
	dialer := &net.Dialer{Timeout: 4 * time.Second}
	client := &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
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

// isVirtualIface 判断是否为虚拟/容器网卡（docker0、br-xxx、veth、virbr 等）
func isVirtualIface(name string) bool {
	for _, p := range []string{"lo", "docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "tun", "tap"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// defaultRouteIface 读取 /proc/net/route 找到默认路由所在的网卡（Linux）
func defaultRouteIface() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // 跳过表头
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		if len(fields) < 8 {
			continue
		}
		// 默认路由：目标 0.0.0.0 且掩码 0.0.0.0
		if fields[1] == "00000000" && fields[7] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// netBytes 读取监控网卡的累计收发字节数
func (c *collector) netBytes() (uint64, uint64) {
	stats, err := gnet.IOCounters(true)
	if err != nil || len(stats) == 0 {
		return 0, 0
	}
	// 1) 优先使用默认路由网卡（真实对外网卡，如 eth0/ens3）
	iface := c.iface
	if iface == "" {
		iface = defaultRouteIface()
	}
	if iface == "" {
		iface = defaultIface()
	}
	if iface != "" {
		for _, s := range stats {
			if s.Name == iface {
				return s.BytesRecv, s.BytesSent
			}
		}
	}
	// 2) 回退：累加所有非回环、非虚拟网卡
	var rx, tx uint64
	for _, s := range stats {
		if isVirtualIface(s.Name) {
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
		PublicIP:  firstNonEmpty(c.publicIPv4(), c.publicIPv6()),
		PublicIP4: c.publicIPv4(),
		PublicIP6: c.publicIPv6(),
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
