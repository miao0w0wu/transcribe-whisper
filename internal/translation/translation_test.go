package translation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 测试辅助：一个可编排行为的假 OpenAI 兼容服务
// ---------------------------------------------------------------------------

// chatRequest 是测试侧解析出的请求体。
type chatRequest struct {
	Model          string `json:"model"`
	ResponseFormat any    `json:"response_format"`
	Messages       []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// systemPrompt 返回系统提示文本。
func (r chatRequest) systemPrompt() string {
	for _, m := range r.Messages {
		if m.Role == "system" {
			return m.Content
		}
	}
	return ""
}

// userPrompt 返回用户消息文本。
func (r chatRequest) userPrompt() string {
	for _, m := range r.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// itemsFromPrompt 从 user 消息里解析出待翻译条目，用于让假服务端"翻译"。
func itemsFromPrompt(t *testing.T, userPrompt string) []promptInput {
	t.Helper()
	idx := strings.Index(userPrompt, "[")
	if idx < 0 {
		t.Fatalf("用户消息里没有 JSON 数组: %q", userPrompt)
	}
	var inputs []promptInput
	if err := json.Unmarshal([]byte(userPrompt[idx:]), &inputs); err != nil {
		t.Fatalf("解析用户消息失败: %v", err)
	}
	return inputs
}

// echoChoices 生成"翻译结果"：把源文本加上目标语言前缀，便于断言。
func echoChoices(inputs []promptInput, prefix string) []Choice {
	out := make([]Choice, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, Choice{ID: in.ID, Text: prefix + in.Text})
	}
	return out
}

// encodeChatResponse 把 choices 包成 chat/completions 的响应体。
func encodeChatResponse(t *testing.T, choices []Choice) string {
	t.Helper()
	payload, err := json.Marshal(choices)
	if err != nil {
		t.Fatalf("序列化内容失败: %v", err)
	}
	resp, err := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"index": 0, "message": map[string]string{"role": "assistant", "content": string(payload)}},
		},
	})
	if err != nil {
		t.Fatalf("序列化响应失败: %v", err)
	}
	return string(resp)
}

// handlerFunc 是测试服务端的处理函数签名。
type handlerFunc func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int)

// newFakeServer 启动一个记录请求次数的假 OpenAI 服务。
func newFakeServer(t *testing.T, fn handlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("请求路径不正确: %s", r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体失败: %v", err)
		}
		var body chatRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		fn(t, w, r, body, int(n))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// writeJSON 写一段 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// newTestOpenAIProvider 构造指向假服务的 Provider。
func newTestOpenAIProvider(srv *httptest.Server) *OpenAICompatibleProvider {
	return NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		BaseURL: srv.URL + "/v1",
		APIKey:  "sk-test-secret-key",
		Model:   "test-model",
	})
}

// testOptions 返回测试用的流水线参数：重试不等待。
func testOptions() Options {
	return Options{
		BatchSize:      20,
		MaxRetries:     3,
		RetryDelay:     0,
		Concurrency:    1,
		TargetLanguage: "zh",
		SourceLanguage: "en",
		Style:          "subtitle",
		ContentMode:    "subtitle",
	}
}

// ---------------------------------------------------------------------------
// 依赖替换：RetryDelay=0 在 Options.normalize 里会被当成默认值，
// 测试需要真正 0 等待，因此用很小的固定值表达"不等待"。
// ---------------------------------------------------------------------------

// noDelay 返回几乎不掉的时间，避免测试因退避变成秒级。
const noDelay = time.Microsecond

// items 生成 n 条字幕。
func items(n int) []Item {
	out := make([]Item, n)
	for i := 0; i < n; i++ {
		out[i] = Item{ID: fmt.Sprintf("%d", i+1), Text: fmt.Sprintf("line %d text", i+1)}
	}
	return out
}

// textOf 按 id 取出译文。
func textOf(choices []Choice, id string) (string, bool) {
	for _, c := range choices {
		if c.ID == id {
			return c.Text, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// 1. 成功单批
// ---------------------------------------------------------------------------

func TestTranslateSingleBatchSuccess(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "译:")))
	})
	tr := NewTranslator(newTestOpenAIProvider(srv), testOptions())

	in := items(5)
	got, err := tr.Translate(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("期望成功，实际错误: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("期望 5 条译文，实际 %d", len(got))
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("期望只请求 1 次，实际 %d", *calls)
	}
	for i, c := range got {
		if c.ID != in[i].ID {
			t.Fatalf("第 %d 条 id 顺序错乱: 期望 %s 实际 %s", i, in[i].ID, c.ID)
		}
		if c.Text != "译:"+in[i].Text {
			t.Fatalf("第 %d 条译文不正确: %q", i, c.Text)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. 多批
// ---------------------------------------------------------------------------

func TestTranslateMultipleBatches(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		inputs := itemsFromPrompt(t, body.userPrompt())
		if len(inputs) > 3 {
			t.Errorf("批大小超过配置上限: %d", len(inputs))
		}
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(inputs, "T-")))
	})
	opts := testOptions()
	opts.BatchSize = 3
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	got, err := tr.Translate(context.Background(), items(7), nil)
	if err != nil {
		t.Fatalf("期望成功，实际错误: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("期望 7 条译文，实际 %d", len(got))
	}
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Fatalf("7 条按每批 3 条应请求 3 次，实际 %d", n)
	}
	for _, c := range got {
		if !strings.HasPrefix(c.Text, "T-") {
			t.Fatalf("译文格式不正确: %q", c.Text)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. 模型返回带 ```json 包裹
// ---------------------------------------------------------------------------

func TestTranslateWithCodeFenceResponse(t *testing.T) {
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		inputs := itemsFromPrompt(t, body.userPrompt())
		payload, _ := json.Marshal(echoChoices(inputs, "F"))
		content := "好的，结果如下：\n```json\n" + string(payload) + "\n```\n以上。"
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
		writeJSON(w, http.StatusOK, string(resp))
	})
	tr := NewTranslator(newTestOpenAIProvider(srv), testOptions())

	got, err := tr.Translate(context.Background(), items(2), nil)
	if err != nil {
		t.Fatalf("代码块包裹应当被容忍，实际错误: %v", err)
	}
	if len(got) != 2 || got[0].Text != "Fline 1 text" || got[1].Text != "Fline 2 text" {
		t.Fatalf("解析结果不正确: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 4. 数组缺条目触发补批
// ---------------------------------------------------------------------------

func TestTranslateMissingEntriesTriggersSupplement(t *testing.T) {
	var mu sync.Mutex
	seenTexts := make(map[string]int)
	hadFullBatch := false

	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		inputs := itemsFromPrompt(t, body.userPrompt())
		mu.Lock()
		defer mu.Unlock()
		for _, in := range inputs {
			seenTexts[in.Text]++
		}
		// 首次整批请求时故意丢掉最后一条；补批请求正常返回。
		if !hadFullBatch && len(inputs) == 3 {
			hadFullBatch = true
			inputs = inputs[:len(inputs)-1]
		}
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(inputs, "S")))
	})
	tr := NewTranslator(newTestOpenAIProvider(srv), testOptions())

	got, err := tr.Translate(context.Background(), items(3), nil)
	if err != nil {
		t.Fatalf("补批应当成功，实际错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条译文，实际 %d: %+v", len(got), got)
	}
	// 首次整批 + 一次补批 = 2 次请求（没有整批重来）。
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("期望补批后共 2 次请求，实际 %d", n)
	}

	// 补批时不应该把已成功的条目重新发一遍。
	mu.Lock()
	defer mu.Unlock()
	if seenTexts["line 1 text"] != 1 || seenTexts["line 2 text"] != 1 {
		t.Fatalf("已成功的条目被重复发送: %+v", seenTexts)
	}
	// 缺失条目在首次整批里出现过一次，补批时再发一次，共 2 次。
	if seenTexts["line 3 text"] != 2 {
		t.Fatalf("缺失条目应在补批时重发一次: %+v", seenTexts)
	}
}

// ---------------------------------------------------------------------------
// 5. 格式错误触发重试
// ---------------------------------------------------------------------------

func TestTranslateFormatErrorRetries(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		if call == 1 {
			// 第一次返回纯文本垃圾，触发解析失败 -> 可重试。
			resp, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "抱歉，我无法完成"}}},
			})
			writeJSON(w, http.StatusOK, string(resp))
			return
		}
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "R")))
	})
	opts := testOptions()
	opts.RetryDelay = noDelay
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	got, err := tr.Translate(context.Background(), items(2), nil)
	if err != nil {
		t.Fatalf("重试后应当成功，实际错误: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(got))
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("格式错误应重试一次，期望 2 次请求，实际 %d", n)
	}
}

// 至少重试到 MaxRetries 用尽，最后全部失败但仍返回聚合错误。
func TestTranslateFormatErrorExhaustsRetries(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "[{\"id\":\"1\"}]"}}},
		})
		writeJSON(w, http.StatusOK, string(resp))
	})
	opts := testOptions()
	opts.RetryDelay = noDelay
	opts.MaxRetries = 2
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	got, err := tr.Translate(context.Background(), items(1), nil)
	if err == nil {
		t.Fatal("全部失败时应当返回错误")
	}
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("期望 *BatchError，实际 %T: %v", err, err)
	}
	if len(be.FailedIDs) != 1 || be.FailedIDs[0] != "1" {
		t.Fatalf("失败 id 列表不正确: %+v", be.FailedIDs)
	}
	if len(got) != 0 {
		t.Fatalf("失败时不应返回条目，实际 %+v", got)
	}
	// 首次 + 2 次重试 = 3 次请求。
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Fatalf("期望 3 次请求，实际 %d", n)
	}
}

// ---------------------------------------------------------------------------
// 6. 429 重试后成功
// ---------------------------------------------------------------------------

func TestTranslateRetriesOn429(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		if call <= 2 {
			writeJSON(w, http.StatusTooManyRequests, `{"error":"rate limited"}`)
			return
		}
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "L")))
	})
	opts := testOptions()
	opts.RetryDelay = noDelay
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	got, err := tr.Translate(context.Background(), items(2), nil)
	if err != nil {
		t.Fatalf("429 重试后应当成功，实际错误: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望 2 条译文，实际 %d", len(got))
	}
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Fatalf("期望 3 次请求，实际 %d", n)
	}
}

// ---------------------------------------------------------------------------
// 7. 致命 401 不重试
// ---------------------------------------------------------------------------

func TestTranslateFatal401NoRetry(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`)
	})
	opts := testOptions()
	opts.RetryDelay = noDelay
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	_, err := tr.Translate(context.Background(), items(3), nil)
	if err == nil {
		t.Fatal("401 应当返回错误")
	}
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("期望 *BatchError，实际 %T", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("致命错误不应重试，期望 1 次请求，实际 %d", n)
	}

	// 错误必须是中文且不能泄露 API Key。
	msg := err.Error()
	if strings.Contains(msg, "sk-test-secret-key") {
		t.Fatalf("错误信息泄露了 API Key: %s", msg)
	}
	if !strings.Contains(msg, "翻译失败") {
		t.Fatalf("错误信息应为中文说明: %s", msg)
	}
}

// 直接验证 Provider 层面的错误分类。
func TestOpenAIProviderErrorKinds(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   ErrorKind
	}{
		{"401", http.StatusUnauthorized, `{"error":"unauthorized"}`, KindFatal},
		{"403", http.StatusForbidden, `{"error":"forbidden"}`, KindFatal},
		{"404", http.StatusNotFound, `{"error":"not found"}`, KindFatal},
		{"400", http.StatusBadRequest, `{"error":"bad request"}`, KindFatal},
		{"408", http.StatusRequestTimeout, `{"error":"timeout"}`, KindRetryable},
		{"429", http.StatusTooManyRequests, `{"error":"slow down"}`, KindRetryable},
		{"500", http.StatusInternalServerError, `{"error":"boom"}`, KindRetryable},
		{"503", http.StatusServiceUnavailable, `{"error":"loading"}`, KindRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
				writeJSON(w, tc.status, tc.body)
			})
			p := newTestOpenAIProvider(srv)
			_, err := p.Translate(context.Background(), Request{
				Items:          []Item{{ID: "1", Text: "hello"}},
				TargetLanguage: "zh",
			})
			if err == nil {
				t.Fatal("期望错误")
			}
			var pe *Error
			if !errors.As(err, &pe) {
				t.Fatalf("期望 *Error，实际 %T: %v", err, err)
			}
			if pe.Kind != tc.kind {
				t.Fatalf("状态码 %d 期望 %s，实际 %s（%s）", tc.status, tc.kind, pe.Kind, pe.Message)
			}
			if strings.Contains(pe.Message, "sk-test-secret-key") {
				t.Fatalf("错误信息泄露 API Key: %s", pe.Message)
			}
		})
	}
}

// 响应体截断到 300 字符以内。
func TestOpenAIProviderTruncatesLongErrorBody(t *testing.T) {
	long := strings.Repeat("x", 2000)
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusInternalServerError, long)
	})
	p := newTestOpenAIProvider(srv)
	_, err := p.Translate(context.Background(), Request{Items: []Item{{ID: "1", Text: "hello"}}, TargetLanguage: "zh"})
	if err == nil {
		t.Fatal("期望错误")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("期望 *Error，实际 %T", err)
	}
	if strings.Count(pe.Message, "x") > maxErrorBody {
		t.Fatalf("错误信息未截断，长度 %d", len(pe.Message))
	}
}

// 服务端不支持 response_format 时，400 自动降级重试一次。
func TestOpenAIProviderRetriesWithoutResponseFormat(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		if body.ResponseFormat != nil {
			writeJSON(w, http.StatusBadRequest, `{"error":"unsupported field: response_format"}`)
			return
		}
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "N")))
	})
	p := newTestOpenAIProvider(srv)
	got, err := p.Translate(context.Background(), Request{
		Items:          []Item{{ID: "1", Text: "hello world"}},
		TargetLanguage: "zh",
	})
	if err != nil {
		t.Fatalf("降级重试后应当成功，实际错误: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("期望 1 条译文，实际 %d", len(got))
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("期望 2 次请求（首次带 response_format，降级一次），实际 %d", n)
	}
}

// ---------------------------------------------------------------------------
// 8. ctx 取消
// ---------------------------------------------------------------------------

func TestTranslateContextCanceled(t *testing.T) {
	block := make(chan struct{})
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		<-block
	})
	defer close(block)

	tr := NewTranslator(newTestOpenAIProvider(srv), testOptions())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := tr.Translate(ctx, items(5), nil)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("期望 context.Canceled，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后没有及时返回")
	}
}

// 已取消的 ctx 直接返回，不应发起请求。
func TestTranslateWithAlreadyCanceledContext(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	tr := NewTranslator(newTestOpenAIProvider(srv), testOptions())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := tr.Translate(ctx, items(2), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际 %v", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("已取消的 ctx 不应发起请求，实际 %d 次", calls)
	}
}

// ---------------------------------------------------------------------------
// 9. 缓存命中不重复请求
// ---------------------------------------------------------------------------

func TestTranslateCacheHitSkipsRequest(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "C")))
	})
	opts := testOptions()
	cache := NewMemoryCache()
	opts.Cache = cache
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	in := items(3)
	first, err := tr.Translate(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("首次翻译失败: %v", err)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("首次应请求 1 次，实际 %d", *calls)
	}

	second, err := tr.Translate(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("二次翻译失败: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("缓存命中不应再请求，实际总请求 %d 次", n)
	}
	if len(second) != len(first) {
		t.Fatalf("缓存结果长度不一致: %d vs %d", len(second), len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("第 %d 条缓存结果不一致: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// 设置变化时缓存必须不命中。
func TestCacheKeyVariesWithSettings(t *testing.T) {
	base := testOptions()
	base.Cache = NewMemoryCache()

	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "K")))
	})
	in := []Item{{ID: "1", Text: "hello"}}

	if _, err := NewTranslator(newTestOpenAIProvider(srv), base).Translate(context.Background(), in, nil); err != nil {
		t.Fatalf("首次翻译失败: %v", err)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("首次应请求 1 次，实际 %d", *calls)
	}

	// 换 Style 后必须重新请求。
	changed := base
	changed.Style = "formal"
	if _, err := NewTranslator(newTestOpenAIProvider(srv), changed).Translate(context.Background(), in, nil); err != nil {
		t.Fatalf("换风格后翻译失败: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("风格变化后缓存应不命中，期望 2 次请求，实际 %d", n)
	}

	// 换目标语言后必须重新请求。
	changed2 := base
	changed2.TargetLanguage = "ja"
	if _, err := NewTranslator(newTestOpenAIProvider(srv), changed2).Translate(context.Background(), in, nil); err != nil {
		t.Fatalf("换目标语言后翻译失败: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Fatalf("目标语言变化后缓存应不命中，期望 3 次请求，实际 %d", n)
	}

	// 换模型后必须重新请求。
	other := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "another-model",
	})
	if _, err := NewTranslator(other, base).Translate(context.Background(), in, nil); err != nil {
		t.Fatalf("换模型后翻译失败: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 4 {
		t.Fatalf("模型变化后缓存应不命中，期望 4 次请求，实际 %d", n)
	}
}

// MemoryCache 并发安全。
func TestMemoryCacheConcurrent(t *testing.T) {
	c := NewMemoryCache()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", i)
			c.Put(key, fmt.Sprintf("v%d", i))
			if v, ok := c.Get(key); !ok || v != fmt.Sprintf("v%d", i) {
				t.Errorf("并发读写结果不一致: %s -> %q, %v", key, v, ok)
			}
		}(i)
	}
	wg.Wait()
	if c.Len() != 50 {
		t.Fatalf("期望 50 条缓存，实际 %d", c.Len())
	}
}

// ---------------------------------------------------------------------------
// 10. 术语表注入
// ---------------------------------------------------------------------------

func TestGlossaryInjectedIntoPrompt(t *testing.T) {
	var mu sync.Mutex
	var systemPrompts []string

	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		mu.Lock()
		systemPrompts = append(systemPrompts, body.systemPrompt())
		mu.Unlock()
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "G")))
	})

	opts := testOptions()
	opts.Glossary = map[string]string{"neural network": "神经网络", "API": "接口"}
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	in := []Item{
		{ID: "1", Text: "A neural network is used here"}, // 命中 neural network
		{ID: "2", Text: "Call the service now"},          // 未命中
		{ID: "3", Text: "Read the API docs"},             // 命中 API
	}
	if _, err := tr.Translate(context.Background(), in, nil); err != nil {
		t.Fatalf("翻译失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(systemPrompts) != 1 {
		t.Fatalf("期望 1 次请求，实际 %d", len(systemPrompts))
	}
	prompt := systemPrompts[0]
	for _, want := range []string{"neural network => 神经网络", "API => 接口", "术语表"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("系统提示缺少术语表内容 %q:\n%s", want, prompt)
		}
	}
}

// 批次里没有用到的术语不下发，减少提示词噪音。
func TestGlossaryOnlyMatchingTermsSent(t *testing.T) {
	var mu sync.Mutex
	var lastSystem string
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		mu.Lock()
		lastSystem = body.systemPrompt()
		mu.Unlock()
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "M")))
	})
	opts := testOptions()
	opts.Glossary = map[string]string{"GPU": "显卡", "Never Appears": "永不出现"}
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	if _, err := tr.Translate(context.Background(), []Item{{ID: "1", Text: "The GPU is hot"}}, nil); err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(lastSystem, "GPU => 显卡") {
		t.Fatalf("命中的术语应出现在提示词里: %s", lastSystem)
	}
	if strings.Contains(lastSystem, "Never Appears") {
		t.Fatalf("未命中的术语不应出现在提示词里: %s", lastSystem)
	}
}

// 译文缺失术语译法时标记待复核但不阻断。
func TestApplyGlossaryMarksMissingTerms(t *testing.T) {
	src := []Item{
		{ID: "1", Text: "The neural network works"},
		{ID: "2", Text: "Nothing special here"},
	}
	choices := []Choice{
		{ID: "1", Text: "这个模型可以工作"}, // 缺少 神经网络
		{ID: "2", Text: "没什么特别的"},
	}
	glossary := map[string]string{"neural network": "神经网络"}

	out, needsReview := applyGlossary(src, choices, glossary)
	if len(out) != 2 {
		t.Fatalf("输出条数不正确: %d", len(out))
	}
	if len(needsReview) != 1 || needsReview[0] != 0 {
		t.Fatalf("期望只标记第 1 条，实际 %+v", needsReview)
	}
	if !strings.Contains(out[0].Text, "待复核") || !strings.Contains(out[0].Text, "神经网络") {
		t.Fatalf("缺失术语应被标记: %q", out[0].Text)
	}
	if out[1].Text != "没什么特别的" {
		t.Fatalf("未命中的条目不应被改动: %q", out[1].Text)
	}

	// 译文已经包含术语译法时不应标记。
	ok, review := applyGlossary(src, []Choice{{ID: "1", Text: "神经网络可以工作"}}, glossary)
	if len(review) != 0 {
		t.Fatalf("已包含术语不应标记: %+v", review)
	}
	if ok[0].Text != "神经网络可以工作" {
		t.Fatalf("不应改动译文: %q", ok[0].Text)
	}
}

// 术语校验不阻断流水线：有缺失术语也照常返回。
func TestGlossaryMismatchDoesNotBlockPipeline(t *testing.T) {
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		inputs := itemsFromPrompt(t, body.userPrompt())
		out := make([]Choice, 0, len(inputs))
		for _, in := range inputs {
			out = append(out, Choice{ID: in.ID, Text: "译文"}) // 故意不含术语
		}
		writeJSON(w, http.StatusOK, encodeChatResponse(t, out))
	})
	opts := testOptions()
	opts.Glossary = map[string]string{"line": "行"}
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	got, err := tr.Translate(context.Background(), []Item{{ID: "1", Text: "line one"}}, nil)
	if err != nil {
		t.Fatalf("术语缺失不应阻断翻译: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Text, "待复核") {
		t.Fatalf("应返回带复核标记的译文: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 11. 并发为 1 时不乱序
// ---------------------------------------------------------------------------

func TestTranslateSequentialPreservesOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	var inFlight, maxInFlight int32

	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		mu.Lock()
		if cur > maxInFlight {
			maxInFlight = cur
		}
		inputs := itemsFromPrompt(t, body.userPrompt())
		order = append(order, inputs[0].ID)
		mu.Unlock()

		time.Sleep(2 * time.Millisecond)
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(inputs, "O")))
	})

	opts := testOptions()
	opts.BatchSize = 2
	opts.Concurrency = 1
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	in := items(8)
	got, err := tr.Translate(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("期望 %d 条，实际 %d", len(in), len(got))
	}
	for i := range in {
		if got[i].ID != in[i].ID {
			t.Fatalf("第 %d 条顺序错乱: 期望 %s 实际 %s", i, in[i].ID, got[i].ID)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 1 {
		t.Fatalf("并发为 1 时不应有并行请求，实际峰值 %d", maxInFlight)
	}
	want := []string{"1", "3", "5", "7"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("批次发送顺序应当稳定: 期望 %v 实际 %v", want, order)
	}
}

// 并发 > 1 时结果仍按输入顺序返回。
func TestTranslateConcurrentPreservesInputOrder(t *testing.T) {
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		inputs := itemsFromPrompt(t, body.userPrompt())
		// 让前面的批次更慢，制造乱序完成。
		time.Sleep(time.Duration(len(inputs)) * time.Millisecond)
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(inputs, "P")))
	})
	opts := testOptions()
	opts.BatchSize = 2
	opts.Concurrency = 2
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	in := items(6)
	got, err := tr.Translate(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	for i := range in {
		if got[i].ID != in[i].ID {
			t.Fatalf("并发下结果顺序错乱: 第 %d 条期望 %s 实际 %s", i, in[i].ID, got[i].ID)
		}
	}
}

// Options 越界值被规范化。
func TestOptionsNormalize(t *testing.T) {
	o := Options{}.normalize()
	if o.BatchSize != defaultBatchSize || o.MaxRetries != defaultMaxRetries ||
		o.RetryDelay != defaultRetryDelay || o.Concurrency != defaultConcurrency {
		t.Fatalf("默认值不正确: %+v", o)
	}
	o = Options{BatchSize: 9999, MaxRetries: 99, Concurrency: 99}.normalize()
	if o.BatchSize != maxBatchSize || o.MaxRetries != maxRetryCount || o.Concurrency != maxConcurrency {
		t.Fatalf("越界值未封顶: %+v", o)
	}
	if got := retryDelay(100*time.Millisecond, 1); got != 100*time.Millisecond {
		t.Fatalf("首次重试延迟应为基值，实际 %v", got)
	}
	if got := retryDelay(100*time.Millisecond, 10); got != maxRetryDelay {
		t.Fatalf("退避应封顶 %v，实际 %v", maxRetryDelay, got)
	}
}

// ---------------------------------------------------------------------------
// 12. 输入校验与进度
// ---------------------------------------------------------------------------

func TestValidateFiltersAndPassthrough(t *testing.T) {
	in := []Item{
		{ID: "1", Text: "   "},
		{ID: "2", Text: ""},
		{ID: "3", Text: "..."},
		{ID: "4", Text: "12:34"},
		{ID: "5", Text: "♪♪"},
		{ID: "6", Text: "  hello world  "},
		{ID: "7", Text: "你好"},
	}
	translatable, passthrough := Validate(in)
	if len(translatable) != 2 {
		t.Fatalf("期望 2 条需要翻译，实际 %d: %+v", len(translatable), translatable)
	}
	if translatable[0].ID != "6" || translatable[0].Text != "hello world" {
		t.Fatalf("文本未做 TrimSpace: %+v", translatable[0])
	}
	if len(passthrough) != 3 {
		t.Fatalf("期望 3 条透传，实际 %d", len(passthrough))
	}
	for i, want := range []string{"3", "4", "5"} {
		if passthrough[i].ID != want {
			t.Fatalf("透传顺序不正确: %+v", passthrough)
		}
	}
	if passthrough[0].Text != "..." {
		t.Fatalf("透传内容应为原文: %q", passthrough[0].Text)
	}
}

// 流水线里空文本被跳过、纯符号按原文透传、不送模型。
func TestTranslateSkipsEmptyAndPassthroughPureSymbols(t *testing.T) {
	var mu sync.Mutex
	var sentIDs []string

	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		inputs := itemsFromPrompt(t, body.userPrompt())
		mu.Lock()
		for _, in := range inputs {
			sentIDs = append(sentIDs, in.ID)
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(inputs, "V")))
	})
	tr := NewTranslator(newTestOpenAIProvider(srv), testOptions())

	in := []Item{
		{ID: "1", Text: "hello"},
		{ID: "2", Text: "   "},
		{ID: "3", Text: "!!!"},
		{ID: "4", Text: "world"},
	}
	got, err := tr.Translate(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("空文本应被跳过，期望 3 条，实际 %d: %+v", len(got), got)
	}
	if v, ok := textOf(got, "3"); !ok || v != "!!!" {
		t.Fatalf("纯符号应原文透传: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(sentIDs, ",") != "1,4" {
		t.Fatalf("只有真实文本才应发给模型，实际 %v", sentIDs)
	}
}

// 进度回调单调递增并以 total 结束。
func TestTranslateProgress(t *testing.T) {
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "G")))
	})
	opts := testOptions()
	opts.BatchSize = 3
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	var mu sync.Mutex
	var reports [][2]int
	progress := func(done, total int) {
		mu.Lock()
		reports = append(reports, [2]int{done, total})
		mu.Unlock()
	}

	in := items(7)
	if _, err := tr.Translate(context.Background(), in, progress); err != nil {
		t.Fatalf("翻译失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reports) == 0 {
		t.Fatal("没有进度回调")
	}
	last := reports[len(reports)-1]
	if last[0] != 7 || last[1] != 7 {
		t.Fatalf("最终进度应为 7/7，实际 %v", last)
	}
	prev := -1
	for _, rep := range reports {
		if rep[1] != 7 {
			t.Fatalf("total 应恒为 7，实际 %d", rep[1])
		}
		if rep[0] < prev {
			t.Fatalf("进度不应回退: %v", reports)
		}
		prev = rep[0]
	}
}

// 全部失败时返回 BatchError，部分成功时同时返回结果。
func TestTranslatePartialFailureReturnsBatchError(t *testing.T) {
	srv, _ := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		inputs := itemsFromPrompt(t, body.userPrompt())
		if inputs[0].ID == "3" {
			writeJSON(w, http.StatusBadRequest, `{"error":"bad request"}`)
			return
		}
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(inputs, "B")))
	})
	opts := testOptions()
	opts.BatchSize = 2
	opts.RetryDelay = noDelay
	tr := NewTranslator(newTestOpenAIProvider(srv), opts)

	got, err := tr.Translate(context.Background(), items(4), nil)
	if err == nil {
		t.Fatal("部分失败时应返回错误")
	}
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("期望 *BatchError，实际 %T: %v", err, err)
	}
	if strings.Join(be.FailedIDs, ",") != "3,4" {
		t.Fatalf("失败 id 不正确: %v", be.FailedIDs)
	}
	if len(got) != 2 {
		t.Fatalf("成功的 2 条应被返回，实际 %d: %+v", len(got), got)
	}
	if _, ok := textOf(got, "1"); !ok {
		t.Fatalf("成功条目缺失: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 提示词构造与解析的单元测试
// ---------------------------------------------------------------------------

func TestBuildSystemPromptContents(t *testing.T) {
	req := Request{
		Items:          []Item{{ID: "1", Text: "hello"}},
		SourceLanguage: "en",
		TargetLanguage: "zh",
		Style:          "subtitle",
		ContentMode:    "subtitle",
		Glossary:       map[string]string{"A": "甲", "B": "乙"},
	}
	prompt := buildSystemPrompt(req)
	checks := []string{
		"字幕翻译",
		"简体中文",
		"只输出 JSON",
		"id",
		"术语表",
		"A => 甲",
		"B => 乙",
		"简洁",
	}
	for _, want := range checks {
		if !strings.Contains(prompt, want) {
			t.Fatalf("系统提示缺少 %q:\n%s", want, prompt)
		}
	}
	// 输入输出格式说明必须出现。
	if !strings.Contains(prompt, `{"id": "原 id", "text": "译文"}`) {
		t.Fatalf("系统提示缺少输出格式说明:\n%s", prompt)
	}
	if strings.Contains(prompt, "hello") {
		t.Fatalf("系统提示不应包含字幕正文:\n%s", prompt)
	}
}

func TestBuildUserPromptIsJSONArray(t *testing.T) {
	req := Request{Items: []Item{{ID: "1", Text: "a"}, {ID: "2", Text: "b"}}}
	user, err := buildUserPrompt(req)
	if err != nil {
		t.Fatalf("构造用户消息失败: %v", err)
	}
	idx := strings.Index(user, "[")
	if idx < 0 {
		t.Fatalf("用户消息缺少数组: %q", user)
	}
	var parsed []promptInput
	if err := json.Unmarshal([]byte(user[idx:]), &parsed); err != nil {
		t.Fatalf("用户消息不是合法 JSON: %v", err)
	}
	if len(parsed) != 2 || parsed[0].ID != "1" || parsed[1].Text != "b" {
		t.Fatalf("用户消息内容不正确: %+v", parsed)
	}
}

func TestParseChoicesTolerance(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"纯数组", `[{"id":"1","text":"甲"}]`, 1},
		{"json 代码块", "```json\n[{\"id\":\"1\",\"text\":\"甲\"}]\n```", 1},
		{"裸代码块", "```\n[{\"id\":\"1\",\"text\":\"甲\"}]\n```", 1},
		{"前后夹带文字", "好的：\n[{\"id\":\"1\",\"text\":\"甲\"}]\n希望有帮助。", 1},
		{"文本里有方括号", `[{"id":"1","text":"[a] 和 ] 都在字符串里"}]`, 1},
		{"数组后有额外说明", `[{"id":"1","text":"甲"},{"id":"2","text":"乙"}] 以上共 2 条`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChoices(tc.raw)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("期望 %d 条，实际 %d", tc.want, len(got))
			}
			for _, c := range got {
				if c.ID == "" || c.Text == "" {
					t.Fatalf("解析结果缺字段: %+v", c)
				}
			}
		})
	}
}

func TestParseChoicesErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"空响应", ""},
		{"没有数组", "抱歉，我无法完成这个请求"},
		{"数组未闭合", `[{"id":"1","text":"甲"}`},
		{"缺少 id", `[{"text":"甲"}]`},
		{"空 text", `[{"id":"1","text":"   "}]`},
		{"id 为空串", `[{"id":"","text":"甲"}]`},
		{"空数组", `[]`},
		{"非法 JSON", `[{"id":"1","text":}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseChoices(tc.raw)
			if err == nil {
				t.Fatal("期望错误")
			}
			var pe *Error
			if !errors.As(err, &pe) {
				t.Fatalf("期望 *Error，实际 %T", err)
			}
			if pe.Kind != KindRetryable {
				t.Fatalf("格式错误应为可重试，实际 %s", pe.Kind)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Ollama Provider
// ---------------------------------------------------------------------------

// newFakeOllama 启动假 Ollama 服务。
func newFakeOllama(t *testing.T, fn func(t *testing.T, w http.ResponseWriter, body map[string]any, call int)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/api/chat" {
			t.Errorf("请求路径不正确: %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		if stream, ok := body["stream"].(bool); !ok || stream {
			t.Errorf("stream 必须为 false，实际 %v", body["stream"])
		}
		fn(t, w, body, int(n))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestOllamaProviderSuccess(t *testing.T) {
	srv, calls := newFakeOllama(t, func(t *testing.T, w http.ResponseWriter, body map[string]any, call int) {
		if model, _ := body["model"].(string); model != defaultOllamaModel {
			t.Errorf("默认模型不正确: %v", model)
		}
		choices := []Choice{{ID: "1", Text: "你好"}, {ID: "2", Text: "世界"}}
		content, _ := json.Marshal(choices)
		resp, _ := json.Marshal(map[string]any{
			"message": map[string]string{"role": "assistant", "content": "```json\n" + string(content) + "\n```"},
			"done":    true,
		})
		writeJSON(w, http.StatusOK, string(resp))
	})
	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL})
	if !strings.HasPrefix(p.Name(), "ollama:") {
		t.Fatalf("Name 不正确: %s", p.Name())
	}

	got, err := p.Translate(context.Background(), Request{
		Items:          []Item{{ID: "1", Text: "hello"}, {ID: "2", Text: "world"}},
		TargetLanguage: "zh",
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if len(got) != 2 || got[0].Text != "你好" {
		t.Fatalf("结果不正确: %+v", got)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("期望 1 次请求，实际 %d", *calls)
	}
}

func TestOllamaProviderErrors(t *testing.T) {
	t.Run("404致命", func(t *testing.T) {
		srv, _ := newFakeOllama(t, func(t *testing.T, w http.ResponseWriter, body map[string]any, call int) {
			writeJSON(w, http.StatusNotFound, `{"error":"model not found"}`)
		})
		p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL})
		_, err := p.Translate(context.Background(), Request{Items: []Item{{ID: "1", Text: "hi"}}, TargetLanguage: "zh"})
		var pe *Error
		if !errors.As(err, &pe) {
			t.Fatalf("期望 *Error，实际 %T: %v", err, err)
		}
		if pe.Kind != KindFatal {
			t.Fatalf("404 应为致命错误，实际 %s", pe.Kind)
		}
		if !strings.Contains(pe.Message, "pull") {
			t.Fatalf("模型缺失应给出 ollama pull 提示: %s", pe.Message)
		}
	})

	t.Run("500可重试", func(t *testing.T) {
		srv, _ := newFakeOllama(t, func(t *testing.T, w http.ResponseWriter, body map[string]any, call int) {
			writeJSON(w, http.StatusInternalServerError, `{"error":"boom"}`)
		})
		p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL})
		_, err := p.Translate(context.Background(), Request{Items: []Item{{ID: "1", Text: "hi"}}, TargetLanguage: "zh"})
		var pe *Error
		if !errors.As(err, &pe) || pe.Kind != KindRetryable {
			t.Fatalf("500 应为可重试，实际 %v", err)
		}
	})

	t.Run("200里的error字段", func(t *testing.T) {
		srv, _ := newFakeOllama(t, func(t *testing.T, w http.ResponseWriter, body map[string]any, call int) {
			writeJSON(w, http.StatusOK, `{"error":"something wrong"}`)
		})
		p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL})
		_, err := p.Translate(context.Background(), Request{Items: []Item{{ID: "1", Text: "hi"}}, TargetLanguage: "zh"})
		if err == nil || !strings.Contains(err.Error(), "Ollama 返回错误") {
			t.Fatalf("应识别 200 响应里的 error 字段，实际 %v", err)
		}
	})

	t.Run("空内容", func(t *testing.T) {
		srv, _ := newFakeOllama(t, func(t *testing.T, w http.ResponseWriter, body map[string]any, call int) {
			writeJSON(w, http.StatusOK, `{"message":{"content":""},"done":true}`)
		})
		p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL})
		_, err := p.Translate(context.Background(), Request{Items: []Item{{ID: "1", Text: "hi"}}, TargetLanguage: "zh"})
		var pe *Error
		if !errors.As(err, &pe) || pe.Kind != KindRetryable {
			t.Fatalf("空内容应为可重试，实际 %v", err)
		}
	})
}

// Ollama 通过流水线走完整链路。
func TestOllamaThroughPipeline(t *testing.T) {
	srv, _ := newFakeOllama(t, func(t *testing.T, w http.ResponseWriter, body map[string]any, call int) {
		messages, _ := body["messages"].([]any)
		userMsg := ""
		for _, m := range messages {
			mm, _ := m.(map[string]any)
			if mm["role"] == "user" {
				userMsg, _ = mm["content"].(string)
			}
		}
		inputs := itemsFromPrompt(t, userMsg)
		content, _ := json.Marshal(echoChoices(inputs, "O"))
		resp, _ := json.Marshal(map[string]any{
			"message": map[string]string{"role": "assistant", "content": string(content)},
			"done":    true,
		})
		writeJSON(w, http.StatusOK, string(resp))
	})
	tr := NewTranslator(NewOllamaProvider(OllamaConfig{BaseURL: srv.URL}), testOptions())
	got, err := tr.Translate(context.Background(), items(3), nil)
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条，实际 %d", len(got))
	}
}

// 路径拼接：BaseURL 带不带 /api 都能正确落到 /api/chat。
func TestJoinOllamaEndpoint(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:11434":      "http://127.0.0.1:11434/api/chat",
		"http://127.0.0.1:11434/":     "http://127.0.0.1:11434/api/chat",
		"http://127.0.0.1:11434/api":  "http://127.0.0.1:11434/api/chat",
		"http://127.0.0.1:11434/api/": "http://127.0.0.1:11434/api/chat",
		"":                            "http://127.0.0.1:11434/api/chat",
	}
	for in, want := range cases {
		if got := joinOllamaEndpoint(in); got != want {
			t.Fatalf("输入 %q 期望 %q，实际 %q", in, want, got)
		}
	}
}

// OpenAI 兼容接口的路径拼接。
func TestJoinEndpoint(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8080/v1":                  "http://127.0.0.1:8080/v1/chat/completions",
		"http://127.0.0.1:8080/v1/":                 "http://127.0.0.1:8080/v1/chat/completions",
		"https://api.openai.com/v1":                 "https://api.openai.com/v1/chat/completions",
		"http://127.0.0.1:8080/v1/chat/completions": "http://127.0.0.1:8080/v1/chat/completions",
	}
	for in, want := range cases {
		if got := joinEndpoint(in, defaultOpenAIPath); got != want {
			t.Fatalf("输入 %q 期望 %q，实际 %q", in, want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// 工厂
// ---------------------------------------------------------------------------

func TestFactoryNewDefaults(t *testing.T) {
	p, err := New(Config{Kind: "ollama"})
	if err != nil {
		t.Fatalf("创建 Ollama Provider 失败: %v", err)
	}
	if !strings.Contains(p.Name(), defaultOllamaModel) {
		t.Fatalf("默认模型不正确: %s", p.Name())
	}

	p, err = New(Config{Kind: "openai"})
	if err != nil {
		t.Fatalf("创建 OpenAI Provider 失败: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "openai:") {
		t.Fatalf("OpenAI Provider 名称不正确: %s", p.Name())
	}

	// 空 Kind 默认按 ollama。
	if _, err := New(Config{}); err != nil {
		t.Fatalf("空 Kind 应默认为 ollama: %v", err)
	}

	// 未知 Kind 返回致命错误。
	_, err = New(Config{Kind: "anthropic"})
	if err == nil {
		t.Fatal("未知 Kind 应返回错误")
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != KindFatal {
		t.Fatalf("未知 Kind 应为致命错误，实际 %v", err)
	}
}

func TestFactoryNameHidesAPIKey(t *testing.T) {
	p, err := New(Config{
		Kind:    "openai",
		BaseURL: "https://user:secret@api.example.com/v1",
		APIKey:  "sk-super-secret",
		Model:   "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	name := p.Name()
	if strings.Contains(name, "secret") || strings.Contains(name, "sk-super") {
		t.Fatalf("Provider 名称不应包含密钥: %s", name)
	}
	if !strings.Contains(name, "gpt-4o-mini") {
		t.Fatalf("Provider 名称应包含模型: %s", name)
	}
}

func TestDescribe(t *testing.T) {
	list := Describe(Config{Kind: "ollama"})
	if len(list) != 2 {
		t.Fatalf("期望 2 个 Provider 描述，实际 %d", len(list))
	}
	var sawOllama, sawOpenAI bool
	for _, d := range list {
		if d.Name == "" || d.Kind == "" || d.Detail == "" {
			t.Fatalf("描述字段不完整: %+v", d)
		}
		switch d.Kind {
		case KindOllama:
			sawOllama = true
			if !d.Local {
				t.Fatalf("Ollama 应为本地: %+v", d)
			}
			if d.Endpoint != defaultOllamaBaseURL {
				t.Fatalf("Ollama 默认端点不正确: %s", d.Endpoint)
			}
			if d.Model != defaultOllamaModel {
				t.Fatalf("Ollama 默认模型不正确: %s", d.Model)
			}
			if len(d.Options) == 0 {
				t.Fatalf("Ollama 应提供可选模型: %+v", d)
			}
		case KindOpenAI:
			sawOpenAI = true
		}
		if strings.Contains(d.Name, "sk-") {
			t.Fatalf("描述里不应出现密钥: %+v", d)
		}
	}
	if !sawOllama || !sawOpenAI {
		t.Fatalf("缺少 Provider 描述: %+v", list)
	}

	// 未知 Kind 不 panic，返回空列表。
	if got := Describe(Config{Kind: "unknown"}); got != nil {
		t.Fatalf("未知 Kind 应返回 nil，实际 %+v", got)
	}
}

// NewWithClient 注入的客户端生效。
func TestNewWithClientInjectsHTTPClient(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusOK, encodeChatResponse(t, echoChoices(itemsFromPrompt(t, body.userPrompt()), "I")))
	})
	var transportUsed int32
	base := srv.Client()
	base.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&transportUsed, 1)
		return http.DefaultTransport.RoundTrip(r)
	})

	p, err := NewWithClient(Config{
		Kind:    "openai",
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
	}, base)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := p.Translate(context.Background(), Request{
		Items:          []Item{{ID: "1", Text: "hello"}},
		TargetLanguage: "zh",
	}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if atomic.LoadInt32(&transportUsed) == 0 {
		t.Fatal("注入的 HTTP 客户端未被使用")
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("期望 1 次请求，实际 %d", *calls)
	}
}

// roundTripFunc 让函数实现 http.RoundTripper。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 网络错误应判为可重试。
func TestOpenAINetworkErrorIsRetryable(t *testing.T) {
	p := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		BaseURL: "http://127.0.0.1:1/v1", // 必然连不上
		Model:   "m",
		Timeout: 2 * time.Second,
	})
	_, err := p.Translate(context.Background(), Request{
		Items:          []Item{{ID: "1", Text: "hello"}},
		TargetLanguage: "zh",
	})
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("期望 *Error，实际 %T: %v", err, err)
	}
	if pe.Kind != KindRetryable {
		t.Fatalf("网络错误应为可重试，实际 %s", pe.Kind)
	}
	if !strings.Contains(pe.Message, "连接翻译服务失败") {
		t.Fatalf("错误信息应为中文说明: %s", pe.Message)
	}
}

// 配置缺失时返回致命错误，而不是发起请求。
func TestOpenAIProviderMissingConfig(t *testing.T) {
	p := NewOpenAICompatibleProvider(OpenAICompatibleConfig{})
	_, err := p.Translate(context.Background(), Request{Items: []Item{{ID: "1", Text: "hi"}}, TargetLanguage: "zh"})
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != KindFatal {
		t.Fatalf("缺少地址应为致命错误，实际 %v", err)
	}

	p = NewOpenAICompatibleProvider(OpenAICompatibleConfig{BaseURL: "http://127.0.0.1:8080/v1"})
	_, err = p.Translate(context.Background(), Request{Items: []Item{{ID: "1", Text: "hi"}}, TargetLanguage: "zh"})
	if !errors.As(err, &pe) || pe.Kind != KindFatal {
		t.Fatalf("缺少模型应为致命错误，实际 %v", err)
	}
}

// 空批次直接返回，不请求。
func TestTranslateEmptyInput(t *testing.T) {
	tr := NewTranslator(&stubProvider{}, testOptions())
	got, err := tr.Translate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("空输入不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("空输入应返回空结果，实际 %+v", got)
	}
}

// stubProvider 是一个不联网的假 Provider。
type stubProvider struct {
	calls int32
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Translate(ctx context.Context, req Request) ([]Choice, error) {
	atomic.AddInt32(&s.calls, 1)
	out := make([]Choice, 0, len(req.Items))
	for _, it := range req.Items {
		out = append(out, Choice{ID: it.ID, Text: "译" + it.Text})
	}
	return out, nil
}

// TranslateTexts 便捷方法。
func TestTranslateTexts(t *testing.T) {
	stub := &stubProvider{}
	tr := NewTranslator(stub, testOptions())
	got, err := tr.TranslateTexts(context.Background(), []string{"a", "b", ""}, nil)
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("结果长度应与输入一致，实际 %d", len(got))
	}
	if got[0] != "译a" || got[1] != "译b" {
		t.Fatalf("译文不正确: %+v", got)
	}
	if got[2] != "" {
		t.Fatalf("空文本应保持为空: %q", got[2])
	}
}

// 配置错误（如未知 Kind）直接暴露给调用方而不是被吞掉。
func TestTranslatorFatalErrorSurfaces(t *testing.T) {
	srv, calls := newFakeServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body chatRequest, call int) {
		writeJSON(w, http.StatusUnauthorized, `{"error":"no auth"}`)
	})
	tr := NewTranslator(newTestOpenAIProvider(srv), testOptions())
	_, err := tr.Translate(context.Background(), items(2), nil)
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("期望 *BatchError，实际 %T", err)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("401 不应重试，期望 1 次请求，实际 %d", *calls)
	}
	if !strings.Contains(be.Error(), "2 条") {
		t.Fatalf("聚合错误应包含失败条数: %s", be.Error())
	}
}

// choosePrefix 仅用于确保 echoChoices 的返回类型可被复用。
var _ = func() []Choice { return echoChoices(nil, "x") }
