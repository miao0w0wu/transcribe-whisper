package translation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 查询本机模型的超时。模型列表接口只读本地，正常在毫秒级返回；
// 给 8 秒是为了容忍 Ollama 刚启动或首次加载索引的情况。
const ollamaTagsTimeout = 8 * time.Second

// OllamaModel 描述本机 Ollama 中已安装的一个模型。
type OllamaModel struct {
	// Name 是 Ollama 里的完整模型标识，例如 qwen2.5:7b-instruct。
	Name string `json:"name"`
	// Size 是模型磁盘占用（字节）。
	Size int64 `json:"size"`
	// ParameterSize 是参数量描述，例如 7.6B；部分模型不提供。
	ParameterSize string `json:"parameterSize"`
	// Quantization 是量化等级，例如 Q4_K_M。
	Quantization string `json:"quantization"`
	// Family 是模型家族，例如 qwen2。
	Family string `json:"family"`
	// ModifiedAt 是最后修改时间（ISO8601），可能为空。
	ModifiedAt string `json:"modifiedAt"`
}

// ollamaTagsResponse 是 /api/tags 的响应体（只取需要的字段）。
type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		ModifiedAt string `json:"modified_at"`
		Size       int64  `json:"size"`
		Details    struct {
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

// ListOllamaModels 查询本机 Ollama 已安装的模型。
//
// baseURL 留空时使用默认的 127.0.0.1:11434。服务未启动时返回带中文提示的
// 错误，调用方据此提示用户执行 ollama serve；这不属于异常路径。
func ListOllamaModels(ctx context.Context, baseURL string, client *http.Client) ([]OllamaModel, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOllamaBaseURL
	}
	endpoint := joinOllamaTagsEndpoint(baseURL)

	if client == nil {
		client = &http.Client{Timeout: ollamaTagsTimeout}
	}
	ctx, cancel := context.WithTimeout(ctx, ollamaTagsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造模型列表请求失败，请检查 Ollama 服务地址是否合法：%s", endpoint)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("连接 Ollama 服务超时（%s），请确认 ollama serve 正在运行", endpoint)
		}
		return nil, fmt.Errorf("连接 Ollama 服务失败，请确认 ollama serve 已启动：%s", endpoint)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Ollama 模型列表接口返回 %d：%s", resp.StatusCode, truncateBody(body))
	}
	if readErr != nil {
		return nil, fmt.Errorf("读取 Ollama 模型列表失败：%v", readErr)
	}

	var parsed ollamaTagsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("无法解析 Ollama 模型列表响应：%v", err)
	}

	models := make([]OllamaModel, 0, len(parsed.Models))
	seen := make(map[string]bool, len(parsed.Models))
	for _, item := range parsed.Models {
		name := strings.TrimSpace(item.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, OllamaModel{
			Name:          name,
			Size:          item.Size,
			ParameterSize: strings.TrimSpace(item.Details.ParameterSize),
			Quantization:  strings.TrimSpace(item.Details.QuantizationLevel),
			Family:        strings.TrimSpace(item.Details.Family),
			ModifiedAt:    strings.TrimSpace(item.ModifiedAt),
		})
	}
	// 名称排序，保证界面上的顺序稳定（不依赖 Ollama 返回顺序）。
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

// joinOllamaTagsEndpoint 拼接 /api/tags，兼容用户把 /api 或 /api/chat 写进 BaseURL。
func joinOllamaTagsEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultOllamaBaseURL
	}
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, "/api/tags"):
		return base
	case strings.HasSuffix(lower, "/api/chat"):
		return strings.TrimSuffix(base, "/chat") + "/tags"
	case strings.HasSuffix(lower, "/api"):
		return base + "/tags"
	default:
		return base + "/api/tags"
	}
}
