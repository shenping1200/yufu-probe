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
  const key = (platform || os || '').toLowerCase();
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
  buildGroupOptions();
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

  el.innerHTML = html;
  el.querySelectorAll('.group-tab').forEach(btn => {
    if (btn.id === 'newGroupBtn') return;
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
  const newBtn = document.getElementById('newGroupBtn');
  if (newBtn) newBtn.onclick = groupCreate;
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

// 新建分组：弹窗输入名称 → 注册到分组表（不需要任何客户端属于此分组）
async function groupCreate() {
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

// 把分组下拉框（<datalist>）的选项同步成当前 state.groups
function buildGroupOptions() {
  const dl = document.getElementById('group-options');
  if (!dl) return;
  dl.innerHTML = state.groups.map(g => `<option value="${escapeHtml(g)}"></option>`).join('');
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
    <div class="agent-card ${a.online ? '' : 'offline'}" data-uuid="${a.uuid}">
      <div class="card-header">
        <div class="card-title">
          <input class="card-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名">
          <button class="btn-edit" data-uuid="${a.uuid}" title="编辑名称/备注/分组/到期">✎</button>
        </div>
        <div class="card-status">
          <span class="dot ${a.online ? 'on' : 'off'}"></span>
          <span class="status-text ${a.online ? 'on' : 'off'}">${a.online ? '在线' : '离线'}</span>
        </div>
      </div>
      <div class="card-meta">
        <span class="card-os">${distroImg}</span>
        <span>${flagImg} ${escapeHtml(loc)} ${code ? '(' + escapeHtml(code) + ')' : ''}</span>
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
  const code = a.country_code || (isPrivateIP(a.ip) ? '内网' : '');
  const loc = a.country ? (a.country.replace(stripFlagEmoji(a.country) || '', '').trim()) : '';
  const flagImg = flagImage(a.country_code, loc);
  const cd = fmtCountdown(a.expire_at);
  const cdHtml = cd ? `<div class="cd-text ${cd.cls}" title="VPS 到期">📅 ${cd.text}</div>` : '';
  return `
    <tr>
      <td><span class="dot ${a.online ? 'on' : 'off'}"></span> <span class="status-text ${a.online ? 'on' : 'off'}">${a.online ? '在线' : '离线'}</span></td>
      <td><input class="list-name" data-uuid="${a.uuid}" value="${escapeHtml(alias)}" title="点击编辑别名"></td>
      <td><span class="flag">${flagImg}</span>${escapeHtml(loc)} ${code ? '(' + escapeHtml(code) + ')' : ''}<br><span style="color:var(--text-2);font-size:12px">${escapeHtml(a.ip)}</span></td>
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
