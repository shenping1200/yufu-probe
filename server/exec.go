package main

// 批量命令下发（方案 A：不改 Agent）。
//
// 复用现有 Web SSH 的交互式 shell 通道：服务端自己扮演「驱动方」，对每台目标机器
// 自动开一个内部 exec 会话（execSession），向 agent 发 shell_open，把多行脚本以
// base64 写进临时文件再执行，用「前后各 echo 一个随机会话标记」界定输出边界，最后
// 发 shell_close。这样无需 Agent 配合新协议，直接给全部已装 Agent 的机器批量跑命令。
//
// 数据回流路径（唯一）：agent → agentWSHandler 的读循环 → feedExecData。
// 本文件严禁自己去读 agent.conn —— 那条连接的读者只能是 agentWSHandler 一个协程，
// gorilla/websocket 不允许并发读；抢读会吞掉状态上报和别人的 Web SSH 数据。

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// tokRoot 是会话标记的固定前缀。脚本里通过变量拼接生成完整标记，
// 保证「注入命令自身被 PTY 回显」时回显文本中不会出现完整标记串（详见 buildExecScript）。
const tokRoot = "YUFU"

// b64LineWidth 注入脚本里 base64 每行长度。远小于 tty 规范模式的 4096 字节/行上限，
// 留足余量给回显与行首提示符。
const b64LineWidth = 512

// maxExecCommand 单次批量命令的脚本体积上限。
// 注入是逐字节写进对端 PTY 的 stdin，太大既慢又可能把 agent 的 readLoop 卡在 pty.Write 上。
const maxExecCommand = 64 * 1024

// maxExecOutput 单台机器输出的累积上限。
// 批量场景下这个上限是必须的：并发 200 台各跑个 journalctl 就能把服务端内存打满。
// 超限后只保留尾部滑动窗口（execTailKeep），既控住内存又不丢结束标记。
const maxExecOutput = 256 * 1024

// execTailKeep 超限后保留的尾部窗口大小。结束标记与 EXIT= 行都在输出末尾，
// 留这么多足够解析出退出码。
const execTailKeep = 8 * 1024

// execSession 一次「服务端驱动」的单台命令执行。
// 与 terminal.go 的 termSession 并存：termSession 桥接浏览器终端，execSession 由
// 服务端代码驱动，没有浏览器参与。
type execSession struct {
	sid   string
	uuid  string
	tok   string // 本次会话的完整标记，输出里出现两次即视为执行完毕
	agent *Client

	mu        sync.Mutex   // 保护 out/tail/abortErr：feedExecData 在 agentWSHandler 协程写，runExec 在 HTTP 协程读
	out       bytes.Buffer // 累积 agent 回传的 shell_data（已 base64 解码），上限 maxExecOutput
	tail      []byte       // 超限后的尾部滑动窗口，保证结束标记与 EXIT= 行不丢
	truncated bool         // 是否已超限截断
	abortErr  string       // 非空表示会话被中止（例如 agent 掉线）

	done      chan struct{}
	closeOnce sync.Once

	// DataCb 供测试观察每片解码后的数据，生产环境为 nil。
	DataCb func(data string)
}

// finish 幂等地结束会话
func (s *execSession) finish() { s.closeOnce.Do(func() { close(s.done) }) }

// feed 累积一片已解码数据，并在见到第二个标记时结束会话。
// 超过 maxExecOutput 后不再增长主缓冲，改写入尾部滑动窗口，避免大输出把内存吃光。
func (s *execSession) feed(text string) {
	s.mu.Lock()
	if !s.truncated && s.out.Len()+len(text) <= maxExecOutput {
		s.out.WriteString(text)
	} else {
		if !s.truncated {
			s.truncated = true
			// 迁移 len(tok)-1 字节重叠，避免标记正好跨在边界上被切成两半。
			// 只能是 len-1：取满 len(tok) 会把已在 out 里计数过的标记在 tail 里重复计一次，
			// 导致标记数虚增到 2、命令还没跑完就判定结束。
			cur := s.out.String()
			if n := len(s.tok) - 1; len(cur) > n {
				s.tail = append(s.tail[:0], cur[len(cur)-n:]...)
			} else {
				s.tail = append(s.tail[:0], cur...)
			}
		}
		s.tail = append(s.tail, text...)
		if len(s.tail) > execTailKeep {
			s.tail = s.tail[len(s.tail)-execTailKeep:]
		}
	}
	hits := strings.Count(s.out.String(), s.tok)
	if s.truncated {
		hits += strings.Count(string(s.tail), s.tok)
	}
	s.mu.Unlock()
	if s.DataCb != nil {
		s.DataCb(text)
	}
	if hits >= 2 {
		s.finish()
	}
}

// snapshot 带锁地取当前已收集到的全部输出（截断过则拼上尾部窗口并注明）
func (s *execSession) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.truncated {
		return s.out.String()
	}
	return s.out.String() + "\n…[输出过长，中间已截断]…\n" + string(s.tail)
}

// abort 中止会话（agent 掉线等），记录原因并唤醒等待方
func (s *execSession) abort(reason string) {
	s.mu.Lock()
	if s.abortErr == "" {
		s.abortErr = reason
	}
	s.mu.Unlock()
	s.finish()
}

func (s *execSession) aborted() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abortErr
}

var (
	execMu       sync.RWMutex
	execSessions = make(map[string]*execSession)
)

func registerExec(sid string, ses *execSession) {
	execMu.Lock()
	execSessions[sid] = ses
	execMu.Unlock()
}

func unregisterExec(sid string) {
	execMu.Lock()
	delete(execSessions, sid)
	execMu.Unlock()
}

func findExec(sid string) *execSession {
	execMu.RLock()
	defer execMu.RUnlock()
	return execSessions[sid]
}

// feedExecData 由 agentWSHandler 收到 agent 回传的 shell_data 时调用。
// 注意：dataB64 与转发给浏览器的负载一样是 base64（agent 侧编码），这里必须先解码，
// 否则标记永远匹配不上、每次执行都会走到超时分支。
func feedExecData(session, dataB64 string) {
	ses := findExec(session)
	if ses == nil {
		return
	}
	text := dataB64
	if dec, err := base64.StdEncoding.DecodeString(dataB64); err == nil {
		text = string(dec)
	}
	ses.feed(text)
}

// abortExecForAgent 在 agent 掉线时中止它名下所有批量命令会话，
// 避免调用方一直干等到 timeout（最长可配 600s）。
func abortExecForAgent(agentUUID string) {
	execMu.RLock()
	var victims []*execSession
	for _, ses := range execSessions {
		if ses.uuid == agentUUID {
			victims = append(victims, ses)
		}
	}
	execMu.RUnlock()
	for _, ses := range victims {
		ses.abort("客户端在执行过程中断开")
	}
}

// ExecResult 单台机器的执行结果
type ExecResult struct {
	UUID     string `json:"uuid"`
	Status   string `json:"status"` // ok | error | timeout | offline
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Error    string `json:"error,omitempty"`
}

// buildExecScript 生成注入到交互式 shell 的驱动脚本。
//
// 三个关键设计（都是踩过的坑）：
//  1. 先 stty -echo 关 PTY 回显，再把脚本 base64 写进临时文件 —— 大坨 base64 的回显
//     发生在起始标记之前，不会污染捕获区。
//  2. 标记用 `__YP=YUFU` + `__YT="${__YP}_EXEC_xxx"` 拼接产生：即使 stty 失败、
//     命令被原样回显，回显文本里也不含完整标记串，不会把「回显」误判成「执行完毕」。
//  3. sudo 先探测再执行（`if sudo -n true; then ... else ... fi`），而不是
//     `sudo -n bash || bash` —— 后者在脚本自身返回非 0 时会把命令重跑一遍。
func buildExecScript(command, tag string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(command))
	tmp := "/tmp/.yufu_exec_" + tag + ".sh"
	var b strings.Builder
	b.WriteString("stty -echo 2>/dev/null\n")
	// 清空提示符：PS1 是 bash 主动打印的，stty -echo 关不掉它。不清的话每台机器的输出里
	// 都会夹两行 `root@host:/path#`（真机实测），批量几百台时噪声很可观。
	// PROMPT_COMMAND 一并清掉，有些发行版用它输出设置终端标题的转义序列。
	b.WriteString("PS1=\nPROMPT_COMMAND=\n")
	// 关掉 readline 的 bracketed paste，否则每次显示提示符都吐 \e[?2004h / \e[?2004l
	b.WriteString("bind 'set enable-bracketed-paste off' 2>/dev/null\n")
	b.WriteString("base64 -d > " + tmp + " <<'YUFU_EOF'\n")
	// base64 必须按行折叠：PTY 处于规范（canonical）模式时，tty 行缓冲对单行有 4096 字节
	// 上限，超长的一行会被内核丢弃/截断，几 KB 的部署脚本就会静默损坏。
	// base64 -d 本身能吃多行输入，折叠没有副作用。
	for i := 0; i < len(b64); i += b64LineWidth {
		end := i + b64LineWidth
		if end > len(b64) {
			end = len(b64)
		}
		b.WriteString(b64[i:end] + "\n")
	}
	b.WriteString("YUFU_EOF\n")
	b.WriteString("__YP=" + tokRoot + "\n")
	b.WriteString("__YT=\"${__YP}_EXEC_" + tag + "\"\n")
	b.WriteString("echo \"$__YT\"\n")
	b.WriteString("if sudo -n true 2>/dev/null; then sudo -n bash " + tmp + " 2>&1; else bash " + tmp + " 2>&1; fi\n")
	b.WriteString("echo \"EXIT=$?\"\n")
	b.WriteString("echo \"$__YT\"\n")
	b.WriteString("rm -f " + tmp + "\n")
	return b.String()
}

// runExec 对单台 agent 执行一段脚本，返回结果。
// agentUUID 用于登记会话（掉线中止、排查用）；command 为用户输入的多行脚本；
// timeout 为整体超时（含开 shell + 注入 + 等输出）。
// dataCb 仅测试用（观察每片数据），生产不传。
func runExec(agent *Client, agentUUID, command string, timeout time.Duration, dataCb ...func(data string)) ExecResult {
	tag := strings.ReplaceAll(uuid.New().String(), "-", "")
	ses := &execSession{
		sid:   uuid.New().String(),
		uuid:  agentUUID,
		tok:   tokRoot + "_EXEC_" + tag,
		agent: agent,
		done:  make(chan struct{}),
	}
	if len(dataCb) > 0 && dataCb[0] != nil {
		ses.DataCb = dataCb[0]
	}

	// 必须先登记再开 shell：agent 可能在 shell_open 的 ack 里立刻回数据
	registerExec(ses.sid, ses)
	defer unregisterExec(ses.sid)

	// 1) 开 shell
	openMsg, _ := json.Marshal(map[string]interface{}{
		"action": "shell_open", "session": ses.sid, "cols": 200, "rows": 24,
	})
	if err := agent.safeWrite(openMsg); err != nil {
		return ExecResult{UUID: ses.uuid, Status: "error", Error: "开 shell 失败: " + err.Error()}
	}
	closeShell := func() {
		closeMsg, _ := json.Marshal(map[string]string{"action": "shell_close", "session": ses.sid})
		_ = agent.safeWrite(closeMsg)
	}

	// 2) 按 3KB 切片注入（模拟键盘输入；同一 stdin 流按序到达，切片边界无所谓）
	script := buildExecScript(command, tag)
	const chunk = 3000
	for i := 0; i < len(script); i += chunk {
		end := i + chunk
		if end > len(script) {
			end = len(script)
		}
		payload := base64.StdEncoding.EncodeToString([]byte(script[i:end]))
		fwd, _ := json.Marshal(map[string]string{"action": "shell_input", "session": ses.sid, "data": payload})
		if err := agent.safeWrite(fwd); err != nil {
			closeShell()
			return ExecResult{UUID: ses.uuid, Status: "error", Error: "注入命令失败: " + err.Error()}
		}
	}

	// 3) 等结束标记 / 中止 / 超时。数据由 agentWSHandler → feedExecData 灌入，这里只等。
	select {
	case <-ses.done:
	case <-time.After(timeout):
		closeShell()
		return ExecResult{UUID: ses.uuid, Status: "timeout", Error: "执行超时（已收集部分输出）",
			Stdout: cleanExecOutput(extractOutput(ses.snapshot(), ses.tok))}
	}
	closeShell()

	if reason := ses.aborted(); reason != "" {
		return ExecResult{UUID: ses.uuid, Status: "error", Error: reason,
			Stdout: cleanExecOutput(extractOutput(ses.snapshot(), ses.tok))}
	}
	res := parseExecOutput(ses.snapshot(), ses.tok)
	res.UUID = ses.uuid
	return res
}

// extractOutput 取第一个 tok 与第二个 tok 之间的内容
func extractOutput(raw, tok string) string {
	s := strings.Index(raw, tok)
	if s < 0 {
		return strings.TrimSpace(raw)
	}
	raw = raw[s+len(tok):]
	e := strings.Index(raw, tok)
	if e < 0 {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(raw[:e])
}

// echoNoise 是驱动脚本自身的特征片段：若目标机 stty -echo 没生效，
// 这些行会被 PTY 原样回显进捕获区，属于噪声，展示前剔除。
// 注意：这里**不能**放临时脚本路径（/tmp/.yufu_exec_*.sh）。bash 的真实报错形如
// `/tmp/.yufu_exec_xxx.sh: line 3: foo: command not found`，按路径过滤会把用户最需要的
// 报错整行删掉（真机实测 exit=127 但 stdout 空）。路径改由 tmpPathRe 换成友好名字。
var echoNoise = []string{
	"__YT", "__YP", "YUFU_EOF", "stty -echo", "PROMPT_COMMAND",
	"enable-bracketed-paste", "sudo -n true 2>/dev/null", "EXIT=$?",
}

// tmpPathRe 把注入用的临时脚本路径换成 `script`，让 bash 报错读起来像
// `script: line 3: foo: command not found`，保留行号这类关键定位信息。
var tmpPathRe = regexp.MustCompile(`/tmp/\.yufu_exec_[0-9a-zA-Z]+\.sh`)

// ansiRe 匹配终端转义序列。PTY 里跑的是 TERM=xterm-256color 的交互 bash，
// 输出里会混进 CSI 序列（bracketed paste 的 \e[?2004h/l、命令自身的颜色码等）
// 和 OSC 序列（设置窗口标题）。前端用 <pre> 展示，不渲染 ANSI，不剥掉就是一堆乱码。
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?<>=]*[a-zA-Z]" + // CSI
	"|\x1b\\][^\a\x1b]*(?:\a|\x1b\\\\)" + // OSC（以 BEL 或 ST 结尾）
	"|\x1b[()][A-Za-z0-9]" + // 字符集切换
	"|\x1b[=>]") // 键盘模式

// ctrlRe 匹配剩余的不可见控制字符（保留 \n 与 \t）
var ctrlRe = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")

// stripANSI 剥掉转义序列与残留控制字符
func stripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	return ctrlRe.ReplaceAllString(s, "")
}

// cleanExecOutput 剔除回显噪声行，让用户看到的 stdout 尽量只是命令自身输出
func cleanExecOutput(body string) string {
	if body == "" {
		return body
	}
	body = stripANSI(body)
	body = tmpPathRe.ReplaceAllString(body, "script")
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		// PTY 输出的行尾是 \r\n，逐行剥掉 \r，否则多行输出在前端展示时每行都拖一个回车符
		ln = strings.TrimRight(ln, "\r")
		noisy := false
		for _, sig := range echoNoise {
			if strings.Contains(ln, sig) {
				noisy = true
				break
			}
		}
		if !noisy {
			kept = append(kept, ln)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// parseExecOutput 从捕获文本里提取 stdout 与 EXIT 码
func parseExecOutput(raw, tok string) ExecResult {
	if strings.Count(raw, tok) < 2 {
		// 没拿到完整边界，退化为「原样返回 + 标记异常」，避免谎报 exit 0
		return ExecResult{Status: "error", Error: "未捕获到完整输出边界", Stdout: cleanExecOutput(raw)}
	}
	body := extractOutput(raw, tok)
	res := ExecResult{Status: "ok", ExitCode: 0}
	// EXIT= 行（脚本里倒数第二步 echo 的真实退出码）
	if idx := strings.LastIndex(body, "EXIT="); idx >= 0 {
		rest := body[idx+len("EXIT="):]
		if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
			rest = rest[:nl]
		}
		if code, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
			res.ExitCode = code
			// 去掉结尾的 EXIT= 行，使 stdout 更干净
			body = body[:idx]
		}
	}
	res.Stdout = cleanExecOutput(body)
	return res
}

// execHandler 批量命令下发入口：POST /api/agents/exec
// body: {uuids:[], command:string, timeout:int(秒,默认60), concurrency:int(默认50), password:string}
func execHandler(cfg *Config, db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UUIDs       []string `json:"uuids"`
			Command     string   `json:"command"`
			Timeout     int      `json:"timeout"`
			Concurrency int      `json:"concurrency"`
			Password    string   `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.UUIDs) == 0 || strings.TrimSpace(req.Command) == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Command) > maxExecCommand {
			http.Error(w, "脚本过大（上限 64KB），请改为下载脚本再执行", http.StatusRequestEntityTooLarge)
			return
		}
		if req.Timeout <= 0 || req.Timeout > 600 {
			req.Timeout = 60
		}
		if req.Concurrency <= 0 || req.Concurrency > 200 {
			req.Concurrency = 50
		}

		// 复用 Web SSH 的密码校验/锁定机制（批量用固定 key，因为密码是全局的、不绑定单台）
		const lockKey = "batch-exec"
		if _, until, _ := GetSSHLock(db, lockKey); until > time.Now().Unix() {
			http.Error(w, "SSH 已锁定，请稍后重试", http.StatusForbidden)
			return
		}
		eff := cfg.SSHPassword
		if eff == "" {
			eff = cfg.Admin.Password
		}
		if req.Password != eff {
			if locked, _, _ := RecordSSHFailure(db, lockKey); locked {
				http.Error(w, "错误次数过多，已锁定 24 小时", http.StatusForbidden)
				return
			}
			http.Error(w, "密码错误", http.StatusUnauthorized)
			return
		}
		_ = ResetSSHLock(db, lockKey)

		log.Printf("[exec] 批量命令 start: uuids=%d timeout=%ds concurrency=%d cmd_len=%d",
			len(req.UUIDs), req.Timeout, req.Concurrency, len(req.Command))
		started := time.Now()

		results := make([]ExecResult, len(req.UUIDs))
		sem := make(chan struct{}, req.Concurrency)
		var wg sync.WaitGroup
		for i, u := range req.UUIDs {
			wg.Add(1)
			go func(i int, u string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				defer func() {
					if rec := recover(); rec != nil {
						log.Printf("[exec] panic uuid=%s: %v", u, rec)
						results[i] = ExecResult{UUID: u, Status: "error", Error: "内部错误"}
					}
				}()
				agent := hub.findAgent(u)
				if agent == nil {
					results[i] = ExecResult{UUID: u, Status: "offline", Error: "客户端不在线"}
					return
				}
				results[i] = runExec(agent, u, req.Command, time.Duration(req.Timeout)*time.Second)
				results[i].UUID = u
			}(i, u)
		}
		wg.Wait()

		var okCnt int
		for _, res := range results {
			if res.Status == "ok" && res.ExitCode == 0 {
				okCnt++
			}
		}
		log.Printf("[exec] 批量命令 done: total=%d success=%d 耗时=%s", len(results), okCnt, time.Since(started).Round(time.Millisecond))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "count": len(results), "success": okCnt, "results": results})
	}
}
