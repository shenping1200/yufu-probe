'use strict';

const state = {
  agents: [],
  // 已注册的自定义分组名（含 0 成员空组）。由 WS 广播或 /api/groups 填充。
  groups: [],
  history: {},
  ws: null,
  loggedIn: false,
  currentUUID: null,
  theme: localStorage.getItem('probe-theme') || 'dark',
  viewMode: localStorage.getItem('probe-view') || 'card',
  // IP 模糊显示开关（仅本浏览器，localStorage 持久化）
  maskIP: localStorage.getItem('yufu_mask_ip') === '1',
  // 当前选中的分组筛选（'' = 全部，'⚠ 离线' = 离线，否则为自定义组名）
  currentGroup: '',
  // 分页：每页显示数量（数字 或 'all'）与当前页（1 基）；选择持久化到 localStorage
  pageSize: (localStorage.getItem('yufu_page_size') === 'all' ? 'all' : (parseInt(localStorage.getItem('yufu_page_size'), 10) || 'all')),
  page: 1,
  // 折叠世界地图开关（仅本浏览器，localStorage 持久化，默认关）
  mapOpen: localStorage.getItem('yufu_map_open') === '1',
  // 角色：admin（管理员） / visitor（访客只读）。由 /api/me 返回。
  role: 'admin',
  // 批量多选：选中的客户端 uuid 集合；切换分组/筛选时会清理
  selected: new Set(),
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
}
applyViewToggle();

// 机器区滚动容器（卡片/列表共用），承载 2000+ 客户端虚拟滚动
const agentsScrollEl = document.getElementById('agentsScroll');

document.getElementById('viewToggle').onclick = (e) => {
  if (e.target.tagName !== 'BUTTON') return;
  state.viewMode = e.target.dataset.mode;
  localStorage.setItem('probe-view', state.viewMode);
  applyViewToggle();
  agentsScrollEl.scrollTop = 0; // 切换视图回到顶部，避免残留滚动位置
  render();
};

// 虚拟滚动阈值：超过此数量才启用「只渲染可视区」的差量渲染，否则走原整表逻辑（更稳、零回归）
const VIRTUAL_THRESHOLD = 300;

// 滚动事件经 rAF 节流后只重算可视窗口（不重建整张表/网格），保证 2000+ 客户端滚动顺滑
let scrollScheduled = false;
agentsScrollEl.addEventListener('scroll', () => {
  if (scrollScheduled) return;
  scrollScheduled = true;
  requestAnimationFrame(() => {
    scrollScheduled = false;
    const list = filteredAgents();
    if (list.length <= VIRTUAL_THRESHOLD) return; // 非虚拟模式无需响应
    if (state.viewMode === 'list') renderListWindow(agentsScrollEl.scrollTop);
    else renderCardWindow(agentsScrollEl.scrollTop);
  });
});

// 窗口尺寸变化（主要影响卡片列数）时，整表重算虚拟窗口
let resizeScheduled = false;
window.addEventListener('resize', () => {
  if (resizeScheduled) return;
  resizeScheduled = true;
  requestAnimationFrame(() => {
    resizeScheduled = false;
    const list = filteredAgents();
    if (list.length <= VIRTUAL_THRESHOLD) return;
    if (state.viewMode === 'card') renderCard(); // 列数可能变化，整体重算
  });
});

// ---------- 登录态 ----------
async function checkLogin() {
  try {
    const r = await fetch('/api/me');
    if (r.ok) {
      const d = await r.json();
      showApp(d.username, d.role || 'admin');
      connectWS();
    } else {
      showLogin();
    }
  } catch (e) {
    showLogin();
  }
}

function showApp(user, role) {
  document.getElementById('login').classList.add('hidden');
  document.getElementById('app').classList.remove('hidden');
  document.getElementById('user').textContent = user + (role === 'visitor' ? '（访客）' : '');
  state.loggedIn = true;
  state.role = role || 'admin';
  applyRoleGating();
}
function showLogin() {
  document.getElementById('app').classList.add('hidden');
  document.getElementById('login').classList.remove('hidden');
  state.loggedIn = false;
}

// ---------- 角色权限门控（admin 全功能 / visitor 只读） ----------
function isVisitor() { return state.role === 'visitor'; }

// 隐藏/禁用访客模式下不可用的入口；每行按钮（编辑/SSH）在 render() 后重新打一次
function applyRoleGating() {
  const v = isVisitor();
  const hide = id => { const e = document.getElementById(id); if (e) e.style.display = v ? 'none' : ''; };
  // 顶部按钮
  hide('installCmdBtn');     // 生成安装命令
  hide('visitorLinkBtn');    // 签发访客链接（只有管理员能给访客开门）
  hide('stressBtn');         // 压力测试入口
  hide('logoutBtn');         // 访客就别登出了，关页面即可
  hide('maskIpBtn');         // IP 模糊开关（访客 IP 已强制隐藏，此开关无意义）
  // 分组行：+ 新建分组 按钮
  hide('newGroupBtn');
  document.querySelectorAll('.sel-only, .del-only, #batchBar').forEach(e => { if (v) e.style.display = 'none'; else e.style.display = ''; });
  // 每行动作按钮
  document.querySelectorAll('.btn-edit, .btn-ssh').forEach(b => { if (v) b.style.display = 'none'; });
  // 别名/名称内联编辑（只读）
  document.querySelectorAll('.list-name, .card-name').forEach(inp => {
    if (v) { inp.setAttribute('readonly', 'readonly'); inp.style.cursor = 'default'; }
    else { inp.removeAttribute('readonly'); inp.style.cursor = ''; }
  });
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

// 自动部署规则入口（仅管理员，访客在 openDeployRules 内拦截）
document.getElementById('deployBtn').onclick = openDeployRules;

// 最近一次已应用的 agents 广播序列号；小于它的帧视为过期直接丢弃，
// 避免后端发送缓冲里积压的旧快照覆盖掉前端乐观更新（详见 hub.go 的 drop-oldest 修复）。
let lastAgentsSeq = 0;

// 分组乐观覆盖锁：批量/单台改分组成功后，在收到服务端确认（seq>floor 且 group 一致）前，
// 任何陈旧广播帧都改不回它的分组，彻底消除「改完分组机器跳回原组」的回跳（seq 去重的兜底）。
const groupOverrides = {};
function pinGroupOverrides(uuids, group) {
  const floor = lastAgentsSeq;
  const expires = Date.now() + 15000;
  for (const u of uuids) groupOverrides[u] = { group, floor, expires };
}

// ---------- WebSocket ----------
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(proto + '://' + location.host + '/ws/viewer');
  state.ws = ws;
  ws.onopen = () => { lastAgentsSeq = 0; };
  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'agents') {
      // 序列号去重：只接受比已应用帧更新的快照，过期/重复帧直接忽略，
      // 彻底消除「改完分组后被旧快照打回原组」的回跳。
      if (typeof msg.seq === 'number' && msg.seq <= lastAgentsSeq) return;
      lastAgentsSeq = msg.seq;
      // 应用分组乐观覆盖锁：服务端未确认前保持本地分组，陈旧帧打不回原组
      for (const a of msg.data) {
        const ov = groupOverrides[a.uuid];
        if (!ov) continue;
        if (Date.now() > ov.expires || (msg.seq > ov.floor && a.group === ov.group)) {
          delete groupOverrides[a.uuid];
        } else {
          a.group = ov.group;
        }
      }
      state.agents = msg.data;
      if (Array.isArray(msg.groups)) state.groups = msg.groups;
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
// 容量显示（GB 入参，<1GB 自动切 MB 避免小内存被舍成 0；
// 4 → "4 GB"，17.53 → "17.5 GB"，0.96 → "956 MB"，0.13 → "128 MB"）
function fmtSize(v) {
  if (v == null || v < 0) return '-';
  if (v < 1) return Math.round(v * 1024) + ' MB';
  return parseFloat(v.toFixed(1)) + ' GB';
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
// 从 "🇸🇬 Singapore" 这类串里剥离开头的国旗 emoji，只返回国家名
function stripFlagEmoji(country) {
  if (!country) return '';
  const m = country.match(/^[\u{1F1E6}-\u{1F1FF}]{2}\uFE0F?/u);
  return m ? m[0] : '';
}
// 发行版 → 自托管 SVG 图标（Linux 专用，零外链）。platform 来自 gopsutil（如 "Ubuntu"/"CentOS Linux"/"Oracle Linux Server"）
const DISTRO_MAP = [
  [/ubuntu/i, 'ubuntu'],
  [/debian/i, 'debian'],
  [/cent\s?os/i, 'centos'],
  [/red\s*hat|rhel/i, 'rhel'],
  [/fedora/i, 'fedora'],
  [/arch/i, 'archlinux'],
  [/alpine/i, 'alpine'],
  [/opensuse|suse/i, 'opensuse'],
  [/mint/i, 'linuxmint'],
  [/kali/i, 'kali'],
  [/rocky/i, 'rocky'],
  [/gentoo/i, 'gentoo'],
  [/oracle/i, 'oracle'],
  [/alibaba|anolis|aliyun/i, 'alibabacloud'],
];
// 发行版官方 logo 的内联 SVG path（自托管，零外链）。用 fill="currentColor" 跟随主题文字色，
// 因此深色/浅色主题都能清晰显示（<img> 嵌入无法继承 currentColor，故内联）。
const DISTRO_SVG = {
  "alpine": "<path d=\"M5.998 1.607L0 12l5.998 10.393h12.004L24 12 18.002 1.607H5.998zM9.965 7.12L12.66 9.9l1.598 1.595.002-.002 2.41 2.363c-.2.14-.386.252-.563.344a3.756 3.756 0 01-.496.217 2.702 2.702 0 01-.425.111c-.131.023-.25.034-.358.034-.13 0-.242-.014-.338-.034a1.317 1.317 0 01-.24-.072.95.95 0 01-.2-.113l-1.062-1.092-3.039-3.041-1.1 1.053-3.07 3.072a.974.974 0 01-.2.111 1.274 1.274 0 01-.237.073c-.096.02-.209.033-.338.033-.108 0-.227-.009-.358-.031a2.7 2.7 0 01-.425-.114 3.748 3.748 0 01-.496-.217 5.228 5.228 0 01-.563-.343l6.803-6.727zm4.72.785l4.579 4.598 1.382 1.353a5.24 5.24 0 01-.564.344 3.73 3.73 0 01-.494.217 2.697 2.697 0 01-.426.111c-.13.023-.251.034-.36.034-.129 0-.241-.014-.337-.034a1.285 1.285 0 01-.385-.146c-.033-.02-.05-.036-.053-.04l-1.232-1.218-2.111-2.111-.334.334L12.79 9.8l1.896-1.897zm-5.966 4.12v2.529a2.128 2.128 0 01-.356-.035 2.765 2.765 0 01-.422-.116 3.708 3.708 0 01-.488-.214 5.217 5.217 0 01-.555-.34l1.82-1.825Z\"/>",
  "archlinux": "<path d=\"M11.39.605C10.376 3.092 9.764 4.72 8.635 7.132c.693.734 1.543 1.589 2.923 2.554-1.484-.61-2.496-1.224-3.252-1.86C6.86 10.842 4.596 15.138 0 23.395c3.612-2.085 6.412-3.37 9.021-3.862a6.61 6.61 0 01-.171-1.547l.003-.115c.058-2.315 1.261-4.095 2.687-3.973 1.426.12 2.534 2.096 2.478 4.409a6.52 6.52 0 01-.146 1.243c2.58.505 5.352 1.787 8.914 3.844-.702-1.293-1.33-2.459-1.929-3.57-.943-.73-1.926-1.682-3.933-2.713 1.38.359 2.367.772 3.137 1.234-6.09-11.334-6.582-12.84-8.67-17.74zM22.898 21.36v-.623h-.234v-.084h.562v.084h-.234v.623h.331v-.707h.142l.167.5.034.107a2.26 2.26 0 01.038-.114l.17-.493H24v.707h-.091v-.593l-.206.593h-.084l-.205-.602v.602h-.091\"/>",
  "centos": "<path d=\"M12.076.066L8.883 3.28H3.348v5.434L0 12.01l3.349 3.298v5.39h5.374l3.285 3.236 3.285-3.236h5.43v-5.374L24 12.026l-3.232-3.252V3.321H15.31zm0 .749l2.49 2.506h-1.69v6.441l-.8.805-.81-.815V3.28H9.627zm-8.2 2.991h4.483L6.485 5.692l4.253 4.279v.654H9.94L5.674 6.423l-1.798 1.77zm5.227 0h1.635v5.415l-3.509-3.53zm4.302.043h1.687l1.83 1.842-3.517 3.539zm2.431 0h4.404v4.394l-1.83-1.842-4.241 4.267h-.764v-.69l4.261-4.287zm2.574 3.3l1.83 1.843v1.676h-5.327zm-12.735.013l3.515 3.462H3.876v-1.69zM3.348 9.454v1.697h6.377l.871.858-.782.77H3.35v1.786L.753 12.01zm17.42.068l2.488 2.503-2.533 2.55v-1.796h-6.41l-.75-.754.825-.83h6.38zm-9.502.978l.81.815.186-.188.614-.618v.686h.768l-.825.83.75.754h-.719v.808l-.842-.83-.741.73v-.707h-.7l.781-.77-.188-.186-.682-.672h.788zm-7.39 2.807h5.402l-3.603 3.55-1.798-1.772zm6.154 0h.708v.7l-4.404 4.338 1.852 1.824h-4.31v-4.342l1.798 1.77zm3.348 0h.715l4.317 4.343.186-.187 1.599-1.61v4.316h-4.366l1.853-1.825-.188-.185-4.116-4.054zm1.46 0h5.357v1.798l-1.785 1.796zm-2.83.191l.842.829v6.37h1.691l-2.532 2.495-2.533-2.495h1.79V14.23zm-1.27 1.251v5.42H8.939l-1.852-1.823zm2.64.097l3.552 3.499-1.853 1.825h-1.7z\"/>",
  "debian": "<path d=\"M13.88 12.685c-.4 0 .08.2.601.28.14-.1.27-.22.39-.33a3.001 3.001 0 01-.99.05m2.14-.53c.23-.33.4-.69.47-1.06-.06.27-.2.5-.33.73-.75.47-.07-.27 0-.56-.8 1.01-.11.6-.14.89m.781-2.05c.05-.721-.14-.501-.2-.221.07.04.13.5.2.22M12.38.31c.2.04.45.07.42.12.23-.05.28-.1-.43-.12m.43.12l-.15.03.14-.01V.43m6.633 9.944c.02.64-.2.95-.38 1.5l-.35.181c-.28.54.03.35-.17.78-.44.39-1.34 1.22-1.62 1.301-.201 0 .14-.25.19-.34-.591.4-.481.6-1.371.85l-.03-.06c-2.221 1.04-5.303-1.02-5.253-3.842-.03.17-.07.13-.12.2a3.551 3.552 0 012.001-3.501 3.361 3.362 0 013.732.48 3.341 3.342 0 00-2.721-1.3c-1.18.01-2.281.76-2.651 1.57-.6.38-.67 1.47-.93 1.661-.361 2.601.66 3.722 2.38 5.042.27.19.08.21.12.35a4.702 4.702 0 01-1.53-1.16c.23.33.47.66.8.91-.55-.18-1.27-1.3-1.48-1.35.93 1.66 3.78 2.921 5.261 2.3a6.203 6.203 0 01-2.33-.28c-.33-.16-.77-.51-.7-.57a5.802 5.803 0 005.902-.84c.44-.35.93-.94 1.07-.95-.2.32.04.16-.12.44.44-.72-.2-.3.46-1.24l.24.33c-.09-.6.74-1.321.66-2.262.19-.3.2.3 0 .97.29-.74.08-.85.15-1.46.08.2.18.42.23.63-.18-.7.2-1.2.28-1.6-.09-.05-.28.3-.32-.53 0-.37.1-.2.14-.28-.08-.05-.26-.32-.38-.861.08-.13.22.33.34.34-.08-.42-.2-.75-.2-1.08-.34-.68-.12.1-.4-.3-.34-1.091.3-.25.34-.74.54.77.84 1.96.981 2.46-.1-.6-.28-1.2-.49-1.76.16.07-.26-1.241.21-.37A7.823 7.824 0 0017.702 1.6c.18.17.42.39.33.42-.75-.45-.62-.48-.73-.67-.61-.25-.65.02-1.06 0C15.082.73 14.862.8 13.8.4l.05.23c-.77-.25-.9.1-1.73 0-.05-.04.27-.14.53-.18-.741.1-.701-.14-1.431.03.17-.13.36-.21.55-.32-.6.04-1.44.35-1.18.07C9.6.68 7.847 1.3 6.867 2.22L6.838 2c-.45.54-1.96 1.611-2.08 2.311l-.131.03c-.23.4-.38.85-.57 1.261-.3.52-.45.2-.4.28-.6 1.22-.9 2.251-1.16 3.102.18.27 0 1.65.07 2.76-.3 5.463 3.84 10.776 8.363 12.006.67.23 1.65.23 2.49.25-.99-.28-1.12-.15-2.08-.49-.7-.32-.85-.7-1.34-1.13l.2.35c-.971-.34-.57-.42-1.361-.67l.21-.27c-.31-.03-.83-.53-.97-.81l-.34.01c-.41-.501-.63-.871-.61-1.161l-.111.2c-.13-.21-1.52-1.901-.8-1.511-.13-.12-.31-.2-.5-.55l.14-.17c-.35-.44-.64-1.02-.62-1.2.2.24.32.3.45.33-.88-2.172-.93-.12-1.601-2.202l.15-.02c-.1-.16-.18-.34-.26-.51l.06-.6c-.63-.74-.18-3.102-.09-4.402.07-.54.53-1.1.88-1.981l-.21-.04c.4-.71 2.341-2.872 3.241-2.761.43-.55-.09 0-.18-.14.96-.991 1.26-.7 1.901-.88.7-.401-.6.16-.27-.151 1.2-.3.85-.7 2.421-.85.16.1-.39.14-.52.26 1-.49 3.151-.37 4.562.27 1.63.77 3.461 3.011 3.531 5.132l.08.02c-.04.85.13 1.821-.17 2.711l.2-.42M9.54 13.236l-.05.28c.26.35.47.73.8 1.01-.24-.47-.42-.66-.75-1.3m.62-.02c-.14-.15-.22-.34-.31-.52.08.32.26.6.43.88l-.12-.36m10.945-2.382l-.07.15c-.1.76-.34 1.511-.69 2.212.4-.73.65-1.541.75-2.362M12.45.12c.27-.1.66-.05.95-.12-.37.03-.74.05-1.1.1l.15.02M3.006 5.142c.07.57-.43.8.11.42.3-.66-.11-.18-.1-.42m-.64 2.661c.12-.39.15-.62.2-.84-.35.44-.17.53-.2.83\"/>",
  "fedora": "<path d=\"M12.001 0C5.376 0 .008 5.369.004 11.992H.002v9.287h.002A2.726 2.726 0 0 0 2.73 24h9.275c6.626-.004 11.993-5.372 11.993-11.997C23.998 5.375 18.628 0 12 0zm2.431 4.94c2.015 0 3.917 1.543 3.917 3.671 0 .197.001.395-.03.619a1.002 1.002 0 0 1-1.137.893 1.002 1.002 0 0 1-.842-1.175 2.61 2.61 0 0 0 .013-.337c0-1.207-.987-1.672-1.92-1.672-.934 0-1.775.784-1.777 1.672.016 1.027 0 2.046 0 3.07l1.732-.012c1.352-.028 1.368 2.009.016 1.998l-1.748.013c-.004.826.006.677.002 1.093 0 0 .015 1.01-.016 1.776-.209 2.25-2.124 4.046-4.424 4.046-2.438 0-4.448-1.993-4.448-4.437.073-2.515 2.078-4.492 4.603-4.469l1.409-.01v1.996l-1.409.013h-.007c-1.388.04-2.577.984-2.6 2.47a2.438 2.438 0 0 0 2.452 2.439c1.356 0 2.441-.987 2.441-2.437l-.001-7.557c0-.14.005-.252.02-.407.23-1.848 1.883-3.256 3.754-3.256z\"/>",
  "gentoo": "<path d=\"M9.94 0a7.31 7.31 0 00-1.26.116c-4.344.795-7.4 4.555-7.661 7.031-.126 1.215.53 2.125.89 2.526.977 1.085 2.924 1.914 4.175 2.601-1.81 1.543-2.64 2.296-3.457 3.154C1.403 16.712.543 18.125.54 19.138c0 .325-.053 1.365.371 2.187.16.309.613 1.338 1.98 2.109.874.494 2.119.675 3.337.501 3.772-.538 8.823-3.737 12.427-6.716 2.297-1.9 3.977-3.739 4.462-4.644.39-.731.434-2.043.207-2.866-.645-2.337-5.887-7.125-10.172-9.051A7.824 7.824 0 009.94 0zm-.008.068a7.4 7.4 0 013.344.755c3.46 1.7 9.308 6.482 9.739 8.886.534 2.972-9.931 11.017-16.297 12.272-2.47.485-4.576.618-5.537-1.99-.832-2.262.783-3.916 3.16-6.09a92.546 92.546 0 012.96-2.576c.065-.069-5.706-2.059-5.89-4.343C1.221 4.634 4.938.3 9.697.076c.08-.004.157-.007.235-.008zm-.112.52a5.647 5.647 0 00-.506.032c-2.337.245-2.785.547-4.903 2.149-.71.537-2.016 1.844-2.35 3.393-.128.59.024 1.1.448 1.458 1.36 1.144 3.639 2.072 5.509 2.97.547.263.185.74-.698 1.505-2.227 1.928-5.24 4.276-5.45 6.066-.099.842.19 1.988 1.213 2.574 1.195.685 3.676.238 5.333-.379 2.422-.902 5.602-2.892 8.127-4.848 2.625-2.034 5.067-4.617 5.188-5.038.148-.517.133-.996-.154-1.546-.448-.862-1.049-1.503-1.694-2.22-1.732-1.825-3.563-3.43-5.754-4.658C12.694 1.242 11.417.564 9.82.588zm1.075 3.623c.546 0 1.176.173 1.853.5 1.688.817 3.422 2.961-.015 4.195-.935.336-3.9-.824-3.81-2.407.09-1.57.854-2.289 1.972-2.288zm.285 1.367c-.317-.002-.575.079-.694.263-.557.861-.303 1.472.212 1.862.192-.457 2.156.043 2.148.472a.32.32 0 00.055-.032c1.704-1.282-.472-2.557-1.72-2.565z\"/>",
  "kali": "<path d=\"M12.778 5.943s-1.97-.13-5.327.92c-3.42 1.07-5.36 2.587-5.36 2.587s5.098-2.847 10.852-3.008zm7.351 3.095l.257-.017s-1.468-1.78-4.278-2.648c1.58.642 2.954 1.493 4.021 2.665zm.42.74c.039-.068.166.217.263.337.004.024.01.039-.045.027-.005-.025-.013-.032-.013-.032s-.135-.08-.177-.137c-.041-.057-.049-.157-.028-.195zm3.448 8.479s.312-3.578-5.31-4.403a18.277 18.277 0 0 0-2.524-.187c-4.506.06-4.67-5.197-1.275-5.462 1.407-.116 3.087.643 4.73 1.408-.007.204.002.385.136.552.134.168.648.35.813.445.164.094.691.43 1.014.85.07-.131.654-.512.654-.512s-.14.003-.465-.119c-.326-.122-.713-.49-.722-.511-.01-.022-.015-.055.06-.07.059-.049-.072-.207-.13-.265-.058-.058-.445-.716-.454-.73-.009-.016-.012-.031-.04-.05-.085-.027-.46.04-.46.04s-.575-.283-.774-.893c.003.107-.099.224 0 .469-.3-.127-.558-.344-.762-.88-.12.305 0 .499 0 .499s-.707-.198-.82-.85c-.124.293 0 .469 0 .469s-1.153-.602-3.069-.61c-1.283-.118-1.55-2.374-1.43-2.754 0 0-1.85-.975-5.493-1.406-3.642-.43-6.628-.065-6.628-.065s6.45-.31 11.617 1.783c.176.785.704 2.094.989 2.723-.815.563-1.733 1.092-1.876 2.97-.143 1.878 1.472 3.53 3.474 3.58 1.9.102 3.214.116 4.806.942 1.52.84 2.766 3.4 2.89 5.703.132-1.709-.509-5.383-3.5-6.498 4.181.732 4.549 3.832 4.549 3.832zM12.68 5.663l-.15-.485s-2.484-.441-5.822-.204C3.37 5.211 0 6.38 0 6.38s6.896-1.735 12.68-.717Z\"/>",
  "oracle": "<path d=\"M16.412 4.412h-8.82a7.588 7.588 0 0 0-.008 15.176h8.828a7.588 7.588 0 0 0 0-15.176zm-.193 12.502H7.786a4.915 4.915 0 0 1 0-9.828h8.433a4.914 4.914 0 1 1 0 9.828z\"/>",
  "alibabacloud": "<path d=\"M3.996 4.517h5.291L8.01 6.324 4.153 7.506a1.668 1.668 0 0 0-1.165 1.601v5.786a1.668 1.668 0 0 0 1.165 1.6l3.857 1.183 1.277 1.807H3.996A3.996 3.996 0 0 1 0 15.487V8.513a3.996 3.996 0 0 1 3.996-3.996m16.008 0h-5.291l1.277 1.807 3.857 1.182c.715.227 1.17.889 1.165 1.601v5.786a1.668 1.668 0 0 1-1.165 1.6l-3.857 1.183-1.277 1.807h5.291A3.996 3.996 0 0 0 24 15.487V8.513a3.996 3.996 0 0 0-3.996-3.996m-4.007 8.345H8.002v-1.804h7.995Z\"/>",
  "linux": "<path d=\"M12.504 0c-.155 0-.315.008-.48.021-4.226.333-3.105 4.807-3.17 6.298-.076 1.092-.3 1.953-1.05 3.02-.885 1.051-2.127 2.75-2.716 4.521-.278.832-.41 1.684-.287 2.489a.424.424 0 00-.11.135c-.26.268-.45.6-.663.839-.199.199-.485.267-.797.4-.313.136-.658.269-.864.68-.09.189-.136.394-.132.602 0 .199.027.4.055.536.058.399.116.728.04.97-.249.68-.28 1.145-.106 1.484.174.334.535.47.94.601.81.2 1.91.135 2.774.6.926.466 1.866.67 2.616.47.526-.116.97-.464 1.208-.946.587-.003 1.23-.269 2.26-.334.699-.058 1.574.267 2.577.2.025.134.063.198.114.333l.003.003c.391.778 1.113 1.132 1.884 1.071.771-.06 1.592-.536 2.257-1.306.631-.765 1.683-1.084 2.378-1.503.348-.199.629-.469.649-.853.023-.4-.2-.811-.714-1.376v-.097l-.003-.003c-.17-.2-.25-.535-.338-.926-.085-.401-.182-.786-.492-1.046h-.003c-.059-.054-.123-.067-.188-.135a.357.357 0 00-.19-.064c.431-1.278.264-2.55-.173-3.694-.533-1.41-1.465-2.638-2.175-3.483-.796-1.005-1.576-1.957-1.56-3.368.026-2.152.236-6.133-3.544-6.139zm.529 3.405h.013c.213 0 .396.062.584.198.19.135.33.332.438.533.105.259.158.459.166.724 0-.02.006-.04.006-.06v.105a.086.086 0 01-.004-.021l-.004-.024a1.807 1.807 0 01-.15.706.953.953 0 01-.213.335.71.71 0 00-.088-.042c-.104-.045-.198-.064-.284-.133a1.312 1.312 0 00-.22-.066c.05-.06.146-.133.183-.198.053-.128.082-.264.088-.402v-.02a1.21 1.21 0 00-.061-.4c-.045-.134-.101-.2-.183-.333-.084-.066-.167-.132-.267-.132h-.016c-.093 0-.176.03-.262.132a.8.8 0 00-.205.334 1.18 1.18 0 00-.09.4v.019c.002.089.008.179.02.267-.193-.067-.438-.135-.607-.202a1.635 1.635 0 01-.018-.2v-.02a1.772 1.772 0 01.15-.768c.082-.22.232-.406.43-.533a.985.985 0 01.594-.2zm-2.962.059h.036c.142 0 .27.048.399.135.146.129.264.288.344.465.09.199.14.4.153.667v.004c.007.134.006.2-.002.266v.08c-.03.007-.056.018-.083.024-.152.055-.274.135-.393.2.012-.09.013-.18.003-.267v-.015c-.012-.133-.04-.2-.082-.333a.613.613 0 00-.166-.267.248.248 0 00-.183-.064h-.021c-.071.006-.13.04-.186.132a.552.552 0 00-.12.27.944.944 0 00-.023.33v.015c.012.135.037.2.08.334.046.134.098.2.166.268.01.009.02.018.034.024-.07.057-.117.07-.176.136a.304.304 0 01-.131.068 2.62 2.62 0 01-.275-.402 1.772 1.772 0 01-.155-.667 1.759 1.759 0 01.08-.668 1.43 1.43 0 01.283-.535c.128-.133.26-.2.418-.2zm1.37 1.706c.332 0 .733.065 1.216.399.293.2.523.269 1.052.468h.003c.255.136.405.266.478.399v-.131a.571.571 0 01.016.47c-.123.31-.516.643-1.063.842v.002c-.268.135-.501.333-.775.465-.276.135-.588.292-1.012.267a1.139 1.139 0 01-.448-.067 3.566 3.566 0 01-.322-.198c-.195-.135-.363-.332-.612-.465v-.005h-.005c-.4-.246-.616-.512-.686-.71-.07-.268-.005-.47.193-.6.224-.135.38-.271.483-.336.104-.074.143-.102.176-.131h.002v-.003c.169-.202.436-.47.839-.601.139-.036.294-.065.466-.065zm2.8 2.142c.358 1.417 1.196 3.475 1.735 4.473.286.534.855 1.659 1.102 3.024.156-.005.33.018.513.064.646-1.671-.546-3.467-1.089-3.966-.22-.2-.232-.335-.123-.335.59.534 1.365 1.572 1.646 2.757.13.535.16 1.104.021 1.67.067.028.135.06.205.067 1.032.534 1.413.938 1.23 1.537v-.043c-.06-.003-.12 0-.18 0h-.016c.151-.467-.182-.825-1.065-1.224-.915-.4-1.646-.336-1.77.465-.008.043-.013.066-.018.135-.068.023-.139.053-.209.064-.43.268-.662.669-.793 1.187-.13.533-.17 1.156-.205 1.869v.003c-.02.334-.17.838-.319 1.35-1.5 1.072-3.58 1.538-5.348.334a2.645 2.645 0 00-.402-.533 1.45 1.45 0 00-.275-.333c.182 0 .338-.03.465-.067a.615.615 0 00.314-.334c.108-.267 0-.697-.345-1.163-.345-.467-.931-.995-1.788-1.521-.63-.4-.986-.87-1.15-1.396-.165-.534-.143-1.085-.015-1.645.245-1.07.873-2.11 1.274-2.763.107-.065.037.135-.408.974-.396.751-1.14 2.497-.122 3.854a8.123 8.123 0 01.647-2.876c.564-1.278 1.743-3.504 1.836-5.268.048.036.217.135.289.202.218.133.38.333.59.465.21.201.477.335.876.335.039.003.075.006.11.006.412 0 .73-.134.997-.268.29-.134.52-.334.74-.4h.005c.467-.135.835-.402 1.044-.7zm2.185 8.958c.037.6.343 1.245.882 1.377.588.134 1.434-.333 1.791-.765l.211-.01c.315-.007.577.01.847.268l.003.003c.208.199.305.53.391.876.085.4.154.78.409 1.066.486.527.645.906.636 1.14l.003-.007v.018l-.003-.012c-.015.262-.185.396-.498.595-.63.401-1.746.712-2.457 1.57-.618.737-1.37 1.14-2.036 1.191-.664.053-1.237-.2-1.574-.898l-.005-.003c-.21-.4-.12-1.025.056-1.69.176-.668.428-1.344.463-1.897.037-.714.076-1.335.195-1.814.12-.465.308-.797.641-.984l.045-.022zm-10.814.049h.01c.053 0 .105.005.157.014.376.055.706.333 1.023.752l.91 1.664.003.003c.243.533.754 1.064 1.189 1.637.434.598.77 1.131.729 1.57v.006c-.057.744-.48 1.148-1.125 1.294-.645.135-1.52.002-2.395-.464-.968-.536-2.118-.469-2.857-.602-.369-.066-.61-.2-.723-.4-.11-.2-.113-.602.123-1.23v-.004l.002-.003c.117-.334.03-.752-.027-1.118-.055-.401-.083-.71.043-.94.16-.334.396-.4.69-.533.294-.135.64-.202.915-.47h.002v-.002c.256-.268.445-.601.668-.838.19-.201.38-.336.663-.336zm7.159-9.074c-.435.201-.945.535-1.488.535-.542 0-.97-.267-1.28-.466-.154-.134-.28-.268-.373-.335-.164-.134-.144-.333-.074-.333.109.016.129.134.199.2.096.066.215.2.36.333.292.2.68.467 1.167.467.485 0 1.053-.267 1.398-.466.195-.135.445-.334.648-.467.156-.136.149-.267.279-.267.128.016.034.134-.147.332a8.097 8.097 0 01-.69.468zm-1.082-1.583V5.64c-.006-.02.013-.042.029-.05.074-.043.18-.027.26.004.063 0 .16.067.15.135-.006.049-.085.066-.135.066-.055 0-.092-.043-.141-.068-.052-.018-.146-.008-.163-.065zm-.551 0c-.02.058-.113.049-.166.066-.047.025-.086.068-.14.068-.05 0-.13-.02-.136-.068-.01-.066.088-.133.15-.133.08-.031.184-.047.259-.005.019.009.036.03.03.05v.02h.003z\"/>",
  "linuxmint": "<path d=\"M5.438 5.906v8.438c0 2.06 1.69 3.75 3.75 3.75h5.625c2.06 0 3.75-1.69 3.75-3.75V9.656a2.827 2.827 0 0 0-2.813-2.812 2.8 2.8 0 0 0-1.875.737A2.8 2.8 0 0 0 12 6.844a2.827 2.827 0 0 0-2.812 2.812v4.688h1.875V9.656c0-.529.408-.937.937-.937s.938.408.938.937v4.688h1.875V9.656c0-.529.408-.937.937-.937s.938.408.938.937v4.688a1.86 1.86 0 0 1-1.875 1.875H9.188a1.86 1.86 0 0 1-1.875-1.875V5.906ZM12 0C5.384 0 0 5.384 0 12s5.384 12 12 12 12-5.384 12-12S18.616 0 12 0m0 1.875A10.11 10.11 0 0 1 22.125 12 10.11 10.11 0 0 1 12 22.125 10.11 10.11 0 0 1 1.875 12 10.11 10.11 0 0 1 12 1.875\"/>",
  "opensuse": "<path d=\"M10.724 0a12 12 0 0 0-9.448 4.623c1.464.391 2.5.727 2.81.832.005-.19.037-1.893.037-1.893s.004-.04.025-.06c.026-.026.065-.018.065-.018.385.056 8.602 1.274 12.066 3.292.427.25.638.517.902.786.958.99 2.223 5.108 2.359 5.957.005.033-.036.07-.054.083a5.177 5.177 0 0 1-.313.228c-.82.55-2.708 1.872-5.13 1.656-2.176-.193-5.018-1.44-8.445-3.699.336.79.668 1.58 1 2.371.497.258 5.287 2.7 7.651 2.651 1.904-.04 3.941-.968 4.756-1.458 0 0 .179-.108.257-.048.085.066.061.167.041.27-.05.234-.164.66-.242.863l-.065.165c-.093.25-.183.482-.356.625-.48.436-1.246.784-2.446 1.305-1.855.812-4.865 1.328-7.66 1.31-1.001-.022-1.968-.133-2.817-.232-1.743-.197-3.161-.357-4.026.269A12 12 0 0 0 10.724 24a12 12 0 0 0 12-12 12 12 0 0 0-12-12zM13.4 6.963a3.503 3.503 0 0 0-2.521.942 3.498 3.498 0 0 0-1.114 2.449 3.528 3.528 0 0 0 3.39 3.64 3.48 3.48 0 0 0 2.524-.946 3.504 3.504 0 0 0 1.114-2.446 3.527 3.527 0 0 0-3.393-3.64zm-.03 1.035a2.458 2.458 0 0 1 2.368 2.539 2.43 2.43 0 0 1-.774 1.706 2.456 2.456 0 0 1-1.762.659 2.461 2.461 0 0 1-2.364-2.542c.02-.655.3-1.26.777-1.707a2.419 2.419 0 0 1 1.756-.655zm.402 1.23c-.602 0-1.087.325-1.087.727 0 .4.485.725 1.087.725.6 0 1.088-.326 1.088-.725 0-.402-.487-.726-1.088-.726Z\"/>",
  "rhel": "<path d=\"M16.009 13.386c1.577 0 3.86-.326 3.86-2.202a1.765 1.765 0 0 0-.04-.431l-.94-4.08c-.216-.898-.406-1.305-1.982-2.093-1.223-.625-3.888-1.658-4.676-1.658-.733 0-.947.946-1.822.946-.842 0-1.467-.706-2.255-.706-.757 0-1.25.515-1.63 1.576 0 0-1.06 2.99-1.197 3.424a.81.81 0 0 0-.028.245c0 1.162 4.577 4.974 10.71 4.974m4.101-1.435c.218 1.032.218 1.14.218 1.277 0 1.765-1.984 2.745-4.593 2.745-5.895.004-11.06-3.451-11.06-5.734a2.326 2.326 0 0 1 .19-.925C2.746 9.415 0 9.794 0 12.217c0 3.969 9.405 8.861 16.851 8.861 5.71 0 7.149-2.582 7.149-4.62 0-1.605-1.387-3.425-3.887-4.512\"/>",
  "rocky": "<path d=\"M23.332 15.957c.433-1.239.668-2.57.668-3.957 0-6.627-5.373-12-12-12S0 5.373 0 12c0 3.28 1.315 6.251 3.447 8.417L15.62 8.245l3.005 3.005zm-2.192 3.819l-5.52-5.52L6.975 22.9c1.528.706 3.23 1.1 5.025 1.1 3.661 0 6.94-1.64 9.14-4.224z\"/>",
  "ubuntu": "<path d=\"M17.61.455a3.41 3.41 0 0 0-3.41 3.41 3.41 3.41 0 0 0 3.41 3.41 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41zM12.92.8C8.923.777 5.137 2.941 3.148 6.451a4.5 4.5 0 0 1 .26-.007 4.92 4.92 0 0 1 2.585.737A8.316 8.316 0 0 1 12.688 3.6 4.944 4.944 0 0 1 13.723.834 11.008 11.008 0 0 0 12.92.8zm9.226 4.994a4.915 4.915 0 0 1-1.918 2.246 8.36 8.36 0 0 1-.273 8.303 4.89 4.89 0 0 1 1.632 2.54 11.156 11.156 0 0 0 .559-13.089zM3.41 7.932A3.41 3.41 0 0 0 0 11.342a3.41 3.41 0 0 0 3.41 3.409 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41zm2.027 7.866a4.908 4.908 0 0 1-2.915.358 11.1 11.1 0 0 0 7.991 6.698 11.234 11.234 0 0 0 2.422.249 4.879 4.879 0 0 1-.999-2.85 8.484 8.484 0 0 1-.836-.136 8.304 8.304 0 0 1-5.663-4.32zm11.405.928a3.41 3.41 0 0 0-3.41 3.41 3.41 3.41 0 0 0 3.41 3.41 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41z\"/>"
};

// 发行版官方品牌色（Simple Icons 收录的标准 HEX）。配上 logo 一眼能认出是哪个发行版。
const DISTRO_COLOR = {
  ubuntu: '#E95420',      // Ubuntu 橙
  debian: '#A81D33',      // Debian 红
  centos: '#262577',      // CentOS 紫
  rhel: '#EE0000',        // Red Hat 红
  fedora: '#294172',      // Fedora 蓝
  archlinux: '#1793D1',   // Arch 蓝
  alpine: '#0D597F',      // Alpine 深蓝
  opensuse: '#73BA25',    // openSUSE 绿
  linuxmint: '#86BE43',   // Linux Mint 绿
  kali: '#557C94',        // Kali 蓝灰
  rocky: '#10B981',       // Rocky 绿
  gentoo: '#54487A',      // Gentoo 紫
  oracle: '#C74634',      // Oracle 红
  alibabacloud: '#FF6A00',// 阿里云/Anolis 橙
};
// 返回发行版官方 logo（仅图标、无文字，内联 SVG）。
// 匹配到具体发行版用品牌色（避免单色全黑看不出是哪个发行版）；
// 回退到通用 Linux 时用 currentColor 跟随主题文字色（深/浅主题自适应）。
function distroIcon(platform, os, title) {
  // 把 platform 和 os 拼成一个 key：真实 agent 的 Platform 是发行版名（"ubuntu"）、
  // OS 是内核版本（"Linux 5.15..."）；压测引擎反过来 platform="linux"、os="Ubuntu 22.04"。
  // 两种来源拼接后都能被 DISTRO_MAP 正则命中；都拿不到具体信息时才回退通用企鹅。
  const key = ((platform || '') + ' ' + (os || '')).toLowerCase();
  let name = 'linux';
  for (const [re, svg] of DISTRO_MAP) {
    if (re.test(key)) { name = svg; break; }
  }
  const t = escapeHtml(title || platform || os || 'Linux');
  const inner = DISTRO_SVG[name] || DISTRO_SVG.linux;
  const color = name === 'linux' ? 'currentColor' : (DISTRO_COLOR[name] || 'currentColor');
  return `<svg class="distro-ico" viewBox="0 0 24 24" fill="${color}" role="img" aria-label="${t}" title="${t}">${inner}</svg>`;
}
// 返回国旗 <img>（按 ISO 3166-1 alpha-2，自托管 SVG，无外链）。无国家码时回退地球 emoji
function flagImage(code, title) {
  if (!code) return '<span class="flag flag-unknown" title="未知地区">🌍</span>';
  const cc = code.toLowerCase();
  const t = escapeHtml(title || code);
  return `<img class="flag-ico" src="/icons/flags/${cc}.svg" alt="${t}" title="${t}">`;
}
function isPrivateIP(ip) {
  return /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.)/.test(ip) || ip === '127.0.0.1' || ip === '::1';
}
// 模糊 IP（仅 IPv4）：保留第 1、4 段，中间两段打星，如 1.2.3.4 -> 1.*.*.4；IPv6/非标准原样返回
function maskIP(ip) {
  if (typeof ip !== 'string') return ip;
  const m = ip.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  if (!m) return ip;
  return m[1] + '.*.*.' + m[4];
}
// ipBlockHTML 双栈公网 IP 展示：v4 在上、v6 在下，有哪个显示哪个；
// 纯 v6 / 纯 v4 只显示一行；无公网 IP 时回退本机 IP 并标注"内网"。
// 访客模式：全局不显示 IP（v4/v6/内网 IP 全部隐藏）。
function ipBlockHTML(a) {
  if (isVisitor()) {
    return '<span class="ip-line ip-hidden">🔒 已隐藏</span>';
  }
  const fmt = ip => escapeHtml(state.maskIP ? maskIP(ip) : ip);
  const v4 = a.public_ip4 || '';
  const v6 = a.public_ip6 || '';
  if (v4 || v6) {
    return [v4, v6].filter(Boolean)
      .map(ip => `<span class="ip-line">${fmt(ip)}</span>`)
      .join('');
  }
  const lan = a.public_ip || a.ip;
  const note = a.public_ip ? '' : ' <span class="ip-note">(内网)</span>';
  return `<span class="ip-line">${fmt(lan)}${note}</span>`;
}
function percent(used, total) {
  if (!total) return 0;
  return Math.min(100, (used / total) * 100);
}
function fmtConfig(a) {
  const cores = a.cpu_count ? a.cpu_count + '核' : '-';
  const mem = a.mem_total ? fmtSize(a.mem_total) : '-';
  const disk = a.disk_total ? fmtSize(a.disk_total) : '-';
  return `${cores} / ${mem}内存 / ${disk}磁盘`;
}

// ---------- 渲染 ----------
function render() {
  renderGroupTabs();
  populateGroupSelect();
  renderSummary();
  renderPager();
  if (state.viewMode === 'list') {
    renderList();
  } else {
    renderCard();
  }
  if (state.mapOpen) renderMap();
  renderBatchBar();
  applyRoleGating(); // 访客模式下隐藏/禁用每行的编辑/SSH 按钮（行内容每次重渲染，需重新打）
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
  // 统计跟随当前分组筛选（复用 filteredAgents：''=全部, '⚠ 离线'=离线, 自定义组=该组）
  const list = filteredAgents();
  const total = list.length;
  const online = list.filter(a => a.online).length;
  const rxTotal = list.reduce((s, a) => s + (a.rx_month || 0), 0);
  const txTotal = list.reduce((s, a) => s + (a.tx_month || 0), 0);
  const rxRate = list.reduce((s, a) => s + (a.rx_rate || 0), 0);
  const txRate = list.reduce((s, a) => s + (a.tx_rate || 0), 0);

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
  if (g === '未分组') return state.agents.filter(a => a.online && !a.group);
  return state.agents.filter(a => a.online && a.group === g);
}

// 当前页要渲染的机器列表：在 filteredAgents() 基础上按 pageSize/page 切片。
// 统计（renderSummary）、全选（selectAll）仍基于 filteredAgents() 全量，不受分页影响。
function currentPageList() {
  const list = filteredAgents();
  const ps = state.pageSize;
  if (ps === 'all' || !ps || list.length <= ps) return list;
  const pageCount = Math.max(1, Math.ceil(list.length / ps));
  const page = Math.min(Math.max(1, state.page || 1), pageCount);
  const start = (page - 1) * ps;
  return list.slice(start, start + ps);
}

// 翻页工具条：每页数量（10/20/50/全部/自定义）+ 上一页/下一页。
// 用签名守卫，仅当 pageSize/page/total/pageCount 变化时才重建 DOM，
// 避免每条 WS 广播都重建、打断自定义输入框的焦点。
let lastPagerSig = '';
function renderPager() {
  const bar = document.getElementById('pagerBar');
  if (!bar) return;
  const list = filteredAgents();
  const total = list.length;
  const ps = state.pageSize;
  const pageCount = ps === 'all' ? 1 : Math.max(1, Math.ceil(total / ps));
  if (state.page > pageCount) state.page = pageCount;
  if (state.page < 1) state.page = 1;
  const sig = [ps, state.page, total, pageCount].join('|');
  if (sig === lastPagerSig) return;
  lastPagerSig = sig;
  const cur = state.page;
  const sizeBtn = (s) => {
    const label = s === 'all' ? '全部' : String(s);
    const active = ps === s ? ' active' : '';
    return `<button class="pg-size${active}" data-size="${s}">${label}</button>`;
  };
  const customVal = (ps !== 'all' && ![10, 20, 50].includes(ps)) ? ps : '';
  let html = `<div class="pg-sizes">每页：<span class="pg-size-wrap">${[10, 20, 50, 'all'].map(sizeBtn).join('')}</span><input class="pg-custom" type="number" min="1" placeholder="自定义" value="${customVal}"></div>`;
  if (ps !== 'all' && total > ps) {
    html += `<div class="pg-nav"><button class="pg-prev" ${cur <= 1 ? 'disabled' : ''}>上一页</button><span class="pg-info">第 ${cur} / ${pageCount} 页 · 共 ${total} 台</span><button class="pg-next" ${cur >= pageCount ? 'disabled' : ''}>下一页</button></div>`;
  } else {
    html += `<div class="pg-nav"><span class="pg-info">共 ${total} 台</span></div>`;
  }
  bar.innerHTML = html;
  bar.querySelectorAll('.pg-size').forEach(b => {
    b.onclick = () => {
      const s = b.dataset.size;
      state.pageSize = s === 'all' ? 'all' : Number(s);
      state.page = 1;
      agentsScrollEl.scrollTop = 0;
      localStorage.setItem('yufu_page_size', String(state.pageSize));
      lastPagerSig = '';
      requestRender();
    };
  });
  const custom = bar.querySelector('.pg-custom');
  if (custom) {
    custom.onchange = () => {
      const n = parseInt(custom.value, 10);
      if (n && n > 0) {
        state.pageSize = n;
        state.page = 1;
        agentsScrollEl.scrollTop = 0;
        localStorage.setItem('yufu_page_size', String(n));
        lastPagerSig = '';
        requestRender();
      } else { custom.value = ''; }
    };
    custom.onkeydown = (e) => { if (e.key === 'Enter') custom.blur(); };
  }
  const prev = bar.querySelector('.pg-prev');
  if (prev) prev.onclick = () => { if (state.page > 1) { state.page--; agentsScrollEl.scrollTop = 0; lastPagerSig = ''; requestRender(); } };
  const next = bar.querySelector('.pg-next');
  if (next) next.onclick = () => { if (state.page < pageCount) { state.page++; agentsScrollEl.scrollTop = 0; lastPagerSig = ''; requestRender(); } };
}

// 渲染顶部筛选标签条：全部(总数) / 已注册自定义组(数量) / 未分组 / ⚠ 离线(数量) / + 新建分组
function renderGroupTabs() {
  const el = document.getElementById('groupTabs');
  const total = state.agents.length;
  const offlineCount = state.agents.filter(a => !a.online).length;
  const ungroupedCount = state.agents.filter(a => a.online && !a.group).length;

  // 每个自定义组的"在线成员"计数
  const groupCounts = {};
  for (const a of state.agents) {
    if (a.online && a.group) {
      groupCounts[a.group] = (groupCounts[a.group] || 0) + 1;
    }
  }

  // 自定义组列表：以「注册表 state.groups」为权威，再把 agents 出现但未注册的补回来（容错）
  const set = new Set(state.groups);
  for (const a of state.agents) {
    if (a.group) set.add(a.group);
  }
  const groupNames = [...set].sort((x, y) => x.localeCompare(y, 'zh'));
  // 若当前选中的自定义组被改名/删除导致不再存在，临时保留以维持高亮
  if (state.currentGroup && state.currentGroup !== OFFLINE_GROUP && state.currentGroup !== '未分组' && !set.has(state.currentGroup)) {
    groupNames.push(state.currentGroup);
  }

  // 单个标签：managed=true 时附加重命名/删除操作（悬停显示），「未分组」与「⚠ 离线」不可管理
  const tabHTML = (name, count, opts) => {
    const active = state.currentGroup === name;
    const cls = ['group-tab', active ? 'active' : '', opts && opts.offline ? 'offline' : '', opts && opts.muted ? 'muted' : ''].join(' ').trim();
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
    html += tabHTML(name, groupCounts[name] || 0, { managed: true });
  }
  html += tabHTML('未分组', ungroupedCount, { muted: true });
  html += tabHTML(OFFLINE_GROUP, offlineCount, { offline: true });
  // 始终排在最后的新建分组按钮
  html += `<button class="group-tab new-group" id="newGroupBtn" title="新建分组">+ 新建分组</button>`;
  // IP 模糊显示切换键（仅本浏览器生效，刷新不丢）
  html += `<button class="group-tab" id="maskIpBtn" title="切换 IP 模糊显示（保留首尾两段）">${state.maskIP ? '🙈 IP已遮' : '👁 IP显示'}</button>`;

  el.innerHTML = html;
  el.querySelectorAll('.group-tab').forEach(btn => {
    if (btn.id === 'newGroupBtn') return;
    btn.onclick = () => {
      state.currentGroup = btn.dataset.group;
      state.page = 1;
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
  const newBtn = document.getElementById('newGroupBtn');
  if (newBtn) newBtn.onclick = groupCreate;
  const maskBtn = document.getElementById('maskIpBtn');
  if (maskBtn) maskBtn.onclick = () => {
    state.maskIP = !state.maskIP;
    localStorage.setItem('yufu_mask_ip', state.maskIP ? '1' : '0');
    renderGroupTabs();
    render();
  };
}

// 重命名分组：改名会作用于该分组下的全部客户端
async function groupRename(oldName) {
  if (isVisitor()) return;
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
    if (state.currentGroup === oldName) { state.currentGroup = newName; state.page = 1; }
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
    if (state.currentGroup === name) { state.currentGroup = ''; state.page = 1; }
    await refreshAgents();
  } catch (e) {
    alert('删除失败：' + e.message);
  }
}

// 新建分组：弹窗输入名称 → 注册到分组表（不需要任何客户端属于此分组）
async function groupCreate() {
  if (isVisitor()) return;
  const input = prompt('新建分组\n请输入分组名（先建一个空组，再用「编辑机器」把客户端加进来）：');
  if (input === null) return;
  const name = input.trim();
  if (name === '') return;
  if (name === '未分组') { alert('「未分组」是默认分组，不能用作自定义分组名'); return; }
  if (name === OFFLINE_GROUP) { alert('该名称保留给离线分组，不可使用'); return; }
  if (/[\\/]/.test(name)) { alert('分组名不能包含 / 或 \\'); return; }
  if (state.groups.includes(name)) { alert('该分组已存在'); return; }
  try {
    const r = await fetch('/api/groups', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (!r.ok) { alert('新建失败：HTTP ' + r.status); return; }
    await refreshAgents();
  } catch (e) {
    alert('新建失败：' + e.message);
  }
}

// 把编辑弹窗里的「分组」下拉框（原生 <select>）同步成当前 state.groups：
// 含一个「（未分组）」空值项 + 所有已注册分组，按中文排序。每次 render 重建，
// 因此 WS 推送来新分组（或改名/删除）后无需手动刷新即可见。
function populateGroupSelect() {
  const sel = document.getElementById('editGroup');
  if (!sel) return;
  const cur = sel.value;                      // 重建前记住当前选中，避免丢失
  const opts = ['<option value="">（未分组）</option>'];
  for (const g of [...state.groups].sort((x, y) => x.localeCompare(y, 'zh'))) {
    opts.push(`<option value="${escapeHtml(g)}">${escapeHtml(g)}</option>`);
  }
  sel.innerHTML = opts.join('');
  if (cur) sel.value = cur;                   // 还原选中（仅当该值仍是合法选项）
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

// 批量操作成功后，立即把改动回写到本地 state.agents，让界面即时反映结果。
// 为什么必须回写：服务端在响应返回时就已经改好了 SQLite 与内存态，但面板渲染
// 读的是浏览器本地的 state.agents；若只调 requestRender() 会拿旧数据重绘，
// 得等下一帧 WS 全量快照推来才更新。机器多时该快照很大（千台级接近 1MB/帧）、
// 客户端消费慢，肉眼可见能延迟数十秒——表现就是"改了分组没马上过去"。
// 单机编辑（editSave）一直是这么做的，这里与之保持一致；WS 后续推送仍作兜底校正。
function patchLocalAgents(uuids, patch) {
  const set = new Set(uuids);
  for (const a of state.agents) {
    if (set.has(a.uuid)) Object.assign(a, patch);
  }
}

// 单张卡片 HTML（分组由渲染层统一处理，这里只画卡片本身）
function cardInner(a) {
  const alias = a.alias || a.hostname || a.uuid.slice(0, 8);
  const code = a.country_code || (isPrivateIP(a.ip) ? '内网' : '');
  const loc = a.country ? (a.country.replace(stripFlagEmoji(a.country) || '', '').trim()) : '';
  // 发行版官方 logo（仅图标、无文字）+ 自托管国旗
  const osName = a.platform || a.os || 'Linux';
  const distroImg = distroIcon(a.platform, a.os, osName);
  const flagImg = flagImage(a.country_code, loc);

  const cd = fmtCountdown(a.expire_at);
  const cdBadge = cd ? `<span class="cd-badge ${cd.cls}" title="VPS 到期">📅 ${cd.text}</span>` : '';
  const groupBadge = a.group ? `<span class="card-group" title="分组">🏷️ ${escapeHtml(a.group)}</span>` : '';
  return `
    <div class="card-header"><label class="sel-only card-chk-wrap"><input class="sel-chk" type="checkbox" data-uuid="${a.uuid}" ${state.selected.has(a.uuid)?'checked':''} onclick="event.stopPropagation()"></label>
      <div class="card-title">
        <input class="card-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名">
        <button class="btn-edit" data-uuid="${a.uuid}" title="编辑名称/备注/分组/到期">✎</button><button class="btn-del del-only" data-uuid="${a.uuid}" title="删除该客户端">🗑</button>${a.online ? `<button class="btn-ssh" data-uuid="${a.uuid}" title="Web SSH 终端">SSH</button>` : ''}
        <div class="card-status">
          <span class="dot ${a.online ? 'on' : 'off'}"></span>
          <span class="status-text ${a.online ? 'on' : 'off'}">${a.online ? '在线' : '离线'}</span>
        </div>
      </div>
    </div>
    <div class="card-meta">
      <span class="card-os">${distroImg}</span>
      <span>${flagImg} ${escapeHtml(loc)} ${code ? '(' + escapeHtml(code) + ')' : ''}</span>
      <span>⏱️ ${fmtUptime(a.uptime)}</span>
    </div>
    <div class="card-ip">${ipBlockHTML(a)}</div>
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
        <div class="metric-label">内存 ${fmtSize(a.mem_used)} / ${fmtSize(a.mem_total)}</div>
        <div class="metric-bar"><div class="bar-mem" style="width:${percent(a.mem_used, a.mem_total)}%"></div></div>
      </div>
      <div class="metric">
        <div class="metric-label">磁盘 ${fmtSize(a.disk_used)} / ${fmtSize(a.disk_total)}</div>
        <div class="metric-bar"><div class="bar-disk" style="width:${percent(a.disk_used, a.disk_total)}%"></div></div>
      </div>
      <div class="metric">
        <div class="metric-label">实时速率</div>
        <div class="metric-value speed">
          <div class="down">↓${fmtRate(a.rx_rate)}</div>
          <div class="up">↑${fmtRate(a.tx_rate)}</div>
        </div>
      </div>
    </div>
    <div class="card-traffic">
      <div class="traffic-item">
        <div class="traffic-label">本月下载 ↓</div>
        <div class="traffic-value down">${fmtBytes(a.rx_month)}</div>
        <div class="traffic-sub">每月1日0点重置</div>
      </div>
      <div class="traffic-item">
        <div class="traffic-label">本月上传 ↑</div>
        <div class="traffic-value up">${fmtBytes(a.tx_month)}</div>
        <div class="traffic-sub">北京时间自然月</div>
      </div>
    </div>`;
}
function cardHTML(a) {
  return `<div class="agent-card ${a.online ? '' : 'offline'}" data-uuid="${a.uuid}">${cardInner(a)}</div>`;
}
function cardHTMLAt(a, style) {
  return `<div class="agent-card ${a.online ? '' : 'offline'}" data-uuid="${a.uuid}" style="${style}">${cardInner(a)}</div>`;
}

// 卡片视图：单卡固定高 400px + 间距 20px。超过阈值时按可视窗口绝对放置卡片（虚拟滚动）
const CARD_H = 400, CARD_GAP = 20, CARD_MIN_W = 340;
function cardCols() {
  const cw = (agentsScrollEl.clientWidth || 1200) - 56; // 减去 .agents-grid 左右内边距
  return Math.max(1, Math.floor((cw + CARD_GAP) / (CARD_MIN_W + CARD_GAP)));
}
// 卡片视图同样用骨架持久化：虚拟滚动下仅在模式切换或列数变化时才重建网格容器，
// 避免每帧整网格 innerHTML 重建 + 滚动复位造成的抖动（与列表视图同源）。
let lastCardMode = '';
let lastCardCols = -1;
function renderCard() {
  const scroll = agentsScrollEl;
  const list = currentPageList();
  if (list.length === 0) {
    scroll.innerHTML = `<div class="empty-tip">该分组下暂无客户端</div>`;
    lastCardMode = 'empty';
    return;
  }
  if (list.length <= VIRTUAL_THRESHOLD) {
    let html = '';
    for (const a of list) html += cardHTML(a);
    scroll.innerHTML = `<div class="agents-grid">${html}</div>`;
    bindCardEvents(scroll);
    lastCardMode = 'full';
    return;
  }
  // 虚拟滚动：网格容器高度撑满 N 行，仅渲染可视窗口内的卡片。
  // 仅当从非虚拟模式切换过来、列数变化（窗口缩放）或骨架丢失时才重建网格容器，
  // 之后每帧只刷新可视窗口，消除抖动。
  const cols = cardCols();
  const rowH = CARD_H + CARD_GAP;
  const rows = Math.ceil(list.length / cols);
  const totalH = rows * CARD_H + (rows - 1) * CARD_GAP;
  if (lastCardMode !== 'virt' || lastCardCols !== cols || !scroll.querySelector('.agents-grid.virtual')) {
    scroll.innerHTML =
      `<div class="agents-grid virtual" style="grid-template-columns:repeat(${cols},1fr);grid-auto-rows:${CARD_H}px;height:${totalH}px;"></div>`;
    lastCardMode = 'virt';
    lastCardCols = cols;
  }
  renderCardWindow(scroll.scrollTop);
}
function renderCardWindow(scrollTop) {
  const scroll = agentsScrollEl;
  const grid = scroll.querySelector('.agents-grid.virtual');
  if (!grid) return;
  const list = currentPageList();
  const cols = cardCols();
  const rowH = CARD_H + CARD_GAP;
  const rows = Math.ceil(list.length / cols);
  const startRow = Math.max(0, Math.floor(scrollTop / rowH) - 1);
  const visRows = Math.ceil(scroll.clientHeight / rowH);
  const endRow = Math.min(rows, startRow + visRows + 2);
  let html = '';
  for (let r = startRow; r < endRow; r++) {
    for (let c = 0; c < cols; c++) {
      const i = r * cols + c;
      if (i >= list.length) break;
      html += cardHTMLAt(list[i], `grid-column:${c + 1};grid-row:${r + 1};`);
    }
  }
  grid.innerHTML = html;
  bindCardEvents(grid);
}
function bindCardEvents(root) {
  root.querySelectorAll('.agent-card').forEach(el => { el.onclick = () => openDetail(el.dataset.uuid); });
  root.querySelectorAll('.btn-edit').forEach(btn => { btn.onclick = e => { e.stopPropagation(); openEdit(btn.dataset.uuid); }; });
  root.querySelectorAll('.btn-ssh').forEach(btn => { btn.onclick = e => { e.stopPropagation(); openSSH(btn.dataset.uuid); }; });
  root.querySelectorAll('.btn-del').forEach(btn => { btn.onclick = e => { e.stopPropagation(); deleteAgent(btn.dataset.uuid); }; });
  bindCardCheckboxes(root);
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
document.getElementById('editDelete').onclick = () => { if (editUUID) deleteAgent(editUUID); };
document.getElementById('editUninstall').onclick = copyUninstallCommand;
document.getElementById('editModal').addEventListener('click', e => {
  if (e.target.id === 'editModal') closeEdit();
});
document.getElementById('editSave').onclick = async () => {
  if (!editUUID || isVisitor()) return;
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
  if (a) { a.alias = name; a.group = group; a.remark = remark; a.expire_at = expireAt; groupOverrides[editUUID] = { group, floor: lastAgentsSeq, expires: Date.now() + 15000 }; }
  closeEdit();
  render();
  if (state.currentUUID === editUUID) drawDetail();
};

function listRowHTML(a) {
  const alias = a.alias || a.hostname || a.uuid.slice(0, 8);
  const code = a.country_code || (isPrivateIP(a.ip) ? '内网' : '');
  const loc = a.country ? (a.country.replace(stripFlagEmoji(a.country) || '', '').trim()) : '';
  const flagImg = flagImage(a.country_code, loc);
  const cd = fmtCountdown(a.expire_at);
  const cdHtml = cd ? `<div class="cd-text ${cd.cls}" title="VPS 到期">📅 ${cd.text}</div>` : '';
  return `
    <tr>
      <td class="sel-td"><label class="sel-only"><input class="sel-chk" type="checkbox" data-uuid="${a.uuid}" ${state.selected.has(a.uuid)?'checked':''} onclick="event.stopPropagation()"></label></td>
      <td><span class="dot ${a.online ? 'on' : 'off'}"></span> <span class="status-text ${a.online ? 'on' : 'off'}">${a.online ? '在线' : '离线'}</span></td>
      <td><input class="list-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名"></td>
      <td><span class="flag">${flagImg}</span>${escapeHtml(loc)} ${code ? '(' + escapeHtml(code) + ')' : ''}<br>${ipBlockHTML(a)}</td>
      <td>${escapeHtml(fmtConfig(a))}</td>
      <td>${fmtUptime(a.uptime)}${cdHtml}</td>
      <td>
        <div>CPU ${a.cpu.toFixed(1)}% <span class="mini-bar"><div class="bar-cpu" style="width:${percent(a.cpu, 100)}%"></div></span></div>
        <div style="margin-top:4px">内存 ${percent(a.mem_used, a.mem_total).toFixed(1)}% <span class="mini-bar"><div class="bar-mem" style="width:${percent(a.mem_used, a.mem_total)}%"></div></span></div>
        <div style="margin-top:4px">磁盘 ${percent(a.disk_used, a.disk_total).toFixed(1)}% <span class="mini-bar"><div class="bar-disk" style="width:${percent(a.disk_used, a.disk_total)}%"></div></span></div>
      </td>
      <td><span class="down">↓${fmtRate(a.rx_rate)}</span><br><span class="up">↑${fmtRate(a.tx_rate)}</span></td>
      <td><span class="down">${fmtBytes(a.rx_month)}</span><br><span class="up">${fmtBytes(a.tx_month)}</span></td>
      <td><button class="btn-chart" data-uuid="${a.uuid}">流量</button> <button class="btn-edit" data-uuid="${a.uuid}">编辑</button>${a.online ? ` <button class="btn-ssh" data-uuid="${a.uuid}">SSH</button>` : ''} <button class="btn-del del-only" data-uuid="${a.uuid}" title="删除该客户端">删除</button></td>
    </tr>
  `;
}

// 列表视图：行高钉死 64px。超过阈值时用上下占位行撑起总高、仅渲染可视行（虚拟滚动）
const LIST_ROW_H = 64;
// 记录上一次列表渲染模式（'empty'|'full'|'virt'）。虚拟滚动下仅当模式切换（或骨架
// 丢失，如从卡片视图切回）时才重建表骨架；之后每帧数据更新只刷新可视窗口（renderListWindow），
// 不再销毁表头、不再复位滚动位置——这是「列表一直抖、静不下来」的根因（每帧整表 innerHTML
// 重建 + scrollTop 归零再还原）。
let lastListMode = '';
function renderList() {
  const scroll = agentsScrollEl;
  const list = currentPageList();
  const thead = `
    <thead>
      <tr>
        <th class="sel-td"><label class="sel-only"><input class="sel-chk" type="checkbox" id="selectAllChk" onclick="event.stopPropagation()"></label></th>
        <th>状态</th><th>别名</th><th>位置</th><th>配置</th><th>运行时间</th>
        <th>使用率</th><th>实时速率</th><th>本月流量</th><th style="width:1%;white-space:nowrap">操作</th>
      </tr>
    </thead>`;
  if (list.length === 0) {
    scroll.innerHTML = `<div class="agents-table"><table>${thead}<tbody><tr><td colspan="10" class="empty-tip">该分组下暂无客户端</td></tr></tbody></table></div>`;
    lastListMode = 'empty';
    return;
  }
  if (list.length <= VIRTUAL_THRESHOLD) {
    let rows = '';
    for (const a of list) rows += listRowHTML(a);
    scroll.innerHTML = `<div class="agents-table"><table>${thead}<tbody>${rows}</tbody></table></div>`;
    bindListEvents(scroll);
    lastListMode = 'full';
    return;
  }
  // 虚拟滚动：tbody 用上下占位行撑起 N*64px 总高，只渲染可视窗口内的行。
  // 仅当从非虚拟模式切换过来（或骨架不存在）时重建表骨架，避免每帧整体重建造成抖动。
  if (lastListMode !== 'virt' || !scroll.querySelector('table')) {
    scroll.innerHTML = `<div class="agents-table"><table>${thead}<tbody></tbody></table></div>`;
    lastListMode = 'virt';
  }
  renderListWindow(scroll.scrollTop);
}
function renderListWindow(scrollTop) {
  const scroll = agentsScrollEl;
  const table = scroll.querySelector('table');
  const tbody = table && table.querySelector('tbody');
  if (!tbody) return;
  const list = currentPageList();
  const theadH = (table.querySelector('thead') && table.querySelector('thead').offsetHeight) || 0;
  const top = scrollTop - theadH;
  const start = Math.max(0, Math.floor(top / LIST_ROW_H) - 1);
  const visRows = Math.ceil(scroll.clientHeight / LIST_ROW_H);
  const end = Math.min(list.length, start + visRows + 2);
  let html = `<tr class="vspacer" style="height:${start * LIST_ROW_H}px"><td colspan="9" style="height:${start * LIST_ROW_H}px"></td></tr>`;
  for (let i = start; i < end; i++) html += listRowHTML(list[i]);
  html += `<tr class="vspacer" style="height:${(list.length - end) * LIST_ROW_H}px"><td colspan="9" style="height:${(list.length - end) * LIST_ROW_H}px"></td></tr>`;
  tbody.innerHTML = html;
  bindListEvents(tbody);
}
function bindListEvents(root) {
  root.querySelectorAll('.btn-chart').forEach(btn => { btn.onclick = () => openDetail(btn.dataset.uuid); });
  root.querySelectorAll('.btn-edit').forEach(btn => { btn.onclick = e => { e.stopPropagation(); openEdit(btn.dataset.uuid); }; });
  root.querySelectorAll('.btn-ssh').forEach(btn => { btn.onclick = e => { e.stopPropagation(); openSSH(btn.dataset.uuid); }; });
  root.querySelectorAll('.btn-del').forEach(btn => { btn.onclick = e => { e.stopPropagation(); deleteAgent(btn.dataset.uuid); }; });
  bindCardCheckboxes(root);
  bindAliasInputs('.list-name');
  // 注意：虚拟滚动（list.length > VIRTUAL_THRESHOLD）下 bindListEvents 的 root 是
  // <tbody> 窗口，而 #selectAllChk 在 <thead> 里，root.querySelector 会返回 null、
  // 导致全选框的 onchange 永远没绑上（千台以上分组点全选=选 0 台）。
  // 因此这里统一从滚动容器 agentsScrollEl 里找勾选框，两种渲染模式都能命中。
  const sa = agentsScrollEl.querySelector('#selectAllChk');
  if (sa) {
    // 语义：表头全选 = 当前页可见行（标准表格 UX）。pageSize='all' 时「当前页」即整组，
    // 满足「千台分组展示全部 + 一键全选整组」；把每页设成 N 时只选当前页那 N 台，
    // 不会再把整组都选走（之前误改成 filteredAgents() 全量导致的回归）。
    const list = currentPageList();
    sa.checked = list.length > 0 && list.every(a => state.selected.has(a.uuid));
    sa.onchange = (e) => {
      e.stopPropagation();
      const checked = sa.checked;
      currentPageList().forEach(a => {
        if (checked) state.selected.add(a.uuid);
        else state.selected.delete(a.uuid);
      });
      requestRender();
    };
  }
}

function bindAliasInputs(selector) {
  document.querySelectorAll(selector).forEach(inp => {
    inp.onclick = e => e.stopPropagation();
    const save = async () => {
      if (isVisitor()) return;
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

// ---------- 生成安装 / 卸载命令（同时拉两个接口） ----------
document.getElementById('installCmdBtn').onclick = async () => {
  if (isVisitor()) return;
  const btn = document.getElementById('installCmdBtn');
  btn.disabled = true;
  try {
    const [ri, ru] = await Promise.all([
      fetch('/api/install-command'),
      fetch('/api/uninstall-command'),
    ]);
    if (!ri.ok) throw new Error('获取安装命令失败: HTTP ' + ri.status);
    if (!ru.ok) throw new Error('获取卸载命令失败: HTTP ' + ru.status);
    const di = await ri.json();
    const du = await ru.json();
    document.getElementById('installCmdText').value = di.command;
    document.getElementById('uninstallCmdText').value = du.command;
    document.getElementById('installModal').classList.remove('hidden');
    // 自动复制安装命令（需 HTTPS / localhost；失败静默回落到用户手动点复制）
    try { await navigator.clipboard.writeText(di.command); } catch (e) {}
  } catch (e) {
    alert('获取命令失败：' + e.message);
  } finally {
    btn.disabled = false;
  }
};
// 卸载命令：复制到剪贴板
document.getElementById('uninstallCopyBtn').onclick = async () => {
  const txt = document.getElementById('uninstallCmdText').value;
  try {
    await navigator.clipboard.writeText(txt);
    const b = document.getElementById('uninstallCopyBtn');
    const old = b.textContent;
    b.textContent = '✓ 已复制';
    setTimeout(() => { b.textContent = old; }, 1500);
  } catch (e) {
    const ta = document.getElementById('uninstallCmdText');
    ta.select();
    document.execCommand && document.execCommand('copy');
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

// ---------- 访客链接（仅管理员可签发） ----------
document.getElementById('visitorLinkBtn').onclick = async () => {
  if (isVisitor()) return;
  const btn = document.getElementById('visitorLinkBtn');
  btn.disabled = true;
  try {
    const r = await fetch('/api/visitor/link', { method: 'POST' });
    if (!r.ok) {
      if (r.status === 403) throw new Error('当前为访客，无权签发');
      throw new Error('签发失败: HTTP ' + r.status);
    }
    const d = await r.json();
    const full = window.location.origin + d.path;
    document.getElementById('visitorLinkInput').value = full;
    document.getElementById('visitorLinkExpire').textContent = '有效期至：' + d.expires + '（UTC）';
    document.getElementById('visitorLinkModal').classList.remove('hidden');
    try { await navigator.clipboard.writeText(full); } catch (e) {}
  } catch (e) {
    alert('签发访客链接失败：' + e.message);
  } finally {
    btn.disabled = false;
  }
};
document.getElementById('visitorLinkCopy').onclick = async () => {
  const txt = document.getElementById('visitorLinkInput').value;
  try {
    await navigator.clipboard.writeText(txt);
    const btn = document.getElementById('visitorLinkCopy');
    const old = btn.textContent;
    btn.textContent = '✓ 已复制';
    setTimeout(() => { btn.textContent = old; }, 1500);
  } catch (e) {
    const inp = document.getElementById('visitorLinkInput');
    inp.select();
    document.execCommand && document.execCommand('copy');
  }
};
document.getElementById('visitorLinkClose').onclick = () => {
  document.getElementById('visitorLinkModal').classList.add('hidden');
};
document.getElementById('installModal').addEventListener('click', e => {
  if (e.target.id === 'installModal') document.getElementById('installModal').classList.add('hidden');
});

// ---------- Web SSH 终端 ----------
let term = null, termFit = null, termWS = null, termUUID = '', termResizeHandler = null;

// base64 编解码（兼容中文等非 Latin1 字符）
function b64enc(str) { return btoa(unescape(encodeURIComponent(str))); }
function b64dec(b64) { return decodeURIComponent(escape(atob(b64))); }

// 打开密码弹窗
function openSSH(uuid) {
  if (isVisitor()) return;
  termUUID = uuid;
  document.getElementById('sshPassInput').value = '';
  document.getElementById('sshPassErr').textContent = '';
  document.getElementById('sshPassModal').classList.remove('hidden');
  setTimeout(() => document.getElementById('sshPassInput').focus(), 50);
}

function closeSSHPass() {
  document.getElementById('sshPassModal').classList.add('hidden');
}

function doCloseTerminal() {
  if (termWS) { try { termWS.close(); } catch (e) {} termWS = null; }
  if (term) { try { term.dispose(); } catch (e) {} term = null; termFit = null; }
  if (termResizeHandler) { window.removeEventListener('resize', termResizeHandler); termResizeHandler = null; }
  document.getElementById('termModal').classList.add('hidden');
}

function closeTerminal(delay) {
  if (delay) setTimeout(doCloseTerminal, delay);
  else doCloseTerminal();
}

function startTerminal(uuid, password) {
  term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: 'ui-monospace,Menlo,Consolas,monospace',
    theme: { background: '#1e1e1e', foreground: '#f0f0f0' },
  });
  termFit = new FitAddon.FitAddon();
  term.loadAddon(termFit);
  const box = document.getElementById('term');
  box.innerHTML = '';
  term.open(box);
  termFit.fit();
  term.focus();
  document.getElementById('termTitle').textContent = 'Web SSH — ' + uuid.slice(0, 8);
  document.getElementById('termModal').classList.remove('hidden');

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  termWS = new WebSocket(proto + '://' + location.host + '/ws/terminal/' + encodeURIComponent(uuid));
  termWS.onopen = () => {
    termWS.send(JSON.stringify({ action: 'auth', password, cols: term.cols, rows: term.rows }));
  };
  termWS.onmessage = (e) => {
    let m;
    try { m = JSON.parse(e.data); } catch (err) { return; }
    if (m.action === 'ready') {
      try { termFit.fit(); } catch (err) {}
    } else if (m.action === 'data') {
      try { term.write(b64dec(m.data)); } catch (err) { term.write(m.data); }
    } else if (m.action === 'error') {
      term.write('\r\n\x1b[31m' + m.message + '\x1b[0m\r\n');
      closeTerminal(2500);
    } else if (m.action === 'ended') {
      term.write('\r\n\x1b[33m' + m.message + '\x1b[0m\r\n');
      closeTerminal(1500);
    }
  };
  termWS.onclose = () => {
    term.write('\r\n\x1b[90m[连接已关闭]\x1b[0m\r\n');
  };
  term.onData(d => {
    if (termWS && termWS.readyState === 1) {
      termWS.send(JSON.stringify({ action: 'input', data: b64enc(d) }));
    }
  });
  term.onResize(({ cols, rows }) => {
    if (termWS && termWS.readyState === 1) {
      termWS.send(JSON.stringify({ action: 'resize', cols, rows }));
    }
  });
  // 浏览器窗口缩放时重新适配终端
  termResizeHandler = () => { if (term && termFit) { try { termFit.fit(); } catch (e) {} } };
  window.addEventListener('resize', termResizeHandler);
}

document.getElementById('sshPassConnect').onclick = () => {
  const pw = document.getElementById('sshPassInput').value;
  const uuid = termUUID;
  closeSSHPass();
  startTerminal(uuid, pw);
};
document.getElementById('sshPassInput').addEventListener('keydown', e => {
  if (e.key === 'Enter') document.getElementById('sshPassConnect').click();
});
document.getElementById('sshPassCancel').onclick = closeSSHPass;
document.getElementById('sshPassModal').addEventListener('click', e => {
  if (e.target.id === 'sshPassModal') closeSSHPass();
});
document.getElementById('termClose').onclick = () => closeTerminal(0);
document.getElementById('termModal').addEventListener('click', e => {
  if (e.target.id === 'termModal') closeTerminal(0);
});

// ---------- 压力测试（虚拟机器）----------
let sfCountriesSel = new Set();
let sfOsesSel = new Set();
let sfTimer = null;

document.getElementById('stressBtn').onclick = async () => {
  document.getElementById('stressModal').classList.remove('hidden');
  await loadStressOptions();
  applyStressPrefs();
  refreshStressStatus();
};
document.getElementById('sfClose').onclick = () => document.getElementById('stressModal').classList.add('hidden');
document.getElementById('stressModal').addEventListener('click', e => {
  if (e.target.id === 'stressModal') document.getElementById('stressModal').classList.add('hidden');
});

// 「随机」勾选时禁用对应的手动输入框
const sfRandMap = {
  sfUptimeRand: ['sfUptimeMin', 'sfUptimeMax'],
  sfCpuRand: ['sfCpuMin', 'sfCpuMax'],
  sfMemRand: ['sfMemMin', 'sfMemMax'],
  sfCpuCoresRand: ['sfCpuCoresMin', 'sfCpuCoresMax'],
  sfMemTotalRand: ['sfMemTotalMin', 'sfMemTotalMax'],
  sfDiskTotalRand: ['sfDiskTotalMin', 'sfDiskTotalMax'],
};
Object.keys(sfRandMap).forEach(id => {
  document.getElementById(id).addEventListener('change', e => {
    sfRandMap[id].forEach(iid => { document.getElementById(iid).disabled = e.target.checked; });
  });
});

async function loadStressOptions() {
  try {
    const r = await fetch('/api/stress/options');
    if (!r.ok) return;
    const d = await r.json();
    const cc = document.getElementById('sfCountries');
    cc.innerHTML = '';
    sfCountriesSel.clear();
    (d.countries || []).forEach(c => {
      const el = document.createElement('span');
      el.className = 'chip';
      el.dataset.code = c.code;
      el.textContent = c.name;
      el.onclick = () => {
        if (el.classList.contains('on')) { el.classList.remove('on'); sfCountriesSel.delete(c.code); }
        else { el.classList.add('on'); sfCountriesSel.add(c.code); }
      };
      cc.appendChild(el);
    });
    const oc = document.getElementById('sfOses');
    oc.innerHTML = '';
    sfOsesSel.clear();
    (d.oses || []).forEach(k => {
      const el = document.createElement('span');
      el.className = 'chip';
      el.dataset.key = k;
      el.textContent = k;
      el.onclick = () => {
        if (el.classList.contains('on')) { el.classList.remove('on'); sfOsesSel.delete(k); }
        else { el.classList.add('on'); sfOsesSel.add(k); }
      };
      oc.appendChild(el);
    });
  } catch (e) {}
}

function sfNum(id, dflt) {
  const v = parseInt(document.getElementById(id).value, 10);
  return isNaN(v) ? dflt : v;
}
function sfFlt(id, dflt) {
  const v = parseFloat(document.getElementById(id).value);
  return isNaN(v) ? dflt : v;
}

// ---------- 压测参数记忆（localStorage）----------
// 每次「开始生成」后自动存下全部参数，下次打开弹窗自动回填，免去重复设置。
const SF_PREFS_KEY = 'yufu_stress_prefs';
function saveStressPrefs() {
  const prefs = {
    count: sfNum('sfCount', 2000),
    duration: sfNum('sfDuration', 0),
    online: sfNum('sfOnline', 100),
    traffic: document.getElementById('sfTraffic').value,
    group: document.getElementById('sfGroup').value,
    countryRand: document.getElementById('sfCountryRand').checked,
    osRand: document.getElementById('sfOsRand').checked,
    countries: Array.from(sfCountriesSel),
    oses: Array.from(sfOsesSel),
    uptimeRand: document.getElementById('sfUptimeRand').checked,
    uptimeMin: sfNum('sfUptimeMin', 0),
    uptimeMax: sfNum('sfUptimeMax', 0),
    cpuRand: document.getElementById('sfCpuRand').checked,
    cpuMin: sfFlt('sfCpuMin', 0),
    cpuMax: sfFlt('sfCpuMax', 0),
    memRand: document.getElementById('sfMemRand').checked,
    memMin: sfFlt('sfMemMin', 0),
    memMax: sfFlt('sfMemMax', 0),
    cpuCoresRand: document.getElementById('sfCpuCoresRand').checked,
    cpuCoresMin: sfNum('sfCpuCoresMin', 0),
    cpuCoresMax: sfNum('sfCpuCoresMax', 0),
    memTotalRand: document.getElementById('sfMemTotalRand').checked,
    memTotalMin: sfFlt('sfMemTotalMin', 0),
    memTotalMax: sfFlt('sfMemTotalMax', 0),
    diskTotalRand: document.getElementById('sfDiskTotalRand').checked,
    diskTotalMin: sfFlt('sfDiskTotalMin', 0),
    diskTotalMax: sfFlt('sfDiskTotalMax', 0),
  };
  try { localStorage.setItem(SF_PREFS_KEY, JSON.stringify(prefs)); } catch (e) {}
}
function applyStressPrefs() {
  let prefs;
  try { prefs = JSON.parse(localStorage.getItem(SF_PREFS_KEY) || 'null'); } catch (e) { return; }
  if (!prefs) return;
  const setVal = (id, v) => { if (v !== undefined && v !== null) { const el = document.getElementById(id); if (el) el.value = v; } };
  const setChk = (id, v) => { if (typeof v === 'boolean') { const el = document.getElementById(id); if (el) el.checked = v; } };
  setVal('sfCount', prefs.count);
  setVal('sfDuration', prefs.duration);
  setVal('sfOnline', prefs.online);
  setVal('sfTraffic', prefs.traffic);
  setVal('sfGroup', prefs.group);
  setChk('sfCountryRand', prefs.countryRand);
  setChk('sfOsRand', prefs.osRand);
  setChk('sfUptimeRand', prefs.uptimeRand);
  setVal('sfUptimeMin', prefs.uptimeMin);
  setVal('sfUptimeMax', prefs.uptimeMax);
  setChk('sfCpuRand', prefs.cpuRand);
  setVal('sfCpuMin', prefs.cpuMin);
  setVal('sfCpuMax', prefs.cpuMax);
  setChk('sfMemRand', prefs.memRand);
  setVal('sfMemMin', prefs.memMin);
  setVal('sfMemMax', prefs.memMax);
  setChk('sfCpuCoresRand', prefs.cpuCoresRand);
  setVal('sfCpuCoresMin', prefs.cpuCoresMin);
  setVal('sfCpuCoresMax', prefs.cpuCoresMax);
  setChk('sfMemTotalRand', prefs.memTotalRand);
  setVal('sfMemTotalMin', prefs.memTotalMin);
  setVal('sfMemTotalMax', prefs.memTotalMax);
  setChk('sfDiskTotalRand', prefs.diskTotalRand);
  setVal('sfDiskTotalMin', prefs.diskTotalMin);
  setVal('sfDiskTotalMax', prefs.diskTotalMax);
  // 触发「随机」勾选的 disabled 联动，使手动输入框按记忆启用/禁用
  Object.keys(sfRandMap).forEach(id => {
    const el = document.getElementById(id);
    if (el) el.dispatchEvent(new Event('change'));
  });
  // 恢复已选国家/系统 chips
  if (Array.isArray(prefs.countries)) {
    sfCountriesSel = new Set(prefs.countries);
    document.querySelectorAll('#sfCountries .chip').forEach(el => {
      if (sfCountriesSel.has(el.dataset.code)) el.classList.add('on');
    });
  }
  if (Array.isArray(prefs.oses)) {
    sfOsesSel = new Set(prefs.oses);
    document.querySelectorAll('#sfOses .chip').forEach(el => {
      if (sfOsesSel.has(el.dataset.key)) el.classList.add('on');
    });
  }
}

document.getElementById('sfStart').onclick = async () => {
  const p = {
    count: sfNum('sfCount', 2000),
    duration_sec: sfNum('sfDuration', 0),
    online_ratio: sfNum('sfOnline', 100) / 100,
    traffic_level: document.getElementById('sfTraffic').value,
    group: document.getElementById('sfGroup').value.trim() || '干活的',
    countries: document.getElementById('sfCountryRand').checked ? [] : Array.from(sfCountriesSel),
    oses: document.getElementById('sfOsRand').checked ? [] : Array.from(sfOsesSel),
    uptime_min: document.getElementById('sfUptimeRand').checked ? 0 : sfNum('sfUptimeMin', 0),
    uptime_max: document.getElementById('sfUptimeRand').checked ? 0 : sfNum('sfUptimeMax', 0),
    cpu_min: document.getElementById('sfCpuRand').checked ? 0 : sfFlt('sfCpuMin', 0),
    cpu_max: document.getElementById('sfCpuRand').checked ? 0 : sfFlt('sfCpuMax', 0),
    mem_min: document.getElementById('sfMemRand').checked ? 0 : sfFlt('sfMemMin', 0),
    mem_max: document.getElementById('sfMemRand').checked ? 0 : sfFlt('sfMemMax', 0),
    cpu_cores_min: document.getElementById('sfCpuCoresRand').checked ? 0 : sfNum('sfCpuCoresMin', 0),
    cpu_cores_max: document.getElementById('sfCpuCoresRand').checked ? 0 : sfNum('sfCpuCoresMax', 0),
    mem_total_min: document.getElementById('sfMemTotalRand').checked ? 0 : sfFlt('sfMemTotalMin', 0),
    mem_total_max: document.getElementById('sfMemTotalRand').checked ? 0 : sfFlt('sfMemTotalMax', 0),
    disk_total_min: document.getElementById('sfDiskTotalRand').checked ? 0 : sfFlt('sfDiskTotalMin', 0),
    disk_total_max: document.getElementById('sfDiskTotalRand').checked ? 0 : sfFlt('sfDiskTotalMax', 0),
  };
  saveStressPrefs();
  const btn = document.getElementById('sfStart');
  btn.disabled = true;
  try {
    const r = await fetch('/api/stress/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(p),
    });
    if (!r.ok) {
      const d = await r.json().catch(() => ({}));
      alert('启动失败：' + (d.error || ('HTTP ' + r.status)));
    } else {
      document.getElementById('sfStatus').dataset.wasRunning = '1';
    }
  } catch (e) {
    alert('启动失败：' + e.message);
  } finally {
    btn.disabled = false;
    refreshStressStatus();
  }
};

document.getElementById('sfStop').onclick = async () => {
  const btn = document.getElementById('sfStop');
  btn.disabled = true;
  try {
    await fetch('/api/stress/stop', { method: 'POST' });
  } catch (e) {}
  finally {
    btn.disabled = false;
    refreshStressStatus();
  }
};

function refreshStressStatus() {
  if (sfTimer) { clearTimeout(sfTimer); sfTimer = null; }
  const modalHidden = document.getElementById('stressModal').classList.contains('hidden');
  if (modalHidden) return;
  fetch('/api/stress/status')
    .then(r => r.ok ? r.json() : null)
    .then(s => {
      if (!s) return;
      const box = document.getElementById('sfStatus');
      const stopBtn = document.getElementById('sfStop');
      const startBtn = document.getElementById('sfStart');
      if (s.running) {
        box.classList.remove('hidden');
        box.innerHTML = `运行中：分组「${escapeHtml(s.group || '')}」 共 <b>${s.total}</b> 台，在线 <b>${s.online}</b> 台，已运行 ${s.elapsedSec}s`;
        stopBtn.classList.remove('hidden');
        startBtn.classList.add('hidden');
      } else {
        stopBtn.classList.add('hidden');
        startBtn.classList.remove('hidden');
        if (box.dataset.wasRunning === '1') {
          box.classList.remove('hidden');
          box.innerHTML = '已停止并清除完毕。';
        } else {
          box.classList.add('hidden');
        }
      }
      box.dataset.wasRunning = s.running ? '1' : '0';
    })
    .catch(() => {})
    .finally(() => {
      if (!document.getElementById('stressModal').classList.contains('hidden')) {
        sfTimer = setTimeout(refreshStressStatus, 2000);
      }
    });
}

// ---------- 折叠世界地图 ----------
// /world.svg 只取一次，之后复用缓存，避免每次 render 都发网络请求
let mapSvgCache = null;
let mapSvgInjected = false;

async function renderMap() {
  if (!state.mapOpen) return;
  const box = document.getElementById('mapBox');
  const countEl = document.getElementById('mapCount');
  if (!box) return;

  // 首次注入 SVG（来自 /world.svg，已编译进 Go 二进制，路径自动可用）
  if (!mapSvgInjected) {
    if (mapSvgCache === null) {
      try {
        const resp = await fetch('/world.svg');
        if (!resp.ok) throw new Error('http ' + resp.status);
        mapSvgCache = await resp.text();
      } catch (e) {
        box.innerHTML = '<div class="map-empty">地图加载失败</div>';
        return;
      }
    }
    box.innerHTML = mapSvgCache;
    mapSvgInjected = true;
  }

  // 聚合各国家/地区机器数量（在线/离线都算），country_code 转小写与 path id 对齐
  const counts = {};
  let covered = 0;
  for (const a of state.agents) {
    const cc = (a.country_code || '').toLowerCase();
    if (!cc) continue;
    counts[cc] = (counts[cc] || 0) + 1;
    covered++;
  }

  // 先清除上一次点亮，再按当前数据重新点亮（压测停止后对应区域应熄灭）
  const svg = box.querySelector('svg');
  if (svg) {
    svg.querySelectorAll('path.lit').forEach(p => p.classList.remove('lit'));
    svg.querySelectorAll('path').forEach(p => {
      const id = p.getAttribute('id');
      if (id && counts[id]) p.classList.add('lit');
    });
  }

  const litN = Object.keys(counts).length;
  countEl.textContent = `已点亮 ${litN} 个国家/地区 · 覆盖 ${covered} 台`;
}

// 地图开关：切换开合 + 持久化 + 打开时即时渲染
const mapToggleEl = document.getElementById('mapToggle');
mapToggleEl.onclick = () => {
  state.mapOpen = !state.mapOpen;
  localStorage.setItem('yufu_map_open', state.mapOpen ? '1' : '0');
  document.getElementById('mapWrap').classList.toggle('hidden', !state.mapOpen);
  mapToggleEl.classList.toggle('active', state.mapOpen);
  if (state.mapOpen) renderMap();
};

// 初始化开关与地图状态（默认关，若上次于本浏览器开启则恢复）
document.getElementById('mapWrap').classList.toggle('hidden', !state.mapOpen);
mapToggleEl.classList.toggle('active', state.mapOpen);
if (state.mapOpen) renderMap();

checkLogin();

// ============================================================
// 批量多选 / 删除 / 卸载命令（依赖后端 /api/agents 与 /api/uninstall-command）
// ============================================================

// 给容器里的 .sel-chk 绑定变更：切换 state.selected 并刷新批量栏。
// 注意：卡片/列表重渲染后必须重新调用（DOM 是新造的），renderBatchBar 同理。
function bindCardCheckboxes(root) {
  root.querySelectorAll('.sel-chk').forEach(cb => {
    cb.onclick = e => e.stopPropagation();
    cb.onchange = () => {
      const u = cb.dataset.uuid;
      if (cb.checked) state.selected.add(u);
      else state.selected.delete(u);
      renderBatchBar();
    };
  });
}

// 底部批量操作栏：有选中时显示，含全选/取消/改分组/设备注/设到期/删除。
// 访客模式由 applyRoleGating 隐藏。无选中时隐藏。
function renderBatchBar() {
  const bar = document.getElementById('batchBar');
  if (!bar) return;
  const n = state.selected.size;
  if (n === 0) { bar.style.display = 'none'; bar.innerHTML = ''; return; }
  bar.style.display = '';
  bar.innerHTML =
    '<span class="bb-info">已选 <b>' + n + '</b> 台</span>' +
    '<button class="bb-btn" id="bbSelAll">全选当前筛选</button>' +
    '<button class="bb-btn" id="bbSelNone">取消选择</button>' +
    '<button class="bb-btn" id="bbGroup">批量改分组</button>' +
    '<button class="bb-btn" id="bbRemark">批量设备注</button>' +
    '<button class="bb-btn" id="bbExpire">批量设到期</button>' +
    '<button class="bb-btn" id="bbBatchCmd">批量命令</button>' +
    '<button class="bb-btn danger" id="bbDel">批量删除</button>';
  const all = document.getElementById('bbSelAll');
  const none = document.getElementById('bbSelNone');
  const grp = document.getElementById('bbGroup');
  const rmk = document.getElementById('bbRemark');
  const exp = document.getElementById('bbExpire');
  const bcmd = document.getElementById('bbBatchCmd');
  const del = document.getElementById('bbDel');
  if (all) all.onclick = () => { filteredAgents().forEach(a => state.selected.add(a.uuid)); requestRender(); };
  if (none) none.onclick = () => { state.selected.clear(); requestRender(); };
  if (grp) grp.onclick = batchChangeGroup;
  if (rmk) rmk.onclick = batchSetRemark;
  if (exp) exp.onclick = batchSetExpire;
  if (bcmd) bcmd.onclick = openBatchCmd;
  if (del) del.onclick = batchDeleteAgents;
}

// 批量命令：向已选机器并发下发脚本，结果以「每台一行 + 可展开 stdout」呈现。
async function openBatchCmd() {
  if (isVisitor()) return;
  const uuids = [...state.selected];
  if (!uuids.length) { alert('请先勾选要执行命令的机器'); return; }
  const m = openCenterModal('批量命令（已选 ' + uuids.length + ' 台）');
  const hint = document.createElement('div');
  hint.className = 'gp-hint';
  hint.textContent = '在每台机器上以交互式 shell 执行下方脚本（支持多行）。提权请用 sudo -n（非交互），失败会自动退回普通用户。需要 Web SSH 密码。';
  m.box.insertBefore(hint, m.box.querySelector('.gp-actions'));

  const ta = document.createElement('textarea');
  ta.className = 'gp-input';
  ta.id = 'execCmd';
  ta.rows = 8;
  ta.placeholder = '例如：\nid\nhostname\nsudo -n apt update';
  m.box.insertBefore(ta, m.box.querySelector('.gp-actions'));

  const pw = document.createElement('input');
  pw.className = 'gp-input';
  pw.type = 'password';
  pw.id = 'execPw';
  pw.placeholder = 'Web SSH 密码';
  m.box.insertBefore(pw, m.box.querySelector('.gp-actions'));

  const opts = document.createElement('div');
  opts.className = 'gp-row';
  opts.innerHTML =
    '<label>超时(秒)<input class="gp-input gp-num" id="execTimeout" type="number" value="60" min="5" max="600"></label>' +
    '<label>并发数<input class="gp-input gp-num" id="execConc" type="number" value="50" min="1" max="200"></label>';
  m.box.insertBefore(opts, m.box.querySelector('.gp-actions'));

  m.ok.textContent = '执行';
  m.ok.onclick = async () => {
    const cmd = ta.value;
    if (!cmd.trim()) { alert('请输入要执行的命令'); return; }
    m.ok.disabled = true;
    m.ok.textContent = '执行中…';
    try {
      const r = await fetch('/api/agents/exec', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          uuids,
          command: cmd,
          timeout: parseInt(document.getElementById('execTimeout').value, 10) || 60,
          concurrency: parseInt(document.getElementById('execConc').value, 10) || 50,
          password: pw.value,
        }),
      });
      if (r.status === 401) { alert('Web SSH 密码错误'); m.ok.disabled = false; m.ok.textContent = '执行'; return; }
      if (r.status === 403) { alert('密码错误次数过多，已被锁定 24 小时'); m.close(); return; }
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const data = await r.json();
      renderExecResults(m, data.results || []);
    } catch (e) {
      alert('批量命令失败：' + e.message);
      m.ok.disabled = false;
      m.ok.textContent = '执行';
    }
  };
}

// 自动部署规则管理：列表 + 新增/编辑/删除/启停
async function openDeployRules() {
  if (isVisitor()) { alert('访客无权限'); return; }
  const m = openCenterModal('自动部署规则');
  m.ok.style.display = 'none';

  const listWrap = document.createElement('div');
  listWrap.className = 'deploy-list';
  m.box.insertBefore(listWrap, m.box.querySelector('.gp-actions'));

  const addBtn = document.createElement('button');
  addBtn.className = 'gp-btn ok';
  addBtn.textContent = '＋ 新增规则';
  addBtn.onclick = () => openDeployRuleForm(null);
  m.box.insertBefore(addBtn, m.box.querySelector('.gp-actions'));

  async function refresh() {
    try {
      const r = await fetch('/api/deploy-rules');
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const rules = await r.json();
      renderDeployList(listWrap, rules, refresh);
    } catch (e) {
      listWrap.textContent = '加载失败：' + e.message;
    }
  }
  await refresh();
}

function renderDeployList(wrap, rules, refresh) {
  if (!rules.length) {
    wrap.innerHTML = '<div class="gp-hint">还没有规则。点「新增规则」：落入源分组的新机器会自动执行部署命令，成功进目标分组、失败进失败分组。</div>';
    return;
  }
  wrap.innerHTML = '';
  rules.forEach(r => {
    const row = document.createElement('div');
    row.className = 'deploy-row';
    const src = (r.source_groups || []).join('、') || '（未指定）';
    const meta = document.createElement('div');
    meta.className = 'deploy-meta';
    meta.innerHTML =
      '<b>' + escapeHtml(r.name || '(未命名)') + '</b>' +
      '<span class="deploy-tags">源: ' + escapeHtml(src) + ' → 成功: ' + escapeHtml(r.target_group || '（不变）') +
      ' / 失败: ' + escapeHtml(r.fail_group || '（不变）') + '</span>' +
      '<span class="deploy-state">' + (r.enabled ? '✅启用' : '⏸停用') + '</span>';
    const acts = document.createElement('div');
    acts.className = 'deploy-acts';
    const edit = document.createElement('button');
    edit.className = 'gp-btn'; edit.textContent = '编辑';
    edit.onclick = () => openDeployRuleForm(r);
    const toggle = document.createElement('button');
    toggle.className = 'gp-btn'; toggle.textContent = r.enabled ? '停用' : '启用';
    toggle.onclick = async () => {
      await fetch('/api/deploy-rules/' + r.id, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: !r.enabled }),
      });
      refresh();
    };
    const del = document.createElement('button');
    del.className = 'gp-btn danger'; del.textContent = '删除';
    del.onclick = async () => {
      if (!confirm('删除规则「' + (r.name || r.id) + '」？')) return;
      await fetch('/api/deploy-rules/' + r.id, { method: 'DELETE' });
      refresh();
    };
    acts.appendChild(edit); acts.appendChild(toggle); acts.appendChild(del);
    row.appendChild(meta); row.appendChild(acts);
    wrap.appendChild(row);
  });
}

// 规则表单（rule=null 新增；否则编辑）
function openDeployRuleForm(rule) {
  const m = openCenterModal(rule ? '编辑规则' : '新增自动部署规则');
  const isEdit = !!rule;
  const grpOptions = ['', ...[...state.groups].sort((x, y) => x.localeCompare(y, 'zh'))];

  const form = document.createElement('div');
  form.className = 'deploy-form';
  form.innerHTML =
    '<label>规则名称<input class="gp-input" id="drName" placeholder="如：新机初始化"></label>' +
    '<label>源分组（可多选，机器落入这些分组即触发部署）<div class="dr-src" id="drSrc"></div></label>' +
    '<label>部署命令（多行，支持 sudo -n）<textarea class="gp-input" id="drCmd" rows="6" placeholder="apt update&#10;apt install -y curl"></textarea></label>' +
    '<label>成功后分配到<select class="gp-input" id="drTarget"></select></label>' +
    '<label>失败后分配到<select class="gp-input" id="drFail"></select></label>' +
    '<div class="gp-row">' +
      '<label>并发数<input class="gp-input gp-num" id="drConc" type="number" value="50" min="1" max="200"></label>' +
      '<label>超时(秒)<input class="gp-input gp-num" id="drTimeout" type="number" value="60" min="5" max="600"></label>' +
    '</div>' +
    '<label>Web SSH 密码<input class="gp-input" id="drPw" type="password" placeholder="' + (isEdit ? '（留空保留原密码）' : '必填') + '"></label>' +
    '<label class="dr-enable"><input type="checkbox" id="drEnabled" checked> 启用此规则</label>';
  m.box.insertBefore(form, m.box.querySelector('.gp-actions'));

  // 源分组多选
  const srcBox = form.querySelector('#drSrc');
  const cur = new Set(rule ? (rule.source_groups || []) : ['']);
  grpOptions.forEach(g => {
    const lab = document.createElement('label');
    lab.className = 'dr-src-item';
    lab.innerHTML = '<input type="checkbox" class="dr-src-chk" value="' + escapeHtml(g) + '"' + (cur.has(g) ? ' checked' : '') + '> ' + escapeHtml(g === '' ? '未分组' : g);
    srcBox.appendChild(lab);
  });

  // 目标/失败分组下拉
  const fillSel = (el) => {
    el.className = 'gp-input';
    grpOptions.forEach(g => {
      const o = document.createElement('option');
      o.value = g; o.textContent = g === '' ? '（未分组/不变）' : g;
      el.appendChild(o);
    });
  };
  fillSel(form.querySelector('#drTarget'));
  fillSel(form.querySelector('#drFail'));

  if (rule) {
    form.querySelector('#drName').value = rule.name || '';
    form.querySelector('#drCmd').value = rule.command || '';
    form.querySelector('#drTarget').value = rule.target_group || '';
    form.querySelector('#drFail').value = rule.fail_group || '';
    form.querySelector('#drConc').value = rule.concurrency || 50;
    form.querySelector('#drTimeout').value = rule.timeout || 60;
    form.querySelector('#drEnabled').checked = rule.enabled;
  }

  m.ok.textContent = isEdit ? '保存' : '创建';
  m.ok.onclick = async () => {
    const name = form.querySelector('#drName').value.trim();
    const cmd = form.querySelector('#drCmd').value;
    const pw = form.querySelector('#drPw').value;
    if (!cmd.trim()) { alert('请填写部署命令'); return; }
    if (!isEdit && !pw) { alert('请填写 Web SSH 密码'); return; }
    const srcs = [...form.querySelectorAll('.dr-src-chk:checked')].map(c => c.value);
    const body = {
      name, command: cmd,
      source_groups: srcs,
      target_group: form.querySelector('#drTarget').value,
      fail_group: form.querySelector('#drFail').value,
      concurrency: parseInt(form.querySelector('#drConc').value, 10) || 50,
      timeout: parseInt(form.querySelector('#drTimeout').value, 10) || 60,
      enabled: form.querySelector('#drEnabled').checked,
      password: pw,
    };
    try {
      const r = await fetch('/api/deploy-rules' + (isEdit ? '/' + rule.id : ''), {
        method: isEdit ? 'PATCH' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (r.status === 400) { alert('命令与密码必填'); return; }
      if (!r.ok) throw new Error('HTTP ' + r.status);
      m.close();
      openDeployRules();
    } catch (e) {
      alert('保存失败：' + e.message);
    }
  };
}

// 把批量命令结果渲染成可展开表格
function renderExecResults(m, results) {
  // 复用弹窗：清掉输入控件，换成结果区
  m.box.querySelectorAll('textarea,input,.gp-row,.gp-hint').forEach(el => el.remove());
  m.ok.style.display = 'none';
  m.cancel.textContent = '关闭';

  const wrap = document.createElement('div');
  wrap.className = 'exec-results';
  let okN = 0, failN = 0;
  for (const r of results) {
    if (r.status === 'ok' && r.exit_code === 0) okN++; else failN++;
    const row = document.createElement('div');
    row.className = 'exec-row';
    const badge = r.status === 'ok' && r.exit_code === 0 ? 'ok' : 'bad';
    const summary = document.createElement('div');
    summary.className = 'exec-summary';
    // 显示机器名而非裸 uuid（沿用列表里的 alias/hostname 回退规则），方便定位是哪台
    const a = state.agents.find(x => x.uuid === r.uuid);
    const label = a ? (a.alias || a.hostname || r.uuid.slice(0, 8)) : r.uuid.slice(0, 8);
    summary.innerHTML = '<span class="exec-uuid">' + escapeHtml(label) + '</span>' +
      '<span class="exec-badge ' + badge + '">' + escapeHtml(r.status) + (r.status === 'ok' ? ' exit=' + r.exit_code : '') + '</span>' +
      '<span class="exec-size">' + (r.stdout ? r.stdout.length : 0) + ' 字节</span>';
    const out = document.createElement('pre');
    out.className = 'exec-out';
    out.style.display = 'none';
    out.textContent = (r.stdout || '') + (r.error ? '\n[错误] ' + r.error : '');
    row.appendChild(summary);
    row.appendChild(out);
    summary.onclick = () => { out.style.display = out.style.display === 'none' ? 'block' : 'none'; };
    wrap.appendChild(row);
  }
  const title = document.createElement('div');
  title.className = 'exec-title';
  title.textContent = '完成：成功 ' + okN + ' / 失败 ' + failN + ' / 共 ' + results.length + '（点行展开输出）';
  m.box.insertBefore(title, m.box.querySelector('.gp-actions'));
  m.box.insertBefore(wrap, m.box.querySelector('.gp-actions'));
}

// 单台删除（含确认）。提示文案说明「仅面板移除 + 复活可能」，引导用卸载命令彻底清理。
async function deleteAgent(uuid) {
  const a = state.agents.find(x => x.uuid === uuid);
  const name = a ? (a.alias || a.hostname || uuid.slice(0, 8)) : uuid.slice(0, 8);
  if (!confirm(
    '确定删除「' + name + '」？\n' +
    '该机器将从面板消失（数据库记录移除）。\n' +
    '若客户端 agent 仍在运行，下次上报可能重新出现——\n' +
    '想永久移除，请到该机器上运行「复制卸载命令」。'
  )) return;
  try {
    const r = await fetch('/api/agents/' + encodeURIComponent(uuid), { method: 'DELETE' });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    state.agents = state.agents.filter(x => x.uuid !== uuid);
    state.selected.delete(uuid);
    requestRender();
  } catch (e) {
    alert('删除失败：' + e.message);
  }
}

// 批量删除（仅管理员）。一次 DELETE /api/agents，body {uuids:[...]}。
async function batchDeleteAgents() {
  const uuids = [...state.selected];
  if (!uuids.length) return;
  if (!confirm(
    '确定删除已选 ' + uuids.length + ' 台机器？\n' +
    '仅面板移除；若 agent 仍在运行可能复活。'
  )) return;
  try {
    const r = await fetch('/api/agents', {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ uuids }),
    });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const set = new Set(uuids);
    state.agents = state.agents.filter(a => !set.has(a.uuid));
    state.selected.clear();
    requestRender();
  } catch (e) {
    alert('批量删除失败：' + e.message);
  }
}

// 批量改分组：弹窗输入（prompt）新分组名（留空 = 未分组）；只改选中机器的分组，其它字段保留。
// ============ 通用居中浮层（统一三个批量操作弹窗） ============

// 打开一个居中浮层；返回 { mask, box, ok, cancel, close }
// body 内容由调用方在 box 里追加（用 insertBefore 到 .gp-actions 之前）
function openCenterModal(title) {
  closeCenterModal();
  const mask = document.createElement('div');
  mask.className = 'gp-mask';
  mask.id = 'gpMask';

  const box = document.createElement('div');
  box.className = 'gp-box';

  const t = document.createElement('div');
  t.className = 'gp-title';
  t.textContent = title;
  box.appendChild(t);

  const actions = document.createElement('div');
  actions.className = 'gp-actions';
  const cancel = document.createElement('button');
  cancel.className = 'gp-btn';
  cancel.textContent = '取消';
  const ok = document.createElement('button');
  ok.className = 'gp-btn ok';
  ok.textContent = '确认';
  actions.appendChild(cancel);
  actions.appendChild(ok);
  box.appendChild(actions);

  mask.appendChild(box);
  document.body.appendChild(mask);

  function close() { closeCenterModal(); }
  cancel.onclick = close;
  mask.onclick = (e) => { if (e.target === mask) close(); };

  return { mask, box, ok, cancel, close };
}

function closeCenterModal() {
  const m = document.getElementById('gpMask');
  if (m && m.parentNode) m.parentNode.removeChild(m);
}

// 批量改分组：下拉选择已有分组
async function batchChangeGroup() {
  const uuids = [...state.selected];
  if (!uuids.length) return;
  const sel = document.createElement('select');
  sel.className = 'gp-select';
  const optNone = document.createElement('option');
  optNone.value = '';
  optNone.textContent = '（未分组）';
  sel.appendChild(optNone);
  [...state.groups].sort((x, y) => x.localeCompare(y, 'zh')).forEach(g => {
    const o = document.createElement('option');
    o.value = g;
    o.textContent = g;
    sel.appendChild(o);
  });
  const m = openCenterModal('批量改分组（已选 ' + uuids.length + ' 台）');
  m.box.insertBefore(sel, m.box.querySelector('.gp-actions'));
  m.ok.onclick = async () => {
    const group = sel.value;
    m.ok.disabled = true;
    m.ok.textContent = '处理中…';
    try {
      const r = await fetch('/api/agents', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ uuids, group }),
      });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      patchLocalAgents(uuids, { group });
      pinGroupOverrides(uuids, group);
      state.selected.clear();
      requestRender();
      m.close();
    } catch (e) {
      alert('批量改分组失败：' + e.message);
      m.ok.disabled = false;
      m.ok.textContent = '确认';
    }
  };
}

// 批量设备注
async function batchSetRemark() {
  const uuids = [...state.selected];
  if (!uuids.length) return;
  const inp = document.createElement('input');
  inp.type = 'text';
  inp.className = 'gp-input';
  inp.placeholder = '请输入设备注（留空 = 清空）';
  const m = openCenterModal('批量设备注（已选 ' + uuids.length + ' 台）');
  m.box.insertBefore(inp, m.box.querySelector('.gp-actions'));
  setTimeout(() => inp.focus(), 0);
  inp.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); m.ok.click(); }
  });
  m.ok.onclick = async () => {
    const remark = inp.value;
    m.ok.disabled = true;
    m.ok.textContent = '处理中…';
    try {
      const r = await fetch('/api/agents', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ uuids, remark }),
      });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      patchLocalAgents(uuids, { remark });
      state.selected.clear();
      requestRender();
      m.close();
    } catch (e) {
      alert('批量设备注失败：' + e.message);
      m.ok.disabled = false;
      m.ok.textContent = '确认';
    }
  };
}

// 批量设到期：YYYY-MM-DD / YYYY.MM.DD / YYYY/MM/DD 任一分隔符都识别；留空 = 清空
async function batchSetExpire() {
  const uuids = [...state.selected];
  if (!uuids.length) return;
  const inp = document.createElement('input');
  inp.type = 'text';
  inp.className = 'gp-input';
  inp.placeholder = 'YYYY-MM-DD 或 YYYY/MM/DD 或 YYYY.MM.DD（留空 = 清空）';
  const m = openCenterModal('批量设到期时间（已选 ' + uuids.length + ' 台）');
  m.box.insertBefore(inp, m.box.querySelector('.gp-actions'));
  setTimeout(() => inp.focus(), 0);
  inp.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); m.ok.click(); }
  });
  m.ok.onclick = async () => {
    const v = inp.value.trim();
    let expireAt = null;
    if (v) {
      const d = parseFlexibleDate(v);
      if (!d) {
        alert('日期格式不正确：' + v + '\n示例：2027-3-3 / 2027.5.18 / 2027/11/01');
        return;
      }
      expireAt = Math.floor(d.getTime() / 1000);
    }
    m.ok.disabled = true;
    m.ok.textContent = '处理中…';
    try {
      const r = await fetch('/api/agents', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ uuids, expire_at: expireAt }),
      });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      patchLocalAgents(uuids, { expire_at: expireAt });
      state.selected.clear();
      requestRender();
      m.close();
    } catch (e) {
      alert('批量设到期失败：' + e.message);
      m.ok.disabled = false;
      m.ok.textContent = '确认';
    }
  };
}

// 灵活日期解析：YYYY[-./]MM[-./]DD
function parseFlexibleDate(s) {
  const m = s.match(/^(\d{4})[-./](\d{1,2})[-./](\d{1,2})$/);
  if (!m) return null;
  const y = parseInt(m[1], 10);
  const mo = parseInt(m[2], 10);
  const d = parseInt(m[3], 10);
  if (mo < 1 || mo > 12 || d < 1 || d > 31) return null;
  const date = new Date(y, mo - 1, d, 23, 59, 59);
  if (isNaN(date.getTime())) return null;
  return date;
}


// 从服务端拉取卸载命令并写入剪贴板（仅管理员；编辑弹窗里点「复制卸载命令」调用）。
async function copyUninstallCommand() {
  try {
    const r = await fetch('/api/uninstall-command');
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const data = await r.json();
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(data.command);
    } else {
      // 旧浏览器降级
      const ta = document.createElement('textarea');
      ta.value = data.command; ta.style.position = 'fixed'; ta.style.opacity = '0';
      document.body.appendChild(ta); ta.select();
      document.execCommand('copy'); document.body.removeChild(ta);
    }
    alert(
      '卸载命令已复制到剪贴板。\n\n' + data.command + '\n\n' +
      '请到目标客户端上以 root 身份粘贴执行：\n' +
      '1) 通知服务端删除该机器记录\n2) 停掉并清理本机 agent 进程/服务/文件'
    );
  } catch (e) {
    alert('获取/复制卸载命令失败：' + e.message);
  }
}
