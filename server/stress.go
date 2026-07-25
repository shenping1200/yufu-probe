package main

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	mrand "math/rand"
	"sync"
	"time"
)

// ---------- 内置数据（确定性强，不依赖外部地理 API）----------

// countryInfo 国家代表信息；ip 仅用于展示，国家名/代码由本结构直接提供
type countryInfo struct {
	code string
	name string
	ip   string
}

var countryList = []countryInfo{
	{"US", "United States", "8.8.8.8"},
	{"CN", "China", "223.5.5.5"},
	{"HK", "Hong Kong", "203.80.0.1"},
	{"TW", "Taiwan", "168.95.1.1"},
	{"JP", "Japan", "202.232.12.0"},
	{"KR", "South Korea", "168.126.63.1"},
	{"SG", "Singapore", "203.119.0.10"},
	{"IN", "India", "49.45.0.1"},
	{"AU", "Australia", "1.1.1.1"},
	{"GB", "United Kingdom", "194.168.4.100"},
	{"DE", "Germany", "194.25.2.129"},
	{"FR", "France", "212.27.40.240"},
	{"NL", "Netherlands", "194.109.6.66"},
	{"RU", "Russia", "77.88.8.8"},
	{"BR", "Brazil", "200.221.11.101"},
	{"CA", "Canada", "206.47.244.101"},
	{"IT", "Italy", "151.99.125.99"},
	{"ES", "Spain", "80.58.61.250"},
	{"SE", "Sweden", "194.71.11.11"},
	{"CH", "Switzerland", "194.7.7.7"},
	{"PL", "Poland", "194.204.152.34"},
	{"TR", "Turkey", "195.175.39.39"},
	{"AE", "United Arab Emirates", "194.170.1.1"},
	{"SA", "Saudi Arabia", "212.93.192.70"},
	{"ZA", "South Africa", "196.7.0.1"},
	{"MX", "Mexico", "201.116.204.70"},
	{"AR", "Argentina", "181.30.0.1"},
	{"CL", "Chile", "200.27.0.1"},
	{"CO", "Colombia", "190.0.0.1"},
	{"EG", "Egypt", "41.33.0.1"},
	{"NG", "Nigeria", "197.0.0.1"},
	{"ID", "Indonesia", "202.155.0.10"},
	{"TH", "Thailand", "203.113.10.10"},
	{"VN", "Vietnam", "203.113.131.1"},
	{"MY", "Malaysia", "202.188.0.1"},
	{"PH", "Philippines", "202.57.0.1"},
	{"PK", "Pakistan", "203.99.0.1"},
	{"BD", "Bangladesh", "202.4.0.1"},
	{"IL", "Israel", "199.203.0.1"},
	{"GR", "Greece", "194.219.0.1"},
	{"PT", "Portugal", "194.65.0.1"},
	{"AT", "Austria", "194.24.0.1"},
	{"DK", "Denmark", "194.255.0.1"},
	{"NO", "Norway", "193.0.0.1"},
	{"FI", "Finland", "194.0.0.1"},
	{"IE", "Ireland", "194.0.0.2"},
	{"NZ", "New Zealand", "202.0.0.1"},
	{"UA", "Ukraine", "193.0.0.2"},
	{"CZ", "Czechia", "194.0.0.3"},
	{"RO", "Romania", "193.0.0.3"},
}

// osMap 操作系统键值 -> 可选版本与平台
var osMap = map[string][]struct {
	os       string
	platform string
}{
	"ubuntu":  {{"Ubuntu 20.04", "linux"}, {"Ubuntu 22.04", "linux"}, {"Ubuntu 24.04", "linux"}},
	"centos":  {{"CentOS 7.9", "linux"}, {"CentOS Stream 9", "linux"}},
	"debian":  {{"Debian 11", "linux"}, {"Debian 12", "linux"}},
	"alpine":  {{"Alpine 3.18", "linux"}, {"Alpine 3.19", "linux"}},
	"rocky":   {{"Rocky Linux 9", "linux"}},
	"oracle":  {{"Oracle Linux 8", "linux"}, {"Oracle Linux 9", "linux"}},
	"fedora":  {{"Fedora 38", "linux"}, {"Fedora 39", "linux"}},
	"win2019": {{"Windows Server 2019", "windows"}},
	"win2022": {{"Windows Server 2022", "windows"}},
}

// ---------- 参数与引擎 ----------

// StressParams 前端传入的压测参数；空数组/零值表示「随机」
type StressParams struct {
	Count        int      `json:"count"`
	Countries    []string `json:"countries"`
	Oses         []string `json:"oses"`
	DurationSec  int      `json:"duration_sec"` // 0 = 手动停止
	UptimeMin    int      `json:"uptime_min"`  // 天；0 = 随机
	UptimeMax    int      `json:"uptime_max"`
	TrafficLevel string   `json:"traffic_level"` // low/mid/high/random
	OnlineRatio  float64  `json:"online_ratio"`  // 0..1，默认 1
	CpuMin       float64  `json:"cpu_min"`       // %；0 = 随机
	CpuMax       float64  `json:"cpu_max"`
	MemMin       float64  `json:"mem_min"` // 使用率 %；0 = 随机
	MemMax       float64  `json:"mem_max"`
	CpuCoresMin  int      `json:"cpu_cores_min"`  // 核心数；0 = 随机
	CpuCoresMax  int      `json:"cpu_cores_max"`
	MemTotalMin  float64  `json:"mem_total_min"` // GB；0 = 随机
	MemTotalMax  float64  `json:"mem_total_max"`
	DiskTotalMin float64  `json:"disk_total_min"` // GB；0 = 随机
	DiskTotalMax float64  `json:"disk_total_max"`
	Group        string   `json:"group"`
}

type stressAgent struct {
	r         *mrand.Rand
	uuid      string
	hostname  string
	ip        string
	country   string
	countryCode string
	osName    string
	platform  string
	cpuCount  int
	memTotal  int     // GB（整数，仅 1 或偶数）
	diskTotal int     // GB（整数，仅 10 的倍数）
	uptime    int64
	cpu       float64
	memPct    float64
	memUsed   float64
	diskUsed  float64
	rxRate    float64
	txRate    float64
	online    bool
}

type StressEngine struct {
	mu       sync.Mutex
	running  bool
	startTime time.Time
	stopFn   context.CancelFunc
	agents   []stressAgent
	group    string
	db       *sql.DB
	hub      *Hub
	params   StressParams
	interval time.Duration
}

// 全局唯一引擎实例（包内 handler 直接引用）
var stressEngine = NewStressEngine()

func NewStressEngine() *StressEngine {
	return &StressEngine{interval: 5 * time.Second}
}

func newUUID() string {
	b := make([]byte, 16)
	crand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
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

func rnd(r *mrand.Rand, lo, hi float64) float64 { return lo + r.Float64()*(hi-lo) }

// pickEvenOrOne 返回"1 或偶数"：~20% 概率出 1，否则在 [max(lo,2), hi] 区间随机偶数。
// VPS 硬件规格通常都是 2 的倍数或 1，避免出现 3/5/7 这类不真实的奇数。
func pickEvenOrOne(r *mrand.Rand, lo, hi int) int {
	if hi < lo {
		lo, hi = hi, lo
	}
	if lo <= 1 && r.Intn(5) == 0 {
		return 1
	}
	l := lo
	if l < 2 {
		l = 2
	}
	if l%2 != 0 {
		l++
	}
	if l > hi {
		if lo <= 1 {
			return 1
		}
		if l%2 != 0 {
			return l - 1
		}
		return l
	}
	k := r.Intn(hi/2-l/2+1) + l/2
	return k * 2
}

// pickMemGE 返回内存大小（GB）：仅"1 或偶数"，且必须 >= minVal（通常为 CPU 核心数）。
// 优先在 [memLo, memHi] 区间内取；若区间内无满足 >= minVal 的合法值（例如核心数已大于内存上限），
// 则向上取到最小合法偶数/1，忽略 memHi 上限——保证"内存必须大于等于核心数"这一硬约束不被破坏。
func pickMemGE(r *mrand.Rand, memLo, memHi, minVal int) int {
	if memLo > memHi {
		memLo, memHi = memHi, memLo
	}
	if memLo < 1 {
		memLo = 1
	}
	// 区间下界提升到 >= max(memLo, minVal) 的最小合法值（1 或偶数）
	lo := memLo
	if minVal > lo {
		lo = minVal
	}
	start := lo
	if start != 1 && start%2 != 0 {
		start++
	}
	if start > memHi {
		// 区间内无合法值：返回向上取到的最小合法值（忽略上限）
		return start
	}
	// 在 [start, memHi] 内随机取一个合法值
	hi := memHi
	if hi%2 != 0 {
		hi--
	}
	if start == 1 {
		// 含 1（~20% 概率，仅当 1>=minVal 时）与 [2..hi] 的偶数
		if minVal <= 1 && r.Intn(5) == 0 {
			return 1
		}
		if hi < 2 {
			return 2
		}
		k := r.Intn(hi/2-1+1) + 1 // 1..hi/2
		return k * 2
	}
	k := r.Intn(hi/2-start/2+1) + start/2
	return k * 2
}

// pickMultipleOf10 在 [lo, hi] 内随机返回一个 10 的倍数（整数 GB）。
// VPS 硬盘规格通常以 10/20/30/... 标称，避免出现 47.3GB 这类不真实的随机值。
func pickMultipleOf10(r *mrand.Rand, lo, hi int) int {
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo < 10 {
		lo = 10
	}
	// 上下界对齐到 10 的倍数
	start := ((lo + 9) / 10) * 10
	end := (hi / 10) * 10
	if start > end {
		return start
	}
	n := (end - start) / 10
	return start + r.Intn(n+1)*10
}

// makeHostname 生成多样化主机名（避免全是 host-XXXXXXXX 风格）：
// 多种前缀 + 小写字母数字混合后缀，长度 3–10 不等，模拟真实 VPS 命名习惯
func makeHostname(r *mrand.Rand) string {
	prefixes := []string{"vps-", "node-", "srv-", "web-", "db-", "cache-", "box-", "app-", "ct", "vm-", "edge-", "prod-", "stg-", "h-", "k8s-", "lab-", "mx-", ""}
	p := prefixes[r.Intn(len(prefixes))]
	n := r.Intn(8) + 3 // 3..10
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alpha[r.Intn(len(alpha))]
	}
	return p + string(b)
}

// trafficCap 返回某流量档位下速率上限（bytes/s）
func trafficCap(level string) float64 {
	switch level {
	case "low":
		return 5 * 1024 * 1024
	case "mid":
		return 30 * 1024 * 1024
	case "high", "random", "":
		return 80 * 1024 * 1024
	default:
		return 80 * 1024 * 1024
	}
}

func pickCountry(r *mrand.Rand, codes []string) countryInfo {
	if len(codes) > 0 {
		code := codes[r.Intn(len(codes))]
		for _, c := range countryList {
			if c.code == code {
				return c
			}
		}
	}
	return countryList[r.Intn(len(countryList))]
}

func pickOS(r *mrand.Rand, keys []string) (string, string) {
	if len(keys) > 0 {
		k := keys[r.Intn(len(keys))]
		if vers, ok := osMap[k]; ok {
			v := vers[r.Intn(len(vers))]
			return v.os, v.platform
		}
	}
	// 随机：从所有版本里挑
	for _, vers := range osMap {
		v := vers[r.Intn(len(vers))]
		return v.os, v.platform
	}
	return "Linux", "linux"
}

// varyIP 在代表 IP 基础上随机化末段，避免大量重复（显示用，不影响国家）
func varyIP(r *mrand.Rand, base string) string {
	last := r.Intn(253) + 1
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '.' {
			return base[:i+1] + fmt.Sprintf("%d", last)
		}
	}
	return base
}

func makeAgent(r *mrand.Rand, p StressParams) stressAgent {
	c := pickCountry(r, p.Countries)
	osName, platform := pickOS(r, p.Oses)
	uptimeDays := rnd(r, 1, 400)
	if p.UptimeMin > 0 && p.UptimeMax > 0 {
		lo, hi := p.UptimeMin, p.UptimeMax
		if hi < lo {
			lo, hi = hi, lo
		}
		uptimeDays = rnd(r, float64(lo), float64(hi))
	}
	// CPU 核心数（仅 1 或偶数）
	cpuCount := pickEvenOrOne(r, 1, 32)
	if p.CpuCoresMin > 0 && p.CpuCoresMax > 0 {
		cpuCount = pickEvenOrOne(r, p.CpuCoresMin, p.CpuCoresMax)
	}
	// 内存大小（GB，整数，仅 1 或偶数，且必须 >= CPU 核心数）
	memLo, memHi := 1, 128
	if p.MemTotalMin > 0 && p.MemTotalMax > 0 {
		memLo, memHi = int(p.MemTotalMin), int(p.MemTotalMax)
	}
	memTotal := pickMemGE(r, memLo, memHi, cpuCount)
	// 硬盘大小（GB，整数，仅 10 的倍数）
	diskTotal := pickMultipleOf10(r, 20, 2000)
	if p.DiskTotalMin > 0 && p.DiskTotalMax > 0 {
		diskTotal = pickMultipleOf10(r, int(p.DiskTotalMin), int(p.DiskTotalMax))
	}
	a := stressAgent{
		r:           r,
		uuid:        newUUID(),
		hostname:    makeHostname(r),
		ip:          varyIP(r, c.ip),
		country:     c.name,
		countryCode: c.code,
		osName:      osName,
		platform:    platform,
		cpuCount:    cpuCount,
		memTotal:    memTotal,
		diskTotal:   diskTotal,
		uptime:      int64(uptimeDays) * 86400,
		online:      r.Float64() < p.OnlineRatio,
	}
	a.memPct = rnd(r, 10, 90)
	a.memUsed = float64(a.memTotal) * a.memPct / 100
	a.diskUsed = float64(a.diskTotal) * rnd(r, 0.1, 0.85)
	cap := trafficCap(p.TrafficLevel)
	a.rxRate = rnd(r, 0, cap)
	a.txRate = rnd(r, 0, cap)
	a.cpu = rnd(r, 1, 99)
	return a
}

func buildReport(a *stressAgent, intervalSec float64) AgentReport {
	return AgentReport{
		UUID:      a.uuid,
		Hostname:  a.hostname,
		IP:        a.ip,
		PublicIP:  a.ip,
		PublicIP4: a.ip,
		OS:        a.osName,
		Platform:  a.platform,
		BootTime:  time.Now().Unix() - a.uptime,
		Uptime:    a.uptime,
		CPU:       a.cpu,
		CPUCount:  a.cpuCount,
		MemUsed:   a.memUsed,
		MemTotal:  float64(a.memTotal),
		DiskUsed:  a.diskUsed,
		DiskTotal: float64(a.diskTotal),
		RxRate:    a.rxRate,
		TxRate:    a.txRate,
		RxDelta:   a.rxRate * intervalSec,
		TxDelta:   a.txRate * intervalSec,
	}
}

func (e *StressEngine) pushInitial(a *stressAgent) {
	// 用 Ephemeral：压测机只活在内存，不入库，服务重启后不会从 DB 复活成孤儿
	live.ApplyReportEphemeral(buildReport(a, e.interval.Seconds()), a.country, a.countryCode)
}

func (e *StressEngine) pushTick(a *stressAgent) {
	p := e.params
	// CPU
	if p.CpuMin > 0 || p.CpuMax > 0 {
		lo, hi := p.CpuMin, p.CpuMax
		if hi < lo {
			lo, hi = hi, lo
		}
		a.cpu = clamp(a.cpu+rnd(a.r, -8, 8), lo, hi)
	} else {
		a.cpu = clamp(a.cpu+rnd(a.r, -8, 8), 1, 99)
	}
	// 内存使用率
	var mlo, mhi float64 = 10, 90
	if p.MemMin > 0 || p.MemMax > 0 {
		mlo, mhi = p.MemMin, p.MemMax
		if mhi < mlo {
			mlo, mhi = mhi, mlo
		}
	}
	a.memPct = clamp(a.memPct+rnd(a.r, -5, 5), mlo, mhi)
	a.memUsed = float64(a.memTotal) * a.memPct / 100
	// 磁盘
	a.diskUsed = clamp(a.diskUsed+rnd(a.r, -3, 3), float64(a.diskTotal)*0.05, float64(a.diskTotal)*0.95)
	// 流量
	cap := trafficCap(p.TrafficLevel)
	a.rxRate = clamp(a.rxRate+rnd(a.r, -cap*0.2, cap*0.2), 0, cap*1.2)
	a.txRate = clamp(a.txRate+rnd(a.r, -cap*0.2, cap*0.2), 0, cap*1.2)
	// 同 pushInitial：用 Ephemeral，只入内存不入库
	live.ApplyReportEphemeral(buildReport(a, e.interval.Seconds()), a.country, a.countryCode)
}

// Start 启动压测；db/hub 用于停止时清理与广播
func (e *StressEngine) Start(p StressParams, db *sql.DB, hub *Hub) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return fmt.Errorf("压测已在运行中，请先停止")
	}
	if p.Count <= 0 {
		p.Count = 2000
	}
	if p.Count > 5000 {
		p.Count = 5000
	}
	if p.OnlineRatio <= 0 {
		p.OnlineRatio = 1
	}
	if p.OnlineRatio > 1 {
		p.OnlineRatio = 1
	}
	group := p.Group
	if group == "" {
		group = "干活的"
	}
	seed, _ := crand.Int(crand.Reader, big.NewInt(1<<62))

	e.group = group
	e.db = db
	e.hub = hub
	e.params = p
	e.startTime = time.Now()
	agents := make([]stressAgent, 0, p.Count)
	for i := 0; i < p.Count; i++ {
		agents = append(agents, makeAgent(mrand.New(mrand.NewSource(seed.Int64()+int64(i))), p))
	}
	e.agents = agents

	ctx, cancel := context.WithCancel(context.Background())
	e.stopFn = cancel
	e.running = true
	e.mu.Unlock()

	// 初始上报（让机器立即出现）
	for i := range e.agents {
		e.pushInitial(&e.agents[i])
	}
	// 分组打标（内存态，立即生效；用 Ephemeral 不标记 dirty，保持不入库）
	for i := range e.agents {
		live.UpdateAdminEphemeral(e.agents[i].uuid, "", "", group, nil)
	}
	live.AddGroup(group)
	if hub != nil {
		broadcastAgents(hub)
	}

	go e.runLoop(ctx)
	if p.DurationSec > 0 {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(p.DurationSec) * time.Second):
				e.Stop()
			}
		}()
	}
	return nil
}

func (e *StressEngine) runLoop(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.mu.Lock()
			agents := e.agents
			e.mu.Unlock()
			for i := range agents {
				a := &agents[i]
				if !a.online {
					continue
				}
				e.pushTick(a)
			}
		}
	}
}

// Stop 停止压测并清理：仅移除本引擎生成的 UUID，并仅在分组已无成员时删分组
func (e *StressEngine) Stop() error {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return nil
	}
	e.running = false
	stop := e.stopFn
	agents := e.agents
	group := e.group
	db := e.db
	hub := e.hub
	e.mu.Unlock()

	if stop != nil {
		stop()
	}
	for i := range agents {
		live.Remove(agents[i].uuid)
		if db != nil {
			DeleteAgent(db, agents[i].uuid)
		}
	}
	e.agents = nil
	// 仅当分组内已无任何成员（即没有真实机器）时才删除分组
	remaining := false
	for _, a := range live.Snapshot() {
		if a.Group == group {
			remaining = true
			break
		}
	}
	if !remaining && group != "" {
		if db != nil {
			DeleteGroup(db, group)
		}
		live.DeleteGroup(group)
	}
	if hub != nil {
		broadcastAgents(hub)
	}
	return nil
}

// Status 返回当前压测运行状态
func (e *StressEngine) Status() map[string]interface{} {
	e.mu.Lock()
	running := e.running
	group := e.group
	total := len(e.agents)
	e.mu.Unlock()
	online := 0
	if running {
		for _, a := range live.Snapshot() {
			if a.Group == group && a.Online {
				online++
			}
		}
	}
	elapsed := 0
	if running {
		elapsed = int(time.Since(e.startTime).Seconds())
	}
	return map[string]interface{}{
		"running":    running,
		"total":      total,
		"online":     online,
		"elapsedSec": elapsed,
		"group":      group,
	}
}

// StressOptions 返回前端多选所需的候选人列表
func StressOptions() map[string]interface{} {
	countries := make([]map[string]string, 0, len(countryList))
	for _, c := range countryList {
		countries = append(countries, map[string]string{"code": c.code, "name": c.name})
	}
	oses := make([]string, 0, len(osMap))
	for k := range osMap {
		oses = append(oses, k)
	}
	return map[string]interface{}{
		"countries": countries,
		"oses":      oses,
		"levels":    []string{"low", "mid", "high", "random"},
	}
}
