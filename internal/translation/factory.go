package translation

import (
	"fmt"
	"net/http"
	"strings"
)

// Provider 种类常量。
const (
	// KindOllama 表示本地 Ollama 服务。
	KindOllama = "ollama"
	// KindOpenAI 表示 OpenAI 兼容接口（llama-server / vLLM / LM Studio / 云端 API）。
	KindOpenAI = "openai"
)

// Config 是 Provider 的创建配置，字段与界面上的设置项一致。
type Config struct {
	// Kind 是 Provider 种类："ollama" 或 "openai"。留空时按 ollama 处理。
	Kind string
	// BaseURL 是服务地址。留空时按种类取默认值。
	BaseURL string
	// APIKey 仅对 openai 生效，可以留空（本地服务通常不需要）。
	APIKey string
	// Model 是模型名称。留空时按种类取默认值。
	Model string
	// KeepThinking 为 true 时保留推理模型的思考过程（默认 false = 关闭）。
	// 批量字幕翻译不需要 reasoning，而 reasoning 会吃光输出预算并导致超时。
	KeepThinking bool
}

// normalizeKind 把用户输入的 Kind 归一化成标准值。
func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", KindOllama:
		return KindOllama
	case KindOpenAI, "openai-compatible", "openaicompatible", "llama-server", "llamaserver", "vllm", "lmstudio", "api":
		return KindOpenAI
	default:
		return ""
	}
}

// resolveConfig 补齐默认值，返回可直接使用的配置。
func resolveConfig(cfg Config) (Config, error) {
	kind := normalizeKind(cfg.Kind)
	if kind == "" {
		return Config{}, &Error{
			Kind:    KindFatal,
			Message: fmt.Sprintf("不支持的翻译服务类型 %q，目前只支持 ollama 与 openai", cfg.Kind),
		}
	}
	out := Config{
		Kind:         kind,
		BaseURL:      strings.TrimSpace(cfg.BaseURL),
		APIKey:       strings.TrimSpace(cfg.APIKey),
		Model:        strings.TrimSpace(cfg.Model),
		KeepThinking: cfg.KeepThinking,
	}
	switch kind {
	case KindOllama:
		if out.BaseURL == "" {
			out.BaseURL = defaultOllamaBaseURL
		}
		if out.Model == "" {
			out.Model = defaultOllamaModel
		}
	case KindOpenAI:
		if out.BaseURL == "" {
			out.BaseURL = "http://127.0.0.1:8080/v1"
		}
		if out.Model == "" {
			// 兼容 llama-server 常见的默认模型别名。
			out.Model = "default"
		}
	}
	return out, nil
}

// Describe 返回可用的 Provider 描述列表，供界面下拉与状态展示使用。
//
// 只描述配置能力与默认值，不做网络探测，因此可以安全地在前端渲染路径上调用。
func Describe(config Config) []ProviderDescription {
	resolved, err := resolveConfig(config)
	if err != nil {
		return nil
	}

	out := make([]ProviderDescription, 0, 2)

	ollamaEndpoint := resolved.BaseURL
	if resolved.Kind != KindOllama {
		ollamaEndpoint = defaultOllamaBaseURL
	}
	ollamaModel := defaultOllamaModel
	if resolved.Kind == KindOllama && resolved.Model != "" {
		ollamaModel = resolved.Model
	}
	out = append(out, ProviderDescription{
		Name:      "ollama:" + ollamaModel,
		Kind:      KindOllama,
		Endpoint:  sanitizeEndpoint(ollamaEndpoint),
		Model:     ollamaModel,
		Local:     true,
		Available: resolved.Kind == KindOllama,
		Detail:    "本地 Ollama 服务，完全离线运行；本地模型务必把并发设为 1，避免批次互相拖慢。",
		Options: []ProviderOption{
			{Name: "translategemma:12b", Kind: KindOllama, Local: true, Description: "专业翻译模型，质量最好；需约 8 GB 显存"},
			{Name: "translategemma:4b", Kind: KindOllama, Local: true, Description: "专业翻译模型，速度快；偶有漏译半句，长片建议用 12b"},
			{Name: "qwen2.5:7b-instruct", Kind: KindOllama, Local: true, Description: "通用模型，中文语感自然、不漏句；术语一致性较好"},
			{Name: "qwen2.5:3b-instruct", Kind: KindOllama, Local: true, Description: "体积小、速度快，适合长视频粗翻"},
		},
	})

	openaiEndpoint := "http://127.0.0.1:8080/v1"
	openaiModel := "default"
	if resolved.Kind == KindOpenAI {
		openaiEndpoint = resolved.BaseURL
		openaiModel = resolved.Model
	}
	hasKey := resolved.Kind == KindOpenAI && resolved.APIKey != ""
	local := isLocalEndpoint(openaiEndpoint)
	out = append(out, ProviderDescription{
		Name:      "openai:" + openaiModel,
		Kind:      KindOpenAI,
		Endpoint:  sanitizeEndpoint(openaiEndpoint),
		Model:     openaiModel,
		Local:     local,
		Available: resolved.Kind == KindOpenAI,
		Detail:    openAIDetail(local, hasKey),
		Options: []ProviderOption{
			{Name: "default", Kind: KindOpenAI, Local: local, Description: "llama-server 默认模型别名"},
			{Name: "gpt-4o-mini", Kind: KindOpenAI, Local: false, Description: "云端 OpenAI 兼容模型，质量稳定"},
			{Name: "qwen2.5:7b-instruct", Kind: KindOpenAI, Local: true, Description: "经由 Ollama /v1 兼容层调用"},
		},
	})

	return out
}

// openAIDetail 生成 OpenAI 兼容 Provider 的说明文字。
func openAIDetail(local, hasKey bool) string {
	switch {
	case local && hasKey:
		return "本地 OpenAI 兼容接口（已配置 API Key）；不支持 response_format 的服务会自动降级重试。"
	case local:
		return "本地 OpenAI 兼容接口（llama-server / vLLM / LM Studio）；不支持 response_format 时会自动降级重试。"
	case hasKey:
		return "远程 OpenAI 兼容接口（已配置 API Key）；会在本地压缩字幕文本后再发送。"
	default:
		return "远程 OpenAI 兼容接口，但尚未填写 API Key，调用会被拒绝。"
	}
}

// New 根据配置创建 Provider。
//
// 未提供 HTTPClient 时使用包内默认客户端（按种类设置超时），便于直接注入到界面层。
func New(config Config) (Provider, error) {
	resolved, err := resolveConfig(config)
	if err != nil {
		return nil, err
	}

	switch resolved.Kind {
	case KindOllama:
		return NewOllamaProvider(OllamaConfig{
			BaseURL:      resolved.BaseURL,
			Model:        resolved.Model,
			KeepThinking: resolved.KeepThinking,
		}), nil
	case KindOpenAI:
		return NewOpenAICompatibleProvider(OpenAICompatibleConfig{
			BaseURL: resolved.BaseURL,
			APIKey:  resolved.APIKey,
			Model:   resolved.Model,
		}), nil
	default:
		return nil, &Error{Kind: KindFatal, Message: "不支持的翻译服务类型"}
	}
}

// NewWithClient 与 New 相同，但允许调用方注入自定义 http.Client（例如统一超时与代理）。
func NewWithClient(config Config, client *http.Client) (Provider, error) {
	resolved, err := resolveConfig(config)
	if err != nil {
		return nil, err
	}
	switch resolved.Kind {
	case KindOllama:
		return NewOllamaProvider(OllamaConfig{
			BaseURL:      resolved.BaseURL,
			Model:        resolved.Model,
			HTTPClient:   client,
			KeepThinking: resolved.KeepThinking,
		}), nil
	case KindOpenAI:
		return NewOpenAICompatibleProvider(OpenAICompatibleConfig{
			BaseURL:    resolved.BaseURL,
			APIKey:     resolved.APIKey,
			Model:      resolved.Model,
			HTTPClient: client,
		}), nil
	default:
		return nil, &Error{Kind: KindFatal, Message: "不支持的翻译服务类型"}
	}
}
