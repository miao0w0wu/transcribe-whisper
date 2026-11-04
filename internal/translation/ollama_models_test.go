package translation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTagsServer 起一个假的 Ollama /api/tags 服务。
func newTagsServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("请求路径应为 /api/tags，实际 %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("应为 GET，实际 %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

const sampleTags = `{
  "models": [
    {
      "name": "qwen2.5:7b-instruct",
      "modified_at": "2026-11-04T20:28:56.2961448+08:00",
      "size": 4683087332,
      "details": {"family": "qwen2", "parameter_size": "7.6B", "quantization_level": "Q4_K_M"}
    },
    {
      "name": "llama3.1:8b",
      "modified_at": "2026-10-01T10:00:00Z",
      "size": 4920000000,
      "details": {"family": "llama", "parameter_size": "8.0B", "quantization_level": "Q4_0"}
    }
  ]
}`

func TestListOllamaModelsParsesTags(t *testing.T) {
	server := newTagsServer(t, sampleTags, http.StatusOK)
	defer server.Close()

	models, err := ListOllamaModels(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("读取模型列表失败: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("应有 2 个模型，实际 %d", len(models))
	}
	// 名称排序保证界面顺序稳定。
	if models[0].Name != "llama3.1:8b" || models[1].Name != "qwen2.5:7b-instruct" {
		t.Fatalf("模型应按名称排序，实际 %q, %q", models[0].Name, models[1].Name)
	}
	qwen := models[1]
	if qwen.Size != 4683087332 || qwen.ParameterSize != "7.6B" || qwen.Quantization != "Q4_K_M" || qwen.Family != "qwen2" {
		t.Fatalf("字段解析不完整: %+v", qwen)
	}
	if qwen.ModifiedAt == "" {
		t.Fatal("修改时间不应为空")
	}
}

func TestListOllamaModelsEmptyIsNotError(t *testing.T) {
	server := newTagsServer(t, `{"models": []}`, http.StatusOK)
	defer server.Close()
	models, err := ListOllamaModels(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("空列表不应报错: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("应为空，实际 %d", len(models))
	}
}

func TestListOllamaModelsDeduplicates(t *testing.T) {
	body := `{"models": [
		{"name": "a:1", "size": 1},
		{"name": "a:1", "size": 1},
		{"name": "b:1", "size": 2}
	]}`
	server := newTagsServer(t, body, http.StatusOK)
	defer server.Close()
	models, err := ListOllamaModels(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("应去重为 2 个，实际 %d", len(models))
	}
}

func TestListOllamaModelsHTTPError(t *testing.T) {
	server := newTagsServer(t, `{"error": "boom"}`, http.StatusInternalServerError)
	defer server.Close()
	_, err := ListOllamaModels(context.Background(), server.URL, server.Client())
	if err == nil {
		t.Fatal("5xx 应返回错误")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("错误信息应包含状态码，实际：%v", err)
	}
}

func TestListOllamaModelsConnectionRefused(t *testing.T) {
	server := newTagsServer(t, sampleTags, http.StatusOK)
	url := server.URL
	server.Close() // 关掉服务，模拟 Ollama 未启动

	_, err := ListOllamaModels(context.Background(), url, nil)
	if err == nil {
		t.Fatal("服务未启动时应返回错误")
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Fatalf("错误信息应提示启动 ollama serve，实际：%v", err)
	}
}

func TestListOllamaModelsInvalidJSON(t *testing.T) {
	server := newTagsServer(t, `not json`, http.StatusOK)
	defer server.Close()
	_, err := ListOllamaModels(context.Background(), server.URL, server.Client())
	if err == nil || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("非法 JSON 应报解析错误，实际：%v", err)
	}
}

func TestJoinOllamaTagsEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                                  "http://127.0.0.1:11434/api/tags",
		"http://127.0.0.1:11434":            "http://127.0.0.1:11434/api/tags",
		"http://127.0.0.1:11434/":           "http://127.0.0.1:11434/api/tags",
		"http://127.0.0.1:11434/api":        "http://127.0.0.1:11434/api/tags",
		"http://127.0.0.1:11434/api/":       "http://127.0.0.1:11434/api/tags",
		"http://127.0.0.1:11434/api/chat":   "http://127.0.0.1:11434/api/tags",
		"http://host:8080/api/tags":         "http://host:8080/api/tags",
		"http://192.168.1.5:11434":          "http://192.168.1.5:11434/api/tags",
	}
	for in, want := range cases {
		if got := joinOllamaTagsEndpoint(in); got != want {
			t.Fatalf("%q -> %q，期望 %q", in, got, want)
		}
	}
}

// 真实本机 Ollama（未启动时跳过）。
func TestListOllamaModelsRealLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实服务测试")
	}
	models, err := ListOllamaModels(context.Background(), "", nil)
	if err != nil {
		t.Skipf("本机 Ollama 不可用，跳过：%v", err)
	}
	t.Logf("本机 Ollama 有 %d 个模型", len(models))
	for _, m := range models {
		t.Logf("  %s (%s %s, %d 字节)", m.Name, m.ParameterSize, m.Quantization, m.Size)
	}
}

// 保证 OllamaModel 可被 JSON 序列化（前端依赖这些字段名）。
func TestOllamaModelJSONShape(t *testing.T) {
	data, err := json.Marshal(OllamaModel{Name: "a:1", Size: 2, ParameterSize: "1B", Quantization: "Q4", Family: "f", ModifiedAt: "t"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	for _, key := range []string{"name", "size", "parameterSize", "quantization", "family", "modifiedAt"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("JSON 缺少字段 %s：%s", key, data)
		}
	}
}
