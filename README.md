# 渔夫探针 (YuFu Probe)

一套类似「哪吒探针」的轻量服务器监控工具，包含**客户端（Agent）**、**服务端（Server）** 与 **Web 界面**，全部用 Go 编写，单二进制部署、零外部依赖（纯 Go SQLite，无需数据库服务）。

## 功能

- **客户端采集**：IP、开机时间、CPU 核心数、内存/磁盘总量、实时上下行流量速率、自然月累计流量、CPU / 内存 / 磁盘使用率
- **Web 面板**：卡片 / 列表双视图、汇总看板、机器配置展示、编辑机器名称与备注、单台实时流量曲线（ECharts）
- **深色 / 浅色主题**：一键切换，偏好自动保存
- **鉴权**：Web 端账号密码登录（session）+ 客户端 Token 接入，双保险
- **跨平台客户端**：Linux / Windows / macOS，amd64 / arm64
- **可选 TLS**：跨公网部署时启用 `wss` / `https`
- **一键安装命令按钮**：顶部「📋 生成安装命令」一键生成并复制到剪贴板（自动用当前访问地址 + Token 拼装），到新 VPS 粘贴即可
- **VPS 到期时间 & 倒计时**：编辑框可设置每台机器的到期时间；卡片右上与列表「运行时间」下方显示剩余天数，**≤ 7 天橙色警告、已到期红色脉冲提醒**
- **Web SSH 终端**：在浏览器内直接打开任意客户端的终端（基于 WebSocket + PTY），无需额外 SSH 客户端、不暴露 22 端口；连接时需输入 Web SSH 密码（可单独设置，留空则复用管理员密码）
- **Web SSH 防爆破**：连接密码连续输错 5 次，自动锁定该客户端 24 小时，期间无法再连
- **一键解封 / 改密**：服务端管理菜单内置「解封」与「修改 SSH 密码」，无需手动改配置或进容器
- **IP 模糊显示**：分组栏提供「IP 显示 / IP已遮」切换键，开启后公网 IPv4 仅保留首尾两段（如 `1.2.3.4` → `1.*.*.4`），防屏幕/截图泄露；开关仅存于本浏览器
- **压力测试（虚拟机器）**：顶部「⚡ 压力测试」弹窗，自定义数量、国家/地区、操作系统、运行时长、在线率、流量档位、CPU 核心数、内存大小、硬盘大小、CPU/内存使用率区间、开机年龄与分组名，一键在服务端生成一批"看着像真机器"的模拟节点（随机且实时抖动）；支持定时自动停止与手动「停止并清除」，仅移除本次生成的机器，绝不影响真实机器。每一项均可选「随机」由程序自动生成
  - **硬件规格约束**：CPU 核心数、内存大小(GB) 仅生成 **1 或偶数**（不会出现 3/5/7 这类不真实奇数）；且**内存大小始终 ≥ CPU 核心数**（如 CPU 1–4 核 + 内存 1–16G，绝不会出现 2核1G、4核3G 这类组合）
  - **记住上次设置**：每次点「开始生成」后，表单所有参数（数量、国家/系统选择、各区间与随机勾选、分组名等）自动存入浏览器本地（localStorage），下次打开弹窗自动回填，无需重复设置
- **自动部署（批量下发命令）**：在「⚙ 自动部署」设置页配置规则（源分组多选 / 部署命令 / 目标分组 / 失败分组 / 并发 / 超时 / Web SSH 密码）。新机器落入源分组后，后端调度器每 10 秒扫描一次，经 Web SSH 通道（WebSocket + PTY）向在线机器下发命令：`exit 0` → 自动移到**目标分组**，失败 / 超时 → 移到**失败分组**；`done`/`failed` 为终态，已部署机器不再重复跑（幂等）。内置**全局「暂停 / 恢复」总开关**——暂停仅挂起调度、保留全部规则配置（停止不再丢失任务）；管理员把机器移入源分组会自动重置部署状态、重新触发部署

## 目录结构

```
probe/
├── server/            # 服务端（HTTP + WebSocket + 静态前端）
│   ├── static/        # Web 前端（index.html / app.js / style.css）
│   ├── config.go      # 配置加载
│   ├── db.go          # SQLite 存储与流量月累计
│   ├── hub.go         # WebSocket 广播
│   ├── auth.go        # 登录 session
│   ├── api.go         # REST API 与 WS 处理
│   ├── terminal.go   # Web SSH 终端桥接（密码校验 / PTY / 锁定）
│   ├── deploy.go     # 自动部署调度器（规则 CRUD / 10s 扫描 / 并发限流 / 密码 AES-GCM 加解密）
│   └── main.go        # 入口
├── agent/             # 客户端
│   ├── collector.go   # 系统指标采集（gopsutil）
│   ├── client.go      # WebSocket 上报 + 断线重连
│   └── main.go        # 入口
├── configs/           # 示例配置
├── build.sh           # 跨平台编译
└── README.md
```

## 编译

```bash
chmod +x build.sh
./build.sh
# 产物在 dist/：probe-server + yufu-agent-<os>-<arch>
```

或直接用 `go build ./server ./agent`。

## 运行

**服务端**（读取 `configs/server.yaml`）：

```bash
./dist/probe-server
# 或指定配置
./dist/probe-server   # 默认监听 :8080，读取同目录/工作目录 configs/server.yaml
```

**客户端**（连接服务端）：

```bash
# 通过参数
./dist/yufu-agent-linux-amd64 -server ws://1.2.3.4:8080 -token change-me-agent-token -interval 5

# 或通过配置文件
./dist/yufu-agent-linux-amd64 -config configs/agent.yaml
```

> 客户端以**宿主机原生进程**运行（不是容器），`gopsutil` 直接读取宿主机 `/proc`，因此 OS、配置、流量均为**真实宿主机数据**。网卡自动选择「默认路由网卡」（如 `eth0`/`ens3`），流量统计反映真实对外带宽。

浏览器打开 `http://<服务端IP>:39689`，默认账号 `admin / admin`（请在 `server.yaml` 中修改）。

## 一键安装（推荐，仅 Linux）

```bash
bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/install.sh)
```

运行后会出现**交互菜单**：

- **1) 安装**：执行下方原有安装流程（自定义端口、管理员账号、Agent Token、域名绑定等）。
- **2) 卸载**：进入卸载子菜单，进一步选择（选中后会有**二次确认**，输入 `y`/`Y` 才真正执行，避免误删）
  - `1) 卸载服务端`：停止并移除服务端容器与数据卷，删除安装目录，并清理本机自监控 agent（文件/服务残留全清）；
  - `2) 卸载客户端`：自动读取本机 `/etc/yufu-agent.conf` 中的服务端地址与 Token（**无需手动输入**），调用 `uninstall-agent.sh` 清理本机 agent 并通知服务端删除记录；若该配置文件不存在才退化为手动输入；
  - `3) 卸载全部`：服务端 + 本机客户端一并卸载（客户端同样自动识别配置）。
- **3) 解封**：一键解除所有被 Web SSH 锁定的客户端（密码连续输错 5 次触发的 24 小时锁定）；如需仅解封某台，可带 UUID 执行 `docker compose exec server probe-server unlock <uuid>`。
- **4) 修改 SSH 密码**：交互输入新的 Web SSH 连接密码（直接回车则复用管理员密码），自动备份 `server.yaml`、写回新密码并重启服务端生效。

> 非交互环境（如 `curl ... | bash` 流水线）会跳过菜单、直接执行安装，保持旧用法兼容。

脚本会自动：
- 检测 Docker / docker compose 环境
- 交互式引导：端口、管理员用户名/密码、Agent Token、绑定域名
- 生成 `configs/server.yaml` 与 `docker-compose.yml`
- 若填写域名，自动配置 Caddy HTTP 反向代理
- **拉取 GHCR 预编译镜像并启动服务**（CI 已编好 amd64/arm64，VPS 零编译，低配机器也能秒级拉起；拉取失败自动回退本地 `docker compose build`）

> **关于预编译镜像**：服务端镜像由 GitHub Actions 在每次推送到 `main` 时自动构建并发布到 `ghcr.io/shenping1200/yufu-probe:latest`（公开，可匿名 `docker pull`）。这意味着安装机**不再需要现编译**——这正是 1C1G 等低配 VPS 把安装从 ~10 分钟降到 ~1-2 分钟的关键。配置 `server.yaml` 通过运行时挂载注入，覆盖镜像内置默认配置。

也可以先 clone 仓库再执行：

```bash
git clone https://github.com/shenping1200/yufu-probe.git
cd yufu-probe
bash install.sh
```

## Web SSH 终端

在服务端安装并注册客户端后，可在面板直接打开任意客户端的 **Web SSH 终端**（浏览器内 PTY，基于 WebSocket 桥接，无需额外 SSH 客户端、不暴露 22 端口）。

**连接密码（连接时一次静态口令确认）**

- 密码由服务端统一配置在 `server.yaml` 的 `ssh_password` 字段；安装时已可设置（位于「监听端口」之后），也可随时通过管理菜单 **4) 修改 SSH 密码** 更改。
- 留空则复用管理员密码；建议为 Web SSH 单独设置一个，与管理员密码解耦。
- 打开某台客户端的 Web SSH 时会弹出密码框，输入正确后方可进入终端。

**防爆破锁定**

- 同一客户端的 Web SSH 密码连续输错 **5 次**，服务端会将该客户端锁定 **24 小时**，期间无法再连接其 Web SSH。
- 锁定状态落盘（SQLite `ssh_lock` 表），服务端重启后依然生效。

**解封**

- 管理菜单选择 **3) 解封** 可一键解除所有锁定；
- 或手动在已安装服务端的 VPS 上执行（需先 `cd /opt/yufu-probe`）：

```bash
docker compose exec server probe-server unlock            # 解封全部
docker compose exec server probe-server unlock <uuid>    # 仅解封某台
```

**修改密码**

- 管理菜单选择 **4) 修改 SSH 密码**，按提示输入新密码（直接回车 = 复用管理员密码），脚本自动备份 `server.yaml`、写回新密码并重启服务端生效。

## 自动部署（批量下发命令）

在面板「⚙ 自动部署」设置页配置**规则**，后端调度器会按规则把部署命令批量下发给落入源分组的在线机器。命令经 **Web SSH 通道**（WebSocket + PTY，与浏览器终端同一链路）执行，因此无需在目标机上预置 SSH 服务或暴露 22 端口。

**一条规则包含：**

| 字段 | 说明 |
|------|------|
| 规则名称 | 便于辨识 |
| 源分组（多选） | 落入这些分组的在线机器会被纳入部署候选 |
| 部署命令 | 要执行的 shell 命令（如 `apt update && apt upgrade -y`、`hostname new-name`）|
| 目标分组 | 部署成功（`exit 0`）后机器自动移入的分组 |
| 失败分组 | 部署失败 / 超时时机器自动移入的分组（未填则不移）|
| 并发数 | 同一规则同时下发的机器数（≤ 0 兜底为 50）|
| 超时（秒） | 单台执行超时（≤ 0 兜底为 60）；超时判为失败 |
| Web SSH 密码 | 下发命令使用的密码；**必须与服务器 Web SSH 密码一致**（留空则复用管理员密码），不一致的规则会被跳过。密码以 AES-GCM 密文存入数据库 |

**调度逻辑：**

- 调度器每 **10 秒**扫描一次所有启用（`enabled`）的规则；
- 候选 = 源分组内、**在线**、且部署状态非 `done`/`failed` 的机器；内存 `inFlight` 表防止同一台被重复捞取；
- 用 **semaphore** 按规则并发数限流，每台独立执行、互不阻塞；
- `exit 0` → 移到目标分组并标记 `done`；失败 / 超时 → 移到失败分组（若配置）并标记 `failed`；
- `done`/`failed` 为终态，已部署机器**不会重复执行**（幂等）；
- 任何单点异常（连接写超时、状态更新阻塞）都会被**硬超时 + recover** 兜住，保证调度器不因个别机器卡死而冻结。

**两个关键交互：**

- **全局「暂停 / 恢复自动部署」总开关**（弹窗顶部）：暂停 = 仅停止扫描调度、**全部规则配置原样保留**；下次点恢复即继续工作，无需重建规则。这是「停止任务后规则不丢失（自动保存配置）」的实现方式。
- **改分组重置部署状态**：管理员把一台机器拖入 / 移出分组时，其 `deploy_state` 终态会被清空。因此「把已部署过的机器重新拖回源分组」会再次触发部署，符合直觉。

> 说明：规则密码的 AES-GCM 密钥取自环境变量 **`YUFU_DEPLOY_KEY`**（任意字符串经 SHA-256 派生为 32 字节）。该变量需在服务端容器的运行环境注入（见 `docker-compose.yml`），缺失时无法加解密规则密码。数据库只存 `base64(nonce + ciphertext)`，明文密码不落盘。

## 本地升级（已安装的服务端）

服务端已升级为 GHCR 预编译镜像，升级只需拉取新镜像并重启容器（数据与已注册客户端全部保留）：

```bash
# 在已安装服务端的 VPS 上执行（默认安装目录 /opt/yufu-probe）
cd /opt/yufu-probe && bash upgrade.sh

# 或一条命令远程升级（需 root + curl）
bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/upgrade.sh)
```

升级脚本流程：`docker compose pull server` 拉新镜像 → `docker compose up -d` 重启。**不执行 `git pull`**——前端已通过 `embed` 打进镜像,拉新镜像即拿到新 UI;`git pull` 会与本机由 `install.sh` 生成的 `docker-compose.yml` / `configs/server.yaml` 冲突,反而会把配置覆盖坏。`probe-data` 数据卷与挂载的 `configs/server.yaml` 保持不变。

## 客户端一键安装（推荐，秒级）

在**每一台要监控的机器**上执行（无需 Docker、无需编译，下载预编译静态二进制并注册 systemd）：

```bash
bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/install-agent.sh) \
  ws://<服务端IP>:39689 <AgentToken>
```

- 第一个参数为服务端 `ws://IP:端口`（也可只填 `IP:端口`），第二个为服务端安装时设置的 **Agent Token**（面板「安装完成」页可见）。
- 脚本自动探测架构（x86_64 / aarch64），下载 `dist/yufu-agent-linux-<arch>`，写入 `/etc/yufu-agent.conf` 并注册 `yufu-agent.service`（开机自启、`Restart=always`）。
- 完成后在服务端面板即可看到该机器，OS / 配置 / 流量均为宿主机真实数据。

## 卸载

### 服务端（Docker，默认安装目录 `/opt/yufu-probe`）

```bash
cd /opt/yufu-probe
docker compose down            # 停止并移除服务端容器
systemctl stop yufu-agent     # 停止本机自监控 agent（原生 systemd 服务，独立于服务端容器）
# 如需彻底删除安装目录与所有数据：
rm -rf /opt/yufu-probe
systemctl disable yufu-agent 2>/dev/null; rm -f /etc/systemd/system/yufu-agent.service /usr/local/bin/yufu-agent /etc/yufu-agent.conf; rm -rf /var/lib/yufu-agent; systemctl daemon-reload
```

### 客户端 —— 推荐：一条命令卸载（原生 / 旧版 Docker 均兼容）

在**要卸载的那台客户端机器**上执行：

```bash
bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/uninstall-agent.sh) \
  ws://<服务端IP>:39689 <AgentToken>
```

#### 服务端地址写法（以下四种都支持，脚本自动归一化）

| 写法 | 适用场景 |
|------|----------|
| `ws://1.2.3.4:39689` | 内网、未启用 TLS（最常用） |
| `wss://域名:39689` | 公网、已启用 HTTPS/WSS |
| `http://1.2.3.4:39689` | 也可以直接写 http（脚本会自动处理） |
| `1.2.3.4:39689` | 裸地址（自动按 http 处理） |

> 第二个参数为服务端安装时设置的 **Agent Token**（面板「安装完成」页可见）。注意：**不要**画蛇添足写成 `http://ws://...` 这类嵌套形式，脚本已能正确识别上述每种写法。

#### 这条命令到底做了什么

1. **服务端移除（核心）**
   - 读取本机 UUID：原生 agent 在 `/var/lib/yufu-agent/uuid`，旧版 Docker agent 在容器内 `/data/uuid`。
   - 调用 `DELETE /api/agents/:uuid`（Agent Token 鉴权）→ 服务端**立即删除**该机器记录，刷新面板随即消失。
   - **兜底机制**：即便该接口因网络不通或服务端版本较旧而失败，`systemctl stop yufu-agent`（原生）或 `docker stop`（旧版容器）会触发 agent 的**优雅注销**——进程收到 SIGTERM 后主动发送 WebSocket 注销请求，服务端同样会删除记录。两种路径保证「服务端看不到这台机器」。
2. **本机清理（文件 / 服务 / 容器全部清干净，不留残留）**
   - 原生 agent：停止并禁用 `yufu-agent.service`，删除二进制 `/usr/local/bin/yufu-agent`、配置 `/etc/yufu-agent.conf`、UUID 目录 `/var/lib/yufu-agent`，最后 `systemctl daemon-reload`。
   - 旧版 Docker agent：按容器名（`yufu-agent` 与 `probe-agent` **两种命名都识别**）并辅以「进程含 `/app/probe-agent`」兜底，强制删除容器，避免容器带重启策略反复复活。

这正是「**在客户端执行一次删除命令，服务端就看不到它**」的体验：本地清理与服务端记录删除一步到位。

> 修复记录：旧版 `uninstall-agent.sh` 存在两个已知问题，现已修复——
> 1. 旧版只识别名为 `probe-agent` 的 Docker 容器，漏掉了实际命名为 `yufu-agent` 的容器，导致旧客户端卸载不干净、面板反复出现；
> 2. 旧版对 `http://` 前缀的输入会错误拼成 `http://http://...`，使 `DELETE` 请求静默失败。新版已对地址做正确归一化。

#### 手动清理（仅原生 agent）

```bash
systemctl stop yufu-agent && systemctl disable yufu-agent && \
  rm -f /etc/systemd/system/yufu-agent.service /usr/local/bin/yufu-agent /etc/yufu-agent.conf && \
  rm -rf /var/lib/yufu-agent && \
  systemctl daemon-reload
```

## Docker 手动部署（推荐，仅 Linux）

项目已提供 `Dockerfile.server`、`Dockerfile.agent` 与 `docker-compose.yml`，默认冷门端口 `39689`。服务端**默认使用 GHCR 预编译镜像**（无需本地编译），一条命令启动：

```bash
cd yufu-probe
docker compose up -d
```

- 服务端映射 `39689`，浏览器访问 `http://<服务器IP>:39689`
- 客户端以 `host` 网络 + 共享宿主 PID 方式运行，因此采集到的是**宿主机真实指标**（而非容器自身）
- 数据库（SQLite）与客户端 uuid 均通过 volume 持久化

自定义：修改 `docker-compose.yml` 里的 `TOKEN`（需与 `server.yaml` 的 `agent_token` 一致）、`INTERVAL`，或挂载修改后的 `configs/server.yaml`，再 `docker compose up -d`。

如需自己构建镜像（例如离线环境、或 CI 不可用时的回退）：

```bash
docker compose up -d --build
# 等价于单独构建：
docker build -f Dockerfile.server -t yufu-probe-server .
docker build -f Dockerfile.agent -t yufu-probe-agent .
```

> 说明：客户端默认只编译 Linux 镜像（`GOOS=linux`），符合「两端均部署在 Linux」的诉求。服务端镜像由 CI 自动发布到 `ghcr.io/shenping1200/yufu-probe:latest`，本地 `--build` 仅作为拉取失败时的兜底。

## 配置说明

`configs/server.yaml`：

| 字段 | 说明 |
|------|------|
| `listen` / `port` | 监听地址与端口 |
| `tls.enabled` / `cert` / `key` | 启用 HTTPS/WSS 与证书路径 |
| `agent_token` | 客户端接入令牌，需与 agent 一致 |
| `ssh_password` | Web SSH 连接密码；留空则复用 `admin.password`，建议单独设置 |
| `admin.username` / `admin.password` | Web 登录账号 |
| `db_path` | SQLite 数据库文件路径（含 `ssh_lock` 锁定表） |

> **环境变量 `YUFU_DEPLOY_KEY`**：自动部署规则密码的 AES-GCM 加密密钥（任意字符串经 SHA-256 派生为 32 字节）。经服务端容器 `environment` 注入（见 `docker-compose.yml`），缺失时无法正常加解密规则密码。

`configs/agent.yaml`：`server`（ws/wss 地址）、`token`、`interval`（秒）、`iface`（监控网卡，留空自动选）、`uuid_file`（UUID 持久化路径）。

## API 一览

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/login` | 账号密码登录 |
| POST | `/api/logout` | 退出 |
| GET | `/api/me` | 当前登录用户 |
| GET | `/api/install-command` | 返回给新 VPS 用的客户端一键安装命令（自动用当前访问地址 + agent_token 拼装） |
| GET | `/api/agents` | 机器列表（含本月累计、`expire_at`） |
| PUT | `/api/agents/:uuid/alias` | 设置别名（行内编辑） |
| PATCH | `/api/agents/:uuid` | 更新显示名称、备注、`expire_at`（到时时间，Unix 秒，传 `null` 清空） |
| DELETE | `/api/agents/:uuid` | 删除机器（Agent Token 鉴权，用于主动注销） |
| GET | `/api/agents/:uuid/traffic` | 各自然月流量历史 |
| WS | `/ws/agent?token=` | 客户端上报通道 |
| WS | `/ws/terminal/{uuid}` | 浏览器 Web SSH 终端桥接（需登录 + Web SSH 密码） |
| POST | `/api/ssh/unlock` | 解除 Web SSH 锁定（需登录，运维） |
| WS | `/ws/viewer` | 浏览器实时订阅（需登录） |
| GET | `/api/deploy-rules` | 自动部署规则列表（需管理员） |
| POST | `/api/deploy-rules` | 新建规则（需管理员） |
| PATCH | `/api/deploy-rules/{id}` | 更新规则（需管理员；支持局部更新，仅覆盖请求中提供的字段） |
| DELETE | `/api/deploy-rules/{id}` | 删除规则（需管理员） |
| GET | `/api/deploy-paused` | 读取全局自动部署暂停状态（公开） |
| POST | `/api/deploy-paused` | 切换全局暂停 / 恢复（需管理员） |

## 流量累计逻辑

- 客户端每次上报**自上次以来的流量增量（字节）**，由服务端按 `uuid + 自然月（YYYY-MM）` 累加。
- 客户端自动选择「默认路由网卡」（优先 `/proc/net/route` 中的默认路由接口，如 `eth0`/`ens3`，回退累加所有非虚拟网卡），因此「本月流量」反映真实对外带宽，不会因选到 `docker0`/`br-*` 等虚拟网卡而偏小。
- 累计值存储于服务端，即使客户端重启也不丢失；跨月自动新建当月记录，旧月历史可查。
- Web 面板「本月累计」= 当前自然月入站 + 出站累计（每月 1 日重置）。

## 部署建议

- **内网**：`tls.enabled: false`，浏览器用 `http`、agent 用 `ws`。
- **跨公网**：申请证书后 `tls.enabled: true` 并填写 `cert`/`key`；agent 的 `server` 改为 `wss://域名`。
- 客户端建议注册为系统服务（systemd / Windows Service）开机自启。
- 首次登录后请务必修改 `admin` 密码与 `agent_token`。
- **客户端为单一原生二进制 + systemd**（设计借鉴[哪吒监控](https://github.com/nezhahq/nezhahq.github.io)）：直接读取宿主机 `/etc/os-release` 与 `/proc`，因此面板显示的是**真实宿主系统/配置/流量**，不会因容器内运行而误报（如 Alpine）。服务端自监控同样为原生进程。
- **优雅注销**：`systemctl stop yufu-agent` 或 `docker stop` 会触发 agent 向服务端发送注销请求，服务端立即删除该机器记录；亦可用 `uninstall-agent.sh` 一条命令完成「通知服务端 + 清理本机」。
