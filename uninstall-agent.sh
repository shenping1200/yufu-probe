#!/usr/bin/env bash
# 渔夫探针 (YuFu Probe) 客户端卸载 / 注销
# 一条命令：通知服务端删除本机记录 + 停止并清理本机 agent 进程、文件与容器。
# 原生 agent 与旧版 Docker agent 均支持。
#
# 用法：
#   bash uninstall-agent.sh <服务端地址> <AgentToken>
#
#   服务端地址支持以下任意一种写法（脚本会自动归一化）：
#     ws://1.2.3.4:39689          内网、未启用 TLS（最常用）
#     wss://example.com:39689     公网、已启用 HTTPS/WSS
#     http://1.2.3.4:39689        直接写 http 也可以
#     1.2.3.4:39689               裸地址，自动按 http 处理
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
[ -z "$SERVER_IN" ] && err "缺少参数：服务端地址，例如 ws://1.2.3.4:39689 或 1.2.3.4:39689"
[ -z "$TOKEN" ]     && err "缺少参数：Agent Token（与服务端 install.sh 安装时一致）"

# ---------- 地址归一化（兼容 http / https / ws / wss / 裸地址）----------
# 关键点：http://1.2.3.4:39689 不能再加前缀，否则会被拼成 http://http://...
#         导致 curl 报 “Could not resolve host: http” 而静默失败。
case "$SERVER_IN" in
  http://*|https://*) HTTP_BASE="$SERVER_IN"; SERVER="$SERVER_IN" ;;
  wss://*)            HTTP_BASE="https://${SERVER_IN#wss://}"; SERVER="$SERVER_IN" ;;
  ws://*)             HTTP_BASE="http://${SERVER_IN#ws://}";  SERVER="$SERVER_IN" ;;
  *)                  HTTP_BASE="http://$SERVER_IN"; SERVER="ws://$SERVER_IN" ;;
esac

command -v curl >/dev/null 2>&1 || err "未检测到 curl"

# ---------- 探测本机已安装的 agent，收集所有 UUID ----------
UUIDS=()
HAS_NATIVE=0
HAS_DOCKER=0
UUID_FILE=/var/lib/yufu-agent/uuid

# 1) 原生 agent
if [ -f "$UUID_FILE" ]; then
  u=$(cat "$UUID_FILE" 2>/dev/null | tr -d '[:space:]')
  if [ -n "$u" ]; then UUIDS+=("$u"); HAS_NATIVE=1; fi
fi

# 2) Docker agent（兼容 yufu-agent 与 probe-agent 两种容器命名，并按进程兜底）
if command -v docker >/dev/null 2>&1; then
  # 2a) 按容器名匹配（旧版镜像容器名可能是 yufu-agent 或 probe-agent）
  for c in $(docker ps -q --filter "name=yufu-agent" 2>/dev/null) \
           $(docker ps -q --filter "name=probe-agent" 2>/dev/null); do
    u=$(docker exec "$c" cat /data/uuid 2>/dev/null | tr -d '[:space:]')
    [ -n "$u" ] && UUIDS+=("$u") && HAS_DOCKER=1
  done
  # 2b) 按进程兜底（防止容器名被改，只要进程是 /app/probe-agent 就认）
  for c in $(docker ps -q 2>/dev/null); do
    if docker top "$c" 2>/dev/null | grep -q 'probe-agent'; then
      u=$(docker exec "$c" cat /data/uuid 2>/dev/null | tr -d '[:space:]')
      [ -n "$u" ] && UUIDS+=("$u") && HAS_DOCKER=1
    fi
  done
fi

# 去重
if [ "${#UUIDS[@]}" -gt 0 ]; then
  UNIQ=$(printf '%s\n' "${UUIDS[@]}" | sort -u)
  UUIDS=()
  while IFS= read -r line; do [ -n "$line" ] && UUIDS+=("$line"); done <<< "$UNIQ"
fi

# ---------- 1) 通知服务端注销（核心：让服务端立即删除记录）----------
if [ "${#UUIDS[@]}" -gt 0 ]; then
  for u in "${UUIDS[@]}"; do
    info "通知服务端注销 agent ($u) ..."
    if curl -fsS -X DELETE -H "Authorization: Bearer $TOKEN" "${HTTP_BASE}/api/agents/${u}"; then
      ok "服务端已删除该机器记录 ($u)"
    else
      warn "服务端注销请求失败（可能服务端版本较旧或网络不通）；稍后可在面板手动删除"
    fi
  done
else
  warn "未在本机发现已安装的 agent（无 UUID 文件、无运行中的相关容器）"
fi

# ---------- 2) 停止并清理本机 agent ----------
# 2a) 原生 agent：停服务 + 删二进制 / 服务 / 配置 / uuid 目录
if [ "$HAS_NATIVE" = "1" ]; then
  systemctl stop yufu-agent 2>/dev/null || pkill -f /usr/local/bin/yufu-agent 2>/dev/null || true
  systemctl disable yufu-agent 2>/dev/null || true
  rm -f /etc/systemd/system/yufu-agent.service /usr/local/bin/yufu-agent /etc/yufu-agent.conf
  rm -rf /var/lib/yufu-agent
  systemctl daemon-reload 2>/dev/null || true
  ok "原生 agent 已卸载并清理"
fi

# 2b) Docker agent：按名 + 按进程双保险删除容器（覆盖 yufu-agent / probe-agent 命名）
if command -v docker >/dev/null 2>&1; then
  docker rm -f yufu-agent probe-agent 2>/dev/null || true
  for c in $(docker ps -q 2>/dev/null); do
    if docker top "$c" 2>/dev/null | grep -q 'probe-agent'; then
      docker rm -f "$c" 2>/dev/null || true
      HAS_DOCKER=1
    fi
  done
fi
if [ "$HAS_DOCKER" = "1" ]; then
  ok "Docker agent 已卸载并清理"
fi

echo ""
echo "=========================================="
echo "  渔夫探针 Agent 卸载完成"
echo "=========================================="
[ "${#UUIDS[@]}" -gt 0 ] && echo "  已注销 UUID: ${UUIDS[*]}"
echo "  服务端:  $SERVER"
echo ""
echo "  刷新面板，该机器应已消失。"
echo "  （本机仅清理了 agent，未触碰服务端进程与配置）"
