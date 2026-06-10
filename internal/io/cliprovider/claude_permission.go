package cliprovider

// claude_permission.go 把 Claude Code 的 PreToolUse hook 接到 issue #10 建好的
// 统一审批 seam(models.ProviderRequest.RequestPermission)。
//
// 为什么 claude 需要这条额外通路: `claude -p`(headless)的权限确认绑死 TTY,
// 无法被远程接管。唯一的程序化卡点是 PreToolUse hook —— claude 每次调工具前
// 同步执行 hook 命令(stdin 喂 {tool_name, tool_input}),hook 阻塞期间 claude
// 等待,hook 返回的 permissionDecision(allow/deny)决定该工具是否执行。正好
// 塞进"等房间审批"。
//
// allow_always 的落地: hook 输出契约只有 allow/deny, 没有持久授权概念, claude
// 也不会因为一次 allow 而停发后续 hook。所以"总是批准"由我们自己记忆 ——
// provider 持有跨 Complete 的进程级规则表(hookAlwaysRules, 与 opencode 的
// rememberAlwaysRules 同构), 房间答复 allow_always 时把本次调用推导出的模式记
// 下来, 之后命中同模式的 hook 直接放行并发一条 system 事件让房间知情, 不再弹卡。
//
// 实现方式: Complete 运行期起一个 localhost HTTP listener(随机端口),并生成
// 一份临时 settings(--settings)把 PreToolUse hook 指向一个临时转发脚本
// (curl 本地端点)。脚本把 hook 负载 POST 给端点 -> handler 映射成一次
// req.RequestPermission(阻塞至房间决策)-> 把决策翻成 claude 的 hook 输出契约
// 写回 -> 脚本原样 echo 给 claude。Complete 结束 defer close() 关 listener 并清
// 理临时文件。
//
// hook 输出契约以当前 claude CLI 的 PreToolUse hookSpecificOutput 形态为准:
//
//	{"hookSpecificOutput":{"hookEventName":"PreToolUse",
//	  "permissionDecision":"allow"|"deny","permissionDecisionReason":"..."}}
//
// 端点带一次性 token(嵌进脚本),防止同机其他进程伪造审批请求。

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-room/internal/models"
)

// hookTokenHeader 携带一次性鉴权 token, 防同机伪造。
const hookTokenHeader = "X-Agent-Room-Hook-Token"

// hookAlwaysRules 是 allow_always 决策的进程级记忆, 由 ClaudeProvider 持有并在
// 每次 Complete 新建的 broker 间共享, 使"总是批准"跨多轮回复持续生效。
// key = tool + "\x00" + pattern(见 hookAlwaysPattern)。
type hookAlwaysRules struct {
	mu    sync.Mutex
	rules map[string]bool
}

func newHookAlwaysRules() *hookAlwaysRules {
	return &hookAlwaysRules{rules: make(map[string]bool)}
}

// match 返回本次调用是否命中已记住的规则。
func (r *hookAlwaysRules) match(tool, pattern string) bool {
	if r == nil || pattern == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rules[tool+"\x00"+pattern]
}

// remember 记入一条 allow_always 规则。
func (r *hookAlwaysRules) remember(tool, pattern string) {
	if r == nil || pattern == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules[tool+"\x00"+pattern] = true
}

// envAssignRe 识别 Bash 命令前导的环境变量赋值(FOO=bar cmd ...)。
var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// cleanProgramRe 是"可静态判定的程序名"白名单字符集。含 $/引号/反斜杠等动态
// 成分的 token 不可静态判定, 不生成规则、匹配时也视为未覆盖(只能精确匹配放行)。
var cleanProgramRe = regexp.MustCompile(`^[A-Za-z0-9_./~+:-]+$`)

// bashSegments 按 shell 语义把命令拆成独立的执行片段。引号感知:
//   - 单/双引号内的文本不参与拆分, 也不进入片段内容(只留下空引号对占位),
//     所以 python3 -c "多行脚本" 的脚本体不会被误当成命令;
//   - 命令替换是真实执行: $(...) 与反引号包住的内容拆成独立片段(双引号内
//     同样生效), 外层命令在替换结束后继续累积, 不会被腰斩;
//   - 未加引号的 ; & | 换行与 ( ) 是片段边界; >& 形式的 fd 重定向(2>&1)不拆;
//   - 反斜杠转义下一字符。
func bashSegments(command string) []string {
	var segs []string
	cur := &strings.Builder{}
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	// 命令替换栈: 进入 $( / ` 时挂起外层 builder, 结束时恢复继续累积。
	type frame struct {
		outer    *strings.Builder
		inDouble bool
		backtick bool
	}
	var stack []frame
	inSingle, inDouble := false, false
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
				cur.WriteRune('\'') // 闭合占位, 见开引号处注释
			}
			continue
		}
		if c == '\\' { // 转义: 下一字符不再有结构含义(双引号内/裸露皆适用)
			i++
			continue
		}
		if inDouble {
			switch c {
			case '"':
				inDouble = false
				cur.WriteRune('"')
			case '$':
				if i+1 < len(runes) && runes[i+1] == '(' { // 双引号内命令替换仍执行
					stack = append(stack, frame{outer: cur, inDouble: true})
					cur = &strings.Builder{}
					inDouble = false
					i++
				}
			case '`':
				stack = append(stack, frame{outer: cur, inDouble: true, backtick: true})
				cur = &strings.Builder{}
				inDouble = false
			}
			continue // 双引号内的其余文本一律跳过
		}
		switch c {
		case '\'', '"':
			// 写入引号对但跳过内容: 既不让脚本文本混进片段, 又让"引号开头的
			// 程序名"(如 "rm" -rf)留下脏 token, 匹配时保守落到精确规则。
			cur.WriteRune(c)
			if c == '\'' {
				inSingle = true
			} else {
				inDouble = true
			}
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				stack = append(stack, frame{outer: cur})
				cur = &strings.Builder{}
				i++
			} else {
				cur.WriteRune(c)
			}
		case '`':
			if n := len(stack); n > 0 && stack[n-1].backtick {
				flush()
				top := stack[n-1]
				stack = stack[:n-1]
				cur, inDouble = top.outer, top.inDouble
			} else {
				stack = append(stack, frame{outer: cur})
				cur = &strings.Builder{}
			}
		case ')':
			if n := len(stack); n > 0 && !stack[n-1].backtick {
				flush()
				top := stack[n-1]
				stack = stack[:n-1]
				cur, inDouble = top.outer, top.inDouble
			} else {
				flush() // 裸 ) (subshell 收尾等): 当边界
			}
		case '(':
			flush() // 裸 ( (subshell/函数定义): 当边界
		case '&':
			if i > 0 && runes[i-1] == '>' { // 2>&1 / >&2 的 fd 重定向, 不是边界
				cur.WriteRune(c)
			} else {
				flush()
			}
		case ';', '|', '\n':
			flush()
		case '#':
			// 词首的 # 开启行注释(shell 语义): 注释体直接跳到行尾, 里面的
			// 括号/分隔符不再有结构含义。词中的 #(如路径 a#b)原样保留。
			if s := cur.String(); s == "" || strings.HasSuffix(s, " ") || strings.HasSuffix(s, "\t") {
				for i+1 < len(runes) && runes[i+1] != '\n' {
					i++
				}
			} else {
				cur.WriteRune(c)
			}
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	// 栈里未闭合的外层(不平衡引号/括号)也要收尾。
	for n := len(stack) - 1; n >= 0; n-- {
		cur = stack[n].outer
		flush()
	}
	return segs
}

// bashPrograms 提取复合命令里每个执行片段的程序名。complete=false 表示存在
// 无法静态判定程序名的片段(变量程序、引号程序等), 此时程序前缀规则不可用于
// 匹配本命令(只能精确匹配), 但已判定出的程序仍可作为记忆候选。
// 纯赋值片段(jid=xxx)与注释片段(# ...)没有程序, 干净跳过。
func bashPrograms(command string) (programs []string, complete bool) {
	complete = true
	seen := map[string]bool{}
	for _, segment := range bashSegments(command) {
		fields := strings.Fields(segment)
		prog := ""
		for _, f := range fields {
			if envAssignRe.MatchString(f) {
				continue
			}
			prog = f
			break
		}
		if prog == "" || strings.HasPrefix(prog, "#") {
			// 纯赋值/注释/空片段: 没有程序, 跳过。
			continue
		}
		if !cleanProgramRe.MatchString(prog) {
			complete = false
			continue
		}
		if !seen[prog] {
			seen[prog] = true
			programs = append(programs, prog)
		}
	}
	return programs, complete
}

// hookAlwaysCandidates 推导一次 hook 调用的 allow_always 候选模式列表, 供审批
// 卡片点选。claude 的 hook 负载没有 opencode 那样的服务端 always 列表, 只能
// 自己推导:
//   - 非 Bash 工具: ["<tool>(*)"] —— 整个工具一刀放行(read/edit 类低危)。
//   - Bash: 每个片段程序一条 "<程序名> *"(echo * / curl * ...), 最后附精确
//     命令本身作为最窄选项。
func hookAlwaysCandidates(tool, input string) []string {
	if tool == "" {
		return nil
	}
	if tool != "Bash" {
		return []string{tool + "(*)"}
	}
	command := strings.TrimSpace(input)
	if command == "" {
		return nil
	}
	programs, _ := bashPrograms(command)
	out := make([]string, 0, len(programs)+1)
	for _, prog := range programs {
		out = append(out, prog+" *")
	}
	return append(out, command)
}

// matchAlwaysRules 判定一次调用是否被已记住的规则覆盖, 返回命中描述(未命中返
// 回空)。三种命中方式:
//   - 非 Bash: 工具级规则 "<tool>(*)"。
//   - Bash 精确规则: 与记忆中的完整命令逐字符相同。
//   - Bash 程序前缀: 命令可完整静态解析(complete)且每个片段程序都有
//     "<程序名> *" 规则 —— `echo "$(curl ...)"` 需要 echo * 和 curl * 同时在册,
//     防止 `git status && rm -rf /` 借单条 git * 溜过。
func (b *claudeHookBroker) matchAlwaysRules(tool, input string) string {
	if tool == "" || b.always == nil {
		return ""
	}
	if tool != "Bash" {
		if b.always.match(tool, tool+"(*)") {
			return tool + "(*)"
		}
		return ""
	}
	command := strings.TrimSpace(input)
	if command == "" {
		return ""
	}
	if b.always.match(tool, command) {
		return command
	}
	programs, complete := bashPrograms(command)
	if !complete || len(programs) == 0 {
		return ""
	}
	rules := make([]string, 0, len(programs))
	for _, prog := range programs {
		if !b.always.match(tool, prog+" *") {
			return ""
		}
		rules = append(rules, prog+" *")
	}
	return strings.Join(rules, " + ")
}

// claudeHookBroker 在一次 Complete 期间提供"PreToolUse hook -> 房间审批"的桥接。
type claudeHookBroker struct {
	requestPermission func(ctx context.Context, p models.PermissionRequest) (models.PermissionDecision, error)
	onEvent           func(models.ProviderEvent)
	always            *hookAlwaysRules
	ctx               context.Context
	token             string
	endpoint          string
	settingsPath      string
	scriptPath        string
	tmpDir            string
	listener          net.Listener
	server            *http.Server
	seq               atomic.Uint64
}

// newClaudeHookBroker 起本地审批端点, 生成转发脚本与临时 settings。返回的 broker
// 的 settingsPath 应作为 `claude --settings` 注入; 调用方负责 defer close()。
// always 为 provider 持有的进程级 allow_always 规则记忆(可为 nil = 不记忆)。
func newClaudeHookBroker(ctx context.Context, req models.ProviderRequest, always *hookAlwaysRules) (*claudeHookBroker, error) {
	token, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("claude hook broker: token: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("claude hook broker: listen: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "agent-room-claude-hook-")
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("claude hook broker: tmp dir: %w", err)
	}

	b := &claudeHookBroker{
		requestPermission: req.RequestPermission,
		onEvent:           req.OnEvent,
		always:            always,
		ctx:               ctx,
		token:             token,
		endpoint:          fmt.Sprintf("http://%s/hook", listener.Addr().String()),
		tmpDir:            tmpDir,
		listener:          listener,
	}

	if err := b.writeScript(); err != nil {
		b.close()
		return nil, err
	}
	if err := b.writeSettings(); err != nil {
		b.close()
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/hook", b.handleHook)
	b.server = &http.Server{Handler: mux}
	go b.server.Serve(listener)

	return b, nil
}

// writeScript 生成 hook 转发脚本: 读 stdin(hook 负载) -> curl 本地端点 ->
// 原样 echo 响应(claude 的 hook 决策 JSON)。端点与 token 直接嵌进脚本, 不依赖
// 环境变量传播。-m 600 给房间审批留 10 分钟。
func (b *claudeHookBroker) writeScript() error {
	b.scriptPath = b.tmpDir + string(os.PathSeparator) + "pretooluse-hook.sh"
	script := fmt.Sprintf(`#!/bin/sh
# Auto-generated by agent-room (claude provider PreToolUse approval hook).
# Forwards the tool call to the bridge's local approval endpoint and echoes
# Claude's hook decision JSON (allow/deny) decided by the remote room.
exec curl -sS -m 600 -X POST '%s' \
  -H 'content-type: application/json' \
  -H '%s: %s' \
  --data-binary @-
`, b.endpoint, hookTokenHeader, b.token)
	if err := os.WriteFile(b.scriptPath, []byte(script), 0o700); err != nil {
		return fmt.Errorf("claude hook broker: write script: %w", err)
	}
	return nil
}

// writeSettings 写一份临时 settings, 注入 PreToolUse hook 指向转发脚本。
func (b *claudeHookBroker) writeSettings() error {
	b.settingsPath = b.tmpDir + string(os.PathSeparator) + "settings.json"
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{"type": "command", "command": b.scriptPath},
					},
				},
			},
		},
	}
	buf, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("claude hook broker: marshal settings: %w", err)
	}
	if err := os.WriteFile(b.settingsPath, buf, 0o600); err != nil {
		return fmt.Errorf("claude hook broker: write settings: %w", err)
	}
	return nil
}

// handleHook 接收 claude PreToolUse hook 的负载, 映射为一次 RequestPermission,
// 阻塞至房间决策, 再把决策翻成 claude 的 hook 输出契约写回。
func (b *claudeHookBroker) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 一次性 token 比对(常量时间), 防同机其他进程伪造审批。
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(hookTokenHeader)), []byte(b.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var payload struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	_ = json.Unmarshal(body, &payload)

	// 无审批回调(理论上不会进到这里, 因为只有 RequestPermission!=nil 才注入
	// hook), 兜底自动放行以兼容旧 headless 行为。
	if b.requestPermission == nil {
		writeHookDecision(w, true, "auto-approved (no approver in loop)")
		return
	}

	input := summarizeHookInput(payload.ToolInput)
	candidates := hookAlwaysCandidates(payload.ToolName, input)

	// 命中已记住的「总是批准」规则: 直接放行, 不再打扰房间; 发一条 system 事件
	// 进 thinking 流, 让房间知道这次是按固定规则放行的, 而非凭空执行。
	if rule := b.matchAlwaysRules(payload.ToolName, input); rule != "" {
		if b.onEvent != nil {
			b.onEvent(models.ProviderEvent{
				Type:    models.ProviderEventSystem,
				Tool:    payload.ToolName,
				Summary: "已按「总是批准」规则自动放行: " + rule,
				Detail:  input,
			})
		}
		writeHookDecision(w, true, "auto-approved by always rule: "+rule)
		return
	}

	reqID, _ := randomHex(8)
	if reqID == "" {
		reqID = fmt.Sprintf("%d", b.seq.Add(1))
	}

	meta := map[string]string{"hook_event": "PreToolUse"}
	if len(candidates) > 0 {
		// 前端审批卡片用 metadata.always 渲染「总是批准」的可点选候选模式
		// (echo * / curl * / 精确命令)。编码见 EncodePatternList: 候选含换行
		// (多行精确命令)时用 JSON 数组, 防止按行拆出一堆假模式。
		meta["always"] = models.EncodePatternList(candidates)
	}
	decision, err := b.requestPermission(b.ctx, models.PermissionRequest{
		RequestID: "claude-" + reqID,
		Tool:      payload.ToolName,
		Input:     input,
		Metadata:  meta,
	})
	if err != nil {
		// ctx 取消 / 房间未决策: 保守拒绝。claude 此时通常已被 cancel。
		writeHookDecision(w, false, "approval unavailable: "+err.Error())
		return
	}

	// hook 仅 allow/deny: allow_once 单次放行; allow_always 同样放行本次, 并把
	// 审批人点选的模式记入进程级规则表(校验确属本次候选, 防任意规则注入),
	// 后续命中走上方快路径; 未点选(老前端)默认记全部候选; 未知值保守按 deny。
	allow := decision.Reply == models.PermissionAllowOnce || decision.Reply == models.PermissionAllowAlways
	if decision.Reply == models.PermissionAllowAlways {
		chosen := decision.Patterns
		if len(chosen) == 0 {
			chosen = candidates
		}
		valid := make(map[string]bool, len(candidates))
		for _, c := range candidates {
			valid[c] = true
		}
		for _, p := range chosen {
			if valid[p] {
				b.always.remember(payload.ToolName, p)
			}
		}
	}
	writeHookDecision(w, allow, decision.Reason)
}

// writeHookDecision 输出 claude PreToolUse hook 的决策 JSON。
func writeHookDecision(w http.ResponseWriter, allow bool, reason string) {
	decision := "deny"
	if allow {
		decision = "allow"
	}
	if strings.TrimSpace(reason) == "" {
		if allow {
			reason = "approved in room"
		} else {
			reason = "denied in room"
		}
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// summarizeHookInput 从 tool_input 提炼可读输入: 优先 command/file_path 等常见字段,
// 退化为原始 JSON。
func summarizeHookInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, key := range []string{"command", "file_path", "path", "pattern", "url"} {
			if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return string(raw)
}

// close 关停审批端点并清理临时文件。多次调用安全。
func (b *claudeHookBroker) close() {
	if b.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = b.server.Shutdown(ctx)
		cancel()
		b.server = nil
	} else if b.listener != nil {
		b.listener.Close()
	}
	if b.tmpDir != "" {
		_ = os.RemoveAll(b.tmpDir)
		b.tmpDir = ""
	}
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
