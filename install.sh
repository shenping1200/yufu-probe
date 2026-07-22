#!/usr/bin/env bash
# 渔夫探针 (YuFu Probe) 一键安装脚本
# 支持：自定义端口、管理员账号/密码、Agent Token、域名绑定（HTTP）
# 用法：
#   方式1：git clone 后执行  bash install.sh
#   方式2：curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/install.sh | bash

set -e

REPO_URL="${REPO_URL:-https://github.com/shenping1200/yufu-probe.git}"
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
admin:
  username: ${ADMIN_USER}
  password: ${ADMIN_PASS}
# SQLite 数据库路径（容器内相对 /app）
db_path: data/probe.db
EOF

  cat > docker-compose.yml <<EOF
services:
  server:
    build:
      context: .
      dockerfile: Dockerfile.server
    container_name: probe-server
    ports:
      - "${PORT}:${PORT}"
    volumes:
      - probe-data:/app/data
    restart: unless-stopped
EOF

  if [[ -n "$DOMAIN" ]]; then
    info "生成 Caddy 反向代理配置（HTTP）..."
    cat > Caddyfile <<EOF
http://${DOMAIN} {
    reverse_probe 127.0.0.1:${PORT}
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
  info "构建并启动服务..."
  $COMPOSE down 2>/dev/null || true
  $COMPOSE up -d --build
  ok "服务已启动"
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

# ---------- 主流程 ----------
check_env
read_config
prepare_code
generate_config
start_services
setup_self_monitor
print_summary
