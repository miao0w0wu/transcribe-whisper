package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 默认值。字段为 0 或空时按这里的取值。
const (
	defaultOpenAIPath        = "/chat/completions"
	defaultOpenAITimeout     = 120 * time.Second
	defaultOpenAIMaxTokens   = 2048
	defaultOpenAITemperature = 0.3
	// maxErrorBody 是错误信息里最多回显的响应体字节数。
	maxErrorBody = 300
	// maxSuccessBody 是一次成功响应最多读取的字节数，防止异常服务端撑爆内存。
	maxSuccessBody = 8 << 20
)

// OpenAICompatibleConfig 是 OpenAI 兼容接口的配置。
//
// BaseURL 指向 /v1 这类前缀（例如 http://127.0.0.1:8080/v1）；
// 若已经写成以 /chat/completions 结尾，则原样使用，不再追加。
type OpenAICompatibleConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	HTTPClient  *http.Client
	MaxTokens   int
	Temperature float64
	Timeout     time.Duration
}

// OpenAICompatibleProvider 对接 OpenAI /chat/completions 协议，
// 适用于 llama-server、vLLM、LM Studio、Ollama 的 /v1 兼容层以及云端 API。
type OpenAICompatibleProvider struct {
	cfg    OpenAICompatibleConfig
	client *http.Client
}

// NewOpenAICompatibleProvider 构造一个 OpenAI 兼容 Provider。
func NewOpenAICompatibleProvider(cfg OpenAICompatibleConfig) *OpenAICompatibleProvider {
	p := &OpenAICompatibleProvider{cfg: cfg}
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	// 复制一份，避免调用方后续修改共享的 http.Client 影响这里的超时。
	c := *base
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultOpenAITimeout
	}
	if c.Timeout == 0 || timeout != defaultOpenAITimeout {
		c.Timeout = timeout
	}
	p.client = &c
	if p.cfg.MaxTokens <= 0 {
		p.cfg.MaxTokens = defaultOpenAIMaxTokens
	}
	if p.cfg.Temperature == 0 {
		p.cfg.Temperature = defaultOpenAITemperature
	}
	return p
}

// Name 返回 Provider 标识，用于缓存键与界面展示。
//
// 形如 openai:llama-server:http://127.0.0.1:8080/v1；绝不包含 API Key。
func (p *OpenAICompatibleProvider) Name() string {
	if p == nil {
		return "openai"
	}
	model := strings.TrimSpace(p.cfg.Model)
	if model == "" {
		model = "default"
	}
	host := sanitizeEndpoint(p.cfg.BaseURL)
	if host == "" {
		return "openai:" + model
	}
	return "openai:" + model + ":" + host
}

// Translate 调用 /chat/completions 完成一批翻译。
//
// 若服务端因为不支持 response_format 返回 400，会自动去掉该字段重试一次；
// 这只针对本地服务兼容性，不改变错误分类规则。
func (p *OpenAICompatibleProvider) Translate(ctx context.Context, req Request) ([]Choice, error) {
	if p == nil {
		return nil, &Error{Kind: KindFatal, Message: "OpenAI 兼容 Provider 未初始化"}
	}
	if len(req.Items) == 0 {
		return nil, nil
	}
	baseURL := strings.TrimSpace(p.cfg.BaseURL)
	if baseURL == "" {
		return nil, &Error{Kind: KindFatal, Message: "OpenAI 兼容接口地址为空，请先填写服务地址"}
	}
	model := strings.TrimSpace(p.cfg.Model)
	if model == "" {
		return nil, &Error{Kind: KindFatal, Message: "未指定模型名称，请先在设置里选择或填写模型"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	system, user, err := buildPrompt(req)
	if err != nil {
		return nil, err
	}

	choices, status, body, contentErr, err := p.postChat(ctx, baseURL, model, system, user, true)
	if err == nil {
		return choices, nil
	}
	// 仅当服务端以 400 拒绝、且这次失败发生在 HTTP 层（而不是内容解析层）时，
	// 去掉 response_format 再试一次——很多本地服务不吃这个字段。
	if status == http.StatusBadRequest && contentErr == nil {
		choices, status, body, contentErr, err = p.postChat(ctx, baseURL, model, system, user, false)
		if err == nil {
			return choices, nil
		}
	}
	if contentErr != nil {
		return nil, contentErr
	}
	if _, ok := err.(*Error); ok {
		return nil, err
	}
	return nil, classifyHTTPFailure("翻译服务返回错误", status, body, err)
}

// postChat 发送一次请求。
//
// 返回值：(译文, HTTP 状态码, 原始响应体, 内容层错误, 传输层错误)。
// 内容层错误（contentErr）表示"服务端正常响应但内容不合规"，属于可重试；
// 传输层错误（err）需要由调用方按状态码分类为可重试或致命。
func (p *OpenAICompatibleProvider) postChat(ctx context.Context, baseURL, model, system, user string, useResponseFormat bool) ([]Choice, int, []byte, error, error) {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": p.cfg.Temperature,
		"max_tokens":  p.cfg.MaxTokens,
	}
	if useResponseFormat {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, nil, nil, &Error{Kind: KindFatal, Message: "构造翻译请求失败", Err: err}
	}

	endpoint := joinEndpoint(baseURL, defaultOpenAIPath)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, nil, nil, &Error{Kind: KindFatal, Message: "构造翻译请求失败，请检查接口地址是否合法"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(p.cfg.APIKey); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, nil, nil, ctxErr
		}
		return nil, 0, nil, nil, &Error{Kind: KindRetryable, Message: "连接翻译服务失败，请确认服务已启动且地址正确", Err: safeNetworkError(err)}
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSuccessBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 交给上层按状态码分类。
		return nil, resp.StatusCode, body, nil, errHTTPFailure
	}
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, resp.StatusCode, nil, nil, ctxErr
		}
		return nil, resp.StatusCode, nil, nil, &Error{Kind: KindRetryable, Message: "读取翻译服务响应失败", Err: safeNetworkError(readErr)}
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, resp.StatusCode, body, choiceErrorf("无法解析 chat/completions 响应"), nil
	}
	if len(parsed.Choices) == 0 {
		return nil, resp.StatusCode, body, choiceErrorf("响应里没有 choices"), nil
	}

	choices, err := parseChoices(parsed.Choices[0].Message.Content)
	if err != nil {
		return nil, resp.StatusCode, body, err, nil
	}
	return choices, resp.StatusCode, body, nil, nil
}

// errHTTPFailure 标记"HTTP 状态码不是 2xx"，调用方据此做状态码分类与降级判断。
var errHTTPFailure = errors.New("translation: non-2xx response")

// classifyHTTPFailure 把 HTTP 状态码与传输错误转成可重试或致命错误。
//
// 规则：408/429/5xx 与网络错误可重试；其余 4xx（401/403/404 等）是配置问题，判为致命。
func classifyHTTPFailure(prefix string, status int, body []byte, cause error) *Error {
	detail := truncateBody(body)
	if status == 0 {
		if cause == nil {
			return &Error{Kind: KindRetryable, Message: prefix}
		}
		return &Error{Kind: KindRetryable, Message: prefix, Err: safeNetworkError(cause)}
	}

	var msg string
	if detail != "" {
		msg = fmt.Sprintf("%s（HTTP %d）：%s", prefix, status, detail)
	} else {
		msg = fmt.Sprintf("%s（HTTP %d）", prefix, status)
	}

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Kind: KindFatal, Message: "翻译服务拒绝访问，请检查 API Key 或访问权限。" + msg}
	case status == http.StatusNotFound:
		return &Error{Kind: KindFatal, Message: "翻译接口不存在，请检查服务地址与模型名是否填写正确。" + msg}
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return &Error{Kind: KindRetryable, Message: msg}
	case status >= 400 && status < 500:
		return &Error{Kind: KindFatal, Message: "翻译请求被服务端拒绝，请检查模型名称与请求参数。" + msg}
	default:
		return &Error{Kind: KindRetryable, Message: msg}
	}
}

// truncateBody 把响应体压缩成单行摘要并截断到 300 字符以内。
//
// 会顺带屏蔽疑似密钥的字样，防止服务端把 Key 回显到错误信息里。
func truncateBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxErrorBody {
		s = s[:maxErrorBody] + "…（已截断）"
	}
	return redactSecrets(s)
}

// safeNetworkError 去掉错误信息里可能出现的完整 URL 与请求头，只保留根因描述。
func safeNetworkError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return redactURLError(urlErr.Err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("请求超时")
	}
	return errors.New(redactSecrets(err.Error()))
}

// redactURLError 把底层网络错误转成不含地址的描述。
func redactURLError(err error) error {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("请求超时")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("请求超时")
	}
	return errors.New(redactSecrets(err.Error()))
}

// redactSecrets 把形如 Bearer xxx 的片段替换成占位符。
func redactSecrets(s string) string {
	return redactBearer(s)
}

// redactBearer 隐藏 Authorization 头里的令牌。
func redactBearer(s string) string {
	const marker = "Bearer "
	lower := s
	for {
		idx := strings.Index(strings.ToLower(lower), strings.ToLower(marker))
		if idx < 0 {
			break
		}
		rest := lower[idx+len(marker):]
		end := strings.IndexAny(rest, " \",}\n\r")
		if end < 0 {
			end = len(rest)
		}
		if end == 0 {
			break
		}
		s = s[:idx+len(marker)] + "***" + s[idx+len(marker)+end:]
		lower = s
	}
	if len(s) > 0 && strings.HasPrefix(s, "sk-") {
		return "sk-***"
	}
	return s
}

// joinEndpoint 拼接接口地址，避免出现双斜杠或丢失 /v1 前缀。
func joinEndpoint(baseURL, path string) string {
	base := strings.TrimSpace(baseURL)
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(strings.ToLower(base), "/chat/completions") ||
		strings.HasSuffix(strings.ToLower(base), "/v1/chat/completions") {
		return base
	}
	return base + path
}

// sanitizeEndpoint 返回不含账号密码与查询参数的地址，用于 Provider 标识与展示。
func sanitizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

// endpointHost 返回地址的 host，用于错误提示与本地/远程判断。
func endpointHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// isLocalEndpoint 判断地址是否指向本机，用于 Provider 描述里的 Local 字段。
func isLocalEndpoint(raw string) bool {
	host := strings.ToLower(endpointHost(raw))
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return strings.HasPrefix(host, "127.")
}
