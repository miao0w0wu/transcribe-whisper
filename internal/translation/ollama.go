package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// 默认值。
const (
	defaultOllamaBaseURL    = "http://127.0.0.1:11434"
	defaultOllamaModel      = "qwen2.5:7b-instruct"
	defaultOllamaTimeout    = 300 * time.Second
	ollamaChatPath          = "/api/chat"
	defaultOllamaNumPredict = 2048
)

// OllamaConfig 是本地 Ollama 服务的配置。
//
// BaseURL 例如 http://127.0.0.1:11434（不需要带 /api）；
// 若配置项 Config.BaseURL 已经写成 http://host:11434/api，会自动避免重复拼接。
type OllamaConfig struct {
	BaseURL     string
	Model       string
	HTTPClient  *http.Client
	Timeout     time.Duration
	Temperature float64
	NumPredict  int
}

// OllamaProvider 通过 /api/chat 调用本地 Ollama 模型。
type OllamaProvider struct {
	cfg    OllamaConfig
	client *http.Client
}

// NewOllamaProvider 构造一个 Ollama Provider。
func NewOllamaProvider(cfg OllamaConfig) *OllamaProvider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultOllamaBaseURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = defaultOllamaModel
	}
	if cfg.NumPredict <= 0 {
		cfg.NumPredict = defaultOllamaNumPredict
	}

	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	c := *base
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultOllamaTimeout
	}
	if c.Timeout == 0 {
		c.Timeout = timeout
	}

	return &OllamaProvider{cfg: cfg, client: &c}
}

// Name 返回 Provider 标识，用于缓存键与界面展示。
func (p *OllamaProvider) Name() string {
	if p == nil {
		return "ollama"
	}
	host := sanitizeEndpoint(p.cfg.BaseURL)
	model := strings.TrimSpace(p.cfg.Model)
	if host == "" {
		return "ollama:" + model
	}
	return "ollama:" + model + ":" + host
}

// ollamaChatRequest 是 /api/chat 的请求体，stream 固定为 false。
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format,omitempty"`
	Options  ollamaOptions   `json:"options"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
	NumPredict  int     `json:"num_predict"`
}

// ollamaChatResponse 是 /api/chat 的响应体（只取需要的字段）。
type ollamaChatResponse struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done  bool   `json:"done"`
	Error string `json:"error"`
}

// Translate 调用 Ollama 的 /api/chat 完成一批翻译。
func (p *OllamaProvider) Translate(ctx context.Context, req Request) ([]Choice, error) {
	if p == nil {
		return nil, &Error{Kind: KindFatal, Message: "Ollama Provider 未初始化"}
	}
	if len(req.Items) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	system, user, err := buildPrompt(req)
	if err != nil {
		return nil, err
	}

	payload := ollamaChatRequest{
		Model: p.cfg.Model,
		Messages: []ollamaMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Stream: false,
		Format: "json",
		Options: ollamaOptions{
			Temperature: p.cfg.Temperature,
			NumPredict:  p.cfg.NumPredict,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &Error{Kind: KindFatal, Message: "构造翻译请求失败", Err: err}
	}

	endpoint := joinOllamaEndpoint(p.cfg.BaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, &Error{Kind: KindFatal, Message: "构造翻译请求失败，请检查 Ollama 服务地址是否合法"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &Error{Kind: KindRetryable, Message: "连接 Ollama 服务失败，请确认 ollama serve 已启动", Err: safeNetworkError(err)}
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSuccessBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failure := classifyHTTPFailure("Ollama 服务返回错误", resp.StatusCode, body, nil)
		if hint := ollamaErrorHint(resp.StatusCode, body, p.cfg.Model); hint != "" {
			failure.Message = hint + "。" + failure.Message
		}
		return nil, failure
	}
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &Error{Kind: KindRetryable, Message: "读取 Ollama 响应失败", Err: safeNetworkError(readErr)}
	}

	var parsed ollamaChatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, choiceErrorf("无法解析 /api/chat 响应")
	}
	if msg := strings.TrimSpace(parsed.Error); msg != "" {
		// Ollama 用 HTTP 200 返回模型错误（例如模型未拉取）时也能识别。
		return nil, &Error{
			Kind:    KindFatal,
			Message: "Ollama 返回错误：" + truncateBody([]byte(msg)),
		}
	}
	content := parsed.Message.Content
	if strings.TrimSpace(content) == "" {
		return nil, choiceErrorf("Ollama 返回的内容为空")
	}

	return parseChoices(content)
}

// joinOllamaEndpoint 拼接 /api/chat，兼容用户把 /api 写进 BaseURL 的情况。
func joinOllamaEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultOllamaBaseURL
	}
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, "/api/chat"):
		return base
	case strings.HasSuffix(lower, "/api"):
		return base + "/chat"
	default:
		return base + ollamaChatPath
	}
}

// ollamaErrorHint 为常见的 Ollama 配置错误补充中文提示，便于用户自查。
func ollamaErrorHint(status int, body []byte, model string) string {
	text := strings.ToLower(string(body))
	if model == "" {
		model = defaultOllamaModel
	}
	switch {
	case strings.Contains(text, "not found") || strings.Contains(text, "try pulling") || strings.Contains(text, "pull"):
		return "模型可能尚未下载，请先执行 ollama pull " + model
	case status == http.StatusNotFound:
		return "接口不存在，请确认地址填写的是 Ollama 服务根地址（例如 http://127.0.0.1:11434）"
	default:
		return ""
	}
}
