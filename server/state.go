package main

import (
	"database/sql"
	"log"
	"net"
	"sort"
	"sync"
	"time"
)

// ServerState 维护全部客户端的实时状态，内存为权威源。
// 设计目标：上报处理只更新内存（O(1)，不碰 DB、不广播），
// 广播与落库由 main.go 里的独立 ticker 周期执行，
// 从而把“每条上报都全量查询+全量序列化+全员广播”的 O(N²) 成本降为 O(N) 且固定频率。
type ServerState struct {
	mu      sync.RWMutex
	agents  map[string]*AgentRow
	dirty   map[string]bool
	traffic map[string]trafficDelta
	// changed 是「自上次广播以来内存发生变化的机器」集合，专供增量广播消费（P3）。
	// 与 dirty 分离是刻意的：dirty 服务于「落库节流」，changed 服务于「广播节流」，
	// 两者消费节奏完全不同（落库 60 秒一次、广播每秒一次），共用一个标记会互相干扰——
	// 比如落库被节流跳过时若顺手清了标记，广播就会误判"没有变化"导致面板静止。
	changed map[string]bool
	// removed 是「自上次广播以来被删除的机器」UUID 集合，供增量广播下发删除指令。
	removed map[string]bool
	// lastPersist 记录每台机器上次落库的 Unix 秒，用于心跳类字段的落库节流（P2）。
	lastPersist map[string]int64
	// forcePersist 标记「必须立即落库」的机器（上线/离线/分组/别名/到期/国家等
	// 重要变化）。这类变化不能等节流窗口，否则管理员改完分组一重启就回退。
	forcePersist map[string]bool
	// groups 是「分组名注册表」，记录所有已存在的自定义分组名（含 0 成员的空组），
	// 用于标签条渲染与编辑下拉。key=分组名，value=创建时间（Unix 秒）。
	groups map[string]int64
	// lastMonth 记录内存态当前归属的月份键（格式 2006-01，北京时间口径）。
	// Flush 每次带入"此刻的月份"，一旦与本字段不同即判定跨月：
	// 先用旧月份把残余流量增量落库，再把所有 agent 的本月流量清零。
	// 空串表示尚未初始化（进程刚起、还没 LoadFromDB / Flush 过）。
	lastMonth string
}

type trafficDelta struct {
	rx float64
	tx float64
}

// live 是全局唯一的实时状态实例
var live = NewServerState()

// heartbeatPersistSecs 是「纯心跳字段」（CPU/内存/磁盘/网卡速率/最后在线时间）的落库节流窗口。
//
// 为什么能节流：这些字段每秒都在变，但它们的价值只在"此刻看着"——面板读的是内存态，
// 每次上报都会立刻更新内存，所以节流**完全不影响面板实时性**；落库只是给"服务重启后
// 恢复到上一次状态"用的，而离线判定本身是分钟级（offline_threshold），差几十秒无感。
//
// 为什么必须节流：不节流时每台机器每次心跳都会产生一条 UPSERT，2000 台就是每秒近千次
// 独立写事务，实测把单核 VPS 的磁盘写满（7.6MB/s、iowait 48%）。节流到 60 秒后，
// 落库条数降为 1/30，配合批量事务可让写入量降两个数量级——这是支撑 5000 台规模的前提。
const heartbeatPersistSecs = 60

func NewServerState() *ServerState {
	return &ServerState{
		agents:       make(map[string]*AgentRow),
		dirty:        make(map[string]bool),
		traffic:      make(map[string]trafficDelta),
		changed:      make(map[string]bool),
		removed:      make(map[string]bool),
		lastPersist:  make(map[string]int64),
		forcePersist: make(map[string]bool),
		groups:       make(map[string]int64),
	}
}

// LoadFromDB 启动时把 DB 全量载入内存，作为实时基盘（含本月流量与分组）
func (s *ServerState) LoadFromDB(db *sql.DB, month string) {
	rows, err := ListAgents(db, month)
	if err != nil {
		return
	}
	// 先把 agents 全部载入；同时把出现过的 group_name 暂存，结束后再与「groups 表」取并集
	// 避免依赖 LOAD ORDER：注册表里有但没人用 → 仍需保留
	seenGroups := make(map[string]struct{})
	s.mu.Lock()
	for i := range rows {
		a := rows[i]
		// 跳过"幽灵孤儿"：online=0 且 last_seen=0 表示这条记录被写入 DB，
		// 但客户端从未真正连上来上报过心跳（典型场景：压测/批量脚本误入库的僵尸）。
		// 真实离线机的 last_seen 是它最后一次上报的时间（永远 >0），不会被误杀，
		// 监控面板仍能正常显示掉线的真机器。详见 CleanupStale。
		if !a.Online && a.LastSeen == 0 {
			continue
		}
		s.agents[a.UUID] = &a
		if a.Group != "" {
			seenGroups[a.Group] = struct{}{}
		}
	}
	// 加载注册表
	gs, err := ListGroups(db)
	if err == nil {
		for _, n := range gs {
			s.groups[n] = time.Now().Unix()
			delete(seenGroups, n)
		}
	}
	// 把 agents 出现但注册表里没有的（脏数据/历史遗留）补回去
	for n := range seenGroups {
		s.groups[n] = time.Now().Unix()
	}
	// 记录内存态归属的月份：载入的本月流量就是这个月的，
	// 后续 Flush 一旦发现月份变了，即触发跨月清零。
	s.lastMonth = month
	s.mu.Unlock()
}

// CleanupStale 清理内存中的"幽灵孤儿"：online=0 且 last_seen=0 的 agent
// （被写入 DB 但从未真正连上来上报过心跳的僵尸）。这些不是真实机器，
// 留着会在面板冒出无意义的"离线幽灵"。真实离线机的 last_seen 永远 >0
// （是其最后一次上报的时间），因此不会被误删，监控面板仍能正常显示掉线的真机器。
// 同时把 DB 中同签名的残留行一并删除，避免重启或下次 LoadFromDB 时复活。
// 由 main.go 在启动后及周期 ticker 中调用。
func (s *ServerState) CleanupStale(db *sql.DB) {
	s.mu.Lock()
	var toRemove []string
	for uuid, a := range s.agents {
		if !a.Online && a.LastSeen == 0 {
			toRemove = append(toRemove, uuid)
		}
	}
	for _, uuid := range toRemove {
		delete(s.agents, uuid)
		delete(s.dirty, uuid)
		delete(s.traffic, uuid)
	}
	s.mu.Unlock()

	if db != nil {
		// 清掉 DB 中同签名的残留行（online=0 且 last_seen=0），幂等且安全：
		// 真实机器的 last_seen 始终 >0，不会被波及。
		if _, err := db.Exec(`DELETE FROM agents WHERE online=0 AND last_seen=0`); err != nil {
			log.Printf("[probe] CleanupStale 清理 DB 幽灵失败: %v", err)
		}
	}
	if len(toRemove) > 0 {
		log.Printf("[probe] CleanupStale 清理了 %d 台幽灵孤儿机器", len(toRemove))
	}
}

// ApplyReport 处理一条上报：原地更新内存、累加流量、标记脏数据，全程不碰 DB、不广播
func (s *ServerState) ApplyReport(rep AgentReport, country, countryCode string) {
	s.applyReport(rep, country, countryCode, true)
}

// ApplyReportEphemeral 与 ApplyReport 行为一致，但不标记脏、不累加流量——
// 数据只活在内存，Flush 不会入库。专用于压测引擎：让模拟机随进程消亡，
// 避免服务重启后从 SQLite 复活成"孤儿"，导致停止按钮清理不掉。
func (s *ServerState) ApplyReportEphemeral(rep AgentReport, country, countryCode string) {
	s.applyReport(rep, country, countryCode, false)
}

func (s *ServerState) applyReport(rep AgentReport, country, countryCode string, persist bool) {
	now := time.Now().Unix()
	s.mu.Lock()
	cur, ok := s.agents[rep.UUID]
	if !ok {
		cur = &AgentRow{UUID: rep.UUID, CreatedAt: now}
		// 新机器首次上报：立刻落库建档，否则服务重启后这台机器会凭空消失
		// （节流窗口可能长达 60 秒，期间重启就丢一台）。
		s.forcePersist[rep.UUID] = true
	}
	cur.Hostname = rep.Hostname
	cur.IP = rep.IP
	cur.PublicIP = rep.PublicIP
	// 双栈公网 IP：优先用 agent 显式上报的 v4/v6；
	// 老 agent 只报单个 public_ip 时，按 IP 格式归类到 v4 或 v6，
	// 保证滚动升级（新 server + 老 agent）期间列表不空白。
	cur.PublicIP4 = rep.PublicIP4
	cur.PublicIP6 = rep.PublicIP6
	if rep.PublicIP4 == "" && rep.PublicIP6 == "" && rep.PublicIP != "" {
		if net.ParseIP(rep.PublicIP).To4() != nil {
			cur.PublicIP4 = rep.PublicIP
		} else {
			cur.PublicIP6 = rep.PublicIP
		}
	}
	cur.BootTime = rep.BootTime
	cur.Uptime = rep.Uptime
	cur.CPU = rep.CPU
	cur.CPUCount = rep.CPUCount
	cur.MemUsed = rep.MemUsed
	cur.MemTotal = rep.MemTotal
	cur.DiskUsed = rep.DiskUsed
	cur.DiskTotal = rep.DiskTotal
	cur.RxRate = rep.RxRate
	cur.TxRate = rep.TxRate
	// 离线→上线属于重要变化：必须立即落库。否则机器刚恢复、或管理员刚改完分组，
	// 库里仍停留在"离线/旧分组"，一旦重启就会回退到错误状态。
	if !cur.Online {
		s.forcePersist[rep.UUID] = true
	}
	cur.Online = true
	cur.LastSeen = now
	if rep.OS != "" {
		cur.OS = rep.OS
	}
	if rep.Platform != "" {
		cur.Platform = rep.Platform
	}
	if country != "" {
		cur.Country = country
	}
	if countryCode != "" {
		cur.CountryCode = countryCode
	}
	// 月累计流量：无论 ephemeral 还是持久化都在内存里累加，
	// 让压测机的「本月流量」与顶部月度聚合能看到累计值（更真实）。
	// 写库用的 delta 桶 s.traffic 仅 persist 时累积，ephemeral 不入库，
	// 停止/重启即随内存一起清零，保持"内存临时态"语义。
	if rep.RxDelta > 0 {
		cur.RxMonth += rep.RxDelta
	}
	if rep.TxDelta > 0 {
		cur.TxMonth += rep.TxDelta
	}
	if persist {
		if rep.RxDelta > 0 {
			d := s.traffic[rep.UUID]
			d.rx += rep.RxDelta
			s.traffic[rep.UUID] = d
		}
		if rep.TxDelta > 0 {
			d := s.traffic[rep.UUID]
			d.tx += rep.TxDelta
			s.traffic[rep.UUID] = d
		}
	}
	s.agents[rep.UUID] = cur
	// 内存态已变：增量广播据此把这台机器推给 viewer（与落库节流无关，
	// 故放在 changed 里而不是 dirty 里，两者互不影响）。
	s.changed[rep.UUID] = true
	if persist {
		s.dirty[rep.UUID] = true
	}
	s.mu.Unlock()
}

// SetCountry 由异步地理查询回调：查成功后立即回写内存态 country/country_code，
// 避免运行中这两个字段永远停留在 server 启动 loadFromDB 时的旧值。
// （lookupCountry 同步路径走 ApplyReport 的 country/code 参数；本方法专供
// cache miss 异步 goroutine 写内存 + dirty，由 SaveAgent 后续落库。）
func (s *ServerState) SetCountry(uuid, country, code string) {
	s.mu.Lock()
	if cur, ok := s.agents[uuid]; ok {
		if country != "" {
			cur.Country = country
		}
		if code != "" {
			cur.CountryCode = code
		}
		s.dirty[uuid] = true
		s.changed[uuid] = true
		// 归属地是低频且重要的字段（异步查询成功后回写），立即落库。
		s.forcePersist[uuid] = true
	}
	s.mu.Unlock()
}

// SetOffline 离线扫描：把超时未上报的标记为离线，并标记脏数据以便落库
func (s *ServerState) SetOffline(threshold int64) {
	now := time.Now().Unix()
	s.mu.Lock()
	for _, a := range s.agents {
		if a.Online && a.LastSeen < now-threshold {
			a.Online = false
			s.dirty[a.UUID] = true
			s.changed[a.UUID] = true
			// 上下线是重要变化：立即落库，不能被心跳节流窗口延迟。
			s.forcePersist[a.UUID] = true
		}
	}
	s.mu.Unlock()
}

// UpdateAdmin 部分更新管理员字段：仅更新传入的非 nil 字段，其余保持不变
// （修复「别名无法清空」与「漏传字段被静默清空」 #5）。
func (s *ServerState) UpdateAdmin(uuid string, alias, remark, group *string, expireAt *int64) {
	s.updateAdmin(uuid, alias, remark, group, expireAt, true)
}

// UpdateAdminEphemeral 与 UpdateAdmin 行为一致，但不标记 dirty——
// 用于压测引擎给模拟机打分组，避免 Flush 把它们写入 SQLite。
func (s *ServerState) UpdateAdminEphemeral(uuid, alias, remark, group string, expireAt *int64) {
	s.updateAdmin(uuid, &alias, &remark, &group, expireAt, false)
}

// PatchAgentFields 只更新请求中提供的字段（指针非 nil 才改），用于批量编辑时
// 不覆盖未传字段（如仅改分组时保留备注/到期）。在锁内完成，避免并发读到半成品。
func (s *ServerState) PatchAgentFields(uuid string, group, remark *string, expireAt *int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[uuid]
	if !ok {
		return
	}
	if group != nil {
		a.Group = *group
	}
	if remark != nil {
		a.Remark = *remark
	}
	if expireAt != nil {
		a.ExpireAt = expireAt
	}
	// 批量编辑同样是管理员改动：立即落库 + 立即广播。
	s.dirty[uuid] = true
	s.changed[uuid] = true
	s.forcePersist[uuid] = true
}

func (s *ServerState) updateAdmin(uuid string, alias, remark, group *string, expireAt *int64, persist bool) {
	s.mu.Lock()
	a, ok := s.agents[uuid]
	if !ok {
		a = &AgentRow{UUID: uuid}
		s.agents[uuid] = a
	}
	if alias != nil {
		a.Alias = *alias
	}
	if remark != nil {
		a.Remark = *remark
	}
	if group != nil {
		a.Group = *group
	}
	if expireAt != nil {
		a.ExpireAt = expireAt
	}
	if persist {
		s.dirty[uuid] = true
		s.changed[uuid] = true
		// 管理员改动（别名/备注/分组/到期）是重要变化：立刻落库，
		// 否则改完一重启就回退，是最容易被用户感知的丢数据。
		s.forcePersist[uuid] = true
	} else {
		// 压测机（ephemeral）不落库，但仍要广播出去让面板看得到。
		s.changed[uuid] = true
	}
	s.mu.Unlock()
}

// Remove 删除一台机器（主动注销/管理员移除），同步移除内存状态
func (s *ServerState) Remove(uuid string) {
	s.mu.Lock()
	delete(s.agents, uuid)
	delete(s.dirty, uuid)
	delete(s.traffic, uuid)
	delete(s.changed, uuid)
	delete(s.lastPersist, uuid)
	delete(s.forcePersist, uuid)
	// 记入删除集合：增量广播需要显式通知 viewer 把这台机器的卡片移除，
	// 否则只推"变化的机器"时，被删掉的机器会永远残留在面板上。
	if _, ok := s.removed[uuid]; !ok {
		s.removed[uuid] = true
	}
	s.mu.Unlock()
}

// RenameGroup 重命名分组：内存态中所有 Group==oldName 的客户端改为 newName，注册表也同步重命名，返回受影响 agent 数。
func (s *ServerState) RenameGroup(oldName, newName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.agents {
		if a.Group == oldName {
			a.Group = newName
			s.dirty[a.UUID] = true
			s.changed[a.UUID] = true
			s.forcePersist[a.UUID] = true
			n++
		}
	}
	if _, ok := s.groups[oldName]; ok {
		s.groups[newName] = s.groups[oldName]
		delete(s.groups, oldName)
	}
	return n
}

// DeleteGroup 删除分组：内存态中所有 Group==name 的客户端置空（移回「未分组」），注册表也删除，返回受影响 agent 数。
func (s *ServerState) DeleteGroup(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.agents {
		if a.Group == name {
			a.Group = ""
			s.dirty[a.UUID] = true
			s.changed[a.UUID] = true
			s.forcePersist[a.UUID] = true
			n++
		}
	}
	delete(s.groups, name)
	return n
}

// AddGroup 注册一个新分组到内存（如果已存在则不覆盖原 created_at）。
func (s *ServerState) AddGroup(name string) {
	s.mu.Lock()
	if _, ok := s.groups[name]; !ok {
		s.groups[name] = time.Now().Unix()
	}
	s.mu.Unlock()
}

// RemoveGroup 从内存中删除一个分组（不影响 agents；agents 的 group_name 由调用方决定是否清空）。
func (s *ServerState) RemoveGroup(name string) {
	s.mu.Lock()
	delete(s.groups, name)
	s.mu.Unlock()
}

// Groups 返回当前所有已注册分组名（按字典序），用于广播 / REST 列表。
func (s *ServerState) Groups() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.groups))
	for n := range s.groups {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// PendingChanged 返回自上次广播以来是否有机器状态发生变化。
// 用于广播门控（P0 性能修复）：状态未变时即便有 viewer 连着也不重新序列化，
// 避免每秒白烧 CPU；agent 上报/离线/管理员改动等事件仍会即时推送，面板实时性不受影响。
//
// 注意：这里刻意用 changed 而不是 dirty。dirty 会被落库节流长时间持有
// （一台机器从上报到落库可能挂 60 秒），若拿它判断"要不要广播"，
// 广播反而会被"待落库"误导；而面板看的是内存态，判断依据必须是"内存有没有变"。
func (s *ServerState) PendingChanged() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.changed) > 0 || len(s.removed) > 0
}

// PendingCount 返回待落库的机器数，仅用于运行期日志统计（[stats]），不参与任何业务逻辑。
func (s *ServerState) PendingCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.dirty)
}

// Counts 返回机器总数与在线数，供周期健康日志使用。
func (s *ServerState) Counts() (total int, online int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total = len(s.agents)
	for _, a := range s.agents {
		if a.Online {
			online++
		}
	}
	return
}

// TakeDelta 取走自上次调用以来发生变化与被删除的机器，用于增量广播（P3）。
//
// 全量快照的成本与机器总数成正比（5000 台约 3.7MB），每秒推一帧会同时压垮
// 服务端序列化/压缩与浏览器 JSON.parse。实际每秒真正发生变化的通常只有几十台
// （心跳是逐台错峰的），因此只推"变化的部分"能把每帧从 MB 级降到 KB 级。
//
// 取走即清空（consume 语义）：调用方必须负责把结果广播出去，否则这次变化就丢了。
// 全量广播（对账帧）走 TakeSnapshot，两者互斥使用。
func (s *ServerState) TakeDelta() (changed []AgentRow, removed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.changed) == 0 && len(s.removed) == 0 {
		return nil, nil
	}
	for u := range s.changed {
		if a, ok := s.agents[u]; ok {
			changed = append(changed, *a)
		}
	}
	for u := range s.removed {
		removed = append(removed, u)
	}
	// 清空，下一轮只推新变化
	s.changed = make(map[string]bool)
	s.removed = make(map[string]bool)
	return changed, removed
}

// MarkBroadcasted 在推送全量快照（首帧或对账帧）后调用，丢弃此前积压的增量。
// 因为全量快照已包含截至此刻的完整最新状态，若不清空，下一帧增量会把同样的
// 变化再推一遍（无害但浪费）；更重要的是保证"全量帧 → 增量帧"的语义衔接正确。
func (s *ServerState) MarkBroadcasted() {
	s.mu.Lock()
	s.changed = make(map[string]bool)
	s.removed = make(map[string]bool)
	s.mu.Unlock()
}

// Snapshot 返回当前全部机器的副本（用于广播 / REST）。
// 必须按「添加时间」升序稳定排序：Go map 迭代是随机的，不排序会让卡片
// 每秒在 UI 上"洗牌"；同时也是用户要求的"按添加时间固定排位"。
// 同秒创建用 uuid 字典序兜底，保证严格稳定。
func (s *ServerState) Snapshot() []AgentRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentRow, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].UUID < out[j].UUID
	})
	return out
}

// Flush 把脏数据与流量增量落库（由单一 goroutine 周期调用，避免并发写）。
//
// month 由调用方传入"此刻的月份键"（北京时间口径，见 clock.go 的 curMonth）。
// 本方法同时承担【跨月重置】职责：一旦 month 与内存记录的 lastMonth 不同，
// 说明刚跨过北京时间每月 1 日 0 点，于是：
//  1. 本轮残余的流量增量仍记到【上一个月】（误差最多一个 Flush 周期即 2 秒，可忽略）；
//  2. 把所有 agent 内存态的 RxMonth/TxMonth 清零，面板「本月流量」立刻从 0 重新计；
//  3. traffic_monthly 里上个月的行原样保留，历史随时可查。
//
// 这样即使服务连续数月不重启，也能在 1 日 0 点准点重置——
// 修复前内存态只加不减，必须重启容器才会"看起来"归零。
func (s *ServerState) Flush(db *sql.DB, month string) {
	s.mu.Lock()
	now := time.Now().Unix()

	// —— 1. 跨月检测 ——
	// 必须放在「挑选本轮落库机器」之前：跨月时要把残余流量增量写进【上一个月】，
	// 所以需要先决定 writeMonth，并强制本轮落库全部机器（否则残余增量会顺着
	// 节流窗口漂到下一个 Flush，那时 lastMonth 已更新，就会错记到新月里）。
	writeMonth := month // 本轮残余增量写入哪个月
	rolledFrom := ""    // 非空表示发生了跨月
	rolledCount := 0
	switch {
	case s.lastMonth == "":
		// 进程刚起、还没有过基准（正常情况下 LoadFromDB 已设过）
		s.lastMonth = month
	case s.lastMonth != month:
		rolledFrom = s.lastMonth
		writeMonth = s.lastMonth
		for u, a := range s.agents {
			// 跨月这一轮强制全量落库：一个月才发生一次，代价可忽略，
			// 换来的是月度流量账单绝对不会串月。
			s.dirty[u] = true
			s.forcePersist[u] = true
			a.RxMonth = 0
			a.TxMonth = 0
			rolledCount++
		}
		s.lastMonth = month
	}

	// —— 2. 落库节流：本轮只挑出「重要变化」或「心跳节流窗口已到」的机器 ——
	// 纯心跳（CPU/内存/网速/最后在线时间）每秒都在变，但面板读的是内存态、上报即更新，
	// 落库慢一点对使用零影响；而每台每次心跳都写一次库，2000 台就是每秒近千次写事务，
	// 实测把单核 VPS 的磁盘打满。节流后落库条数降为约 1/30。
	// 重要变化（新机器/上下线/分组/别名/到期/归属地）走 forcePersist 无条件立即落库。
	uuids := make([]string, 0, 64)
	for u := range s.dirty {
		if s.forcePersist[u] || now-s.lastPersist[u] >= heartbeatPersistSecs {
			uuids = append(uuids, u)
		}
	}
	for _, u := range uuids {
		delete(s.dirty, u)
		delete(s.forcePersist, u)
		s.lastPersist[u] = now
	}

	// 流量增量只搬走本轮真正落库机器的那部分。
	// 被节流跳过的机器必须把增量继续留在 s.traffic 里累积到下次落库，
	// 否则这段流量就永久丢了（月度流量会平白少一截）。
	tmap := make(map[string]trafficDelta, len(uuids))
	for _, u := range uuids {
		if d, ok := s.traffic[u]; ok {
			tmap[u] = d
			delete(s.traffic, u)
		}
	}

	s.mu.Unlock()

	if rolledFrom != "" {
		log.Printf("[probe] 跨月重置：%s -> %s（北京时间），已清零 %d 台机器的本月流量，%s 的历史数据保留在 traffic_monthly",
			rolledFrom, month, rolledCount, rolledFrom)
	}

	if len(uuids) == 0 {
		// 节流窗口内无可落库机器：一次 SQL 都不发（绝大多数周期都是这种情况）。
		return
	}

	// —— 3. 批量事务：把本轮全部写入合并成一次提交 ——
	// 逐条 db.Exec 时每条 SQL 都是一个独立事务，SQLite 要为每次提交追加 WAL 帧、
	// 并重复写被修改的页面；2000 台实测每秒近千次提交、持续 7.6MB/s 写入，
	// 把单核 VPS 的磁盘打满（iowait 48%，进程长期卡在 D 状态）。
	// 合成一个事务后：提交次数 N 次 → 1 次，同一页面在 WAL 中只落一次，
	// 写入量降一个数量级，而最终落盘的数据内容与逐条写入完全等价。
	tx, err := db.Begin()
	if err != nil {
		log.Printf("[probe] Flush 开启事务失败，本轮跳过落库: %v", err)
		return
	}
	committed := false
	defer func() {
		// 任何提前 return 都要回滚，避免残留一个未关闭的事务长期占着写锁，
		// 那会让后续所有 Flush 与 API 写操作全部卡死。
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, u := range uuids {
		s.mu.RLock()
		a, ok := s.agents[u]
		var row AgentRow
		if ok {
			row = *a // 在持读锁期间完成值拷贝，避免与 applyReport 的并发写产生撕裂读
		}
		s.mu.RUnlock()
		if !ok {
			continue // 机器已在本轮之间被删除
		}
		if err := UpsertAgentTx(tx, row); err != nil {
			log.Printf("[probe] Flush 写入机器 %s 失败: %v", u, err)
			continue
		}
		if d, ok := tmap[u]; ok && (d.rx > 0 || d.tx > 0) {
			if err := AddTrafficTx(tx, u, writeMonth, d.rx, d.tx, now); err != nil {
				log.Printf("[probe] Flush 写入流量 %s 失败: %v", u, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[probe] Flush 提交事务失败，本轮写入已回滚: %v", err)
		return
	}
	committed = true
}
