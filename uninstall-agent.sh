#!/usr/bin/env bash
# 渔夫探针 (YuFu Probe) 客户端卸载 / 注销
# 一条命令：通知服务端删除本机记录 + 停止并清理本机 agent 进程与文件。
# 原生 agent 与 Docker agent 均支持。
#
# 用法：
#   bash uninstall-agent.sh <服务端地址:端口> <AgentToken>
#   bash uninstall-agent.sh ws://1.2.3.4:39689  your-token
#   bash uninstall-agent.sh 1.2.3.4:39689       your-token
#
# 环境变量：
#   REPO=owner/repo          源码仓库（默认 shenping1200/yufu-probe，仅用于提示）

set -e

RED='\033[0;31m'; GREEN='\033[0;32m'; BLUE='\033[34m'; YELLOW='\033[33m'; NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()   { echo -e "${RED}[ERR]${NC} $*"; exit 1; }

REPO="${REPO:-shenping1200/yufu-probe}"

# ---------- 参数 ----------
SERVER_IN="$1"
TOKEN="$2"
[ -z "$SERVER_IN" ] && err "缺少参数：服务端地址，例如 ws://1.2.3.4:39689"
[ -z "$TOKEN" ]     && err "缺少参数：Agent Token（与服务端 install.sh 安装时一致）"

# 归一化为 ws:// 前缀，并推导 HTTP 基址（用于 DELETE 接口）
case "$SERVER_IN" in
  ws://*|wss://*) SERVER="$SERVER_IN" ;;
  *) SERVER="ws://${SERVER_IN}" ;;
esac
HTTP_BASE=$(echo "$SERVER" | sed -E 's#^wss://#https://#; s#^ws://#http://#')

command -v curl >/dev/null 2>&1 || err "未检测到 curl"

# ---------- 探测本机已安装的 agent，拿到 UUID ----------
MODE="none"
UUID=""
UUID_FILE=/var/lib/yufu-agent/uuid

if [ -f "$UUID_FILE" ]; then
  UUID=$(cat "$UUID_FILE" 2>/dev/null | tr -d '[:space:]')
  [ -n "$UUID" ] && MODE="native"
elif command -v docker >/dev/null 2>&1; then
  CID=$(docker ps -q --filter "name=^probe-agent$" --filter status=running | head -n1)
  if [ -n "$CID" ]; then
    UUID=$(docker exec "$CID" cat /data/uuid 2>/dev/null | tr -d '[:space:]' || true)
    [ -n "$UUID" ] && MODE="docker"
  fi
fi

# ---------- 1) 通知服务端注销（核心：让服务端立即删除记录） ----------
if [ -n "$UUID" ]; then
  info "通知服务端注销 agent ($UUID) ..."
  if curl -fsS -X DELETE -H "Authorization: Bearer $TOKEN" "${HTTP_BASE}/api/agents/${UUID}"; then
    ok "服务端已删除该机器记录"
  else
    warn "服务端注销请求失败（可能服务端版本较旧或网络不通）；稍后可在面板手动删除"
  fi
else
  warn "未在本机发现已安装的 agent（无 UUID 文件、无运行中的 probe-agent 容器）"
fi

# ---------- 2) 停止并清理本机 agent ----------
if [ "$MODE" = "native" ]; then
  systemctl stop yufu-agent 2>/dev/null || pkill -f /usr/local/bin/yufu-agent 2>/dev/null || true
  systemctl disable yufu-agent 2>/dev/null || true
  rm -f /etc/systemd/system/yufu-agent.service /usr/local/bin/yufu-agent /etc/yufu-agent.conf
  rm -rf /var/lib/yufu-agent
  systemctl daemon-reload 2>/dev/null || true
  ok "原生 agent 已卸载并清理"
elif [ "$MODE" = "docker" ]; then
  # docker stop 会发 SIGTERM，新版本 agent 收到后会再向服务端发一次注销
  docker stop probe-agent >/dev/null 2>&1 || true
  docker rm -f probe-agent >/dev/null 2>&1 || true
  ok "Docker agent 已卸载并清理"
fi

echo ""
echo "=========================================="
echo "  渔夫探针 Agent 卸载完成"
echo "=========================================="
echo "  模式:    $MODE"
[ -n "$UUID" ] && echo "  UUID:    $UUID"
echo "  服务端:  $SERVER"
echo ""
echo "  刷新面板，该机器应已消失。"
echo "  （本机仅清理了 agent，未触碰服务端进程与配置）"
