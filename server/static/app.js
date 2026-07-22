'use strict';

const state = {
  agents: [],
  history: {},
  ws: null,
  loggedIn: false,
  currentUUID: null,
  theme: localStorage.getItem('probe-theme') || 'dark',
  viewMode: localStorage.getItem('probe-view') || 'card',
  // 当前选中的分组筛选（'' = 全部，'⚠ 离线' = 离线，否则为自定义组名）
  currentGroup: '',
};
let chart = null;

// ---------- 主题 ----------
function applyTheme() {
  document.body.classList.toggle('light', state.theme === 'light');
  const btn = document.getElementById('themeBtn');
  if (btn) btn.textContent = state.theme === 'dark' ? '☀️' : '🌙';
}
applyTheme();

document.getElementById('themeBtn').onclick = () => {
  state.theme = state.theme === 'dark' ? 'light' : 'dark';
  localStorage.setItem('probe-theme', state.theme);
  applyTheme();
  if (state.currentUUID) drawDetail();
};

// ---------- 视图切换 ----------
function applyViewToggle() {
  document.querySelectorAll('#viewToggle button').forEach(b => {
    b.classList.toggle('active', b.dataset.mode === state.viewMode);
  });
  document.getElementById('agentsGrid').classList.toggle('hidden', state.viewMode !== 'card');
  document.getElementById('agentsTable').classList.toggle('hidden', state.viewMode !== 'list');
}
applyViewToggle();

document.getElementById('viewToggle').onclick = (e) => {
  if (e.target.tagName !== 'BUTTON') return;
  state.viewMode = e.target.dataset.mode;
  localStorage.setItem('probe-view', state.viewMode);
  applyViewToggle();
  render();
};

// ---------- 登录态 ----------
async function checkLogin() {
  try {
    const r = await fetch('/api/me');
    if (r.ok) {
      const d = await r.json();
      showApp(d.username);
      connectWS();
    } else {
      showLogin();
    }
  } catch (e) {
    showLogin();
  }
}

function showApp(user) {
  document.getElementById('login').classList.add('hidden');
  document.getElementById('app').classList.remove('hidden');
  document.getElementById('user').textContent = user;
  state.loggedIn = true;
}
function showLogin() {
  document.getElementById('app').classList.add('hidden');
  document.getElementById('login').classList.remove('hidden');
  state.loggedIn = false;
}

document.getElementById('loginBtn').onclick = async () => {
  const u = document.getElementById('username').value;
  const p = document.getElementById('password').value;
  const r = await fetch('/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: u, password: p }),
  });
  if (r.ok) {
    checkLogin();
  } else {
    document.getElementById('loginErr').textContent = '用户名或密码错误';
  }
};
document.getElementById('password').addEventListener('keydown', e => {
  if (e.key === 'Enter') document.getElementById('loginBtn').click();
});
document.getElementById('logoutBtn').onclick = async () => {
  await fetch('/api/logout', { method: 'POST' });
  if (state.ws) state.ws.close();
  showLogin();
};

// ---------- WebSocket ----------
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(proto + '://' + location.host + '/ws/viewer');
  state.ws = ws;
  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'agents') {
      state.agents = msg.data;
      updateHistory(msg.data);
      requestRender();
    }
  };
  ws.onclose = () => {
    if (state.loggedIn) setTimeout(connectWS, 3000);
  };
}

function updateHistory(list) {
  for (const a of list) {
    if (!state.history[a.uuid]) state.history[a.uuid] = { rx: [], tx: [] };
    const h = state.history[a.uuid];
    h.rx.push(a.rx_rate);
    h.tx.push(a.tx_rate);
    if (h.rx.length > 60) { h.rx.shift(); h.tx.shift(); }
  }
}

// ---------- 格式化 ----------
function fmtBytes(b) {
  if (!b || b < 0) return '0 B';
  if (b < 1024) return b.toFixed(0) + ' B';
  const u = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let i = -1, n = b;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(2) + ' ' + u[i];
}
function fmtRate(bps) {
  if (!bps || bps < 0) return '0 B/s';
  if (bps < 1024) return bps.toFixed(0) + ' B/s';
  const u = ['KB/s', 'MB/s', 'GB/s'];
  let i = -1, n = bps;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(2) + ' ' + u[i];
}
function fmtUptime(s) {
  if (!s) return '-';
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  if (d > 0) return d + '天' + h + '时';
  const m = Math.floor((s % 3600) / 60);
  return h + '时' + m + '分';
}
// 倒计时（到期时间 → 天），返回 {text, cls, days}
// cls: 'expired' 已到期 / 'soon' ≤7天 / 'ok' >7天 / null 未设置
function fmtCountdown(expireAt) {
  if (!expireAt || !expireAt > 0) return null;
  const now = Math.floor(Date.now() / 1000);
  const diff = expireAt - now;
  const days = Math.ceil(diff / 86400);
  if (diff <= 0) return { text: '已到期', cls: 'expired', days: 0 };
  if (days <= 7) return { text: days + '天', cls: 'soon', days };
  return { text: days + '天', cls: 'ok', days };
}
function escapeHtml(s) {
  return (s || '').replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function flagFromCountry(country) {
  if (!country) return '';
  const m = country.match(/^[\u{1F1E6}-\u{1F1FF}]{2}\uFE0F?/u);
  return m ? m[0] : '';
}
function isPrivateIP(ip) {
  return /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.)/.test(ip) || ip === '127.0.0.1' || ip === '::1';
}
function percent(used, total) {
  if (!total) return 0;
  return Math.min(100, (used / total) * 100);
}
function fmtConfig(a) {
  const cores = a.cpu_count ? a.cpu_count + '核' : '-';
  const mem = a.mem_total ? fmtBytes(a.mem_total * 1e9) : '-';
  const disk = a.disk_total ? fmtBytes(a.disk_total * 1e9) : '-';
  return `${cores} / ${mem}内存 / ${disk}磁盘`;
}

// ---------- 渲染 ----------
function render() {
  renderGroupTabs();
  renderSummary();
  if (state.viewMode === 'list') {
    renderList();
  } else {
    renderCard();
  }
}

// requestRender 把多次实时更新合并到下一帧渲染一次，避免高频消息下重复重建 DOM
let renderPending = false;
function requestRender() {
  if (renderPending) return;
  renderPending = true;
  requestAnimationFrame(() => {
    renderPending = false;
    render();
    if (state.currentUUID) drawDetail();
  });
}

function renderSummary() {
  const total = state.agents.length;
  const online = state.agents.filter(a => a.online).length;
  const rxTotal = state.agents.reduce((s, a) => s + (a.rx_month || 0), 0);
  const txTotal = state.agents.reduce((s, a) => s + (a.tx_month || 0), 0);
  const rxRate = state.agents.reduce((s, a) => s + (a.rx_rate || 0), 0);
  const txRate = state.agents.reduce((s, a) => s + (a.tx_rate || 0), 0);

  document.getElementById('sumTotal').textContent = total;
  document.getElementById('sumOnline').textContent = online;
  document.getElementById('sumOffline').textContent = total - online;
  document.getElementById('sumRxTraffic').textContent = fmtBytes(rxTotal);
  document.getElementById('sumTxTraffic').textContent = fmtBytes(txTotal);
  document.getElementById('sumRxRate').textContent = fmtRate(rxRate);
  document.getElementById('sumTxRate').textContent = fmtRate(txRate);
}

// ---------- 分组（顶部筛选标签条） ----------
// 分组模型：在线客户端按自定义分组（空则为「未分组」），离线客户端自动归入「⚠ 离线」
const OFFLINE_GROUP = '⚠ 离线';

// 根据当前选中的分组筛选可见客户端
// state.currentGroup: '' = 全部；OFFLINE_GROUP = 离线；其余为自定义组名
function filteredAgents() {
  const g = state.currentGroup;
  if (!g) return state.agents;
  if (g === OFFLINE_GROUP) return state.agents.filter(a => !a.online);
  return state.agents.filter(a => a.online && a.group === g);
}

// 渲染顶部筛选标签条：全部(总数) / 各自定义组(数量) / ⚠ 离线(数量)
function renderGroupTabs() {
  const el = document.getElementById('groupTabs');
  const total = state.agents.length;
  const offlineCount = state.agents.filter(a => !a.online).length;

  // 在线分组计数
  const groupCounts = {};
  for (const a of state.agents) {
    if (a.online) {
      const g = a.group || '未分组';
      groupCounts[g] = (groupCounts[g] || 0) + 1;
    }
  }
  // 按名称排序；若当前选中的自定义组已无成员（例如刚被改名/移走），仍保留标签以免丢失高亮
  const groupNames = Object.keys(groupCounts).sort((x, y) => x.localeCompare(y, 'zh'));
  if (state.currentGroup && state.currentGroup !== OFFLINE_GROUP && !groupCounts[state.currentGroup]) {
    groupNames.push(state.currentGroup);
  }

  // 单个标签：managed=true 时附加重命名/删除操作（悬停显示），「未分组」与「⚠ 离线」不可管理
  const tabHTML = (name, count, opts) => {
    const active = state.currentGroup === name;
    const cls = ['group-tab', active ? 'active' : '', opts && opts.offline ? 'offline' : ''].join(' ').trim();
    const btn = `<button class="${cls}" data-group="${escapeHtml(name)}">${escapeHtml(name)} <span class="gt-count">${count}</span></button>`;
    if (opts && opts.managed) {
      const acts = `<span class="gt-acts">
        <span class="gt-act" data-act="rename" data-group="${escapeHtml(name)}" title="重命名分组">✎</span>
        <span class="gt-act" data-act="delete" data-group="${escapeHtml(name)}" title="删除分组（成员移回未分组）">✕</span>
      </span>`;
      return `<span class="group-tab-wrap">${btn}${acts}</span>`;
    }
    return btn;
  };

  let html = tabHTML('', total, {});
  for (const name of groupNames) {
    html += tabHTML(name, groupCounts[name], { managed: name !== '未分组' });
  }
  html += tabHTML(OFFLINE_GROUP, offlineCount, { offline: true });

  el.innerHTML = html;
  el.querySelectorAll('.group-tab').forEach(btn => {
    btn.onclick = () => {
      state.currentGroup = btn.dataset.group;
      requestRender();
    };
  });
  el.querySelectorAll('.gt-act').forEach(act => {
    act.onclick = () => {
      const g = act.dataset.group;
      if (act.dataset.act === 'rename') groupRename(g);
      else groupDelete(g);
    };
  });
}

// 重命名分组：改名会作用于该分组下的全部客户端
async function groupRename(oldName) {
  const input = prompt('重命名分组「' + oldName + '」\n将把该分组下的所有客户端移动到新分组：', oldName);
  if (input === null) return;
  const newName = input.trim();
  if (newName === '' || newName === oldName) return;
  if (newName === OFFLINE_GROUP) { alert('该名称保留给离线分组，不可使用'); return; }
  try {
    const r = await fetch('/api/groups/' + encodeURIComponent(oldName), {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: newName }),
    });
    if (!r.ok) { alert('重命名失败：HTTP ' + r.status); return; }
    if (state.currentGroup === oldName) state.currentGroup = newName;
    await refreshAgents();
  } catch (e) {
    alert('重命名失败：' + e.message);
  }
}

// 删除分组：成员移回「未分组」，不会被删除
async function groupDelete(name) {
  if (!confirm('确定删除分组「' + name + '」？\n该分组下的所有客户端将移回「未分组」（不会被删除）。')) return;
  try {
    const r = await fetch('/api/groups/' + encodeURIComponent(name), { method: 'DELETE' });
    if (!r.ok) { alert('删除失败：HTTP ' + r.status); return; }
    if (state.currentGroup === name) state.currentGroup = '';
    await refreshAgents();
  } catch (e) {
    alert('删除失败：' + e.message);
  }
}

// 立即从 REST 拉取最新全量列表（分组改名/删除后保证界面即时刷新，不依赖 WS 推送时序）
async function refreshAgents() {
  try {
    const r = await fetch('/api/agents');
    if (r.ok) {
      const list = await r.json();
      state.agents = list;
      updateHistory(list);
      requestRender();
    }
  } catch (e) {}
}

// 单张卡片 HTML（分组由渲染层统一处理，这里只画卡片本身）
function cardHTML(a) {
  const alias = a.alias || a.hostname || a.uuid.slice(0, 8);
  const flag = flagFromCountry(a.country) || '🌍';
  const code = a.country_code || (isPrivateIP(a.ip) ? '内网' : '');
  const loc = a.country ? (a.country.replace(flagFromCountry(a.country) || '', '').trim()) : (isPrivateIP(a.ip) ? '内网' : '');
  const osText = [a.os, a.platform].filter(Boolean).join(' · ') || 'Linux';

  const cd = fmtCountdown(a.expire_at);
  const cdBadge = cd ? `<span class="cd-badge ${cd.cls}" title="VPS 到期">📅 ${cd.text}</span>` : '';
  const groupBadge = a.group ? `<span class="card-group" title="分组">🏷️ ${escapeHtml(a.group)}</span>` : '';
  return `
    <div class="agent-card ${a.online ? '' : 'offline'}" data-uuid="${a.uuid}">
      <div class="card-header">
        <div class="card-title">
          <span class="flag">${flag}</span>
          <input class="card-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名">
          <button class="btn-edit" data-uuid="${a.uuid}" title="编辑名称/备注/分组/到期">✎</button>
        </div>
        <div class="card-status">
          <span class="dot ${a.online ? 'on' : 'off'}"></span>
          <span class="status-text ${a.online ? 'on' : 'off'}">${a.online ? '在线' : '离线'}</span>
        </div>
      </div>
      <div class="card-meta">
        <span>🖥️ ${escapeHtml(osText)}</span>
        <span>📍 ${escapeHtml(loc)} ${code ? '(' + escapeHtml(code) + ')' : ''}</span>
        <span>⏱️ ${fmtUptime(a.uptime)}</span>
      </div>
      <div class="card-config">
        <span class="card-config-text">${escapeHtml(fmtConfig(a))}</span>
        ${cdBadge}
      </div>
      ${groupBadge ? `<div class="card-remark">${groupBadge}${a.remark ? ' 📝 ' + escapeHtml(a.remark) : ''}</div>` : (a.remark ? `<div class="card-remark">📝 ${escapeHtml(a.remark)}</div>` : '')}
      <div class="card-metrics">
        <div class="metric">
          <div class="metric-label">CPU ${a.cpu.toFixed(1)}%</div>
          <div class="metric-bar"><div class="bar-cpu" style="width:${percent(a.cpu, 100)}%"></div></div>
        </div>
        <div class="metric">
          <div class="metric-label">内存 ${fmtBytes(a.mem_used * 1e9)} / ${fmtBytes(a.mem_total * 1e9)}</div>
          <div class="metric-bar"><div class="bar-mem" style="width:${percent(a.mem_used, a.mem_total)}%"></div></div>
        </div>
        <div class="metric">
          <div class="metric-label">磁盘 ${fmtBytes(a.disk_used * 1e9)} / ${fmtBytes(a.disk_total * 1e9)}</div>
          <div class="metric-bar"><div class="bar-disk" style="width:${percent(a.disk_used, a.disk_total)}%"></div></div>
        </div>
        <div class="metric">
          <div class="metric-label">实时速率</div>
          <div class="metric-value"><span class="down">↓${fmtRate(a.rx_rate)}</span> <span class="up">↑${fmtRate(a.tx_rate)}</span></div>
        </div>
      </div>
      <div class="card-traffic">
        <div class="traffic-item">
          <div class="traffic-label">本月下载 ↓</div>
          <div class="traffic-value down">${fmtBytes(a.rx_month)}</div>
          <div class="traffic-sub">每月1日重置</div>
        </div>
        <div class="traffic-item">
          <div class="traffic-label">本月上传 ↑</div>
          <div class="traffic-value up">${fmtBytes(a.tx_month)}</div>
          <div class="traffic-sub">自然月累计</div>
        </div>
      </div>
    </div>`;
}

function renderCard() {
  const grid = document.getElementById('agentsGrid');
  const list = filteredAgents();
  let html = '';
  if (list.length === 0) {
    html = `<div class="empty-tip">该分组下暂无客户端</div>`;
  } else {
    for (const a of list) html += cardHTML(a);
  }
  grid.innerHTML = html;
  grid.querySelectorAll('.agent-card').forEach(el => {
    el.onclick = () => openDetail(el.dataset.uuid);
  });
  grid.querySelectorAll('.btn-edit').forEach(btn => {
    btn.onclick = e => { e.stopPropagation(); openEdit(btn.dataset.uuid); };
  });
  bindAliasInputs('.card-name');
}

// ---------- 编辑名称/备注/到期 ----------
let editUUID = null;
function openEdit(uuid) {
  const a = state.agents.find(x => x.uuid === uuid);
  if (!a) return;
  editUUID = uuid;
  document.getElementById('editName').value = a.alias || '';
  document.getElementById('editGroup').value = a.group || '';
  document.getElementById('editRemark').value = a.remark || '';
  // 到期时间：Unix 秒 → YYYY-MM-DD（按本地时区）
  const exp = document.getElementById('editExpire');
  if (a.expire_at) {
    const d = new Date(a.expire_at * 1000);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    exp.value = y + '-' + m + '-' + day;
  } else {
    exp.value = '';
  }
  document.getElementById('editModal').classList.remove('hidden');
}
function closeEdit() {
  document.getElementById('editModal').classList.add('hidden');
  editUUID = null;
}
document.getElementById('editCancel').onclick = closeEdit;
document.getElementById('editModal').addEventListener('click', e => {
  if (e.target.id === 'editModal') closeEdit();
});
document.getElementById('editSave').onclick = async () => {
  if (!editUUID) return;
  const name = document.getElementById('editName').value;
  const group = document.getElementById('editGroup').value.trim();
  const remark = document.getElementById('editRemark').value;
  const dateStr = document.getElementById('editExpire').value;
  // 日期 → 该日 23:59:59 的 Unix 秒（按本地时区）
  let expireAt = null;
  if (dateStr) {
    const d = new Date(dateStr + 'T23:59:59');
    if (!isNaN(d.getTime())) expireAt = Math.floor(d.getTime() / 1000);
  }
  await fetch('/api/agents/' + editUUID, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, group, remark, expire_at: expireAt }),
  });
  const a = state.agents.find(x => x.uuid === editUUID);
  if (a) { a.alias = name; a.group = group; a.remark = remark; a.expire_at = expireAt; }
  closeEdit();
  render();
  if (state.currentUUID === editUUID) drawDetail();
};

function listRowHTML(a) {
  const alias = a.alias || a.hostname || a.uuid.slice(0, 8);
  const flag = flagFromCountry(a.country) || '🌍';
  const code = a.country_code || (isPrivateIP(a.ip) ? '内网' : '');
  const loc = a.country ? (a.country.replace(flagFromCountry(a.country) || '', '').trim()) : (isPrivateIP(a.ip) ? '内网' : '');
  const cd = fmtCountdown(a.expire_at);
  const cdHtml = cd ? `<div class="cd-text ${cd.cls}" title="VPS 到期">📅 ${cd.text}</div>` : '';
  return `
    <tr>
      <td><span class="dot ${a.online ? 'on' : 'off'}"></span> <span class="status-text ${a.online ? 'on' : 'off'}">${a.online ? '在线' : '离线'}</span></td>
      <td><input class="list-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名"></td>
      <td><span class="flag">${flag}</span>${escapeHtml(loc)} ${code ? '(' + escapeHtml(code) + ')' : ''}<br><span style="color:var(--text-2);font-size:12px">${escapeHtml(a.ip)}</span></td>
      <td>${escapeHtml(fmtConfig(a))}</td>
      <td>${fmtUptime(a.uptime)}${cdHtml}</td>
      <td>
        <div>CPU ${a.cpu.toFixed(1)}% <span class="mini-bar"><div class="bar-cpu" style="width:${percent(a.cpu, 100)}%"></div></span></div>
        <div style="margin-top:4px">内存 ${percent(a.mem_used, a.mem_total).toFixed(1)}% <span class="mini-bar"><div class="bar-mem" style="width:${percent(a.mem_used, a.mem_total)}%"></div></span></div>
        <div style="margin-top:4px">磁盘 ${percent(a.disk_used, a.disk_total).toFixed(1)}% <span class="mini-bar"><div class="bar-disk" style="width:${percent(a.disk_used, a.disk_total)}%"></div></span></div>
      </td>
      <td><span class="down">↓${fmtRate(a.rx_rate)}</span><br><span class="up">↑${fmtRate(a.tx_rate)}</span></td>
      <td><span class="down">${fmtBytes(a.rx_month)}</span><br><span class="up">${fmtBytes(a.tx_month)}</span></td>
      <td><button class="btn-chart" data-uuid="${a.uuid}">流量</button> <button class="btn-edit" data-uuid="${a.uuid}">编辑</button></td>
    </tr>
  `;
}

function renderList() {
  const table = document.getElementById('agentsTable');
  const list = filteredAgents();
  let rows = '';
  if (list.length === 0) {
    rows = `<tr><td colspan="9" class="empty-tip">该分组下暂无客户端</td></tr>`;
  } else {
    for (const a of list) rows += listRowHTML(a);
  }
  table.innerHTML = `
    <table>
      <thead>
        <tr>
          <th>状态</th>
          <th>别名</th>
          <th>位置</th>
          <th>配置</th>
          <th>运行时间</th>
          <th>使用率</th>
          <th>实时速率</th>
          <th>本月流量</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>${rows}</tbody>
    </table>
  `;

  table.querySelectorAll('.btn-chart').forEach(btn => {
    btn.onclick = () => openDetail(btn.dataset.uuid);
  });
  table.querySelectorAll('.btn-edit').forEach(btn => {
    btn.onclick = e => { e.stopPropagation(); openEdit(btn.dataset.uuid); };
  });
  bindAliasInputs('.list-name');
}

function bindAliasInputs(selector) {
  document.querySelectorAll(selector).forEach(inp => {
    inp.onclick = e => e.stopPropagation();
    const save = async () => {
      await fetch('/api/agents/' + inp.dataset.uuid + '/alias', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ alias: inp.value }),
      });
    };
    inp.onblur = save;
    inp.onkeydown = e => { if (e.key === 'Enter') inp.blur(); };
  });
}

// ---------- 详情曲线 ----------
function openDetail(uuid) {
  state.currentUUID = uuid;
  document.getElementById('detail').classList.remove('hidden');
  if (!chart) chart = echarts.init(document.getElementById('chart'));
  drawDetail();
}
function drawDetail() {
  const uuid = state.currentUUID;
  if (!uuid) return;
  const a = state.agents.find(x => x.uuid === uuid);
  const h = state.history[uuid] || { rx: [], tx: [] };
  document.getElementById('detailTitle').textContent =
    '实时流量 — ' + ((a && (a.alias || a.hostname)) || uuid.slice(0, 8));
  const color = getComputedStyle(document.body).color;
  chart.setOption({
    tooltip: { trigger: 'axis' },
    legend: { data: ['下行', '上行'], textStyle: { color: color } },
    grid: { left: 60, right: 20, top: 40, bottom: 30 },
    xAxis: { type: 'category', data: h.rx.map((_, i) => i), axisLabel: { show: false } },
    yAxis: { type: 'value', axisLabel: { formatter: v => fmtRate(v) } },
    series: [
      { name: '下行', type: 'line', data: h.rx, showSymbol: false, smooth: true, areaStyle: { opacity: 0.1 } },
      { name: '上行', type: 'line', data: h.tx, showSymbol: false, smooth: true, areaStyle: { opacity: 0.1 } },
    ],
  });
}
document.getElementById('closeDetail').onclick = () => {
  document.getElementById('detail').classList.add('hidden');
  state.currentUUID = null;
};

// ---------- 生成安装命令 ----------
document.getElementById('installCmdBtn').onclick = async () => {
  const btn = document.getElementById('installCmdBtn');
  btn.disabled = true;
  try {
    const r = await fetch('/api/install-command');
    if (!r.ok) throw new Error('获取失败: HTTP ' + r.status);
    const d = await r.json();
    document.getElementById('installCmdText').value = d.command;
    document.getElementById('installModal').classList.remove('hidden');
    // 自动复制（需 HTTPS / localhost；失败静默回落到用户手动点复制）
    try { await navigator.clipboard.writeText(d.command); } catch (e) {}
  } catch (e) {
    alert('获取安装命令失败：' + e.message);
  } finally {
    btn.disabled = false;
  }
};
document.getElementById('installCopyBtn').onclick = async () => {
  const txt = document.getElementById('installCmdText').value;
  try {
    await navigator.clipboard.writeText(txt);
    const btn = document.getElementById('installCopyBtn');
    const old = btn.textContent;
    btn.textContent = '✓ 已复制';
    setTimeout(() => { btn.textContent = old; }, 1500);
  } catch (e) {
    const ta = document.getElementById('installCmdText');
    ta.select();
    document.execCommand && document.execCommand('copy');
  }
};
document.getElementById('installCloseBtn').onclick = () => {
  document.getElementById('installModal').classList.add('hidden');
};
document.getElementById('installModal').addEventListener('click', e => {
  if (e.target.id === 'installModal') document.getElementById('installModal').classList.add('hidden');
});

checkLogin();
