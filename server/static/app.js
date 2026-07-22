'use strict';

const state = {
  agents: [],
  history: {},
  ws: null,
  loggedIn: false,
  currentUUID: null,
  theme: localStorage.getItem('probe-theme') || 'dark',
  viewMode: localStorage.getItem('probe-view') || 'card',
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
      render();
      if (state.currentUUID) drawDetail();
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
  renderSummary();
  if (state.viewMode === 'list') {
    renderList();
  } else {
    renderCard();
  }
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

function renderCard() {
  const grid = document.getElementById('agentsGrid');
  grid.innerHTML = '';
  for (const a of state.agents) {
    const card = document.createElement('div');
    card.className = 'agent-card' + (a.online ? '' : ' offline');
    card.onclick = () => openDetail(a.uuid);

    const alias = a.alias || a.hostname || a.uuid.slice(0, 8);
    const flag = flagFromCountry(a.country) || '🌍';
    const code = a.country_code || (isPrivateIP(a.ip) ? '内网' : '');
    const loc = a.country ? (a.country.replace(flagFromCountry(a.country) || '', '').trim()) : (isPrivateIP(a.ip) ? '内网' : '');
    const osText = [a.os, a.platform].filter(Boolean).join(' · ') || 'Linux';

    card.innerHTML = `
      <div class="card-header">
        <div class="card-title">
          <span class="flag">${flag}</span>
          <input class="card-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名">
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
      <div class="card-config">${escapeHtml(fmtConfig(a))}</div>
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
    `;
    grid.appendChild(card);
  }
  bindAliasInputs('.card-name');
}

function renderList() {
  const table = document.getElementById('agentsTable');
  const rows = state.agents.map(a => {
    const alias = a.alias || a.hostname || a.uuid.slice(0, 8);
    const flag = flagFromCountry(a.country) || '🌍';
    const code = a.country_code || (isPrivateIP(a.ip) ? '内网' : '');
    const loc = a.country ? (a.country.replace(flagFromCountry(a.country) || '', '').trim()) : (isPrivateIP(a.ip) ? '内网' : '');
    return `
      <tr>
        <td><span class="dot ${a.online ? 'on' : 'off'}"></span> <span class="status-text ${a.online ? 'on' : 'off'}">${a.online ? '在线' : '离线'}</span></td>
        <td><input class="list-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名"></td>
        <td><span class="flag">${flag}</span>${escapeHtml(loc)} ${code ? '(' + escapeHtml(code) + ')' : ''}<br><span style="color:var(--text-2);font-size:12px">${escapeHtml(a.ip)}</span></td>
        <td>${escapeHtml(fmtConfig(a))}</td>
        <td>${fmtUptime(a.uptime)}</td>
        <td>
          <div>CPU ${a.cpu.toFixed(1)}% <span class="mini-bar"><div class="bar-cpu" style="width:${percent(a.cpu, 100)}%"></div></span></div>
          <div style="margin-top:4px">内存 ${percent(a.mem_used, a.mem_total).toFixed(1)}% <span class="mini-bar"><div class="bar-mem" style="width:${percent(a.mem_used, a.mem_total)}%"></div></span></div>
          <div style="margin-top:4px">磁盘 ${percent(a.disk_used, a.disk_total).toFixed(1)}% <span class="mini-bar"><div class="bar-disk" style="width:${percent(a.disk_used, a.disk_total)}%"></div></span></div>
        </td>
        <td><span class="down">↓${fmtRate(a.rx_rate)}</span><br><span class="up">↑${fmtRate(a.tx_rate)}</span></td>
        <td><span class="down">${fmtBytes(a.rx_month)}</span><br><span class="up">${fmtBytes(a.tx_month)}</span></td>
        <td><button class="btn-chart" data-uuid="${a.uuid}">流量</button></td>
      </tr>
    `;
  }).join('');

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

checkLogin();
