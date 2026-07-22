# 渔夫探针 (YuFu Probe)

一套类似「哪吒探针」的轻量服务器监控工具，包含**客户端（Agent）**、**服务端（Server）** 与 **Web 界面**，全部用 Go 编写，单二进制部署、零外部依赖（纯 Go SQLite，无需数据库服务）。

## 功能

- **客户端采集**：IP、开机时间、CPU 核心数、内存/磁盘总量、实时上下行流量速率、自然月累计流量、CPU / 内存 / 磁盘使用率
- **Web 面板**：卡片 / 列表双视图、汇总看板、机器配置展示、行内编辑别名、单台实时流量曲线（ECharts）
- **深色 / 浅色主题**：一键切换，偏好自动保存
- **鉴权**：Web 端账号密码登录（session）+ 客户端 Token 接入，双保险
- **跨平台客户端**：Linux / Windows / macOS，amd64 / arm64
- **可选 TLS**：跨公网部署时启用 `wss` / `https`

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
# 产物在 dist/：probe-server + agent-<os>-<arch>
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
./dist/agent-linux-amd64 -server ws://1.2.3.4:8080 -token change-me-agent-token -interval 5

# 或通过配置文件
./dist/agent-linux-amd64 -config configs/agent.yaml
```

浏览器打开 `http://<服务端IP>:39689`，默认账号 `admin / admin`（请在 `server.yaml` 中修改）。

## 一键安装（推荐，仅 Linux）

```bash
bash <(curl -sSL https://raw.githubusercontent.com/shenping1200/yufu-probe/main/install.sh)
```

脚本会自动：
- 检测 Docker / docker compose 环境
- 交互式引导：端口、管理员用户名/密码、Agent Token、绑定域名
- 生成 `configs/server.yaml` 与 `docker-compose.yml`
- 若填写域名，自动配置 Caddy HTTP 反向代理
- 拉取代码、构建镜像并启动服务

也可以先 clone 仓库再执行：

```bash
git clone https://github.com/shenping1200/yufu-probe.git
cd yufu-probe
bash install.sh
```

## Docker 手动部署（推荐，仅 Linux）

项目已提供 `Dockerfile.server`、`Dockerfile.agent` 与 `docker-compose.yml`，默认冷门端口 `39689`，一条命令启动：

```bash
cd yufu-probe
docker compose up -d --build
```

- 服务端映射 `39689`，浏览器访问 `http://<服务器IP>:39689`
- 客户端以 `host` 网络 + 共享宿主 PID 方式运行，因此采集到的是**宿主机真实指标**（而非容器自身）
- 数据库（SQLite）与客户端 uuid 均通过 volume 持久化

自定义：修改 `docker-compose.yml` 里的 `TOKEN`（需与 `server.yaml` 的 `agent_token` 一致）、`INTERVAL`，或挂载修改后的 `configs/server.yaml`，再 `docker compose up -d`。

单独构建镜像：

```bash
docker build -f Dockerfile.server -t yufu-probe-server .
docker build -f Dockerfile.agent -t yufu-probe-agent .
```

> 说明：客户端默认只编译 Linux 镜像（`GOOS=linux`），符合「两端均部署在 Linux」的诉求。

## 配置说明

`configs/server.yaml`：

| 字段 | 说明 |
|------|------|
| `listen` / `port` | 监听地址与端口 |
| `tls.enabled` / `cert` / `key` | 启用 HTTPS/WSS 与证书路径 |
| `agent_token` | 客户端接入令牌，需与 agent 一致 |
| `admin.username` / `admin.password` | Web 登录账号 |
| `db_path` | SQLite 数据库文件路径 |

`configs/agent.yaml`：`server`（ws/wss 地址）、`token`、`interval`（秒）、`iface`（监控网卡，留空自动选）、`uuid_file`（UUID 持久化路径）。

## API 一览

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/login` | 账号密码登录 |
| POST | `/api/logout` | 退出 |
| GET | `/api/me` | 当前登录用户 |
| GET | `/api/agents` | 机器列表（含本月累计） |
| PUT | `/api/agents/:uuid/alias` | 设置别名 |
| GET | `/api/agents/:uuid/traffic` | 各自然月流量历史 |
| WS | `/ws/agent?token=` | 客户端上报通道 |
| WS | `/ws/viewer` | 浏览器实时订阅（需登录） |

## 流量累计逻辑

- 客户端每次上报**自上次以来的流量增量（字节）**，由服务端按 `uuid + 自然月（YYYY-MM）` 累加。
- 累计值存储于服务端，即使客户端重启也不丢失；跨月自动新建当月记录，旧月历史可查。
- Web 面板「本月累计」= 当前自然月入站 + 出站累计。

## 部署建议

- **内网**：`tls.enabled: false`，浏览器用 `http`、agent 用 `ws`。
- **跨公网**：申请证书后 `tls.enabled: true` 并填写 `cert`/`key`；agent 的 `server` 改为 `wss://域名`。
- 客户端建议注册为系统服务（systemd / Windows Service）开机自启。
- 首次登录后请务必修改 `admin` 密码与 `agent_token`。
