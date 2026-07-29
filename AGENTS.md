# AGENTS.md — 接手指南（写给下一个 AI / 协作者）

> 本文件目标：让任何接手的 AI 或人在**不向我（上一个处理者）追问**的情况下，直接看懂项目结构、部署方式、关键约定与已踩过的坑，并安全地把改动推上线。

---

## 0. 项目一句话定位

**渔夫探针（YuFu Probe）**：中心化的服务器/客户端监控探针系统。
- 中心 **server**（Go，Web 面板）管理所有客户端。
- 各台被监控机器跑 **agent**（预编译二进制），定时上报 CPU / 内存 / 硬盘 / 流量 / 在线状态。
- Web 面板支持：实时列表、详情曲线、SSH 终端、编辑别名/分组/备注/到期、删除客户端、**多选批量操作**、生成安装 / 卸载命令。

技术栈：Go 后端 + 原生 JS 前端（经 Go `embed` 打进同一个镜像）；SQLite 存储；Docker Compose 部署；镜像发布到 GHCR；CI 用 GitHub Actions。

---

## 1. 仓库结构（关键路径）

```
.
├── server/                  # Go 后端
│   ├── api.go               # HTTP 路由 + 处理器（含 /api/agents 增删改、install/uninstall command、鉴权）
│   ├── db.go                # SQLite 读写；部分更新函数 SetAgentGroup/Remark/Expire
│   ├── state.go             # 内存态（agents / groups / 广播）；PatchAgentFields
│   └── static/              # 前端三件套（被 Go embed 打进镜像）
│       ├── app.js           # 全部前端逻辑（~1600 行）
│       ├── index.html       # 入口，引用 app.js?v=N / style.css?v=N
│       └── style.css        # 全部样式（含暗/亮双主题 CSS 变量）
├── dist/                    # 预编译 agent 二进制
├── agent/                   # agent 源码
├── configs/                 # 配置模板
├── install.sh               # 服务端一键安装（生成 docker-compose.yml + configs/server.yaml）
├── install-agent.sh         # 客户端安装
├── uninstall-agent.sh       # 客户端卸载（探测本机 UUID → DELETE /api/agents/{uuid} → 停服务）
├── upgrade.sh               # 拉 GHCR 镜像 + docker compose up -d（详见 §3）
├── docker-compose.yml       # 由 install.sh 在本机生成（不 git 跟踪其实际端口/挂载）
├── Dockerfile.server / Dockerfile.agent
├── build.sh / tools/
└── README.md                # 用户向说明（面向使用者，不是开发/接手文档）
```

**重要**：`docker-compose.yml` 与 `configs/server.yaml` 是 `install.sh` 在**目标机器上生成**的，仓库里的是模板/占位。CI 构建镜像时不需要它们；部署机上的真实文件也不要 `git pull` 覆盖（见 §3、§7）。

---

## 2. 前端资源是"编译进镜像"的（最容易踩的坑）

前端 `server/static/*` 不是单独部署，而是被 Go 用 `//go:embed` 打进 `ghcr.io/shenping1200/yufu-probe:latest` 镜像。

**推论（务必记住）**：
1. 任何改 `app.js` / `index.html` / `style.css` 的改动，**都必须重新走 CI 构建镜像**，再升级容器。直接改 VPS 上运行的文件无效（容器重启即丢失，且根本没挂载出来）。
2. `index.html` 用 `app.js?v=N`、`style.css?v=N` 做缓存戳。改对应文件后**必须**把 `N` +1，否则用户浏览器会用旧缓存，看起来"改了没生效"。
3. 验证时抓线上文件确认新代码 Serve（见 §8）。

---

## 3. 部署流水线（改动 → 上线）

```
本地改代码 → git commit → git push origin main
        │
        ▼
GitHub Actions: "Build and Push Server Image"
  - 构建 amd64 + arm64
  - 推 ghcr.io/shenping1200/yufu-probe:latest
        │
        ▼
中心服务端 VPS 上执行（或远程触发）：
  cd /opt/yufu-probe && bash upgrade.sh
  # = docker compose pull server && docker compose up -d
```

- 升级只换镜像、重启容器，**不动卷与挂载**，数据库与已注册客户端保留。
- `upgrade.sh` 还会把老式 `build:` compose 自动转成 `image:` 模式（见脚本注释）。
- 工作流：**直接 push 到 `main`**（本项目未走 PR 流程）。CI 跑完即生效。

---

## 4. 如何连到中心 VPS（验证 / 排障）

中心服务端不在公网直连，需经 **SOCKS5 跳板** SSH 上去（paramiko + pysocks）。

- 本机脚本：`/tmp/ssh_sock.py`（本会话维护，随沙箱存在）。用法：
  ```bash
  python3.11 ssh_sock.py <host> <port> <user> <pass> "<cmd>" <timeout秒>
  ```
- ⚠️ **凭据不在此文件内联**。host / 端口 / SSH 用户名密码 / SOCKS5 账号 / 面板密码 / WS URL / Agent Token 均请从事本机脚本与 VPS 上的 `configs/server.yaml` 读取，不要把它们写进仓库或任何文档后提交。
- Web 面板由 caddy 反代，对外域名例如 `mailzyf.de5.net`（实际以 VPS 配置为准）。**从 VPS 本机 curl 验证时必须带 `Host` 头**，否则 caddy 不认：
  ```bash
  curl -s -H 'Host: <域名>' http://127.0.0.1/app.js | grep 关键字
  curl -s -H 'Host: <域名>' http://127.0.0.1/       | grep -o 'app.js?v=[0-9]*'
  ```

---

## 5. 前端关键约定

- **静态文件改动一律用 Python（utf-8 读写）做字符串替换**，不要直接用 Edit 工具改含中文注释的 `.go` 文件——Edit 会把多字节字符截断成 `?` 导致 `invalid UTF-8 encoding` 编译失败。追加新函数时 append 到文件末尾最安全。
- **三个批量弹窗统一走 `openCenterModal(title)` 助手**（居中浮层，返回 `{mask, box, ok, cancel, close}`），不用浏览器原生 `prompt()`（会冒到屏幕顶部）。改分组用 `<select>`，设备注/设到期用 `<input>`（支持回车提交）。
- **到期日期解析用 `parseFlexibleDate`**：正则 `^(\d{4})[-./](\d{1,2})[-./](\d{1,2})$`，支持 `2027-3-3` / `2027.5.18` / `2027/11/01` 任意分隔符。
- **表头 `<thead>` 第一格是复选框列 `#selectAllChk`**（与每行的 `.sel-td` 对齐）；新增列时记得同步 `colspan`。
- **操作列四个按钮一致性**：`流量/编辑/SSH/删除` 统一 `padding: 2px 8px; font-size: 13px; line-height: 1.4; border: 1px solid var(--border); background: var(--bg-3)`；删除保留红字（`#b3344a`），hover 红框。访客模式经 `.sel-only / .del-only` 隐藏。
- **操作表头**：`<th>操作</th>` 带 `width:1%; white-space:nowrap`，让列宽收缩贴合按钮组、消除右侧白条。
- 主题：CSS 变量定义暗/亮双套（`--bg-2/--bg-3/--border/--text/--text-2/--primary/--danger`），浅色主题下 `--bg-2:#fff`、`--bg-3:#f3f4f6`。改色优先用变量。

---

## 6. 后端关键约定

- **DB**：SQLite，路径在 VPS 宿主机 `/var/lib/docker/volumes/yufu-probe_probe-data/_data/probe.db`；容器 `probe-server` 挂 `/app/data`。`agents` 表（uuid/online/group_name/alias/remark/expire_at…）+ `groups` 表（分组注册表）。
- **"复活陷阱"**（影响删除设计）：`state.go` 的 `applyReport` 中，若上报的 UUID 不在内存则自动新建条目 → 仅删服务端对**在线** agent 不持久（下次上报又回来）。因此"删除客户端"做成**面板移除 + 提供卸载命令**（让用户在客户端跑 `uninstall-agent.sh`），而非真删库。
- **WS 指令**：agent 端只认 `shell_open/input/resize/close`，**没有**卸载/停止类指令；卸载只能走客户端执行 `uninstall-agent.sh`。
- **批量更新用部分更新函数**：`SetAgentGroup(db,uuid,group)` / `SetAgentRemark` / `SetAgentExpire` 各自只 `UPDATE` 对应单列，避免 `UpdateAgent` 全量覆盖把备注/到期清空。`state.PatchAgentFields` 在锁内只改非 nil 字段。
- **鉴权**：`/api/agents`（增删改）需 `requireAdmin`；`/api/agents/{uuid}` 用 `requireAgentTokenOrAdmin`（admin session cookie 或 agent token 任一放行）。install/uninstall command 接口需 admin。
- **命令生成**：`/api/install-command` 与 `/api/uninstall-command` 都返回 `{command}`，内容是从 `install-agent.sh` / `uninstall-agent.sh` 拼 WS URL + Agent Token。前端"生成安装/卸载命令"按钮同时拉这两个接口，弹窗里两块文本框各自带复制按钮。

---

## 7. 已知坑 / 运维备忘（排障先看这里）

1. **`latest` 标签竞态**：连续快速提交会触发多轮 CI 都推 `:latest`，**先提交的后完成会把 `:latest` 覆盖成旧代码镜像**。现象：本地验证改了、推了、CI 绿了，但线上"没生效"。排查：`docker compose images server` 看镜像 ID 是否最新；解决：`gh run rerun <最新那次>` 重跑让其重新抢占 `:latest`，再 `upgrade.sh` 重拉。
2. **前端改动必须重构建镜像**（§2），只改 VPS 文件无效。
3. **UTF-8 Edit 陷阱**（§5 开头）：用 Python 做 UTF-8 安全替换。
4. **缓存戳**：改了 `app.js`/`style.css` 一定要把 `index.html` 里对应的 `?v=N` +1，否则浏览器旧缓存。
5. **caddy 需 Host 头**才能本地 curl 验证（§4）。
6. 中心 VPS `/tmp` 下残留 `build*.log`、`ck*.txt`、`*.json` 及旧 `probe.db.bak.*` 备份，待清理（**不影响运行**，清理前先确认备份可删）。

---

## 8. 改动后验证清单

1. `node --check server/static/app.js`（前端语法）
2. `go build ./...` / `go vet ./...`（后端，可选，CI 也会跑）
3. `git commit` → `git push origin main`
4. `gh run watch <run_id> --exit-status` 等 CI 绿
5. 中心 VPS `bash /opt/yufu-probe/upgrade.sh`（或 `docker compose pull server && docker compose up -d`）
6. 抓线上验证（§4 的 curl 带 Host 头）：
   - `index.html` 的 `app.js?v=N`/`style.css?v=N` 已 +1
   - 改动对应的关键字确实出现在线上 `app.js`/`style.css` 中
7. 浏览器**强刷**（Ctrl/Cmd+Shift+R）确认视觉生效。

---

## 9. 历史决策与已做事项（背景，便于判断"为什么是这样"）

- 内存/硬盘显示改为 **GiB 口径、自适应 MB**（commit `9d12325`）；曾核对某实例真实读数无误。
- 删除 **100 台离线压测机**：内存/硬盘读数按要求**保持不动**，只删 DB 记录 + 重启容器生效（重启后从 DB 重新加载，面板即只剩 24 台）。
- 删除客户端 + 多选批量（删除/改分组/设备注/设到期）已上线；分组下拉用原生 `<select>`（不手输、不新建）。
- 安装/卸载命令合一，弹窗双文本框。
- 表头补全选复选框列、四个操作按钮尺寸/边框/间距统一、操作表头贴合按钮组——均已上线。

---

## 10. 待办 / 遗留

- [ ] 清理中心 VPS `/tmp` 残留（`build*.log`/`ck*.txt`/`*.json`）+ 观察后删 `probe.db.bak.*`。
- [ ] 本项目"双 CI 抢 `latest`"隐患：后续若再遇"改了没生效"，优先 `gh run rerun` 最新那次（见 §7.1）。如要根治，可考虑 CI 给镜像打 commit-sha 标签、部署按 sha 拉取，而非只依赖 `latest`。
- [ ] 本 `AGENTS.md` 随项目演进持续维护（每次大改动顺手更新对应章节）。
