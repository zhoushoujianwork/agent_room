package models

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// ErrProviderInterrupted is returned by a provider when an in-flight
// completion was deliberately cancelled (e.g. a user pressed "stop"),
// as distinct from a timeout or a genuine failure. Callers use it to
// surface a "stopped" status instead of an error.
var ErrProviderInterrupted = errors.New("provider completion interrupted")

// AgentProvider abstracts a local AI CLI such as Claude Code, Gemini CLI, or Codex.
type AgentProvider interface {
	Name() string
	Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error)
}

type ProviderRequest struct {
	RoomID       string
	AgentID      string
	AgentLabel   string
	Capabilities string
	Input        ChatMessage
	History      []ChatMessage
	RoomSummary  string
	// Attachments 是入站消息携带的图片附件, bridge 已把消息 metadata 里的
	// 相对引用解析成可直接 GET 的绝对 URL(指向 relay)。provider 负责让
	// agent 真正"看到"它们 —— claude 侧会先下载到本地临时文件, 再在
	// prompt 里指引 Read 本地路径(Read 只对本地文件有视觉能力)。
	Attachments []ProviderAttachment
	// ContextDoc is operator-provided background loaded from a local file at
	// bridge startup (see config.ContextFile). It is trusted context, distinct
	// from untrusted room messages, and surfaced to the agent as reference
	// material. Empty when no context file was configured.
	ContextDoc   string
	SystemPrompt string
	MaxTurns     int
	RelayHTTPURL string
	OnEvent      func(ProviderEvent)
	// RequestPermission, 非 nil 时由 provider 在某个工具调用需要人工授权时调用。
	// 阻塞直到房间内产生决策或 ctx 取消; nil = 无人在回路, provider 须自动放行
	// (兼容旧 claude -p 行为)。它与 OnEvent 对称: OnEvent 是 provider -> 房间的
	// 单向上报, RequestPermission 是 provider -> 房间 -> provider 的反向审批通道。
	RequestPermission func(ctx context.Context, p PermissionRequest) (PermissionDecision, error)
	// DelegateExec, 非 nil 时由 provider 把一条 shell 命令委派给同房间的 executor
	// peer 执行, 阻塞直到结果回来或 ctx 取消。它由持有房间 WebSocket 的 bridge 注入:
	// bridge 经自己的 WS 发出 command 并按 command_id 等回 command_result, 免去 agent
	// 子进程裸 curl 轮询 relay。nil = 无 bridge 在回路(直连/非 agent 模式), provider
	// 须退回旧的"prompt 教 curl"行为。命令仍走 relay 的 exec-token 中介, provider 既
	// 不持有也不接触 exec_token。
	DelegateExec func(ctx context.Context, r DelegateExecRequest) (DelegateExecResult, error)
}

// ProviderAttachment 是交给 provider 的一份图片附件:URL 指向 relay 的附件
// 下载端点(知道房间 URL 即可访问,与房间消息同一授权边界)。LocalPath 由
// provider 在调用本地 CLI 前按需填入, 指向已下载好的临时文件。
type ProviderAttachment struct {
	URL       string
	MIME      string
	Name      string
	LocalPath string
}

// DelegateExecRequest 描述一次委派给 executor peer 的 shell 命令。
type DelegateExecRequest struct {
	TargetID  string // 目标 executor 的 agent id(必填)
	Command   string // shell 命令文本(必填)
	TimeoutMS int    // 执行超时(毫秒), <=0 时由 bridge/executor 用默认值
	Cwd       string // 可选工作目录
	Shell     string // 可选 shell: powershell|cmd|bash
}

// DelegateExecResult is an executor's command_result, surfaced back to the
// provider. The executor encodes exit_code / timed_out / truncation as string
// metadata and renders the combined stdout+stderr into Output (its message
// Content), so Output is the same human-readable text a room participant sees.
type DelegateExecResult struct {
	ExitCode        int
	Output          string // formatted exit_code + stdout + stderr (executor Content)
	TimedOut        bool
	StdoutTruncated bool
	StderrTruncated bool
	ErrorType       string // executor-side error class (e.g. unauthorized/timeout); empty on success
}

// PermissionRequest 描述一次需要人工授权的工具调用, 由 provider 透传给房间。
type PermissionRequest struct {
	RequestID string            // provider 原生 id(opencode permissionID; claude 侧 bridge 生成)
	Tool      string            // read / edit / bash / ...
	Input     string            // 命令文本 / 文件路径
	Pattern   string            // 命中的规则(可空)
	Metadata  map[string]string // call_id 等(可空)
}

// EncodePatternList 把模式列表编码进单个 metadata 字符串。模式不含换行时用
// 换行分隔(旧 bridge/前端可直接按行拆,保持兼容);任一模式含换行(多行精确
// 命令)时改用 JSON 数组,杜绝"多行命令被按行拆成一堆假模式"。
func EncodePatternList(patterns []string) string {
	multiline := false
	for _, p := range patterns {
		if strings.Contains(p, "\n") {
			multiline = true
			break
		}
	}
	if !multiline {
		return strings.Join(patterns, "\n")
	}
	buf, err := json.Marshal(patterns)
	if err != nil {
		return strings.Join(patterns, "\n")
	}
	return string(buf)
}

// DecodePatternList 解析 EncodePatternList 的两种形态: JSON 数组优先, 否则按行拆。
func DecodePatternList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			out := make([]string, 0, len(arr))
			for _, p := range arr {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	var out []string
	for _, line := range strings.Split(trimmed, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// PermissionReply 是归一化后的审批结果枚举(各 provider 在内部翻译成自己的原生值)。
type PermissionReply string

const (
	PermissionAllowOnce   PermissionReply = "allow_once"
	PermissionAllowAlways PermissionReply = "allow_always"
	PermissionDeny        PermissionReply = "deny"
)

// PermissionDecision 是房间回灌给 provider 的审批决策。
type PermissionDecision struct {
	Reply  PermissionReply
	By     string // 审批人 login / agent id(审计用, 可空)
	Reason string // 可空
	// Patterns 是 allow_always 时审批人从候选模式里点选的放行模式(来自
	// control 消息 metadata.patterns, 换行分隔)。空表示未点选(老前端/默认),
	// provider 按自己的默认口径记忆。provider 必须校验模式确属本次请求的候选,
	// 不得照单全收 —— 防止任意规则注入。
	Patterns []string
}

type ProviderResponse struct {
	Content   string
	Raw       string
	SessionID string
	Metadata  map[string]string
}

// ProviderEvent reports an intermediate step from a streaming CLI provider.
type ProviderEvent struct {
	Type     ProviderEventType
	Tool     string
	Summary  string
	Detail   string
	Metadata map[string]string
}

type ProviderEventType string

const (
	ProviderEventSystem     ProviderEventType = "system"
	ProviderEventText       ProviderEventType = "text"
	ProviderEventToolUse    ProviderEventType = "tool_use"
	ProviderEventToolResult ProviderEventType = "tool_result"
	ProviderEventError      ProviderEventType = "error"
	// ProviderEventPermissionRequest 表示 provider 命中需人工授权的工具调用,
	// 房间据此展示"等待授权"卡片。复用 Tool/Detail/Metadata 承载工具名、输入、
	// request_id/pattern/call_id 等。
	ProviderEventPermissionRequest ProviderEventType = "permission_request"
)
