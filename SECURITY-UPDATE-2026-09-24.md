# yufu-probe 安全加固与健壮性修复 更新说明

> 日期：2026-09-24
> 分支：`main`（fast-forward 合并 `hardening`，`9f754cd..c4d5c0c`）
> 触发：第三方安全审计（`D:\EXE\yufu-probe-code-review.md`）+ 自审
> 部署状态：VPS `69.12.75.218:19527` 已重建镜像并验证通过

---

## 一、本次修复清单（含 P0/P1/P2 与自审补充）

### 第一批：公网暴露面（最危险，立即修）
| 项 | 问题 | 修复 |
|---|---|---|
| #1 | 默认弱口令 `admin/admin` + SSH 密码静默回退管理员密码 + 登录零限流 | 登录加 **IP 限流**（5 次/15min，超限 15min 封禁）+ **常数时间比较**；启动期检测到默认口令打印告警（不致命，避免 brick 已有部署） |
| #2 | Web SSH 锁定仅按 `uuid` 维度，攻击者轮换 uuid 即可重置尝试次数，爆破面=任意机 root | 锁定改为 **`(uuid, 来源IP)` 双维度复合键**；新增审计日志 |
| #3 | `state.go` Flush 在释放读锁后才解引用拷贝 agent，与 `applyReport` 写字段存在数据竞争 | 锁内完成 `AgentRow` 值拷贝（已静态确认，建议后续补 `go test -race` 跑通） |
| #19 | `app.js` 8 处 `data-uuid` 未转义，持 `agent_token` 者可注册恶意 uuid 触发存储型 XSS | 全部注入点改用 `escapeHtml()` 转义 |

### 第二批：正确性与资源泄露
| 项 | 问题 | 修复 |
|---|---|---|
| #4 | geo 地理查询失败不写缓存、cache miss 窗口内每条上报都起 goroutine，首启/外网不通时风暴 | 加 **在途去重** + **负缓存(10min)** + **并发上限 100** |
| #5 | `PATCH /api/agents/{uuid}` 无条件覆盖，漏传字段（如 alias）被静默清空 | `UpdateAgent`/`UpdateAdmin` 改 **指针分部更新**（nil=不动，空串=清空）；顺带修复别名无法清空 |
| #16 | 流量增量 `rx-tx` 无下溢守卫，计数器回绕/网卡切换时变成天文数字且不可逆累加到月流量 | 加下溢守卫 + **网卡切换重置基线**（记录选中网卡） |
| #6 | viewer WS 断开后 `send` 通道永不关闭，`writePump` goroutine 永久泄漏 | 新增 `closeOnce` 幂等关闭 `send`，断开时先 `removeViewer` 再 `closeSend` |

### 第三批：稳健性 / 合规 / 清理
| 项 | 问题 | 修复 |
|---|---|---|
| #7 | agent WS 无读超时 / ping，掉电断网产生的半开连接不被回收（Web SSH/部署干等超时） | 加 **读超时(60s)** + **服务端 50s ping / agent 自动 pong** 保活 |
| #8 | 离线阈值硬编码 15s，与上报间隔解耦，高 interval 部署易抖动 | 改为可配置 `offline_threshold`（默认 15s） |
| #9 | `pickOS` 兜底分支依赖 map 遍历随机性"碰巧"随机 | 显式随机选 key |
| #10 | HTTP 加固缺失：无 MaxBytesReader、缺超时、Cookie 非 Secure、`CheckOrigin` 恒 true、内部错误回显 | 全链路超时 + **4MB 请求体限制** + Cookie 标记 `Secure` + WS **同源校验** + 内部错误不再回显 |
| #11 | `sessions` / `visitor_links` 只增不减 | 每日清理过期访客链接 + 30 天以上会话（24h 周期） |
| #12 | 零 SQLite 索引，几百~上千行全表扫描 | 为 `agents/sessions/visitor_links/traffic_monthly/ssh_lock` 加 **幂等索引** |
| #18 | 配置路径硬编码 `configs/server.yaml`，运维在错误目录执行会连错库 | 支持 `-config` 参数 / `YUFU_CONFIG` 环境变量，并打印实际加载路径 |
| #20 | 合规：HK/TW 被列为独立国家 | 名称改为 **中国香港 / 中国台湾** |
| #21 | 死代码 / 一致性 | 清理 agent `locale` 占位、terminal 冗余赋值等 |

> 说明：审计报告的 #13/#14/#15/#17 等其余 P2 项（调度 ticker stall、Windows Web SSH、CWD 依赖配置路径等）多为场景限制或低概率，本次未逐一处理，已在仓库 `rollback` 锚点前保留原状；bcrypt 因本机无 Go 工具链、且 VPS 构建联网拉依赖有风险，当时**降级为"常数时间比较 + 强口令告警 + 登录限流"直接掐断爆破**，bcrypt 暂列后续建议——**已于「六、bcrypt 落地」完成落地**。

---

## 二、部署与回退

### 已部署
- VPS 镜像：`ghcr.io/shenping1200/yufu-probe:latest` = `7915deb8cfcb`（构建于 2026-09-25，含 V6-1/V4-2/V6-2/V6-3 复审修复；历史镜像：`ec6a18c1992f` v5、`4c06a1e499a3` bcrypt 落地、`4fc42b2b2202` P1+D、`15d118cef089` 二次热修+拖拽手柄、`f1d6fbb80c9a` 二次热修、`8dbb113f93cc` 初版 hardening）
- 容器 `probe-server` 已重建运行，端口 19527，挂载卷 **`yufu-probe_probe-data`**
- 验证：登录正常、25 台机器在线、**23 个自定义别名完好**（数据卷未丢）、前端 `?v=48` 已生效；本次重建后再验证——25+ agent 经 WS 正常回连、日志无 panic、P1 自动信任分支经回环+XFF 探测实测触发（见「五」）

### 回退方式（双锚点）
- **源码**：`git tag rollback-pre-hardening-20260924` → `9f754cd`（已打 tag，未推送，本地留存）
- **镜像**：`yufu-probe:rollback-pre-hardening-20260924` = 旧镜像 `8dbb113f93cc`（已在 VPS 保留）
- 回退步骤：`docker tag yufu-probe:rollback-pre-hardening-20260924 ghcr.io/shenping1200/yufu-probe:latest && cd /opt/yufu-probe && docker compose up -d --pull never`

### 部署铁律（务必遵守）
1. **VPS 永远 `docker compose up -d --pull never`**，绝不裸 `docker run -v probe-data` —— 裸名 `probe-data` 会被 Docker 静默建空卷，造成"数据丢失"假象（历史事故）。
2. 真实数据卷是 compose 前缀的 **`yufu-probe_probe-data`**，不是裸 `probe-data`（后者是事故孤儿，勿动）。
3. 前端是 Go embed 打进二进制，**改前端必须重编镜像**，切勿 scp 静态文件到磁盘（无效）。
4. VPS 上的 `configs/server.yaml`（含真实口令）**绝不从本地 push**；本地 `main` 的 `configs/server.yaml` 是默认模板，勿覆盖生产配置。
5. 改完前端记得 bump `index.html` 里的 `?v=` 版本号，否则浏览器按 `immutable` 缓存一年不刷新（本次已从 46→47）。

> ⚠️ VPS `/opt/yufu-probe` 目录的 git 状态较旧（被 scp 覆盖、残留早期未提交改动），**不影响运行**（运行用镜像）。如需在 VPS 重新构建，请先 `git pull` 或重新 scp 最新源码，**并保留 VPS 本地 `configs/server.yaml`**。

---

## 三、二次热修（v2 复审回归修复，2026-09-24 当晚）

> 触发：第三方交叉验证复审（`D:\EXE\yufu-probe-code-review-v2.md`）对 hardening 改动实测，发现 2 个新引入问题 + 1 个未修项 + 若干小隐患。
> 分支：`main`（fast-forward 合并 `hotfix-n1n2u1`，`dfd811a..fcb97c5`，5 个提交）
> 部署状态：VPS 已重建镜像 `f1d6fbb80c9a` 并验证通过

| 项 | 类型 | 问题 | 修复 |
|---|---|---|---|
| **N1** | 功能回归（P0） | `main.go` 新加的 `WriteTimeout:60s` 会掐断最长 600s 的批量执行/部署（`/api/agents/exec` 同步阻塞 `wg.Wait()`，超时上限 600s），客户端拿到连接重置而非结果 | **移除 WriteTimeout**（Read/ReadHeader/IdleTimeout 仍防慢速攻击）；正解异步 exec 留作后续 |
| **N2** | goroutine 泄漏（P0） | `api.go` agent WS 的 ping 保活协程 `for range pingTicker.C` 依赖 `Stop()` 退出，但 `Stop()` 不关 channel，每次 agent 断开泄漏一个 goroutine（规模 2500 台，比 #6 更严重） | 改用 **`done` channel**：handler 退出前 `close(done)` 显式通知，且 `close(done)` 排在 `conn.Close()` 之前 |
| **U1** | 数据竞争 + 卡顿（P1） | `stress.go` `Stop()` 锁外写 `e.agents=nil`、`Status()` 锁外读 `e.startTime`（实测 DATA RACE）；且 `Stop()` 对压测机逐台 `DeleteAgent`（走 `SetMaxOpenConns(1)` 串行 5000 次空删，停压测卡数秒） | 锁内写 `e.agents=nil`/`e.startTime`；`Status()` 锁内读 `startTime`；**删除冗余 DeleteAgent 循环**（已验证压测机走 `ApplyReportEphemeral` 从不在 `agents` 表落库） |

### 顺手项（v2 报告小隐患，一并清掉）
| 项 | 修复 |
|---|---|
| safePing/safeWrite 无写超时（对端不读则永久持 `writeMu`，卡死该 agent 全部 Web SSH/部署写入） | 写前设 10s 写超时、写后清除（与 `exec.go` 同款模式），不影响同连接其他写入 |
| XFF 限流绕过（直连暴露时攻击者伪造 XFF 换 IP 绕过登录限流） | 新增 `trusted_proxy` 配置（逗号分隔 IP/CIDR）；**仅当直连来源 IP 落在该列表才信任 X-Forwarded-For**，默认永不信任 |
| `loginFails` 无界增长（失败 IP 永久残留） | 加 30min TTL，访问时过期即清；锁内重置计数 |
| 冗余索引（`idx_ssh_lock_uuid` 主键已覆盖、`idx_agents_online` 0/1 低选择性） | 删除两索引（`DROP INDEX IF EXISTS` 兼容线上库） |
| `ssh_lock` 只增不减（`ResetSSHLock` 写 0 而非删行） | 改为 **DELETE**（成功即删行，与 `GetSSHLock` 缺行语义等价） |

### 回退锚点
- 源码：`git tag pre-hotfix-n1n2u1-20260924` → `dfd811a`（已打 tag，本地留存；回退即 checkout 该 tag 重编）
- 镜像：当前 `ghcr.io/shenping1200/yufu-probe:latest` = `f1d6fbb80c9a`（构建于 2026-09-24 ~13:05）

---

## 四、拖拽排序回归修复（2026-09-24 深夜）

### 问题
新增拖拽排序后，用户反馈**无法再用鼠标选中复制服务器 IP / 别名**。

### 根因（读源码定位）
`attachDragSort()` 给每个卡片/行无差别设 `draggable="true"`（原生 HTML5 拖拽）。
元素一旦 `draggable=true`，鼠标在其内部按下会优先触发**拖拽手势**而非**文本选择**，
导致卡片/行内的纯文本（IP、别名）无法选中复制。CSS 里的 `user-select:none` 仅在分组
标签按钮上，不是元凶。

### 修复
采用**独立拖拽手柄**方案（零依赖、不改后端）：
- `attachDragSort()`：默认 `item.setAttribute('draggable','false')`，仅在 `.drag-handle`
  元素的 `mousedown` 时临时置 `true`、`mouseup/mouseleave/dragend` 时复位 `false`。
  这样卡片/行默认可正常选中文本，只有抓住手柄（⠿）才发起拖拽。
- `cardInner()` / `listRowHTML()`：在卡片头部 / 列表首格加入 `<span class="drag-handle" title="拖拽排序">⠿</span>`。
- `style.css`：新增 `.drag-handle` 样式（`cursor:grab/grabbing`、`user-select:none`）。
- `index.html`：升缓存版本号 `style.css?v=27`、`app.js?v=48`（前端 Go embed，必须重建镜像）。

### 涉及文件
`server/static/app.js`、`server/static/style.css`、`server/static/index.html`

### 已部署 / 回退锚点
- 已部署镜像：`15d118cef089`（见「二、部署与回退」），`?v=48` 已生效。
- 源码回退：`git tag pre-fix-drag-text-select-20260924` → `e852b16`（已推送 GitHub）。

---

## 五、v3 复审修复（🔴 P1 + 🟠🟡 D 项，2026-09-25）

> 触发：第三方交叉验证复审（`D:\EXE\yufu-probe-code-review-v3.md`）对照源码复核 hardening + 二次热修，
> 确认 N1/N2/U1 真修好，并给出 🔴 P1（空 trusted_proxy+反代=自我 DoS）与若干 D 项。
> 分支：`main`（fast-forward 合并 `fix-p1-trusted-proxy`，`502e6c4..3db6938`，1 个提交）
> 部署状态：VPS 已重建镜像 `4fc42b2b2202` 并验证通过

### 🔴 P1：空 trusted_proxy + 反代 = 自我 DoS
- **机制（属实）**：二次热修引入 `trusted_proxy` 后，若运维**不填**该值（675 直连部署即如此），`clientIP()` 一律用 `RemoteAddr` 当限流 key。面板一旦经 caddy/nginx 反代，所有请求共用反代 IP 一个桶，连续 5 次登录失败锁死管理员 15 分钟（self-DoS）。
- **严重度校正**：675 实际是**直连暴露**（无 caddy 容器，docker-proxy 直连 19527），公网客户端 `RemoteAddr` 为公网 IP，本就不共用桶 → **675 不触发**；风险只在「反代拓扑 + 不配 trusted_proxy」。
- **修复（根治，覆盖两种拓扑）**：重写 `clientIP()`——
  1. 显式配置 `trusted_proxy` 时，仅当直连来源落在该列表才信任 XFF（原逻辑）；
  2. **未配置时，若直连来源是私网/回环（反代几乎必然），自动信任 XFF 取真实客户端**，使每个客户端各自一个限流桶，不被压成同一个；
  3. 公网直连暴露（直连来源是公网 IP）仍只用 `RemoteAddr`，保留「不信任伪造 XFF」安全意图（公网攻击者无法伪造私网/回环来源，BCP38 入口过滤会丢弃）；
  4. 显式配置但不匹配时打 `[WARN]` 提示运维核对。
- **验证（VPS 实测）**：从回环 `127.0.0.1` 带 `X-Forwarded-For: 203.0.113.99` 打 `/api/login`，日志打出
  `[config] 检测到反代（私网/回环来源），已自动信任 X-Forwarded-For 取真实客户端 IP；如需收紧可显式配置 trusted_proxy`，证明自动信任分支生效；675 公网直连行为不变（agents 正常回连、无 panic）。

### 🟠 D1：裸 IPv6 误补 /32
- `parseTrustedProxies()` 裸 IP 一律补 `/32`，裸 IPv6 会被当成 2^96 个地址的可信反代。
- 修复：按版本补掩码，IPv4→`/32`、IPv6→`/128`。

### 🟡 D2：loginFails 惰性 TTL 缓慢泄漏
- 二次热修加的 TTL 只在**命中该 IP** 时触发清理；一次性扫描 IP（每个 IP 只试一次）永不回访则条目永久残留、map 缓慢增长。
- 修复：新增 `startLoginFailsReaper()` 每 10 分钟**主动**扫描删除过期条目兜底（`main.go` 启动调用）。

### 🟡 D3：exec.go 死代码（写超时语义混乱）
- `runExec()` 内 3 处 `SetWriteDeadline(15s)` 与 `safeWrite/safePing` 统一 10s 写超时重复且冲突（15s 盖 10s），属死代码。
- 修复：删除 3 处 `SetWriteDeadline`，写超时完全交给 `safeWrite`；`exec.go` 仍用 `time` 包（timeout 等），无 orphan import。

### 涉及文件
`server/api.go`（clientIP 改写 + reaper + 两个 `sync.Once` 日志）、`server/config.go`（IPv6 /128）、
`server/exec.go`（删死代码）、`server/main.go`（启 reaper）、`configs/server.yaml`（补 `trusted_proxy` + `offline_threshold` 注释模板；该文件被上传脚本排除，不覆盖生产配置）

### 回退锚点
- 源码：`git tag pre-fix-p1-trusted-proxy-20260925` → `502e6c4`（**已推送 GitHub**，回退即 checkout 该 tag 重编）
- 镜像：当前 `ghcr.io/shenping1200/yufu-probe:latest` = `4fc42b2b2202`（构建于 2026-09-25）

---

## 六、bcrypt 落地（管理员口令哈希存储，2026-09-25）

> 触发：用户批准方案 B（bcrypt 落地，老规矩流程：分支 → ff 合并 main → 重建镜像 → VPS 验证 → 推 GitHub → 回退 tag）。
> 分支：`main`（fast-forward 合并 `fix-bcrypt-password`，`edd97e0..109a5c2`，1 个提交）
> 提交：`109a5c2` — feat: 管理员口令支持 bcrypt 哈希存储（兼容明文，向后平滑迁移）
> 部署状态：VPS 已重建镜像 `4c06a1e499a3` 并验证通过

### 目标校正（读源码后）
经核对 `server/api.go:281-282`、`server/config.go:37`、`server/config.go:110-111`：管理员登录口令**不落 SQLite，而是写在 `configs/server.yaml` 的 `admin.password`**；真正存库的是 deploy 规则密码且已 AES-GCM 加密（`server/deploy.go:38/61`）。故 bcrypt 的落地目标是 **yaml 明文管理员口令**，不是数据库。

### 改动
| 文件 | 改动 |
|---|---|
| `server/api.go` | 新增 `isBcryptHash()` + `checkAdminPassword()`：`admin.password` 以 `$2a$/$2b$/$2y$` 开头则走 `bcrypt.CompareHashAndPassword`，否则回退 `subtle.ConstantTimeCompare` 明文比对；loginHandler 口令比对改走 `checkAdminPassword()` |
| `server/main.go` | 新增 `hash-password <明文>` 子命令（`bcrypt.GenerateFromPassword`，默认 cost）；启动期若 `admin.password` 非 bcrypt 哈希则打 `[SECURITY]` 明文告警 |
| `server/config.go` | `AdminConfig.Password` 注释更新（可明文或 bcrypt 哈希，建议哈希；旧部署明文仍兼容） |
| `go.mod` / `go.sum` | 钉死 `golang.org/x/crypto v0.36.0`（兼容 Go 1.25 构建镜像；避开 `v0.57.0` 需 Go 1.26 的坑，否则 `go build -mod=readonly` 失败） |

### 向后兼容设计（关键）
- 存储值若是 bcrypt 哈希 → bcrypt 比对；否则 → 原常数时间明文比对。**老部署（yaml 仍是明文）无需停机、无需改配置即可继续登录**，仅启动期打一条明文告警提示迁移。
- 提供 `yufu-server hash-password <口令>` 一键生成哈希，运维替换 `admin.password` 即可完成迁移，登录路径对该次登录瞬间切换为哈希比对。

### 验证（VPS 实测，全绿）
- `docker exec probe-server /app/probe-server hash-password secret123` → `$2a$10$9jKT6r7z...`（`$2a$` 是 Go bcrypt 标准输出，证明 bcrypt 已编入二进制）。
- 启动日志触发 `[SECURITY] 管理员口令为明文存储，建议使用 bcrypt 哈希...`（当前生产 yaml 仍为明文，告警提示迁移）。
- 首页 `GET /` → HTTP 200；错误口令登录 `POST /api/login` → 401；19 个 agent 经 WS 正常回连、日志无 panic。

### 涉及文件
`server/api.go`、`server/main.go`、`server/config.go`、`go.mod`、`go.sum`

### 回退锚点
- 源码：`git tag pre-fix-bcrypt-20260925` → `edd97e0`（**已推送 GitHub**，回退即 checkout 该 tag 重编）
- 镜像：`yufu-probe:rollback-pre-bcrypt-20260925` = `4fc42b2b2202`（已在 VPS 保留；回退：`docker tag yufu-probe:rollback-pre-bcrypt-20260925 ghcr.io/shenping1200/yufu-probe:latest && cd /opt/yufu-probe && docker compose up -d --pull never`）

---

## 七、v5 复审修复（V5-1 + V4-1 + V5-3/4/5，2026-09-25）

> 触发：第三方交叉验证复审（`D:\EXE\yufu-probe-code-review-v5.md`）对照源码复核，确认 V5-1（关键）、V4-1（实质仍存）、V5-3/4/5 成立；675 直连暴露下 V4-1/V5-2 不触发。
> 分支：`main`（fast-forward 合并 `fix-v5-review`，`3b50d8f..aa6ac6a`，1 个提交）
> 提交：`aa6ac6a` — fix(v5): V5-1 切断 admin.password to SSH 回退链 + V4-1 XFF 取最右合法项 + V5-3/4/5 健壮性
> 部署状态：VPS 已重建镜像 `ec6a18c1992f` 并验证通过

### V5-1（🔴 关键）：`admin.password` 字段两用导致迁移后 SSH 全挂 + 24h 锁
- **机制（属实）**：`configs/server.yaml` 的 `admin.password` **既是面板登录凭据**（bcrypt 落地后走哈希比对），**又是** Web SSH（`terminal.go`）、批量执行（`exec.go`）、自动部署（`deploy.go` 的 `effectiveSSHPassword`）三处的**共享明文 SSH 密钥**。三处都有内联回退 `eff = cfg.SSHPassword; if eff=="" { eff = cfg.Admin.Password }`。
- **灾难链**：运维一旦按「六」执行 `hash-password` 把 `admin.password` 迁移成 bcrypt 哈希，而 `server.yaml` 模板缺 `ssh_password` 项（旧部署绝大多数如此）→ 三处回退把 **bcrypt 哈希当明文 SSH 口令** 比对 → Web SSH / 批量执行 / 自动部署 **全部失败**，且 Web SSH / 批量会触发 **24h 锁定**（self-DoS，且定位极难，因为日志只报认证失败）。
- **修复（根治）**：
  1. 抽出统一函数 `effectiveSSHPassword(cfg)`（deploy.go）：`ssh_password` 优先；其次 `admin.password` 是**明文**才回退；**若为 bcrypt 哈希则一律返回空串**（绝把哈希当明文）；都为空则返回空。
  2. `terminal.go:143`、`exec.go:476` 两处内联回退改为调用该函数。
  3. `main.go` 启动期新增 **V5-1 告警**：`admin.password` 是 bcrypt 哈希且 `ssh_password` 为空 → 打 `[SECURITY]` 明确提示"会把哈希当明文、将全部失败（且触发 24h 锁定），请显式设置 `ssh_password` 再迁移"；`ssh_password` 未设置提示也按"哈希态=将不可用 / 明文态=将复用管理员密码"分说。
  4. `configs/server.yaml` 模板在 `admin:` 块下补 `ssh_password: ""` 及注释（说明留空回退、哈希态必须显式设置、仅服务端比对不下发）。**该文件被上传脚本排除，不覆盖生产配置。**
- **关键约束**：运维**必须**在 V5-1 修好上线验证后，才能执行 `hash-password` 迁移 `admin.password`——顺序反了就会触发上述灾难链。

### V4-1（🟠 实质仍存）：伪造 XFF 绕过登录限流
- **机制（属实）**：原 `clientIP()` 取 XFF **最左**项 `[0]` 且无 IP 合法性校验，伪造 `X-Forwarded-For` 即可无限换 IP 绕过登录限流（每次失败换一个新桶）。bcrypt 70ms 成本在反代拓扑下变成 CPU DoS 杠杆。
- **修复**：新增 `rightmostValidXFF(xff)` 取**最右合法 IP**（从右往左首个能 `net.ParseIP` 的项）；`trustXFF` 只在「显式 trusted_proxy 命中」或「未配置 trusted_proxy 且直连来源是私网/回环」时信任 XFF；公网直连暴露（直连来源是公网 IP）仍只用 `RemoteAddr`。
- **675 现状**：675 是**直连暴露**，公网客户端 `RemoteAddr` 为公网 IP，不进 XFF 分支 → **不触发**；风险仅在「反代拓扑 + 不配 trusted_proxy」。

### V5-3 / V5-4 / V5-5（🟡 健壮性）
| 项 | 问题 | 修复 |
|---|---|---|
| **V5-3** | `hash-password` 明文口令经命令行参数传入，出现在进程列表（`ps`）、shell 历史（`history`） | 优先从 **stdin** 读取（`io.ReadAll(os.Stdin)` + 去 `\r\n`）；仍兼容 argv 但打 `[WARN]` 提示不推荐 |
| **V5-4** | bcrypt 仅取前 72 字节，超长口令 `GenerateFromPassword` 直接报错且无中文提示；比对侧静默截断导致"改长口令后旧哈希仍匹配" | 生成前 `len(pw) > 72` 主动 `log.Fatalf` 中文提示（生成侧防呆）；比对侧靠 `isBcryptHash` 前缀校验 + 启动期探合法性（见 V5-5）兜底 |
| **V5-5** | `isBcryptHash` 仅查 `$2a$/$2b$/$2y$` 前缀，复制截断/格式错的"伪哈希"会让登录**永远失败且无任何诊断** | 启动期用错误口令 `bcrypt.CompareHashAndPassword` 探一次合法性，若非 `ErrMismatchedHashAndPassword`（即哈希本身解析失败）则打 `[SECURITY]` 告警，提前暴露"复制截断"类事故 |

### 涉及文件
`server/deploy.go`（抽 `effectiveSSHPassword`）、`server/terminal.go`（改调）、`server/exec.go`（改调）、
`server/api.go`（`clientIP` 改写 + `rightmostValidXFF`）、`server/main.go`（`hash-password` 重写 + V5-1/V5-5 启动告警 + `ssh_password` 提示）、`configs/server.yaml`（补 `ssh_password` 模板项；该文件被上传脚本排除，**不覆盖生产配置**）

### 验证（VPS 实测，全绿）
- **编译 gate**：`docker build -f Dockerfile.server` → `BUILD_EXIT=0`（go.mod/go.sum 解析正常、实编译通过）。
- **部署**：重建镜像 `ec6a18c1992f`，容器 `probe-server` 重建运行，**日志无 panic**。
- **V5-3/4（hash-password）**：
  - stdin 路径 → 输出 `$2a$10$...`，RC=0；
  - argv 路径 → 先打 `[WARN] 通过命令行参数传入明文口令会出现在进程列表与 shell 历史中...`，再出哈希，RC=0；
  - 80 字节 → `口令过长：bcrypt 上限为 72 字节，请缩短后再生成（当前 80 字节）`，RC=1（Fatalf 中文提示）；
  - 恰好 72 字节 → 正常出哈希 RC=0（边界正确）。
- **V5-1（effectiveSSHPassword 就位）**：`deploy.go:275` 定义，`terminal.go:143` + `exec.go:476` 两处调用均存活。bcrypt+空 `ssh_password` 返回空串分支经源码审查确认；运行时生产 yaml 为明文 `admin.password` + 显式 `ssh_password`，走回退分支——**20 个 agent 全部回连并"Web SSH 可用"**，印证正常态未回归。
- **V4-1（XFF）**：`trustXFF` + `rightmostValidXFF` + `trusted_proxy` 逻辑全部就位。
- **HTTP smoke**：首页 `GET /` → 200；错误口令 `POST /api/login` → 401。

### 回退锚点
- 源码：`git tag pre-fix-v5-20260925` → `3b50d8f`（**已推送 GitHub**，回退即 checkout 该 tag 重编）
- 镜像：`yufu-probe:rollback-pre-v5-20260925` = `4c06a1e499a3`（已在 VPS 保留；回退：`docker tag yufu-probe:rollback-pre-v5-20260925 ghcr.io/shenping1200/yufu-probe:latest && cd /opt/yufu-probe && docker compose up -d --pull never`）

---

## 八、v6 复审修复（V6-1 空口令直通回归 + V4-2/V6-2/V6-3 + V4-7，2026-09-25）

> 触发：第三方交叉验证复审（`D:\EXE\yufu-probe-code-review-v6.md`）对照源码复核 v5 增量（`3b50d8f..b15cdc0`），确认 V5-1/V4-1/V5-3·4·5 修对，但发现 v5 引入的**空口令直通回归 V6-1**，并复核 V4-2/V6-2/V6-3/V4-7 等历史项。
> 分支：`main`（fast-forward 合并 `fix-v6-review`，`b15cdc0..a05a59b`，1 个提交）
> 提交：`a05a59b` — fix(v6): V6-1 三处调用点显式拒绝空口令直通 + V4-2 trustXFF 私网回环恒信任 + V6-2 跳可信段 + V6-3 IP 规范化 + V4-7 注释
> 部署状态：VPS 已重建镜像 `7915deb8cfcb` 并验证通过

### 🔴 V6-1（v5 引入的回归，本轮最该先修）：`eff == ""` 导致空口令直通
- **根因（如实）**：v5 把 `effectiveSSHPassword` 的返回值契约从"永不为空"改成"bcrypt+空 ssh_password 时返回空串"，但三处调用点仍是单向 `!=` 比较——`eff==""` 且提交空口令时 `"" != ""` 为 false → 通过鉴权。
- **触发路径恰好是文档引导运维去做的事**：按 v5/文档把 `admin.password` 迁成 bcrypt 且没配 `ssh_password` → 提交**空口令**即可通过 Web SSH / 批量执行拿到被管机 root shell；自动部署中"规则未存密码"也会因 `pw==""` 放行。**且全程无日志**（不进"密码错误"分支）。
- **前置**：`requireAdmin` 已生效，攻击者需先有合法 admin 会话——但 Web SSH/批量执行是"第二道门"（设计意图：即使面板会话被盗也拿不到被管机 root），这道门对空字符串敞开 = 第二道门拆了。
- **修复（3 行）**：三处调用点都加 `eff == ""` 显式拒绝——
  - `terminal.go` / `exec.go`：Web SSH / 批量执行在 `eff == ""` 时直接返回明确错误"未配置 ssh_password，Web SSH 不可用"（**不误触发 24h 锁定**，便于运维定位）；
  - `deploy.go`：自动部署 `eff == "" || pw != eff` → 跳过该规则。
- **范围说明**：v5 修复的"bcrypt+空 ssh_password → SSH 全挂"与本轮"空口令直通"是同一配置态的两面；V6-1 修好后，**未配 ssh_password 既不会全挂（V5-1）也不会被空口令绕过（V6-1）**——统一为"明确拒绝 + 提示先配 ssh_password"。运维迁移 `admin.password` 为 bcrypt 的**硬前置条件**仍是：先显式设置 `ssh_password`。

### 🟠 V4-2（显式 trusted_proxy 配错 → 回落 RemoteAddr → self-DoS）
- **根因（如实）**：v3/v5 的 `trustXFF` 仅在"直连源在 trusted 列表"或"未配 trusted 且源私网/回环"时为真；若**显式配了 trusted_proxy 但直连源（私网/回环反代）没在列表内**，就回落成"所有请求共用反代 IP 一个限流桶"→ 5 次失败锁死管理员 15 分钟（P1 self-DoS 复现）。
- **修复**：`trustXFF` 改为 `sourcePrivate || (len(trusted) > 0 && isTrustedProxy(host, trusted))`——**直连来源是私网/回环即信任 XFF**（公网攻击者无法伪造私网/回环，BCP38 入口过滤丢弃，安全），既修 self-DoS 又不弱化安全；公网直连仍只用 RemoteAddr。配置不匹配的告警文案同步修正（已自动信任，提示可把源加入 trusted 收紧）。

### 🟡 V6-2（多层反代时最右项是反代自身 IP）
- **根因（如实）**：`rightmostValidXFF` 自右向左取第一个合法 IP，两层反代时最右项就是第二层反代的 IP → 复用桶（P1 另一种触发）。
- **修复**：显式配置 `trusted_proxy` 时，扫描跳过落在可信网段内的项（`isTrustedProxy(cand, trusted)`），从而取到真实客户端 IP。**未配置 trusted 时不跳过**（保留 CGNAT 客户端按真实私网 IP 分桶的正确性，避免误伤）。

### 🟡 V6-3（IP 未规范化 → 同一来源拆多个桶）
- **根因（如实）**：`rightmostValidXFF` 返回原始串而非 `net.ParseIP(cand).String()`，`1.2.3.4` 与 `::ffff:1.2.3.4`、IPv6 不同写法 → 多个限流桶（阈值变相翻倍）。
- **修复**：`return net.ParseIP(cand).String()` 规范化（一行）。

### 🟡 V4-7（`trusted_proxy` 注释自相矛盾）
- **根因（如实）**：原注释既说"反代时【必须】填写"又说"留空也会自动信任"——打架，会误导运维填错值（填错后果是静默 P1）。
- **修复**：重写注释为准确口径：直连暴露/私网回源反代 → 留空自动信任；**仅公网反代/CDN（呈现公网 IP 给探针）才必须填**；填错也不 self-DoS（已按私网回环自动信任），只是放弃收紧。

### 涉及文件
`server/terminal.go`（Web SSH 加 `eff==""` 拒绝）、`server/exec.go`（批量执行加 `eff==""` 拒绝）、`server/deploy.go`（自动部署 `eff=="" || pw!=eff`）、`server/api.go`（`trustXFF` 重写 + `rightmostValidXFF(xff, trusted)` 跳过可信段 + 规范化 + 告警文案）、`configs/server.yaml`（重写 `trusted_proxy` 注释；该文件被上传脚本排除，**不覆盖生产配置**）

### 验证（VPS 实测，全绿）
- **编译 gate**：`docker build -f Dockerfile.server` → `BUILD_EXIT=0`。
- **部署**：重建镜像 `7915deb8cfcb`，容器重建运行，**日志无 panic**；运行容器镜像 ID = `sha256:7915deb8cfcb...` 确认。
- **V6-1 就位**：`terminal.go`/`exec.go` 的 `if eff == "" {` 各 1 处；`deploy.go` 的 `if eff == "" || pw != eff` 1 处（grep 确认）；prod 为明文 admin.password + 显式 ssh_password → eff 非空，正常态未回归（24 个 agent 回连"Web SSH 可用"）。
- **V4-2/V6-2/V6-3 就位**：`api.go` 的 `sourcePrivate || (len(trusted)`、`rightmostValidXFF(xff, trusted)`、`isTrustedProxy(cand, trusted)` 各 1 处（grep 确认）。
- **HTTP smoke**：首页 200、错误口令 401；`hash-password` stdin → `$2a$10$...` RC=0。
- **注**：V6-1 的"空口令直通"只能在 `admin.password`=bcrypt + `ssh_password`=空 的配置态复现（非本生产态），已通过源码审查 + 三处 `eff==""` 显式拒绝确认修复；完整运行时复测交由交叉复审者按报告第八节清单执行。

### 回退锚点
- 源码：`git tag pre-fix-v6-20260925` → `b15cdc0`（**已推送 GitHub**，回退即 checkout 该 tag 重编）
- 镜像：`yufu-probe:rollback-pre-v6-20260925` = `ec6a18c1992f`（已在 VPS 保留；回退：`docker tag yufu-probe:rollback-pre-v6-20260925 ghcr.io/shenping1200/yufu-probe:latest && cd /opt/yufu-probe && docker compose up -d --pull never`）

---

## 九、v7 复审修复（V7-1 未配置 trusted_proxy 多层反代 + CI，2026-09-25）

> 触发：第三方交叉验证复审（`D:\EXE\yufu-probe-code-review-v7.md`）对照源码复核 v6 增量（`b15cdc0..38ee814`），结论：v6 的 V6-1/V4-2/V6-2/V6-3/V4-7 **全部修对、无新回归**（第一轮七轮里首次零新回归）。仅剩一个实质性残留 **V7-1**：`rightmostValidXFF` 只在显式配置 `trusted_proxy` 时才跳过可信反代自身 IP。
> 分支：`main`（fast-forward 合并 `fix-v7-review`，`38ee814..600b9c2`，1 个提交）
> 提交：`600b9c2` — fix(v7): rightmostValidXFF 未配置 trusted_proxy 时也跳过私网/回环段（V7-1）+ 新增 clientip 单测 + 新增 CI 工作流
> 部署状态：VPS 已重建镜像 `a8bd14f82d74` 并验证通过

### 🟠 V7-1（两层及以上反代 + 未配置 trusted_proxy → 取到反代自身内网 IP，P1 self-DoS 变体）
- **根因（如实）**：v6 的 `rightmostValidXFF` 只在 `len(trusted) > 0` 时跳过可信段；未配置 `trusted_proxy`（默认部署，也是模板推荐值）时**不跳过任何项**，两层反代的最右项就是内层反代自己的私网 IP → 所有请求共用该私网 IP 一个限流桶 → 5 次失败锁死管理员 15 分钟（与 P1 等价，但触发条件更窄：需两层及以上反代）。
- **对 675 现状零影响**：`rightmostValidXFF` 仅在 `trustXFF` 为真（直连来源是私网/回环，即前面真架了反代）时才被调用；675 直连暴露、公网来源 → `trustXFF` 为假 → 该函数根本不执行，V7-1 在 675 不触发。源码已核（api.go:144/192）。
- **修复（~6 行）**：未配置 `trusted_proxy` 时，用私网/回环作启发式跳过——反代自身回源地址几乎必然为私网/回环，据此跳过即可取到真实客户端 IP；全部被跳过（XFF 全私网/回环）时返回空串，调用方回落 RemoteAddr，不取伪造值。
- **配套**：新增 `server/clientip_test.go` 纯函数单测，覆盖 V7-1 两层反代未配置 / V4-2 配置不匹配 / V6-3 IPv4-mapped 规范化 / 全私网回落等场景，直接锁住 V7-1 修复。

### 📌 CI 自动化（七轮里所有真 bug 均靠 race 检测 + 实测抓出，读代码抓不到）
- **新增** `.github/workflows/ci.yml`：push/pr 到 main 时自动跑 `go build ./...` + `go vet ./...` + `go test -race ./server/` + `gofmt`（仅查本次改动文件，历史 10 个未格式化文件见 V4-5 backlog，不阻断）。
- 价值：以后每次提交自动发现回归，不再依赖人工交叉复审。

### 涉及文件
`server/api.go`（`rightmostValidXFF` 未配置 trusted 时按私网/回环跳过 + 注释同步）、`server/clientip_test.go`（新增单测）、`.github/workflows/ci.yml`（新增 CI）

### 验证（VPS 实测，全绿）
- **编译 gate**：`docker build -f Dockerfile.server` → `BUILD_EXIT=0`。
- **部署**：重建镜像 `a8bd14f82d74`，容器重建运行，运行容器镜像 ID = `sha256:a8bd14f82d74` 确认；**日志无 panic**；24 个 agent 经 WS 回连"Web SSH 可用"（无回归）。
- **V7-1 就位**：`api.go` 的 `else if ip.IsLoopback() || ip.IsPrivate()` 1 处（grep 确认）；`clientip_test.go` 含 `TestClientIP` + `TestRightmostValidXFF`（grep 确认 2 处）。
- **HTTP smoke**：首页 200、错误口令 401；`hash-password` stdin → `$2a$10$...` RC=0。
- **注**：V7-1 在 675 直连部署不触发（见上"对 675 现状零影响"）；CI 的 `go test -race ./server/` 含 V7-1 单测，GitHub 推送后自动跑。

### 回退锚点
- 源码：`git tag pre-fix-v7-20260925` → `38ee814`（**已推送 GitHub**，回退即 checkout 该 tag 重编）
- 镜像：`yufu-probe:rollback-pre-v7-20260925` = `7915deb8cfcb`（已在 VPS 保留；回退：`docker tag yufu-probe:rollback-pre-v7-20260925 ghcr.io/shenping1200/yufu-probe:latest && cd /opt/yufu-probe && docker compose up -d --pull never`）

---

## 十、后续建议（更新）
- ~~引入 **bcrypt** 替换明文口令比对（需评估构建依赖）。~~ **已完成（见「六、bcrypt 落地」）；生产 `admin.password` 仍明文，待运维执行 `hash-password` 迁移。**
- ✅ **CI 已落地**（见「九」）：`go test -race ./server/` + `go vet` + `gofmt` + 构建，push/pr 自动跑；长请求冒烟用例（断言 exec 超时 > WriteTimeout 不失败）留作后续补充——N1/U1 这类"读代码难发现、一跑就露馅"的回归主要靠它自动抓。
- 评估 `exec` 改异步（提交返回 job id、前端轮询），彻底解 N1 且顺带解调度 ticker stall（原 #15）。
- 评估 `exec` 改异步（提交返回 job id、前端轮询），彻底解 N1 且顺带解调度 ticker stall（原 #15）。
- 评估报告其余 P2 项（调度 ticker stall、Windows Web SSH 等）是否在本项目适用。
