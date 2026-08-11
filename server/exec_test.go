package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// tokenFromScript 从注入脚本里还原出本次会话的完整标记。
// 脚本里标记是拼接生成的（`__YT="${__YP}_EXEC_<tag>"`），所以这里要按结构解析，
// 而不是直接搜完整标记串 —— 那正是本功能要保证「脚本里不出现」的东西。
func tokenFromScript(script string) string {
	const marker = `__YT="${__YP}_EXEC_`
	i := strings.Index(script, marker)
	if i < 0 {
		return ""
	}
	rest := script[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return tokRoot + "_EXEC_" + rest[:j]
}

// startMockAgent 起一个假 Agent：累积 shell_input 解出的脚本，等注入完整（见到收尾的 rm -f）后，
// 按真实 Agent 的行为回一帧 base64 编码的 shell_data，内容是「标记 / 命令输出 / EXIT=n / 标记」。
func startMockAgent(t *testing.T, stdout string, exitCode int) *httptest.Server {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, openData, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var openMsg struct {
			Action  string `json:"action"`
			Session string `json:"session"`
		}
		_ = json.Unmarshal(openData, &openMsg)
		sid := openMsg.Session

		var acc strings.Builder
		replied := false
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Action string `json:"action"`
				Data   string `json:"data"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			switch msg.Action {
			case "shell_close":
				return
			case "shell_input":
				dec, _ := base64.StdEncoding.DecodeString(msg.Data)
				acc.WriteString(string(dec))
				script := acc.String()
				// 注入完整（收尾清理临时文件的那行到了）才回显，模拟 shell 顺序执行
				if replied || !strings.Contains(script, "rm -f /tmp/.yufu_exec_") {
					continue
				}
				tok := tokenFromScript(script)
				if tok == "" {
					t.Errorf("脚本里解析不出标记: %q", script)
					return
				}
				replied = true
				body := fmt.Sprintf("%s\r\n%s\r\nEXIT=%d\r\n%s\r\n", tok, stdout, exitCode, tok)
				payload := base64.StdEncoding.EncodeToString([]byte(body))
				_ = conn.WriteMessage(websocket.TextMessage,
					[]byte(`{"action":"shell_data","session":"`+sid+`","data":"`+payload+`"}`))
			}
		}
	}))
}

func dialMock(t *testing.T, url string) *Client {
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http"), nil)
	if err != nil {
		t.Fatalf("连 mock agent 失败: %v", err)
	}
	return &Client{conn: conn}
}

// startExecRouter 复刻生产环境 agentWSHandler 的读循环：agent 连接的唯一读者，
// 收到 shell_data 就交给 feedExecData（负载是 base64，与生产完全一致）。
// runExec 自己绝不读连接，所以这里是唯一读者，不存在并发读。
func startExecRouter(agent *Client) {
	go func() {
		for {
			_, raw, err := agent.conn.ReadMessage()
			if err != nil {
				return
			}
			var m struct {
				Action  string `json:"action"`
				Session string `json:"session"`
				Data    string `json:"data"`
			}
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}
			if m.Action == "shell_data" {
				feedExecData(m.Session, m.Data)
			}
		}
	}()
}

// TestRunExecCapturesOutput 端到端走生产路径：注入脚本 → mock agent 回显 → 路由 → feedExecData → 解析。
func TestRunExecCapturesOutput(t *testing.T) {
	srv := startMockAgent(t, "uid=0(root) gid=0(root)\r\nvps-tokyo-01", 0)
	defer srv.Close()
	agent := dialMock(t, srv.URL)
	startExecRouter(agent)

	res := runExec(agent, "test-uuid", "id\nhostname\n", 5*time.Second)
	if res.Status != "ok" {
		t.Fatalf("期望 status=ok，实际 %s (err=%s stdout=%q)", res.Status, res.Error, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "uid=0(root)") || !strings.Contains(res.Stdout, "vps-tokyo-01") {
		t.Fatalf("输出未捕获到命令结果: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际 %d", res.ExitCode)
	}
	if strings.Contains(res.Stdout, "EXIT=") {
		t.Fatalf("stdout 里不应残留 EXIT= 行: %q", res.Stdout)
	}
	if res.UUID != "test-uuid" {
		t.Fatalf("uuid 未回填: %q", res.UUID)
	}
}

// TestRunExecNonZeroExit 验证非 0 退出码能被正确解析（用户要靠它判断部署是否成功）。
func TestRunExecNonZeroExit(t *testing.T) {
	srv := startMockAgent(t, "bash: line 1: foo: command not found", 127)
	defer srv.Close()
	agent := dialMock(t, srv.URL)
	startExecRouter(agent)

	res := runExec(agent, "test-uuid", "foo\n", 5*time.Second)
	if res.Status != "ok" {
		t.Fatalf("期望 status=ok（命令跑完了，只是退出码非 0），实际 %s", res.Status)
	}
	if res.ExitCode != 127 {
		t.Fatalf("期望退出码 127，实际 %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "command not found") {
		t.Fatalf("stderr 未合并进输出: %q", res.Stdout)
	}
}

// TestRunExecTimeout 验证超时路径：agent 不回任何 shell_data 时返回 timeout 而非卡死。
func TestRunExecTimeout(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	agent := dialMock(t, srv.URL)
	startExecRouter(agent)

	got := make(chan ExecResult, 1)
	go func() { got <- runExec(agent, "test-uuid", "sleep 999\n", 1*time.Second) }()
	select {
	case res := <-got:
		if res.Status != "timeout" {
			t.Fatalf("期望 status=timeout，实际 %s", res.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("runExec 未在超时后返回（疑似卡死）")
	}
}

// TestBuildExecScriptSafety 锁住脚本构造的三条关键不变量（都是修过的 bug）：
//  1. 脚本文本里不能出现完整标记 —— 否则 PTY 回显命令时会被误判成「执行完毕」，命令还没跑就返回；
//  2. sudo 必须先探测再执行，不能用 `sudo -n bash || bash` —— 后者在脚本返回非 0 时会重跑一遍；
//  3. 必须关回显、且用户命令以 base64 承载，避免特殊字符破坏注入。
func TestBuildExecScriptSafety(t *testing.T) {
	const tag = "deadbeefdeadbeefdeadbeefdeadbeef"
	script := buildExecScript("systemctl restart nginx\n", tag)
	tok := tokRoot + "_EXEC_" + tag

	if strings.Contains(script, tok) {
		t.Fatalf("脚本里出现了完整标记 %q，PTY 回显会导致提前判定执行完毕:\n%s", tok, script)
	}
	if tokenFromScript(script) != tok {
		t.Fatalf("标记拼接结构变了，解析得到 %q 期望 %q", tokenFromScript(script), tok)
	}
	if strings.Contains(script, "|| bash") {
		t.Fatalf("sudo 回退写成了 `|| bash`，脚本返回非 0 时会重复执行:\n%s", script)
	}
	if !strings.Contains(script, "if sudo -n true 2>/dev/null; then") {
		t.Fatalf("缺少 sudo 探测分支:\n%s", script)
	}
	if !strings.Contains(script, "stty -echo") {
		t.Fatalf("缺少关闭 PTY 回显:\n%s", script)
	}
	// 用户命令不该以明文出现，应当是 base64 承载
	if strings.Contains(script, "systemctl restart nginx") {
		t.Fatalf("用户命令未 base64 编码，特殊字符会破坏注入:\n%s", script)
	}
	if !strings.Contains(script, base64.StdEncoding.EncodeToString([]byte("systemctl restart nginx\n"))) {
		t.Fatalf("脚本里找不到 base64 后的用户命令:\n%s", script)
	}
}

// TestBuildExecScriptLineLength 回归：PTY 规范模式对 tty 单行有 4096 字节上限，
// 超长行会被内核丢弃/截断。所以注入脚本里任何一行都不能太长（base64 必须折叠），
// 否则几 KB 的部署脚本会静默损坏。
func TestBuildExecScriptLineLength(t *testing.T) {
	// 20KB 的脚本，base64 后约 27KB，不折叠就是一行 27000 字节
	big := strings.Repeat("echo hello-world-this-is-a-long-deploy-script\n", 450)
	script := buildExecScript(big, "abc123")
	for i, ln := range strings.Split(script, "\n") {
		if len(ln) > 1024 {
			t.Fatalf("第 %d 行长度 %d 超过 1024，会触发 tty 行缓冲截断", i+1, len(ln))
		}
	}
	// 折叠后仍要能还原：把 heredoc 内容拼回去做 base64 解码
	// 注意开头是 `<<'YUFU_EOF'`（带引号），结束行才是裸 `YUFU_EOF`
	const opener = "<<'YUFU_EOF'\n"
	i := strings.Index(script, opener)
	if i < 0 {
		t.Fatalf("找不到 heredoc 开始标记:\n%s", script)
	}
	start := i + len(opener)
	j := strings.Index(script[start:], "YUFU_EOF\n")
	if j < 0 {
		t.Fatalf("找不到 heredoc 结束标记:\n%s", script)
	}
	blob := strings.ReplaceAll(script[start:start+j], "\n", "")
	dec, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		t.Fatalf("折叠后的 base64 解不开: %v", err)
	}
	if string(dec) != big {
		t.Fatalf("折叠后还原的脚本与原始不一致（长度 %d vs %d）", len(dec), len(big))
	}
}

// TestCleanExecOutputStripsCR 回归：PTY 输出行尾是 \r\n，多行结果不能给每行都留个 \r。
func TestCleanExecOutputStripsCR(t *testing.T) {
	tok := tokRoot + "_EXEC_cr"
	raw := tok + "\r\nline-one\r\nline-two\r\nline-three\r\nEXIT=0\r\n" + tok + "\r\n"
	res := parseExecOutput(raw, tok)
	if strings.Contains(res.Stdout, "\r") {
		t.Fatalf("stdout 里仍残留 \\r: %q", res.Stdout)
	}
	if res.Stdout != "line-one\nline-two\nline-three" {
		t.Fatalf("多行输出解析不对: %q", res.Stdout)
	}
}

// TestParseExecOutputRealWorldPTY 用生产真机（qwenpaw-sbs-prod-lm2sm，交互 bash + TERM=xterm-256color）
// 实际抓到的原始字节做回归。这段噪声是单测造不出来的，只有真机才暴露：
//   - \x1b[?2004h / \x1b[?2004l：readline 的 bracketed paste 开关序列
//   - root@host:/path#：bash 主动打印的 PS1 提示符（stty -echo 关不掉）
//
// 前端用 <pre> 展示不渲染 ANSI，不剥掉用户就看到一堆乱码。
func TestParseExecOutputRealWorldPTY(t *testing.T) {
	tok := tokRoot + "_EXEC_realworld"
	const prompt = "root@qwenpaw-sbs-prod-lm2sm:/run/csi/mount-root/nas/4079184d856ecc166ed19d4887083405/workspaces/default# "
	raw := tok + "\r\n" +
		"\x1b[?2004h" + prompt + "\x1b[?2004l\n" +
		"uid=0(root) gid=0(root) groups=0(root)\n" +
		"qwenpaw-sbs-prod-lm2sm\n" +
		"5.10.134-18.0.12.lifsea8.x86_64\n" +
		"多行脚本-中文-OK\n" +
		"\x1b[?2004h" + prompt + "\x1b[?2004l" +
		"EXIT=0\r\n" + tok + "\r\n"

	res := parseExecOutput(raw, tok)
	if res.Status != "ok" || res.ExitCode != 0 {
		t.Fatalf("状态/退出码不对: %+v", res)
	}
	if strings.ContainsRune(res.Stdout, '\x1b') {
		t.Fatalf("stdout 里仍残留 ANSI 转义序列: %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "2004h") || strings.Contains(res.Stdout, "2004l") {
		t.Fatalf("bracketed paste 序列未剥净: %q", res.Stdout)
	}
	// 真实内容必须完好，包括中文
	for _, want := range []string{
		"uid=0(root) gid=0(root) groups=0(root)",
		"qwenpaw-sbs-prod-lm2sm",
		"5.10.134-18.0.12.lifsea8.x86_64",
		"多行脚本-中文-OK",
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Fatalf("真实输出丢了 %q，得到:\n%s", want, res.Stdout)
		}
	}
}

// TestBuildExecScriptSuppressesPrompt 回归：脚本必须清掉 PS1/PROMPT_COMMAND 并关 bracketed paste，
// 否则每台机器的输出都会夹提示符与转义序列（真机实测）。
func TestBuildExecScriptSuppressesPrompt(t *testing.T) {
	script := buildExecScript("id\n", "tagx")
	for _, want := range []string{"PS1=", "PROMPT_COMMAND=", "enable-bracketed-paste off"} {
		if !strings.Contains(script, want) {
			t.Fatalf("脚本缺少 %q:\n%s", want, script)
		}
	}
	// PS1= 必须在注入用户脚本之前生效
	if strings.Index(script, "PS1=") > strings.Index(script, "<<'YUFU_EOF'") {
		t.Fatalf("PS1= 出现在 heredoc 之后，提示符已经打出来了:\n%s", script)
	}
}

// TestFeedExecDataDecodesBase64 回归：agentWSHandler 传进来的是 base64（和转发给浏览器的一样），
// feedExecData 必须解码后再匹配标记。曾经忘了解码，导致生产环境每次都走超时分支。
func TestFeedExecDataDecodesBase64(t *testing.T) {
	ses := &execSession{sid: "sid-1", uuid: "u1", tok: tokRoot + "_EXEC_abc", done: make(chan struct{})}
	registerExec(ses.sid, ses)
	defer unregisterExec(ses.sid)

	body := ses.tok + "\r\nhello\r\nEXIT=0\r\n" + ses.tok + "\r\n"
	feedExecData(ses.sid, base64.StdEncoding.EncodeToString([]byte(body)))

	select {
	case <-ses.done:
	case <-time.After(time.Second):
		t.Fatal("喂入 base64 数据后会话未结束，说明没解码")
	}
	res := parseExecOutput(ses.snapshot(), ses.tok)
	if res.Status != "ok" || !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("解析结果不对: %+v", res)
	}
}

// TestCleanExecOutputStripsEcho 验证 stty -echo 没生效时，驱动脚本自身的回显不会污染展示输出。
func TestCleanExecOutputStripsEcho(t *testing.T) {
	tok := tokRoot + "_EXEC_xyz"
	raw := "stty -echo 2>/dev/null\r\n" +
		"__YP=" + tokRoot + "\r\n" +
		`__YT="${__YP}_EXEC_xyz"` + "\r\n" +
		tok + "\r\n" +
		"if sudo -n true 2>/dev/null; then sudo -n bash /tmp/.yufu_exec_xyz.sh 2>&1; else bash /tmp/.yufu_exec_xyz.sh 2>&1; fi\r\n" +
		"real-command-output\r\n" +
		`echo "EXIT=$?"` + "\r\n" +
		"EXIT=0\r\n" +
		tok + "\r\n"
	res := parseExecOutput(raw, tok)
	if res.Stdout != "real-command-output" {
		t.Fatalf("回显噪声未清干净，得到 %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("退出码解析错: %d", res.ExitCode)
	}
}

// TestAbortExecForAgent 验证 agent 掉线时会立刻中止在跑的批量会话，而不是干等到 timeout。
func TestAbortExecForAgent(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	agent := dialMock(t, srv.URL)
	startExecRouter(agent)

	got := make(chan ExecResult, 1)
	// 超时给 60s：如果中止没生效，用例会在下面的 3s 断言里失败
	go func() { got <- runExec(agent, "gone-uuid", "sleep 999\n", 60*time.Second) }()

	// 等会话登记完成再模拟掉线
	deadline := time.Now().Add(2 * time.Second)
	for {
		execMu.RLock()
		n := 0
		for _, s := range execSessions {
			if s.uuid == "gone-uuid" {
				n++
			}
		}
		execMu.RUnlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("2s 内会话未登记")
		}
		time.Sleep(10 * time.Millisecond)
	}
	abortExecForAgent("gone-uuid")

	select {
	case res := <-got:
		if res.Status != "error" || !strings.Contains(res.Error, "断开") {
			t.Fatalf("期望中止为 error/断开，实际 %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent 掉线后 runExec 未及时返回（会干等到 timeout）")
	}
}
