package main

import (
	"database/sql"
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
}

type trafficDelta struct {
	rx float64
	tx float64
}

// live 是全局唯一的实时状态实例
var live = NewServerState()

func NewServerState() *ServerState {
	return &ServerState{
		agents:  make(map[string]*AgentRow),
		dirty:   make(map[string]bool),
		traffic: make(map[string]trafficDelta),
	}
}

// LoadFromDB 启动时把 DB 全量载入内存，作为实时基盘（含本月流量与分组）
func (s *ServerState) LoadFromDB(db *sql.DB, month string) {
	rows, err := ListAgents(db, month)
	if err != nil {
		return
	}
	s.mu.Lock()
	for i := range rows {
		a := rows[i]
		s.agents[a.UUID] = &a
	}
	s.mu.Unlock()
}

// ApplyReport 处理一条上报：原地更新内存、累加流量、标记脏数据，全程不碰 DB、不广播
func (s *ServerState) ApplyReport(rep AgentReport, country string) {
	now := time.Now().Unix()
	s.mu.Lock()
	cur, ok := s.agents[rep.UUID]
	if !ok {
		cur = &AgentRow{UUID: rep.UUID, CreatedAt: now}
	}
	cur.Hostname = rep.Hostname
	cur.IP = rep.IP
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
	if rep.RxDelta > 0 {
		cur.RxMonth += rep.RxDelta
		d := s.traffic[rep.UUID]
		d.rx += rep.RxDelta
		s.traffic[rep.UUID] = d
	}
	if rep.TxDelta > 0 {
		cur.TxMonth += rep.TxDelta
		d := s.traffic[rep.UUID]
		d.tx += rep.TxDelta
		s.traffic[rep.UUID] = d
	}
	s.agents[rep.UUID] = cur
	s.dirty[rep.UUID] = true
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
		}
	}
	s.mu.Unlock()
}

// UpdateAdmin 更新管理员字段（别名/备注/分组/到期），同步内存
func (s *ServerState) UpdateAdmin(uuid, alias, remark, group string, expireAt *int64) {
	s.mu.Lock()
	a, ok := s.agents[uuid]
	if !ok {
		a = &AgentRow{UUID: uuid}
		s.agents[uuid] = a
	}
	if alias != "" {
		a.Alias = alias
	}
	a.Remark = remark
	a.Group = group
	a.ExpireAt = expireAt
	s.dirty[uuid] = true
	s.mu.Unlock()
}

// Remove 删除一台机器（主动注销/管理员移除），同步移除内存状态
func (s *ServerState) Remove(uuid string) {
	s.mu.Lock()
	delete(s.agents, uuid)
	delete(s.dirty, uuid)
	delete(s.traffic, uuid)
	s.mu.Unlock()
}

// RenameGroup 重命名分组：内存态中所有 Group==oldName 的客户端改为 newName，返回受影响数。
func (s *ServerState) RenameGroup(oldName, newName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.agents {
		if a.Group == oldName {
			a.Group = newName
			s.dirty[a.UUID] = true
			n++
		}
	}
	return n
}

// DeleteGroup 删除分组：内存态中所有 Group==name 的客户端置空（移回「未分组」），返回受影响数。
func (s *ServerState) DeleteGroup(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.agents {
		if a.Group == name {
			a.Group = ""
			s.dirty[a.UUID] = true
			n++
		}
	}
	return n
}

// Snapshot 返回当前全部机器的副本（用于广播 / REST）
func (s *ServerState) Snapshot() []AgentRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentRow, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, *a)
	}
	return out
}

// Flush 把脏数据与流量增量落库（由单一 goroutine 周期调用，避免并发写）
func (s *ServerState) Flush(db *sql.DB, month string) {
	s.mu.Lock()
	uuids := make([]string, 0, len(s.dirty))
	for u := range s.dirty {
		uuids = append(uuids, u)
	}
	s.dirty = make(map[string]bool)
	tmap := s.traffic
	s.traffic = make(map[string]trafficDelta)
	s.mu.Unlock()

	for _, u := range uuids {
		s.mu.RLock()
		a := s.agents[u]
		s.mu.RUnlock()
		if a == nil {
			continue
		}
		UpsertAgent(db, *a)
		if d, ok := tmap[u]; ok && (d.rx > 0 || d.tx > 0) {
			AddTraffic(db, u, month, d.rx, d.tx)
		}
	}
}
