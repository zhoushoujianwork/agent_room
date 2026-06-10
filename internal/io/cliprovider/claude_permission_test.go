package cliprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"agent-room/internal/models"
)

// hookOutput 是 claude PreToolUse hook 的决策契约形态, 测试据此断言端点响应。
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// postHook 模拟 hook 转发脚本: 带 token 把 hook 负载 POST 给本地端点, 解析决策。
func postHook(t *testing.T, b *claudeHookBroker, token, payload string) hookOutput {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.endpoint, bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set(hookTokenHeader, token)
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post hook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, string(body))
	}
	var out hookOutput
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode hook output: %v", err)
	}
	if out.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hookEventName = %q, want PreToolUse", out.HookSpecificOutput.HookEventName)
	}
	return out
}

// TestHookBrokerAllow: hook 负载 -> RequestPermission 收到正确字段 -> 房间放行 ->
// 端点返回 permissionDecision=allow。同时验证生成的脚本/settings 文件就位。
func TestHookBrokerAllow(t *testing.T) {
	var gotReq models.PermissionRequest
	var calls int
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			calls++
			gotReq = pr
			return models.PermissionDecision{Reply: models.PermissionAllowOnce}, nil
		},
	}
	b, err := newClaudeHookBroker(context.Background(), req, newHookAlwaysRules())
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	defer b.close()

	if _, err := os.Stat(b.settingsPath); err != nil {
		t.Fatalf("settings file missing: %v", err)
	}
	if _, err := os.Stat(b.scriptPath); err != nil {
		t.Fatalf("hook script missing: %v", err)
	}

	out := postHook(t, b, b.token, `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`)
	if out.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("permissionDecision = %q, want allow", out.HookSpecificOutput.PermissionDecision)
	}
	if calls != 1 {
		t.Fatalf("RequestPermission called %d times, want 1", calls)
	}
	if gotReq.Tool != "Bash" {
		t.Fatalf("Tool = %q, want Bash", gotReq.Tool)
	}
	if gotReq.Input != "ls -la" {
		t.Fatalf("Input = %q, want ls -la", gotReq.Input)
	}
	if gotReq.RequestID == "" {
		t.Fatal("RequestID is empty")
	}
}

// TestHookBrokerAllowAlwaysRemembered: allow_always 放行本次并记入规则表, 同模式
// 的后续 hook 不再询问房间直接放行, 且发 system 事件告知房间。规则表由 provider
// 跨 Complete 共享 —— 用第二个 broker(模拟下一轮回复)验证持久性。
func TestHookBrokerAllowAlwaysRemembered(t *testing.T) {
	rules := newHookAlwaysRules()
	var asks int
	var events []models.ProviderEvent
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			asks++
			if pr.Metadata["always"] != "git *\ngit status" {
				t.Errorf("Metadata[always] = %q, want candidates git * + exact", pr.Metadata["always"])
			}
			// 模拟审批人点选了 git * 候选。
			return models.PermissionDecision{Reply: models.PermissionAllowAlways, Patterns: []string{"git *"}}, nil
		},
		OnEvent: func(ev models.ProviderEvent) { events = append(events, ev) },
	}
	b, err := newClaudeHookBroker(context.Background(), req, rules)
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}

	// 第一次: 问房间, 房间答 allow_always -> 放行 + 记规则。
	out := postHook(t, b, b.token, `{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	if out.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("first decision = %q, want allow", out.HookSpecificOutput.PermissionDecision)
	}
	if asks != 1 {
		t.Fatalf("RequestPermission called %d times, want 1", asks)
	}

	// 第二次同程序不同参数: 命中 git * 规则, 不再问房间。
	out = postHook(t, b, b.token, `{"tool_name":"Bash","tool_input":{"command":"git diff --stat"}}`)
	if out.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("second decision = %q, want allow", out.HookSpecificOutput.PermissionDecision)
	}
	if asks != 1 {
		t.Fatalf("RequestPermission called %d times after rule hit, want still 1", asks)
	}
	if !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "git *") {
		t.Fatalf("reason = %q, want always-rule mention", out.HookSpecificOutput.PermissionDecisionReason)
	}
	if len(events) != 1 || events[0].Type != models.ProviderEventSystem {
		t.Fatalf("events = %+v, want one system event for auto-allow", events)
	}
	b.close()

	// 第二个 broker 共享同一规则表(模拟下一次 Complete): 规则仍生效。
	b2, err := newClaudeHookBroker(context.Background(), req, rules)
	if err != nil {
		t.Fatalf("newClaudeHookBroker(2): %v", err)
	}
	defer b2.close()
	out = postHook(t, b2, b2.token, `{"tool_name":"Bash","tool_input":{"command":"git log -3"}}`)
	if out.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("cross-broker decision = %q, want allow", out.HookSpecificOutput.PermissionDecision)
	}
	if asks != 1 {
		t.Fatalf("RequestPermission called %d times across brokers, want still 1", asks)
	}

	// 不同程序不命中: 仍要问房间(回调里断言的是 git *, 这里换成宽松回调避免误报)。
	b2.requestPermission = func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
		asks++
		return models.PermissionDecision{Reply: models.PermissionAllowOnce}, nil
	}
	_ = postHook(t, b2, b2.token, `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`)
	if asks != 2 {
		t.Fatalf("RequestPermission called %d times for unmatched program, want 2", asks)
	}
}

// TestHookAlwaysCandidates: allow_always 候选模式的推导口径。
func TestHookAlwaysCandidates(t *testing.T) {
	cases := []struct {
		tool, input string
		want        []string
	}{
		{"Edit", "/tmp/x", []string{"Edit(*)"}},
		{"Read", "", []string{"Read(*)"}},
		{"Bash", "git status", []string{"git *", "git status"}},
		{"Bash", "FOO=1 BAR=2 go test ./...", []string{"go *", "FOO=1 BAR=2 go test ./..."}},
		// 复合命令: 每个片段程序一条前缀候选 + 精确命令。
		{"Bash", "git status && rm -rf /tmp/x", []string{"git *", "rm *", "git status && rm -rf /tmp/x"}},
		// 命令替换里的程序也是真实执行, 必须入候选(内层先 flush, 故 curl 在前)。
		{"Bash", `echo "ip: $(curl -s ifconfig.me)"; echo done`, []string{"curl *", "echo *", `echo "ip: $(curl -s ifconfig.me)"; echo done`}},
		// 变量程序不可静态判定: 只剩精确命令。
		{"Bash", "$HOME/run.sh", []string{"$HOME/run.sh"}},
		{"Bash", "", nil},
		{"", "x", nil},
	}
	for _, c := range cases {
		got := hookAlwaysCandidates(c.tool, c.input)
		if len(got) != len(c.want) {
			t.Errorf("hookAlwaysCandidates(%q, %q) = %v, want %v", c.tool, c.input, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("hookAlwaysCandidates(%q, %q)[%d] = %q, want %q", c.tool, c.input, i, got[i], c.want[i])
			}
		}
	}
}

// TestBashProgramsQuoteAware: 引号感知的程序提取——多行命令、引号内脚本体、
// 注释、纯赋值、fd 重定向都不会污染程序集合(线上踩坑: python3 -c "多行脚本"
// 的每一行都被拆成了假模式)。
func TestBashProgramsQuoteAware(t *testing.T) {
	command := "jid=be7d1cdfd\n" +
		"vid=20ba6b65\n" +
		`curl -s "http://170.106.62.233:30728/jobs/$jid/vertices/$vid/subtasktimes" >/dev/null 2>&1` + "\n" +
		"# 取每个子任务 read-records (numRecordsIn) 累计值\n" +
		`curl -s "http://170.106.62.233:30728/jobs/$jid/subtasks/accumulators" | python3 -c "` + "\n" +
		"import sys,json\n" +
		"d=json.load(sys.stdin)\n" +
		"rows=[]\n" +
		"for st in d['subtasks']:\n" +
		"    pass\n" +
		`print('subtasks count=',len(d['subtasks']))` + "\n" +
		`" 2>/dev/null || echo "accumulators not available"`
	programs, complete := bashPrograms(command)
	want := []string{"curl", "python3", "echo"}
	if !complete {
		t.Fatalf("complete = false, want true; programs = %v", programs)
	}
	if len(programs) != len(want) {
		t.Fatalf("programs = %v, want %v", programs, want)
	}
	for i := range want {
		if programs[i] != want[i] {
			t.Fatalf("programs = %v, want %v", programs, want)
		}
	}

	// 单引号脚本体同样不拆; 引号程序名(伪装)落为 dirty。
	if progs, ok := bashPrograms(`python3 -c 'import os; os.system("rm -rf /")'`); !ok || len(progs) != 1 || progs[0] != "python3" {
		t.Fatalf("single-quoted script: programs=%v complete=%v, want [python3] true", progs, ok)
	}
	if _, ok := bashPrograms(`"rm" -rf /`); ok {
		t.Fatal("quoted program name should make extraction incomplete")
	}
	// 反引号替换也是真实执行。
	if progs, ok := bashPrograms("echo `rm -rf /tmp/x`"); !ok || len(progs) != 2 || progs[0] != "rm" || progs[1] != "echo" {
		t.Fatalf("backtick: programs=%v complete=%v, want [rm echo] true", progs, ok)
	}
}

// TestPatternListEncodeRoundTrip: 含多行精确命令的候选列表经 JSON 编码往返
// 不变形; 单行列表保持换行分隔(兼容旧解析)。
func TestPatternListEncodeRoundTrip(t *testing.T) {
	multi := []string{"curl *", "python3 *", "line1\nline2\nline3"}
	encoded := models.EncodePatternList(multi)
	if !strings.HasPrefix(encoded, "[") {
		t.Fatalf("multiline list should JSON-encode, got %q", encoded)
	}
	decoded := models.DecodePatternList(encoded)
	if len(decoded) != 3 || decoded[2] != "line1\nline2\nline3" {
		t.Fatalf("round trip = %v, want %v", decoded, multi)
	}

	plain := models.EncodePatternList([]string{"echo *", "curl *"})
	if plain != "echo *\ncurl *" {
		t.Fatalf("single-line list should stay newline-joined, got %q", plain)
	}
	if got := models.DecodePatternList(plain); len(got) != 2 || got[0] != "echo *" {
		t.Fatalf("newline decode = %v", got)
	}
}

// TestHookBrokerCompoundCommandCoverage: 复合命令(含命令替换)按"全部片段程序
// 均有规则"判定 —— 记住 echo * + curl * 后, 同形态但参数不同的复合命令自动放行;
// 混入未授权程序(rm)则仍要问房间。这是审批卡片点选多个程序前缀的端到端场景。
func TestHookBrokerCompoundCommandCoverage(t *testing.T) {
	rules := newHookAlwaysRules()
	var asks int
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			asks++
			// 点选 echo * 与 curl * 两条, 不选精确命令。
			return models.PermissionDecision{Reply: models.PermissionAllowAlways, Patterns: []string{"echo *", "curl *"}}, nil
		},
	}
	b, err := newClaudeHookBroker(context.Background(), req, rules)
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	defer b.close()

	post := func(command string) hookOutput {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{
			"tool_name":  "Bash",
			"tool_input": map[string]string{"command": command},
		})
		return postHook(t, b, b.token, string(payload))
	}

	// 第一次: 截图同款复合命令, 问房间, 点选 echo */curl *。
	out := post(`echo "ifconfig.me: $(curl -s --max-time 8 ifconfig.me)"; echo "ipinfo.io: $(curl -s ipinfo.io/ip)"`)
	if out.HookSpecificOutput.PermissionDecision != "allow" || asks != 1 {
		t.Fatalf("first: decision=%q asks=%d, want allow/1", out.HookSpecificOutput.PermissionDecision, asks)
	}

	// 第二次: 参数完全不同的 echo+curl 复合命令 -> 全片段被 echo */curl * 覆盖, 不再问。
	out = post(`echo "icanhazip: $(curl -s icanhazip.com)"`)
	if out.HookSpecificOutput.PermissionDecision != "allow" || asks != 1 {
		t.Fatalf("covered compound: decision=%q asks=%d, want allow/1", out.HookSpecificOutput.PermissionDecision, asks)
	}

	// 第三次: 混入未授权程序 rm -> 覆盖判定失败, 必须再问房间。
	_ = post(`echo hi && rm -rf /tmp/x`)
	if asks != 2 {
		t.Fatalf("uncovered program: asks=%d, want 2", asks)
	}
}

// TestHookBrokerPatternValidation: 决策携带的模式必须属于本次候选集, 防任意
// 规则注入 —— 点选不存在的 "rm *" 不会被记住。
func TestHookBrokerPatternValidation(t *testing.T) {
	rules := newHookAlwaysRules()
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			return models.PermissionDecision{Reply: models.PermissionAllowAlways, Patterns: []string{"rm *"}}, nil
		},
	}
	b, err := newClaudeHookBroker(context.Background(), req, rules)
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	defer b.close()

	out := postHook(t, b, b.token, `{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	if out.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("decision = %q, want allow (this call itself approved)", out.HookSpecificOutput.PermissionDecision)
	}
	if rules.match("Bash", "rm *") {
		t.Fatal("injected pattern rm * was remembered; want rejected")
	}
	if rules.match("Bash", "git *") {
		t.Fatal("git * remembered though approver did not pick it")
	}
}

// TestHookBrokerDeny: 房间拒绝 -> permissionDecision=deny。
func TestHookBrokerDeny(t *testing.T) {
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			return models.PermissionDecision{Reply: models.PermissionDeny, Reason: "nope"}, nil
		},
	}
	b, err := newClaudeHookBroker(context.Background(), req, newHookAlwaysRules())
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	defer b.close()

	out := postHook(t, b, b.token, `{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)
	if out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny", out.HookSpecificOutput.PermissionDecision)
	}
	if out.HookSpecificOutput.PermissionDecisionReason != "nope" {
		t.Fatalf("reason = %q, want nope", out.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestHookBrokerApprovalErrorDenies: RequestPermission 返回错误(ctx 取消/中断)->
// 端点保守返回 deny。
func TestHookBrokerApprovalErrorDenies(t *testing.T) {
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			return models.PermissionDecision{}, errors.New("interrupted")
		},
	}
	b, err := newClaudeHookBroker(context.Background(), req, newHookAlwaysRules())
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	defer b.close()

	out := postHook(t, b, b.token, `{"tool_name":"Bash","tool_input":{"command":"ls"}}`)
	if out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny on approval error", out.HookSpecificOutput.PermissionDecision)
	}
}

// TestHookBrokerContextCancelUnblocks: 待审批期间 ctx 取消(等价 stop)必须能让
// 阻塞的 RequestPermission 返回, 端点不悬挂。
func TestHookBrokerContextCancelUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := models.ProviderRequest{
		RequestPermission: func(reqCtx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			// 模拟 seam 行为: 阻塞直到 ctx 取消。
			<-reqCtx.Done()
			return models.PermissionDecision{}, reqCtx.Err()
		},
	}
	b, err := newClaudeHookBroker(ctx, req, newHookAlwaysRules())
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	defer b.close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan hookOutput, 1)
	go func() {
		done <- postHook(t, b, b.token, `{"tool_name":"Bash","tool_input":{"command":"sleep 999"}}`)
	}()

	select {
	case out := <-done:
		if out.HookSpecificOutput.PermissionDecision != "deny" {
			t.Fatalf("permissionDecision = %q, want deny after cancel", out.HookSpecificOutput.PermissionDecision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook endpoint did not unblock after ctx cancel")
	}
}

// TestHookBrokerRejectsBadToken: 缺失/错误 token 的请求被拒, 防同机伪造。
func TestHookBrokerRejectsBadToken(t *testing.T) {
	var calls int
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			calls++
			return models.PermissionDecision{Reply: models.PermissionAllowOnce}, nil
		},
	}
	b, err := newClaudeHookBroker(context.Background(), req, newHookAlwaysRules())
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	defer b.close()

	httpReq, _ := http.NewRequest(http.MethodPost, b.endpoint, bytes.NewReader([]byte(`{"tool_name":"Bash"}`)))
	httpReq.Header.Set(hookTokenHeader, "wrong-token")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if calls != 0 {
		t.Fatalf("RequestPermission called %d times on bad token, want 0", calls)
	}
}

// TestHookBrokerCloseCleansUp: close 后临时目录被清理。
func TestHookBrokerCloseCleansUp(t *testing.T) {
	req := models.ProviderRequest{
		RequestPermission: func(ctx context.Context, pr models.PermissionRequest) (models.PermissionDecision, error) {
			return models.PermissionDecision{Reply: models.PermissionAllowOnce}, nil
		},
	}
	b, err := newClaudeHookBroker(context.Background(), req, newHookAlwaysRules())
	if err != nil {
		t.Fatalf("newClaudeHookBroker: %v", err)
	}
	dir := b.tmpDir
	b.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("tmp dir %q not cleaned up: err=%v", dir, err)
	}
}
