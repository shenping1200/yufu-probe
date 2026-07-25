package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

const (
	defaultConfig  = "/opt/yufu-probe/configs/server.yaml"
	defaultUUIDs   = "/tmp/yufu_fake_uuids.txt"
	defaultPID     = "/tmp/fakegen.pid"
	sessionCookie  = "probe_session"
	defaultGroup   = "干活的"
	reportInterval = 8 * time.Second // 必须小于服务端 15s 离线阈值
)

// serverConfig 只读取我们用到的字段，其余忽略
type serverConfig struct {
	Listen     string `yaml:"listen"`
	Port       int    `yaml:"port"`
	AgentToken string `yaml:"agent_token"`
	Admin      struct {
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"admin"`
}

func loadConfig(path string) (*serverConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c serverConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Port == 0 {
		c.Port = 8080
	}
	if c.Listen == "" {
		c.Listen = "0.0.0.0"
	}
	return &c, nil
}

// ---------- 随机工具 ----------

func newRng() *mrand.Rand {
	n, _ := crand.Int(crand.Reader, big.NewInt(1<<62))
	return mrand.New(mrand.NewSource(n.Int64()))
}

func randInt(r *mrand.Rand, min, max int) int { return min + r.Intn(max-min+1) }
func randFloat(r *mrand.Rand, min, max float64) float64 {
	return min + r.Float64()*(max-min)
}
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func uuid() string {
	b := make([]byte, 16)
	crand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------- 随机数据池 ----------

// ipPool 为稳定的公网 IP（多为各国公共 DNS/Anycast），用于让服务端通过 ipwho.is 反查出真实国家与国旗。
// 服务端对相同 IP 有缓存，因此 2000 台机器只会触发约 len(ipPool) 次地理查询，不会被限流。
var ipPool = []string{
	"8.8.8.8", "8.8.4.4", // 美国 Google
	"1.1.1.1", "1.0.0.1", // Cloudflare Anycast
	"9.9.9.9", "149.112.112.112", // Quad9
	"208.67.222.222", "208.67.220.220", // OpenDNS
	"64.6.64.6", "156.154.70.1", // Neustar/UltraDNS
	"223.5.5.5", "223.6.6.6", "119.29.29.29", "180.76.76.76", // 中国 阿里/腾讯/百度
	"77.88.8.8", "77.88.8.1", // 俄罗斯 Yandex
	"195.175.39.39", "212.156.42.40", // 土耳其
	"168.126.63.1", // 韩国 KT
	"168.95.1.1", // 中国台湾 HiNet
	"139.130.4.4", "203.50.2.71", // 澳大利亚 Telstra
	"194.25.2.129", "212.18.3.18", // 德国
	"212.27.40.240", // 法国 Free
	"194.168.4.100", "217.169.20.20", // 英国
	"194.109.6.66", // 荷兰 XS4ALL
	"194.71.11.11", // 瑞典
	"194.7.7.7", "212.42.212.42", // 瑞士
	"200.221.11.101", // 巴西 UOL
	"201.116.204.70", // 墨西哥
	"181.30.0.0", // 阿根廷（占位公共段）
	"203.113.10.10", // 泰国
	"202.155.0.10", // 印度尼西亚
	"203.119.0.0", // 印度（占位公共段）
	"196.7.0.0", // 南非（占位公共段）
	"41.33.0.0", // 埃及（占位公共段）
	"212.93.192.70", // 沙特
	"194.170.1.1", // 阿联酋
}

var osList = []struct {
	os, platform string
}{
	{"Ubuntu 22.04", "linux"},
	{"Ubuntu 20.04", "linux"},
	{"CentOS 7.9", "linux"},
	{"Debian 11", "linux"},
	{"Debian 12", "linux"},
	{"Alpine 3.18", "linux"},
	{"Rocky Linux 9", "linux"},
	{"Oracle Linux 8", "linux"},
	{"Fedora 38", "linux"},
	{"Windows Server 2019", "windows"},
	{"Windows Server 2022", "windows"},
}

// ---------- AgentReport 与服务端结构保持一致 ----------

type agentReport struct {
	UUID      string  `json:"uuid"`
	Hostname  string  `json:"hostname"`
	IP        string  `json:"ip"`
	PublicIP  string  `json:"public_ip"`
	PublicIP4 string  `json:"public_ip4"`
	OS        string  `json:"os"`
	Platform  string  `json:"platform"`
	BootTime  int64   `json:"boot_time"`
	Uptime    int64   `json:"uptime"`
	CPU       float64 `json:"cpu"`
	CPUCount  int     `json:"cpu_count"`
	MemUsed   float64 `json:"mem_used"` // 单位 GB（前端 *1e9 显示）
	MemTotal  float64 `json:"mem_total"`
	DiskUsed  float64 `json:"disk_used"`
	DiskTotal float64 `json:"disk_total"`
	RxRate    float64 `json:"rx_rate"` // 单位 bytes/s
	TxRate    float64 `json:"tx_rate"`
	RxDelta   float64 `json:"rx_delta"`
	TxDelta   float64 `json:"tx_delta"`
}

type agentState struct {
	r         *mrand.Rand
	hostname  string
	ip        string
	osInfo    struct{ os, platform string }
	cpu       float64
	cpuCount  int
	memTotal  float64
	memUsed   float64
	diskTotal float64
	diskUsed  float64
	rxRate    float64
	txRate    float64
	uptime    int64
}

func newAgentState(r *mrand.Rand, ip string) *agentState {
	s := &agentState{r: r, ip: ip}
	s.hostname = fmt.Sprintf("host-%s", uuid()[:8])
	s.osInfo = osList[randInt(r, 0, len(osList)-1)]
	s.cpu = randFloat(r, 2, 35)
	s.cpuCount = randInt(r, 1, 32)
	s.memTotal = randFloat(r, 1, 128)
	s.memUsed = s.memTotal * randFloat(r, 0.2, 0.9)
	s.diskTotal = randFloat(r, 20, 2000)
	s.diskUsed = s.diskTotal * randFloat(r, 0.1, 0.85)
	s.rxRate = randFloat(r, 0, 50*1024*1024)
	s.txRate = randFloat(r, 0, 50*1024*1024)
	s.uptime = int64(randInt(r, 1, 400)) * 86400
	return s
}

// tick 随机游走，使指标看着在实时变化，并返回本次上报
func (s *agentState) tick(intervalSec float64) agentReport {
	s.cpu = clamp(s.cpu+randFloat(s.r, -8, 8), 1, 99)
	s.memUsed = clamp(s.memUsed+randFloat(s.r, -2, 2), s.memTotal*0.1, s.memTotal*0.97)
	s.diskUsed = clamp(s.diskUsed+randFloat(s.r, -3, 3), s.diskTotal*0.05, s.diskTotal*0.95)
	s.rxRate = clamp(s.rxRate+randFloat(s.r, -5*1024*1024, 5*1024*1024), 0, 80*1024*1024)
	s.txRate = clamp(s.txRate+randFloat(s.r, -5*1024*1024, 5*1024*1024), 0, 80*1024*1024)
	now := time.Now().Unix()
	return agentReport{
		UUID:      "",
		Hostname:  s.hostname,
		IP:        s.ip,
		PublicIP:  s.ip,
		PublicIP4: s.ip,
		OS:        s.osInfo.os,
		Platform:  s.osInfo.platform,
		BootTime:  now - s.uptime,
		Uptime:    s.uptime,
		CPU:       s.cpu,
		CPUCount:  s.cpuCount,
		MemUsed:   s.memUsed,
		MemTotal:  s.memTotal,
		DiskUsed:  s.diskUsed,
		DiskTotal: s.diskTotal,
		RxRate:    s.rxRate,
		TxRate:    s.txRate,
		RxDelta:   s.rxRate * intervalSec,
		TxDelta:   s.txRate * intervalSec,
	}
}

// ---------- HTTP 客户端（登录 / 分组 / 删除）----------

type httpClient struct {
	base   string
	http   *http.Client
	cookie string
}

func newHTTPClient(port int) *httpClient {
	return &httpClient{
		base: "http://127.0.0.1:" + strconv.Itoa(port),
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *httpClient) login(user, pass string) error {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, _ := http.NewRequest("POST", c.base+"/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("登录失败，HTTP %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			c.cookie = ck.Value
		}
	}
	if c.cookie == "" {
		return fmt.Errorf("登录成功但未返回会话 cookie（检查管理员账号）")
	}
	return nil
}

func (c *httpClient) patchGroup(uuid, group string) error {
	body, _ := json.Marshal(map[string]string{"group": group})
	req, _ := http.NewRequest("PATCH", c.base+"/api/agents/"+uuid, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("PATCH %s 返回 %d", uuid, resp.StatusCode)
	}
	return nil
}

func (c *httpClient) deleteAgent(uuid, token string) error {
	u := c.base + "/api/agents/" + uuid + "?token=" + url.QueryEscape(token)
	req, _ := http.NewRequest("DELETE", u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("DELETE %s 返回 %d", uuid, resp.StatusCode)
	}
	return nil
}

func (c *httpClient) deleteGroup(group string) error {
	u := c.base + "/api/groups/" + url.PathEscape(group)
	req, _ := http.NewRequest("DELETE", u, nil)
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("删除分组返回 %d", resp.StatusCode)
	}
	return nil
}

// ---------- gen 子命令 ----------

func cmdGen(cfg *serverConfig, count int, group, uuidsFile, pidFile string) error {
	// 写 PID，供 clean 停止生成器
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return err
	}
	defer os.Remove(pidFile)

	uuids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		uuids = append(uuids, uuid())
	}
	if err := os.WriteFile(uuidsFile, []byte(joinLines(uuids)), 0644); err != nil {
		return err
	}
	log.Printf("[fakegen] 已生成 %d 个 UUID，写入 %s", count, uuidsFile)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	base := "ws://127.0.0.1:" + strconv.Itoa(cfg.Port) + "/ws/agent?token=" + url.QueryEscape(cfg.AgentToken)
	var wg sync.WaitGroup
	for _, id := range uuids {
		wg.Add(1)
		go runAgent(ctx, &wg, base, id, ipPool[mrand.Intn(len(ipPool))])
	}
	log.Printf("[fakegen] 已启动 %d 个模拟连接，每 %s 上报一次。按 Ctrl+C 停止上报（机器仍保留，运行 clean 可移除）", count, reportInterval)

	// 延迟后批量打分组标签（落库需约 2s，等一等再 PATCH）
	go assignGroups(ctx, cfg, uuids, group)

	<-ctx.Done()
	log.Printf("[fakegen] 收到停止信号，正在关闭 %d 个连接...", count)
	wg.Wait()
	log.Printf("[fakegen] 已停止上报。%d 台机器仍在面板中（约 15s 后变离线），运行 clean 可一键移除。", count)
	return nil
}

func assignGroups(ctx context.Context, cfg *serverConfig, uuids []string, group string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(6 * time.Second):
	}
	hc := newHTTPClient(cfg.Port)
	if err := hc.login(cfg.Admin.Username, cfg.Admin.Password); err != nil {
		log.Printf("[fakegen] 分组打标登录失败：%v（机器仍会生成，只是不在「%s」分组）", err, group)
		return
	}
	for attempt := 1; attempt <= 5; attempt++ {
		if ctx.Err() != nil {
			return
		}
		var failed int
		for _, id := range uuids {
			if err := hc.patchGroup(id, group); err != nil {
				failed++
			}
		}
		if failed == 0 {
			log.Printf("[fakegen] 已将 %d 台机器归入「%s」分组", len(uuids), group)
			return
		}
		log.Printf("[fakegen] 分组打标第 %d 次：成功 %d，失败 %d，重试中...", attempt, len(uuids)-failed, failed)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
	log.Printf("[fakegen] 分组打标部分失败，可稍后重新运行 gen 或手动在面板分配「%s」", group)
}

func runAgent(ctx context.Context, wg *sync.WaitGroup, base, id, ip string) {
	defer wg.Done()
	r := newRng()
	st := newAgentState(r, ip)
	dialer := &websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()

	intervalSec := reportInterval.Seconds()
	send := func(conn *websocket.Conn) error {
		rep := st.tick(intervalSec)
		rep.UUID = id
		data, err := json.Marshal(rep)
		if err != nil {
			return err
		}
		return conn.WriteMessage(websocket.TextMessage, data)
	}

outer:
	for {
		if ctx.Err() != nil {
			return
		}
		conn, _, err := dialer.Dial(base, nil)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		// 读协程：仅消费服务端消息（Web SSH 指令等），出错即退出
		go func() {
			for {
				if _, _, e := conn.ReadMessage(); e != nil {
					return
				}
			}
		}()
		if err := send(conn); err != nil {
			conn.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		for {
			select {
			case <-ctx.Done():
				conn.Close()
				return
			case <-ticker.C:
				if err := send(conn); err != nil {
					conn.Close()
					continue outer
				}
			}
		}
	}
}

// ---------- clean 子命令 ----------

func cmdClean(cfg *serverConfig, group, uuidsFile, pidFile string) error {
	// 1) 先停生成器（避免它继续上报把刚删的机器写回）
	if data, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(syscall.SIGTERM)
				log.Printf("[fakegen] 已向生成器进程 %d 发送停止信号", pid)
			}
		}
		os.Remove(pidFile)
	}
	// 给生成器一点时间退出，避免竞态
	time.Sleep(1 * time.Second)

	uuids := readLines(uuidsFile)
	if len(uuids) == 0 {
		log.Printf("[fakegen] %s 无 UUID，无需清理", uuidsFile)
		return nil
	}
	log.Printf("[fakegen] 开始移除 %d 台虚拟机器...", len(uuids))

	hc := newHTTPClient(cfg.Port)
	// 2) 并发删除每台机器（仅删列表里的 UUID，不影响真实机器）
	const workers = 20
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed int
	for _, id := range uuids {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := hc.deleteAgent(u, cfg.AgentToken); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				log.Printf("[fakegen] 删除 %s 失败：%v", u, err)
			}
		}(id)
	}
	wg.Wait()

	// 3) 删除空分组
	if err := hc.login(cfg.Admin.Username, cfg.Admin.Password); err != nil {
		log.Printf("[fakegen] 登录失败，跳过删除分组：%v", err)
	} else if err := hc.deleteGroup(group); err != nil {
		log.Printf("[fakegen] 删除分组「%s」失败：%v", group, err)
	} else {
		log.Printf("[fakegen] 已删除分组「%s」", group)
	}

	os.Remove(uuidsFile)
	log.Printf("[fakegen] 清理完成：成功移除 %d 台，失败 %d 台。真实机器不受影响。", len(uuids)-failed, failed)
	return nil
}

// ---------- 工具 ----------

func joinLines(s []string) string {
	var b bytes.Buffer
	for _, v := range s {
		b.WriteString(v)
		b.WriteString("\n")
	}
	return b.String()
}

func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		t := strings.TrimSpace(string(line))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ---------- main ----------

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: fakegen <gen|clean> [flags]")
		fmt.Println("  gen    生成虚拟机器（默认 2000 台）并归入「干活的」分组")
		fmt.Println("  clean  一键移除全部虚拟机器与「干活的」分组（不影响真实机器）")
		os.Exit(1)
	}

	configPath := flag.String("config", defaultConfig, "server.yaml 路径")
	count := flag.Int("count", 2000, "生成数量")
	group := flag.String("group", defaultGroup, "分组名")
	uuidsFile := flag.String("uuids", defaultUUIDs, "UUID 列表文件")
	pidFile := flag.String("pid", defaultPID, "PID 文件")

	sub := os.Args[1]
	flag.CommandLine.Parse(os.Args[2:])

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("[fakegen] 读取配置失败 %s: %v", *configPath, err)
	}
	if cfg.AgentToken == "" {
		log.Fatalf("[fakegen] 配置中未找到 agent_token")
	}

	switch sub {
	case "gen":
		if err := cmdGen(cfg, *count, *group, *uuidsFile, *pidFile); err != nil {
			log.Fatalf("[fakegen] gen 失败: %v", err)
		}
	case "clean":
		if err := cmdClean(cfg, *group, *uuidsFile, *pidFile); err != nil {
			log.Fatalf("[fakegen] clean 失败: %v", err)
		}
	default:
		fmt.Printf("未知子命令: %s\n", sub)
		os.Exit(1)
	}
}
