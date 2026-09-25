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
- VPS 镜像：`ghcr.io/shenping1200/yufu-probe:latest` = `4c06a1e499a3`（构建于 2026-09-25，含 P1+D 修复 + bcrypt 落地；历史镜像：`4fc42b2b2202` P1+D、`15d118cef089` 二次热修+拖拽手柄、`f1d6fbb80c9a` 二次热修、`8dbb113f93cc` 初版 hardening）
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

## 七、后续建议（更新）
- ~~引入 **bcrypt** 替换明文口令比对（需评估构建依赖）。~~ **已完成（见「六、bcrypt 落地」）；生产 `admin.password` 仍明文，待运维执行 `hash-password` 迁移。**
- 加 CI 跑 **`go test -race ./server/`**，并加一条**长请求冒烟用例**（断言 exec 超时 > WriteTimeout 不失败）——N1/U1 这类"读代码难发现、一跑就露馅"的回归只能靠它自动抓。
- 评估 `exec` 改异步（提交返回 job id、前端轮询），彻底解 N1 且顺带解调度 ticker stall（原 #15）。
- 评估报告其余 P2 项（调度 ticker stall、Windows Web SSH 等）是否在本项目适用。
