#!/usr/bin/env bash
# 渔夫探针 (YuFu Probe) 一键安装脚本
# 支持：自定义端口、管理员账号/密码、Agent Token、域名绑定（HTTP）
# 用法：
#   方式1：git clone 后执行  bash install.sh
#   方式2：curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/install.sh | bash

set -e

REPO_URL="${REPO_URL:-https://github.com/shenping1200/yufu-probe.git}"
REPO_RAW="${REPO_RAW:-https://raw.githubusercontent.com/shenping1200/yufu-probe/main}"
INSTALL_DIR="${INSTALL_DIR:-/opt/yufu-probe}"
COMPOSE="docker compose"

# ---------- 工具函数 ----------
info() { echo -e "\033[34m[INFO]\033[0m $*"; }
ok() { echo -e "\033[32m[OK]\033[0m $*"; }
warn() { echo -e "\033[33m[WARN]\033[0m $*"; }
err() { echo -e "\033[31m[ERR]\033[0m $*"; exit 1; }

random_str() { tr -dc 'a-zA-Z0-9' </dev/urandom | head -c "$1"; }

# ---------- 环境检查 ----------
check_env() {
  info "检查运行环境..."

  OS=$(uname -s)
  ARCH=$(uname -m)
  if [[ "$OS" != "Linux" ]]; then
    err "本脚本仅支持 Linux 系统，当前系统: $OS"
  fi
  if [[ "$ARCH" != "x86_64" && "$ARCH" != "aarch64" && "$ARCH" != "arm64" ]]; then
    err "本脚本仅支持 x86_64 / aarch64 架构，当前架构: $ARCH"
  fi

  if ! command -v docker &>/dev/null; then
    err "未检测到 Docker，请先安装 Docker: https://docs.docker.com/engine/install/"
  fi

  if docker compose version &>/dev/null; then
    COMPOSE="docker compose"
  elif docker-compose version &>/dev/null; then
    COMPOSE="docker-compose"
  else
    err "未检测到 docker compose 插件或 docker-compose，请先安装"
  fi

  ok "环境检查通过"
}

# ---------- 获取代码 ----------
prepare_code() {
  # 如果当前目录存在 docker-compose.yml，认为是本地运行
  if [[ -f "docker-compose.yml" && -f "Dockerfile.server" ]]; then
    INSTALL_DIR="$(pwd)"
    info "检测到本地源码，使用当前目录: $INSTALL_DIR"
    # 若是 git 仓库，先拉取最新（确保 install.sh / Dockerfile 等是最新版，
    # 否则重跑旧脚本会沿用旧的本地编译逻辑，看不到预编译镜像提速）
    if [[ -d "$INSTALL_DIR/.git" ]]; then
      info "拉取最新代码..."
      git pull --rebase --autostash 2>/dev/null \
        || warn "git pull 失败，继续使用当前代码（如需最新请先 git pull）"
    fi
    return
  fi

  info "准备安装目录: $INSTALL_DIR"
  if [[ -d "$INSTALL_DIR/.git" ]]; then
    info "目录已存在，拉取最新代码..."
    (cd "$INSTALL_DIR" && git pull --rebase)
  else
    rm -rf "$INSTALL_DIR"
    git clone --depth=1 "$REPO_URL" "$INSTALL_DIR"
  fi
  cd "$INSTALL_DIR"
}

# ---------- 交互式配置 ----------
read_config() {
  echo ""
  info "请按提示输入配置，直接回车使用默认值"
  echo ""

  read -rp "监听端口 [39689]: " PORT
  PORT=${PORT:-39689}
  if ! [[ "$PORT" =~ ^[0-9]+$ && "$PORT" -ge 1 && "$PORT" -le 65535 ]]; then
    err "端口号无效: $PORT"
  fi

  # Web SSH 连接密码（方案 A：连接客户端时一次静态口令确认，服务端校验）。
  # 留空则复用管理员密码；建议新装时单独设置一个，与管理员密码解耦。
  read -rp "Web SSH 连接密码（留空则复用管理员密码）: " SSH_PASSWORD
  info "解封：SSH 密码错误 5 次会锁定该客户端 24h；运维可执行 'docker compose exec server probe-server unlock <uuid>' 解封（不带 uuid 则解全部），无需在此设置"

  read -rp "管理员用户名 [admin]: " ADMIN_USER
  ADMIN_USER=${ADMIN_USER:-admin}

  DEFAULT_PASS="admin"
  read -rp "管理员密码 [$DEFAULT_PASS]: " ADMIN_PASS
  ADMIN_PASS=${ADMIN_PASS:-$DEFAULT_PASS}

  DEFAULT_TOKEN="change-me-agent-token"
  read -rp "Agent 接入 Token [$DEFAULT_TOKEN]: " AGENT_TOKEN
  AGENT_TOKEN=${AGENT_TOKEN:-$DEFAULT_TOKEN}

  read -rp "绑定域名（留空则使用 IP:端口 访问）: " DOMAIN

  read -rp "安装目录 [$INSTALL_DIR]: " INPUT_DIR
  if [[ -n "$INPUT_DIR" ]]; then
    INSTALL_DIR="$INPUT_DIR"
  fi
}

# ---------- 生成配置文件 ----------
generate_config() {
  info "生成服务端配置..."

  mkdir -p configs

  # 把升级脚本也放进安装目录，方便日后「cd $INSTALL_DIR && bash upgrade.sh」原地升级
  # （脚本自带从仓库 raw 下载的兜底，避免旧版/局部安装缺失该文件）
  if [[ -f "upgrade.sh" ]]; then
    cp -f "upgrade.sh" "$INSTALL_DIR/" && info "已放置升级脚本 → $INSTALL_DIR/upgrade.sh"
  elif curl -fsSL "${REPO_RAW}/upgrade.sh" -o "$INSTALL_DIR/upgrade.sh" 2>/dev/null; then
    info "已下载升级脚本 → $INSTALL_DIR/upgrade.sh"
  else
    warn "未能准备 upgrade.sh（日后升级可用 curl 直接拉取，不影响当前使用）"
  fi

  cat > configs/server.yaml <<EOF
listen: 0.0.0.0
# 探针面板监听端口
port: ${PORT}
tls:
  enabled: false
  cert: ""
  key: ""
# 客户端接入令牌
agent_token: "${AGENT_TOKEN}"
# Web SSH 连接密码（留空则复用下方管理员密码）；连接客户端时做一次静态口令确认
ssh_password: "${SSH_PASSWORD}"
admin:
  username: ${ADMIN_USER}
  password: ${ADMIN_PASS}
# SQLite 数据库路径（容器内相对 /app）
db_path: data/probe.db
EOF

  # 使用 GHCR 预编译镜像（CI 已编好 amd64/arm64），VPS 零编译、安装更快更稳。
  # 配置 server.yaml 改为运行时挂载，覆盖镜像内置的默认配置。
  cat > docker-compose.yml <<EOF
services:
  server:
    image: ghcr.io/shenping1200/yufu-probe:latest
    container_name: probe-server
    ports:
      - "${PORT}:${PORT}"
    volumes:
      - ./configs/server.yaml:/app/configs/server.yaml:ro
      - probe-data:/app/data
    restart: unless-stopped
EOF

  if [[ -n "$DOMAIN" ]]; then
    info "生成 Caddy 反向代理配置（HTTP）..."
    # 注意：Caddy v2 指令为 reverse_proxy（带 y）。
    # 上游必须用 docker compose 同网络的服务名 server，不能写 127.0.0.1
    # （127.0.0.1 在 caddy 容器里指代 caddy 自己，不是宿主机/也不是 probe-server）。
    cat > Caddyfile <<EOF
http://${DOMAIN} {
    # 由 install.sh 生成：反向代理到同 compose 网络的 probe-server
    reverse_proxy server:${PORT}
}
EOF
    cat >> docker-compose.yml <<EOF

  caddy:
    image: caddy:2-alpine
    container_name: probe-caddy
    ports:
      - "80:80"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
      - caddy-config:/config
    restart: unless-stopped
EOF
  fi

  cat >> docker-compose.yml <<EOF

volumes:
  probe-data:
EOF

  if [[ -n "$DOMAIN" ]]; then
    cat >> docker-compose.yml <<EOF
  caddy-data:
  caddy-config:
EOF
  fi

  ok "配置已生成"
}

# ---------- 启动服务 ----------
start_services() {
  info "拉取预编译镜像并启动服务..."
  $COMPOSE down 2>/dev/null || true
  # 优先使用 GHCR 预编译镜像（零编译，1C1G 等低配机器秒级拉起）
  if $COMPOSE pull server 2>/dev/null; then
    $COMPOSE up -d
    ok "服务已启动（使用预编译镜像，无需本地编译）"
  else
    warn "预编译镜像拉取失败，回退到本地编译（较慢，可能受机器内存限制）..."
    $COMPOSE up -d --build
    ok "服务已启动（本地编译）"
  fi
}

# ---------- 本机原生自监控 ----------
setup_self_monitor() {
  case "$ARCH" in
    x86_64|amd64) BIN_ARCH=amd64 ;;
    aarch64|arm64) BIN_ARCH=arm64 ;;
    *) warn "跳过自监控：不支持的架构 $ARCH"; return 0 ;;
  esac

  LOCAL_BIN_FILE="./dist/yufu-agent-linux-${BIN_ARCH}"
  if [ ! -f "$LOCAL_BIN_FILE" ]; then
    warn "未找到本地二进制 $LOCAL_BIN_FILE，跳过自监控（可后续手动在该机运行 install-agent.sh）"
    return 0
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    warn "未检测到 systemd，跳过自监控 agent（可改用 install-agent.sh 的 nohup 兜底）"
    return 0
  fi

  info "为本机安装原生自监控 agent（读取宿主机真实系统/流量，不再误报 Alpine）..."
  LOCAL_BIN="$LOCAL_BIN_FILE" bash ./install-agent.sh "ws://127.0.0.1:${PORT}" "${AGENT_TOKEN}" 5
  ok "自监控 agent 已注册（systemctl status yufu-agent 查看）"
}

# ---------- 输出信息 ----------
print_summary() {
  LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "<本机IP>")

  echo ""
  echo "=========================================="
  echo "  渔夫探针 (YuFu Probe) 安装完成"
  echo "=========================================="
  echo ""
  if [[ -n "$DOMAIN" ]]; then
    echo "  访问地址: http://${DOMAIN}"
  fi
  echo "  访问地址: http://${LOCAL_IP}:${PORT}"
  echo "  管理员用户名: ${ADMIN_USER}"
  echo "  管理员密码: ${ADMIN_PASS}"
  echo "  Agent Token: ${AGENT_TOKEN}"
  if [[ -n "$SSH_PASSWORD" ]]; then
    echo "  Web SSH 密码: ${SSH_PASSWORD}"
  else
    echo "  Web SSH 密码: （未单独设置，复用管理员密码）"
  fi
  echo "  Web SSH 锁定: 密码错误 5 次锁定该客户端 24 小时（docker compose exec server probe-server unlock <uuid> 解单台，不带 uuid 解全部）"
  echo ""
  echo "  安装目录: ${INSTALL_DIR}"
  echo "  常用命令:"
  echo "    cd ${INSTALL_DIR}"
  echo "    $COMPOSE ps          # 查看服务端状态"
  echo "    $COMPOSE logs -f     # 查看服务端日志"
  echo "    $COMPOSE down        # 停止服务端"
  echo "    systemctl status yufu-agent   # 查看本机自监控 agent"
  echo "    systemctl restart yufu-agent   # 重启自监控 agent"
  echo ""
  echo "  在其他机器安装 Agent（秒级，无需 Docker）:"
  echo "    bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/install-agent.sh) \\"
  echo "      ws://${LOCAL_IP}:${PORT} ${AGENT_TOKEN}"
  echo ""
  echo "  卸载某台客户端（一条命令，服务端立即移除该机器）:"
  echo "    bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/uninstall-agent.sh) \\"
  echo "      ws://${LOCAL_IP}:${PORT} ${AGENT_TOKEN}"
  echo ""
  echo "  （Agent 以宿主机原生进程运行，OS/配置/流量均为真实宿主机数据；"
  echo "   如需 Docker 方式，见 README 的「Agent Docker 方式」）"
  echo ""
  echo "=========================================="
  echo ""
}

# ---------- 卸载：服务端 ----------
uninstall_server() {
  info "开始卸载服务端..."
  if [[ -d "$INSTALL_DIR" ]]; then
    ( cd "$INSTALL_DIR" 2>/dev/null && command -v docker &>/dev/null && \
      { $COMPOSE down -v 2>/dev/null || docker compose down -v 2>/dev/null; } ) || true
    rm -rf "$INSTALL_DIR"
    ok "已删除安装目录 $INSTALL_DIR"
  else
    warn "未找到默认安装目录 $INSTALL_DIR（若曾自定义目录，请手动删除对应目录）"
  fi
  # 兜底：清理可能残留的容器与数据卷
  command -v docker &>/dev/null && {
    docker rm -f probe-server probe-caddy 2>/dev/null || true
    docker volume rm yufu-probe_probe-data 2>/dev/null || true
  } || true
  # 清理本机自监控 agent（服务端安装时一并安装了 yufu-agent 自监控）
  systemctl stop yufu-agent 2>/dev/null || true
  systemctl disable yufu-agent 2>/dev/null || true
  rm -f /etc/systemd/system/yufu-agent.service /usr/local/bin/yufu-agent /etc/yufu-agent.conf
  rm -rf /var/lib/yufu-agent
  systemctl daemon-reload 2>/dev/null || true
  ok "服务端卸载完成（含容器、数据卷与本机自监控 agent 清理）"
}

# ---------- 卸载：客户端（自动识别本机 agent 配置，绝不手输）----------
uninstall_client() {
  info "开始卸载客户端（本机 agent）..."
  local CLI_SERVER="" CLI_TOKEN=""
  local CONF="/etc/yufu-agent.conf"
  if [[ -f "$CONF" ]]; then
    CLI_SERVER=$(grep -E '^[[:space:]]*SERVER=' "$CONF" | head -n1 | cut -d= -f2- | tr -d '[:space:]')
    CLI_TOKEN=$(grep -E  '^[[:space:]]*TOKEN='  "$CONF" | head -n1 | cut -d= -f2- | tr -d '[:space:]')
  fi
  if [[ -n "$CLI_SERVER" && -n "$CLI_TOKEN" ]]; then
    info "已自动识别本机 agent 配置：服务端=$CLI_SERVER（无需手动输入）"
    local UA_URL="https://raw.githubusercontent.com/shenping1200/yufu-probe/main/uninstall-agent.sh"
    if curl -fsSL "$UA_URL" -o "/tmp/uninstall-agent.$$.sh" 2>/dev/null; then
      info "调用 uninstall-agent.sh 清理本机 agent 并通知服务端删除记录..."
      bash "/tmp/uninstall-agent.$$.sh" "$CLI_SERVER" "$CLI_TOKEN" \
        || warn "uninstall-agent.sh 返回非0（可能服务端已不可达）；本机 agent 由下方兜底清理，服务端记录可到面板手动删除"
      rm -f "/tmp/uninstall-agent.$$.sh"
    else
      warn "无法下载 uninstall-agent.sh（网络异常），改为仅清理本机 agent"
    fi
  else
    warn "未在 $CONF 找到服务端地址或 Token（agent 可能已被部分清理），仅清理本机 agent 文件与服务"
  fi
  # 兜底：无论上述是否成功，都把本机 agent 文件/服务/进程清干净（绝不手输）
  systemctl stop yufu-agent 2>/dev/null || pkill -f /usr/local/bin/yufu-agent 2>/dev/null || true
  systemctl disable yufu-agent 2>/dev/null || true
  rm -f /etc/systemd/system/yufu-agent.service /usr/local/bin/yufu-agent /etc/yufu-agent.conf
  rm -rf /var/lib/yufu-agent
  systemctl daemon-reload 2>/dev/null || true
  ok "本机 agent 已清理"
}

# ---------- 卸载：子菜单（含二次确认）----------
do_uninstall() {
  echo ""
  echo "========== 卸载 =========="
  echo "1) 卸载服务端"
  echo "2) 卸载客户端"
  echo "3) 卸载全部（服务端 + 本机客户端）"
  echo "=========================="
  read -rp "请选择 [1-3]: " sub || true

  local desc=""
  case "$sub" in
    1) desc="将卸载服务端：停止并删除容器/数据卷，删除安装目录 $INSTALL_DIR，并清理本机自监控 agent（服务残留一并清除）。" ;;
    2) desc="将卸载本机客户端 agent：停止进程、删除文件，并自动识别本机配置后通知服务端删除本机记录。" ;;
    3) desc="将卸载全部：服务端（容器/数据卷/安装目录/自监控）+ 本机客户端 agent（进程/文件/服务端记录）一次性清理。" ;;
    *) err "无效选择: $sub" ;;
  esac

  echo -e "\033[33m[确认]\033[0m $desc"
  read -rp "确认执行以上卸载操作？[y/N]: " sure || true
  case "$sure" in
    y|Y|yes|YES) ;;
    *) info "已取消，未做任何改动"; return ;;
  esac

  case "$sub" in
    1) uninstall_server ;;
    2) uninstall_client ;;
    3) uninstall_client; uninstall_server ;;
  esac
}

# ---------- 安装（保持原有逻辑不变）----------
do_install() {
  check_env
  read_config
  prepare_code
  generate_config
  start_services
  setup_self_monitor
  print_summary
}

# ---------- 入口：交互显示菜单；非交互（如 curl|bash 流水线）默认直接安装 ----------
if [[ -t 0 ]]; then
  echo ""
  echo "========== 渔夫探针 (YuFu Probe) =========="
  echo "1) 安装"
  echo "2) 卸载"
  echo "=========================================="
  read -rp "请选择 [1/2]: " top || true
  case "$top" in
    1) do_install ;;
    2) do_uninstall ;;
    *) err "无效选择: $top" ;;
  esac
else
  do_install
fi
