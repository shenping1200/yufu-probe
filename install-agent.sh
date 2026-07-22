#!/usr/bin/env bash
# 渔夫探针 (YuFu Probe) 客户端一键安装
# 下载预编译的 Linux 静态二进制，注册 systemd 服务，秒级完成（无需 Docker / 无需编译）
#
# 用法：
#   bash install-agent.sh <服务端地址:端口> <AgentToken> [上报间隔秒]
#   bash install-agent.sh ws://1.2.3.4:39689  your-token
#   bash install-agent.sh 1.2.3.4:39689       your-token 5
#
# 环境变量：
#   REPO=owner/repo          源码仓库（默认 shenping1200/yufu-probe）
#   BRANCH=main              下载分支（默认 main）

set -e

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[34m'; NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
err()   { echo -e "${RED}[ERR]${NC} $*"; exit 1; }

REPO="${REPO:-shenping1200/yufu-probe}"
BRANCH="${BRANCH:-main}"
BASE="https://raw.githubusercontent.com/${REPO}/${BRANCH}/dist"

# ---------- 参数 ----------
SERVER_IN="$1"
TOKEN="$2"
INTERVAL="${3:-5}"

[ -z "$SERVER_IN" ] && err "缺少参数：服务端地址，例如 ws://1.2.3.4:39689"
[ -z "$TOKEN" ]    && err "缺少参数：Agent Token（服务端 install.sh 安装时设置的 Agent 接入 Token）"

# 归一化服务端地址：缺省补 ws:// 前缀
case "$SERVER_IN" in
  ws://*|wss://*) SERVER="$SERVER_IN" ;;
  *) SERVER="ws://${SERVER_IN}" ;;
esac

# ---------- 环境检查 ----------
OS=$(uname -s); ARCH=$(uname -m)
[ "$OS" = "Linux" ] || err "本脚本仅支持 Linux；其他系统请参考 README 从源码构建 agent"
case "$ARCH" in
  x86_64|amd64) BIN_ARCH=amd64 ;;
  aarch64|arm64) BIN_ARCH=arm64 ;;
  *) err "不支持的架构: $ARCH（仅支持 x86_64 / aarch64）" ;;
esac

command -v curl >/dev/null 2>&1 || err "未检测到 curl"

# ---------- 下载静态二进制 ----------
URL="${BASE}/yufu-agent-linux-${BIN_ARCH}"
info "下载 agent 二进制: $URL"
TMP=$(mktemp /tmp/yufu-agent.XXXXXX)
if ! curl -fsSL "$URL" -o "$TMP"; then
  rm -f "$TMP"
  err "下载失败。请确认仓库 ${REPO} 的 dist/ 下存在 yufu-agent-linux-${BIN_ARCH}（可改用源码构建，见 README）。"
fi
install -m 0755 "$TMP" /usr/local/bin/yufu-agent
rm -f "$TMP"
ok "已安装 /usr/local/bin/yufu-agent"

# ---------- 配置文件 ----------
CONFIG_DIR=/etc
CONF="${CONFIG_DIR}/yufu-agent.conf"
UUID_DIR=/var/lib/yufu-agent
UUID_FILE="${UUID_DIR}/uuid"
mkdir -p "$UUID_DIR"
info "写入配置: $CONF"
cat > "$CONF" <<EOF
# 渔夫探针 Agent 配置（由 install-agent.sh 生成）
SERVER=${SERVER}
TOKEN=${TOKEN}
INTERVAL=${INTERVAL}
UUID_FILE=${UUID_FILE}
EOF

# ---------- 注册 systemd 服务 ----------
if command -v systemctl >/dev/null 2>&1; then
  info "注册 systemd 服务 yufu-agent"
  cat > /etc/systemd/system/yufu-agent.service <<EOF
[Unit]
Description=YuFu Probe Agent
After=network.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=${CONF}
ExecStart=/usr/local/bin/yufu-agent
Restart=always
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable yufu-agent
  systemctl restart yufu-agent
  sleep 2
  if systemctl is-active --quiet yufu-agent; then
    ok "服务已启动：systemctl status yufu-agent"
  else
    err "服务启动失败，请查看：journalctl -u yufu-agent -n 50"
  fi
else
  # 无 systemd（如部分容器）：用 nohup 兜底
  info "未检测到 systemd，使用 nohup 后台运行"
  pkill -f /usr/local/bin/yufu-agent 2>/dev/null || true
  nohup /usr/local/bin/yufu-agent >/var/log/yufu-agent.log 2>&1 &
  ok "已在后台启动（日志：/var/log/yufu-agent.log）"
fi

echo ""
echo "=========================================="
echo "  渔夫探针 Agent 安装完成"
echo "=========================================="
echo "  服务端:  $SERVER"
echo "  配置:    $CONF"
echo "  管理命令:"
echo "    systemctl status yufu-agent"
echo "    systemctl restart yufu-agent"
echo "    systemctl stop yufu-agent"
echo ""
echo "  稍后到服务端面板即可看到本机（OS / 配置 / 流量均为宿主机真实数据）"
