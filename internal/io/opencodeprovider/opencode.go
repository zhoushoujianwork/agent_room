// Package opencodeprovider 实现 models.AgentProvider, 用 OpenCode 的本地
// HTTP server + SSE 事件总线驱动一次对话。与 cliprovider.ClaudeProvider 的
// 关键区别是: OpenCode 的权限审批不绑死在 TTY 上, 而是走 server 事件
// (permission.asked) + 回写接口 (POST .../permissions/:id), 因此能在 headless
// 场景下打通"请求 → 房间审批 → 放行/拒绝"的完整回路 —— 这正是本 provider 的
// 价值点, 通过 models.ProviderRequest.RequestPermission 这条统一审批 seam 接入。
//
// 注意: OpenCode 1.14.30 的 /doc(OpenAPI)只暴露裁剪版, 不可作为完整接口依据。
// 这里的字段名以 issue #10 的实测路由 + 二进制常量为准; 不确定处用宽松解析
// (map[string]any) 并留 TODO, 待真实触发场景再校准。
package opencodeprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent-room/internal/models"
)

// OpenCodeProvider 通过外部已启动的 `opencode serve` HTTP server 驱动对话。
// 本期不负责自起子进程; serverURL 指向一个已就绪的 server(例如
// http://127.0.0.1:4096)。
type OpenCodeProvider struct {
	serverURL       string
	workingDir      string
	timeout         time.Duration
	providerID      string
	modelID         string
	skipPermissions bool
	client          *http.Client

	// alwaysRules 记住房间「总是批准」过的放行模式(key = tool + "\x00" + pattern)。
	// OpenCode 的 always 回复只在单个 session 内生效, 而本 provider 每次 Complete
	// 都新建 session, 跨消息的"总是"必须由 provider 进程级记忆兜底(bridge 重启即清)。
	alwaysMu    sync.Mutex
	alwaysRules map[string]bool
}

// NewOpenCodeProvider 构造一个 OpenCode provider。参数风格对齐
// cliprovider.NewClaudeProvider:
//   - serverURL: 已启动的 opencode server 基地址(http://host:port), 必填。
//   - workingDir: 透传给 session 的工作目录(本期仅作记录, opencode server
//     的 cwd 由 server 自身决定; 留作未来 /session 建会话时下发)。
//   - timeout: 单次 Complete 的整体超时; <=0 表示不限。
//   - providerID/modelID: POST .../message 时 body.model 的两个字段。
//   - skipPermissions: true 时无人值守, 一律自动放行(等价 claude 的
//     --dangerously-skip-permissions); false 时启用审批回路。
func NewOpenCodeProvider(serverURL, workingDir string, timeout time.Duration, providerID, modelID string, skipPermissions bool) *OpenCodeProvider {
	return &OpenCodeProvider{
		serverURL:       strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		workingDir:      workingDir,
		timeout:         timeout,
		providerID:      strings.TrimSpace(providerID),
		modelID:         strings.TrimSpace(modelID),
		skipPermissions: skipPermissions,
		client:          &http.Client{},
		alwaysRules:     make(map[string]bool),
	}
}

func (p *OpenCodeProvider) Name() string { return "opencode" }

func (p *OpenCodeProvider) Complete(ctx context.Context, req models.ProviderRequest) (models.ProviderResponse, error) {
	if p.serverURL == "" {
		return models.ProviderResponse{}, errors.New("opencode provider: server URL is empty (set -opencode-server / AGENT_ROOM_OPENCODE_SERVER)")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if p.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	} else {
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
	}

	// ① 建立会话。每次 Complete 新建一个 session, 与 claude 的
	// NoSessionPersistence 默认语义对齐(无跨轮记忆, 历史靠 prompt 重建)。
	sessionID, err := p.createSession(runCtx)
	if err != nil {
		return models.ProviderResponse{}, err
	}

	// ② 并发订阅 SSE 事件流。SSE 必须在发 prompt 之前订阅好, 否则可能漏掉
	// 早期事件(message.part.* / permission.asked)。
	sseResp, err := p.openEventStream(runCtx)
	if err != nil {
		return models.ProviderResponse{}, err
	}
	defer sseResp.Body.Close()

	prompt := buildOpenCodePrompt(req)

	// 聚合器: 在 SSE goroutine 内累计最终文本, idle 信号触发后由主 goroutine 读取。
	agg := newAggregator()
	done := make(chan struct{})
	var streamErr error

	go func() {
		defer close(done)
		streamErr = p.consumeEvents(runCtx, sseResp.Body, sessionID, req, agg)
		// 无论 idle 正常结束还是流错误(如审批回写失败), 都取消 runCtx:
		// sendMessage 是同步 POST, assistant 不结束它不返回, 必须在这里解除
		// 阻塞, 否则一次回写失败会把整个 Complete 挂到超时。
		cancel()
	}()

	// ③ 发 prompt。SSE 已订阅, 这里把用户输入投进 session。该 POST 阻塞到
	// assistant 本轮完成(或被上面的 goroutine / 超时取消)。
	sendErr := p.sendMessage(runCtx, sessionID, prompt)
	if sendErr != nil {
		cancel()
	}

	// ⑤ 等 SSE goroutine 结束(session.idle / 流错误 / 取消)。
	<-done

	// ⑥ ctx 取消 -> 中断语义, 对齐 claude provider。
	if errors.Is(ctx.Err(), context.Canceled) {
		return models.ProviderResponse{}, models.ErrProviderInterrupted
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return models.ProviderResponse{}, fmt.Errorf("opencode provider timed out after %s", p.timeout)
	}
	if streamErr != nil && !errors.Is(streamErr, errStreamDone) {
		return models.ProviderResponse{}, fmt.Errorf("opencode provider stream: %w: %s", streamErr, truncate(agg.raw.String(), 2048))
	}
	// 流没给 idle(直接 EOF)且 sendMessage 自身报错 -> 返回发送错误; 正常 idle
	// 时 sendMessage 可能因上面的 cancel 返回 ctx 错误, 属预期, 忽略。
	if !errors.Is(streamErr, errStreamDone) && sendErr != nil {
		return models.ProviderResponse{}, sendErr
	}

	content := strings.TrimSpace(agg.finalText())
	if content == "" {
		return models.ProviderResponse{}, fmt.Errorf("opencode provider returned empty response: %s", truncate(agg.raw.String(), 2048))
	}

	return models.ProviderResponse{
		Content:   content,
		SessionID: sessionID,
		Raw:       agg.raw.String(),
		Metadata:  map[string]string{"provider": p.Name()},
	}, nil
}

// errStreamDone 是内部哨兵: consumeEvents 收到 session.idle 后正常结束, 用它
// 区分"自然结束"与"真实错误"。
var errStreamDone = errors.New("opencode stream done")

// aggregator 累计 SSE 的最终文本与原始事件流(后者仅用于调试/报错)。
// 文本按 partID 覆盖式聚合: message.part.updated 每次携带该 part 的全量快照,
// 直接 append 会重复; 这里记录每个 part 的最新全文, 结束时按出现顺序拼接。
// userMsgs 记录 role=user 的消息 id —— 我们 POST 的 prompt 会以 user 消息的
// part 原样回显在事件流里, 必须过滤, 否则 prompt 会泄进 trace 与最终回复。
// 所有字段只在 SSE goroutine 内写, Complete 在 <-done 之后才读, 无并发。
type aggregator struct {
	raw      strings.Builder
	userMsgs map[string]bool
	order    []string
	parts    map[string]string
	seen     map[string]bool
}

func newAggregator() *aggregator {
	return &aggregator{
		userMsgs: make(map[string]bool),
		parts:    make(map[string]string),
		seen:     make(map[string]bool),
	}
}

func (a *aggregator) markUserMessage(id string) { a.userMsgs[id] = true }

func (a *aggregator) isUserMessage(id string) bool { return a.userMsgs[id] }

// setText 记录 partID 的最新全文, 返回是否首次见到该 part(trace 只发一次)。
func (a *aggregator) setText(partID, text string) bool {
	if partID == "" {
		partID = fmt.Sprintf("part-%d", len(a.order))
	}
	_, exists := a.parts[partID]
	if !exists {
		a.order = append(a.order, partID)
	}
	a.parts[partID] = text
	return !exists
}

// firstEmit 对任意非空 key 去重, 首次返回 true。
func (a *aggregator) firstEmit(key string) bool {
	if a.seen[key] {
		return false
	}
	a.seen[key] = true
	return true
}

func (a *aggregator) finalText() string {
	var b strings.Builder
	for _, id := range a.order {
		b.WriteString(a.parts[id])
	}
	return b.String()
}

// createSession POST {server}/session 建会话, 返回 session id。
func (p *OpenCodeProvider) createSession(ctx context.Context) (string, error) {
	// TODO(opencode): 1.14.30 的 POST /session body 字段未完全核实; 这里发空
	// body, 让 server 用默认值建会话。若未来需要下发 cwd/title, 在此补充。
	resp, err := p.doJSON(ctx, http.MethodPost, "/session", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("opencode create session: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("opencode create session: status %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("opencode create session: decode: %w: %s", err, truncate(string(body), 512))
	}
	// session id 可能直接在顶层 id, 也可能裹在 info/session 下 —— 宽松取。
	if id := firstStringField(parsed, "id"); id != "" {
		return id, nil
	}
	for _, key := range []string{"info", "session", "data"} {
		if nested, ok := parsed[key].(map[string]any); ok {
			if id := firstStringField(nested, "id"); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("opencode create session: no session id in response: %s", truncate(string(body), 512))
}

// openEventStream GET {server}/event(SSE), 返回未关闭的 response 供调用方读取。
func (p *OpenCodeProvider) openEventStream(ctx context.Context) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.serverURL+"/event", nil)
	if err != nil {
		return nil, fmt.Errorf("opencode event stream: build request: %w", err)
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("opencode event stream: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("opencode event stream: status %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	return resp, nil
}

// sendMessage POST {server}/session/:id/message, body 含 model + parts。
func (p *OpenCodeProvider) sendMessage(ctx context.Context, sessionID, prompt string) error {
	body := map[string]any{
		"model": map[string]any{
			"providerID": p.providerID,
			"modelID":    p.modelID,
		},
		"parts": []any{
			map[string]any{"type": "text", "text": prompt},
		},
	}
	resp, err := p.doJSON(ctx, http.MethodPost, "/session/"+sessionID+"/message", body)
	if err != nil {
		return fmt.Errorf("opencode send message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("opencode send message: status %d: %s", resp.StatusCode, truncate(string(b), 512))
	}
	// 丢弃响应体: 真正的回复经 SSE 流式回来。
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// replyPermission POST {server}/session/:id/permissions/:permissionID。
// 实测 1.14.30: body 字段是 {"response": "once|always|reject"} —— 用 "reply"
// 会 400, 而事件总线侧(permission.replied)的字段名才叫 reply, 两边不对称。
func (p *OpenCodeProvider) replyPermission(ctx context.Context, sessionID, permissionID, reply string) error {
	body := map[string]any{"response": reply}
	resp, err := p.doJSON(ctx, http.MethodPost, "/session/"+sessionID+"/permissions/"+permissionID, body)
	if err != nil {
		return fmt.Errorf("opencode reply permission: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("opencode reply permission: status %d: %s", resp.StatusCode, truncate(string(b), 512))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// doJSON 发一个 JSON 请求, 返回未关闭的 response(调用方负责关闭 Body)。
func (p *OpenCodeProvider) doJSON(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var reader io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, p.serverURL+path, reader)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	return p.client.Do(httpReq)
}

// consumeEvents 逐行解析 SSE, 把 message.part.* 映射成 ProviderEvent, 把
// permission.asked 接到审批回路, session.idle 作为结束信号。正常结束返回
// errStreamDone, 其余返回真实错误。
func (p *OpenCodeProvider) consumeEvents(ctx context.Context, body io.Reader, sessionID string, req models.ProviderRequest, agg *aggregator) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	emit := func(ev models.ProviderEvent) {
		if req.OnEvent != nil {
			req.OnEvent(ev)
		}
	}

	// SSE 的 data: 行可能跨多行拼接; opencode 实测每个事件是单行 JSON, 但这里
	// 仍按标准 SSE "data:" 前缀解析以求稳健。
	var dataBuf strings.Builder
	flush := func() error {
		raw := strings.TrimSpace(dataBuf.String())
		dataBuf.Reset()
		if raw == "" {
			return nil
		}
		agg.raw.WriteString(raw)
		agg.raw.WriteByte('\n')
		var event map[string]any
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil // 宽松: 跳过无法解析的事件
		}
		return p.dispatchEvent(ctx, event, sessionID, req, agg, emit)
	}

	for scanner.Scan() {
		line := scanner.Text()
		// SSE: 空行表示一个事件结束。
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // 注释/心跳
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			dataBuf.WriteString(rest)
			continue
		}
		// 某些 server 直接发裸 JSON 行(无 data: 前缀)。宽松兼容。
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			dataBuf.WriteString(line)
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		// ctx 取消时 Body 被关, scanner 报错; 交由上层按 ctx 判定中断。
		return err
	}
	return nil
}

// dispatchEvent 把单个解析好的事件映射成 ProviderEvent / 审批 / 结束信号。
func (p *OpenCodeProvider) dispatchEvent(ctx context.Context, event map[string]any, sessionID string, req models.ProviderRequest, agg *aggregator, emit func(models.ProviderEvent)) error {
	eventType := firstStringField(event, "type")
	props := mapField(event, "properties")
	if props == nil {
		// 有些事件把负载直接放在顶层; 退化为整个事件。
		props = event
	}

	switch {
	case eventType == "session.idle":
		// 仅当是本 session 的 idle 才结束(宽松: 无 sessionID 字段也接受)。
		if sid := firstStringField(props, "sessionID"); sid == "" || sid == sessionID {
			return errStreamDone
		}
		return nil

	case eventType == "permission.asked":
		return p.handlePermissionAsked(ctx, props, sessionID, req)

	case eventType == "message.part.delta", eventType == "message.part.updated":
		p.handleMessagePart(props, agg, emit)
		return nil

	case eventType == "message.updated":
		// 只用来登记消息 role: 我们 POST 的 prompt 会以 user 消息回显在事件流
		// 里, 其 part 需按 messageID 过滤。不产生 trace。
		if info := mapField(props, "info"); info != nil {
			if id := firstStringField(info, "id"); id != "" && firstStringField(info, "role") == "user" {
				agg.markUserMessage(id)
			}
		}
		return nil

	case strings.HasPrefix(eventType, "session."), strings.HasPrefix(eventType, "server."):
		// 状态类事件(session.updated/status/diff、server.connected/heartbeat):
		// 纯噪声, 不进 trace; 原始串已记录在 agg.raw 供调试。
		return nil

	default:
		return nil
	}
}

// handleMessagePart 把 message.part.* 的 part 负载映射成 text/tool_use/tool_result。
// 实测 1.14.30 的 part 形态: {id, messageID, sessionID, type, text|tool|...},
// part.updated 每次携带全量快照(会对同一 part 反复触发), delta 平铺为
// {messageID, partID, field, delta}(无 type, 自然落空, 靠快照聚合)。
func (p *OpenCodeProvider) handleMessagePart(props map[string]any, agg *aggregator, emit func(models.ProviderEvent)) {
	part := mapField(props, "part")
	if part == nil {
		// delta 事件把字段平铺在 props 上。
		part = props
	}
	// 过滤用户消息的 part: 我们 POST 的 prompt 会原样回显, 不进 trace 不进聚合。
	msgID := firstStringField(part, "messageID")
	if msgID == "" {
		msgID = firstStringField(props, "messageID")
	}
	if msgID != "" && agg.isUserMessage(msgID) {
		return
	}
	partID := firstStringField(part, "id")
	if partID == "" {
		partID = firstStringField(props, "partID")
	}
	partType := firstStringField(part, "type")

	switch partType {
	case "text":
		text := firstStringField(part, "text")
		if strings.TrimSpace(text) == "" {
			return
		}
		// 覆盖式聚合; trace 只在 part 首次出现时发一条, 避免快照风暴刷屏。
		if agg.setText(partID, text) {
			emit(models.ProviderEvent{
				Type:    models.ProviderEventText,
				Summary: previewLine(text),
				Detail:  text,
			})
		}

	case "tool", "tool_use", "tool-invocation":
		tool := firstStringField(part, "tool")
		if tool == "" {
			tool = firstStringField(part, "name")
		}
		input := part["input"]
		if input == nil {
			input = part["args"]
		}
		detail := stringifyJSON(input)
		meta := map[string]string{}
		callID := firstStringField(part, "callID")
		if callID != "" {
			meta["call_id"] = callID
		}
		dedup := partID
		if dedup == "" {
			dedup = callID
		}
		// tool part 会随 state 演进反复 updated; tool_use 只发一次。
		if dedup == "" || agg.firstEmit("tool:"+dedup) {
			emit(models.ProviderEvent{
				Type:     models.ProviderEventToolUse,
				Tool:     tool,
				Summary:  summarizeToolInput(tool, input),
				Detail:   detail,
				Metadata: meta,
			})
		}
		// tool 调用可能带 state/result: 有结果时同时映射一条 tool_result(同样去重)。
		if result := part["result"]; result != nil && (dedup == "" || agg.firstEmit("toolres:"+dedup)) {
			emit(models.ProviderEvent{
				Type:    models.ProviderEventToolResult,
				Tool:    tool,
				Summary: previewLine(stringifyToolResult(result)),
				Detail:  stringifyToolResult(result),
			})
		}

	case "tool_result", "tool-result":
		content := stringifyToolResult(part["result"])
		if content == "" {
			content = stringifyToolResult(part["content"])
		}
		if content == "" {
			return
		}
		if partID != "" && !agg.firstEmit("toolres:"+partID) {
			return
		}
		emit(models.ProviderEvent{
			Type:    models.ProviderEventToolResult,
			Summary: previewLine(content),
			Detail:  content,
		})
	}
}

// handlePermissionAsked 是审批回路的核心。收到 permission.asked 后:
//   - skipPermissions 或 RequestPermission==nil: 无人值守, 直接放行 (reply=once)。
//   - 否则: 先 emit 一条 permission_request 事件让房间展示卡片, 再阻塞调用
//     req.RequestPermission 等房间决策, 拿到决策后映射枚举并回写 server。
func (p *OpenCodeProvider) handlePermissionAsked(ctx context.Context, props map[string]any, sessionID string, req models.ProviderRequest) error {
	permissionID := firstStringField(props, "id")
	if permissionID == "" {
		permissionID = firstStringField(props, "requestID")
	}
	sid := firstStringField(props, "sessionID")
	if sid == "" {
		sid = sessionID
	}
	// 实测 1.14.30 的 permission.asked payload:
	//   {id, sessionID, permission:"bash", patterns:[<完整命令>...],
	//    metadata:{}, always:[<总是批准会放行的模式>...],
	//    tool:{messageID, callID}}
	// 工具名在 permission 字段; 命令在 patterns 数组; tool 是对象而非字符串。
	meta := stringMap(mapField(props, "metadata"))
	tool := firstStringField(props, "permission")
	if tool == "" {
		tool = meta["tool"] // 退化: 其他版本/工具可能放 metadata
	}
	patterns := stringSlice(props["patterns"])
	input := strings.Join(patterns, "\n")
	if input == "" {
		// 退化: metadata.command / title。
		if input = meta["command"]; input == "" {
			input = firstStringField(props, "title")
		}
	}
	toolObj := mapField(props, "tool")
	callID := firstStringField(toolObj, "callID")
	always := stringSlice(props["always"])

	// 无人值守 / 无审批回调: 自动放行。
	if p.skipPermissions || req.RequestPermission == nil {
		return p.replyPermission(ctx, sid, permissionID, "once")
	}

	// 命中已记住的「总是批准」规则: 自动放行, 不再打扰房间。回写 "always" 让
	// 本 session 内同模式的后续调用也不再触发 permission.asked。发一条 system
	// 事件进 thinking 流, 让房间知道这次是按固定规则放行的, 而非凭空执行。
	if rule := p.matchAlwaysRule(tool, always); rule != "" {
		if req.OnEvent != nil {
			req.OnEvent(models.ProviderEvent{
				Type:    models.ProviderEventSystem,
				Tool:    tool,
				Summary: "已按「总是批准」规则自动放行: " + rule,
				Detail:  input,
			})
		}
		return p.replyPermission(ctx, sid, permissionID, "always")
	}

	// 阻塞等房间决策(或 ctx 取消)。审批卡片由 bridge 的 RequestPermission 闭包
	// 统一渲染(它持有 permission_id <-> pending channel 映射), provider 不另发
	// 卡片事件, 避免房间出现重复卡片。
	decision, err := req.RequestPermission(ctx, models.PermissionRequest{
		RequestID: permissionID,
		Tool:      tool,
		Input:     input,
		Metadata:  permissionMetadata(callID, always, meta),
	})
	if err != nil {
		// ctx 取消 / 房间未决策: 不主动放行, 让回路因中断收尾。返回错误交上层
		// 按 ctx 判定; 但要避免把 server 卡死, 尽力回写一个 reject 兜底。
		_ = p.replyPermission(context.Background(), sid, permissionID, "reject")
		return err
	}

	// ④ 归一化枚举 -> opencode 原生值: allow_once->once / allow_always->always /
	// deny->reject。未知值保守按 reject 处理。
	reply := "reject"
	switch decision.Reply {
	case models.PermissionAllowOnce:
		reply = "once"
	case models.PermissionAllowAlways:
		reply = "always"
		// 记住本次 always 模式, 让之后新 session 里的同模式调用自动放行。
		// 审批人有点选时只记点选的(取与服务端 always 列表的交集, 防注入)。
		chosen := always
		if len(decision.Patterns) > 0 {
			picked := make(map[string]bool, len(decision.Patterns))
			for _, p := range decision.Patterns {
				picked[p] = true
			}
			chosen = nil
			for _, a := range always {
				if picked[a] {
					chosen = append(chosen, a)
				}
			}
		}
		p.rememberAlwaysRules(tool, chosen)
	case models.PermissionDeny:
		reply = "reject"
	}
	return p.replyPermission(ctx, sid, permissionID, reply)
}

// matchAlwaysRule 返回命中的已记住模式(无命中返回空)。permission.asked 的
// always 数组列出了「总是批准」会放行本次调用的模式, 与记忆求交即可判定,
// 无需 provider 自己实现通配符匹配。
func (p *OpenCodeProvider) matchAlwaysRule(tool string, always []string) string {
	p.alwaysMu.Lock()
	defer p.alwaysMu.Unlock()
	for _, pattern := range always {
		if p.alwaysRules[alwaysRuleKey(tool, pattern)] {
			return pattern
		}
	}
	return ""
}

// rememberAlwaysRules 把一次 allow_always 决策对应的放行模式记入进程级记忆。
func (p *OpenCodeProvider) rememberAlwaysRules(tool string, always []string) {
	if len(always) == 0 {
		return
	}
	p.alwaysMu.Lock()
	defer p.alwaysMu.Unlock()
	if p.alwaysRules == nil {
		p.alwaysRules = make(map[string]bool)
	}
	for _, pattern := range always {
		p.alwaysRules[alwaysRuleKey(tool, pattern)] = true
	}
}

func alwaysRuleKey(tool, pattern string) string { return tool + "\x00" + pattern }

// permissionMetadata 把 callID / always 模式与原始 metadata 合并成传给房间的
// Metadata。always 是「总是批准」将放行的模式列表, 前端卡片用它做提示。
func permissionMetadata(callID string, always []string, meta map[string]string) map[string]string {
	if len(meta) == 0 && callID == "" && len(always) == 0 {
		return nil
	}
	out := make(map[string]string, len(meta)+2)
	maps.Copy(out, meta)
	if callID != "" {
		out["call_id"] = callID
	}
	if len(always) > 0 {
		out["always"] = models.EncodePatternList(always)
	}
	return out
}

// ---------- 小工具 ----------
// 注: aggregator 的 Builder 仅在单个 consumeEvents goroutine 内写入, 主
// goroutine 在 <-done 之后才读取, 存在 happens-before 关系, 故无需加锁。

func firstStringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// stringSlice 把 []any 里的字符串元素取出来(忽略空串与非字符串)。
func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func stringMap(m map[string]any) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch x := v.(type) {
		case string:
			out[k] = x
		default:
			out[k] = stringifyJSON(v)
		}
	}
	return out
}

func summarizeToolInput(tool string, input any) string {
	obj, ok := input.(map[string]any)
	if !ok {
		return tool
	}
	for _, key := range []string{"command", "file_path", "path", "pattern", "url"} {
		if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
			return previewLine(v)
		}
	}
	return tool
}

func stringifyToolResult(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch x := item.(type) {
			case string:
				parts = append(parts, x)
			case map[string]any:
				if text, ok := x["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return stringifyJSON(value)
	}
}

func stringifyJSON(value any) string {
	if value == nil {
		return ""
	}
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(b)
}

func previewLine(text string) string {
	trimmed := strings.TrimSpace(text)
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	if len(trimmed) > 160 {
		trimmed = trimmed[:160] + "…"
	}
	return trimmed
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(truncated)"
}
