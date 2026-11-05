package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// DisableThinking 为 true 时向 Ollama 发送 think=false，关闭推理模型的思考过程。
	// 默认开启（即默认不思考），因为批量字幕翻译不需要 reasoning，
	// 而 reasoning 会吃光输出预算并导致超时。需要保留思考时显式设为 false。
	KeepThinking bool
}

// boolPtr 返回 v 的地址，用于构造可空 bool 字段（区分「未设置」与「显式 false」）。
func boolPtr(v bool) *bool { return &v }

// thinkFlag 把 KeepThinking 转成请求里的 think 字段。
//
// 默认返回显式 false（关闭思考）；只有调用方明确要求保留思考时才发 nil，
// 让 Ollama 使用模型自身的默认行为。
func thinkFlag(keepThinking bool) *bool {
	if keepThinking {
		return nil
	}
	return boolPtr(false)
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
	// Think 显式关闭推理模型的思考过程。
	//
	// 推理模型（qwen3 / qwen3.5 / deepseek-r1 等）默认会先输出一大段 reasoning，
	// 且 reasoning 走 message.thinking 字段而不是 message.content。对翻译任务而言
	// 这段思考毫无用处，却会带来两个致命后果：
	//  1. 思考独占 num_predict 预算，把正文全部挤掉，message.content 变成空串；
	//     实测 huihui-qwen3.5-9b 在 num_predict=2048 下思考 2048 token 后
	//     done_reason=length，content 为空，单次请求耗时 28~32 秒，
	//     调用方的 20~25 秒超时必然触发，界面就报「context deadline exceeded」。
	//  2. 明显拖慢速度：同一请求从 16.6 秒降到 0.26 秒。
	// 传 false 是 Ollama 的原生开关（0.5+ 支持），非推理模型忽略该字段，无副作用。
	Think *bool `json:"think,omitempty"`
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
		Role string `json:"role"`
		// Content 是正文；推理模型可能把它留空而只填 Thinking。
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
	Error      string `json:"error"`
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
		Think:  thinkFlag(p.cfg.KeepThinking),
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
		// 单个条目引发的解码循环会让整批失败：Ollama 检测到 token 重复达到
		// 上限就直接中止预测并返回 500。这类错误对整批是致命的，但如果
		// 拆成更小的批次（最终单条），肇事条目就能被隔离出来，其余条目
		// 照常翻译，不会因为一条烂字幕丢掉 19 条好字幕。
		if isRepeatLoopFailure(body) {
			return nil, &Error{
				Kind: KindFatal,
				Message: "Ollama 中止了预测：模型在翻译本批内容时陷入重复循环（该批将被自动拆分重试）。" +
					"这种输入通常来自语音识别产生的长串重复语气词",
			}
		}
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
		// 正文为空但思考内容不为空 = 输出预算被 reasoning 吃光了。
		// 即使已经传了 think=false，老版本 Ollama 或不认该字段的模型仍可能这样，
		// 所以这里给出可操作的中文提示，而不是让用户看一句「内容为空」。
		if thinking := strings.TrimSpace(parsed.Message.Thinking); thinking != "" {
			return nil, &Error{
				Kind: KindFatal,
				Message: fmt.Sprintf("模型把输出预算全用在思考过程上，正文为空（思考 %d 字符）。"+
					"请换用非推理模型，或调大 num_predict；推理模型不适合本工具的批量翻译。模型：%s",
					len([]rune(thinking)), p.cfg.Model),
			}
		}
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

// isRepeatLoopFailure 判断 Ollama 的错误响应是否属于「解码陷入重复循环被中止」。
//
// Ollama 在 token 重复达到上限时会以 HTTP 500 返回
// {"error":"prediction aborted, token repeat limit reached"}。
func isRepeatLoopFailure(body []byte) bool {
	text := strings.ToLower(string(body))
	return strings.Contains(text, "token repeat limit") ||
		strings.Contains(text, "prediction aborted")
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
