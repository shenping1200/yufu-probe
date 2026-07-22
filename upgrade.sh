#!/usr/bin/env bash
# 渔夫探针 (YuFu Probe) 本地升级脚本
# 用法（在已安装服务端的那台 VPS 上执行）：
#   方式1：cd /opt/yufu-probe && bash upgrade.sh
#   方式2：bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/upgrade.sh)
# 作用：
#   1) 拉取最新 GHCR 预编译镜像（前端已通过 Go embed 打进镜像，
#      拉新镜像 = 拿到新 UI；不需要也不应该 git pull，避免覆盖本地生成的 compose/config）
#   2) 重启容器
# 已注册的客户端与 SQLite 数据库均保留（只重启容器，卷与挂载不动）。

set -e

# 安装目录：默认 /opt/yufu-probe，可通过环境变量覆盖
INSTALL_DIR="${INSTALL_DIR:-/opt/yufu-probe}"
COMPOSE="docker compose"

# ---------- 工具函数 ----------
info() { echo -e "\033[34m[INFO]\033[0m $*"; }
ok()   { echo -e "\033[32m[OK]\033[0m $*"; }
warn() { echo -e "\033[33m[WARN]\033[0m $*"; }
err()  { echo -e "\033[31m[ERR]\033[0m $*"; exit 1; }

# ---------- 环境检查 ----------
command -v docker >/dev/null || err "未检测到 docker，请先安装 Docker"
if docker compose version &>/dev/null; then
  COMPOSE="docker compose"
elif docker-compose version &>/dev/null; then
  COMPOSE="docker-compose"
else
  err "未检测到 docker compose 插件或 docker-compose"
fi

# 定位安装目录
if [ ! -d "$INSTALL_DIR" ]; then
  err "未找到安装目录 $INSTALL_DIR
如果你的安装目录不是默认值，请设置环境变量再执行：
  INSTALL_DIR=/your/path bash upgrade.sh"
fi
cd "$INSTALL_DIR" || err "无法进入 $INSTALL_DIR"

if [ ! -f docker-compose.yml ]; then
  err "$INSTALL_DIR 缺少 docker-compose.yml，不像已安装的目录
（请确认这是用 install.sh 安装的服务端目录）"
fi

ok "安装目录: $INSTALL_DIR"
ok "compose  : $COMPOSE"

# 注意：不执行 git pull。
# 原因：docker-compose.yml 与 configs/server.yaml 是 install.sh 在本机生成的，
#      与仓库里跟踪的模板会冲突，git pull --rebase --autostash 可能把这些文件覆盖掉，
#      导致端口/挂载全错、服务起不来。新 UI 已通过 embed 打进镜像，
#      拉新镜像即可拿到，无需动本机文件。

# ---------- 0. 兼容旧版（build 式）compose：转成 image 式 ----------
# 早期安装脚本生成的是 build: 本地编译式 compose，没有 image: 字段，
# 此时 docker compose pull 会直接 "Skipped No image to be pulled"，升级无效。
# 这里把 build: 块（无论缩进）换成 image:，并补上配置挂载（保留本机端口/账号/Token）。
if grep -q "build:" docker-compose.yml 2>/dev/null; then
  info "检测到旧版 build 式 compose，自动转换为预编译镜像方式..."
  python3 - <<'PY'
import re, os
p = 'docker-compose.yml'
s = open(p).read()

# 删除任意缩进下的 build: 映射块（build: 行 + 其下更深的子行），换成 image: 行
def repl(m):
    ind = m.group(1)
    return ind + "image: ghcr.io/shenping1200/yufu-probe:latest\n"

pat = re.compile(r'^(?P<ind>\s*)build:\n(?:(?P=ind)  [^\n]*\n)+', re.MULTILINE)
new_s, n = pat.subn(repl, s)
if n == 0:
    print("WARN: 未找到可识别的 build: 块，请手动把 server 服务的 build: 替换为：")
    print("  image: ghcr.io/shenping1200/yufu-probe:latest")
else:
    # 补上本机配置挂载（若存在），确保用上你的端口/账号/Token
    mount = "      - ./configs/server.yaml:/app/configs/server.yaml:ro\n"
    if "configs/server.yaml:ro" not in new_s and os.path.exists('configs/server.yaml'):
        new_s = new_s.replace("      - probe-data:/app/data\n",
                              mount + "      - probe-data:/app/data\n", 1)
    open(p, 'w').write(new_s)
    print(f"已转换 {n} 处 build -> image: ghcr.io/shenping1200/yufu-probe:latest，并挂载本机 server.yaml")
PY
  if [ $? -ne 0 ]; then warn "compose 转换失败，将继续尝试（若升级无效请检查 docker-compose.yml）"; fi
fi

# ---------- 1. 拉取最新预编译镜像 ----------
info "拉取最新 GHCR 预编译镜像..."
if $COMPOSE pull server; then
  ok "镜像已拉取"
  # 若转换没生效，compose 仍是 build 式，pull 会是空操作；这里明确提示
  if ! grep -q "ghcr.io/shenping1200/yufu-probe" docker-compose.yml 2>/dev/null; then
    warn "compose 仍未引用 GHCR 镜像（pull 可能无效），请检查 docker-compose.yml 里 server 服务是否为 image: 模式"
  fi
else
  err "拉取镜像失败，请检查网络或 GHCR 可达性（也可以临时用 install.sh 重装）"
fi

# ---------- 2. 重启服务 ----------
info "重启服务..."
$COMPOSE up -d
ok "升级完成"

# ---------- 状态输出 ----------
echo ""
echo "=========================================="
echo "  升级完成"
echo "=========================================="
echo "  已注册的客户端与数据库均保留。"
echo ""
echo "  运行中的服务端镜像："
$COMPOSE images server 2>/dev/null || docker inspect probe-server --format '  {{.Config.Image}}' 2>/dev/null
echo "  （应为 ghcr.io/shenping1200/yufu-probe:latest；若显示本地 build 镜像说明未生效）"
echo ""
echo "  常用命令："
echo "    cd $INSTALL_DIR"
echo "    $COMPOSE ps              # 查看容器状态"
echo "    $COMPOSE logs -f server  # 查看实时日志"
echo "    $COMPOSE restart server  # 手动重启服务端"
echo "=========================================="
